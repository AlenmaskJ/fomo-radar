package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/testutil"
)

func TestDexScreenerEnricherSelectsMostLiquidBasePairWithoutInventingEvidence(t *testing.T) {
	body := testutil.Fixture(t, "dex_token_pairs.json")
	var requestURI, userAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI, userAgent = r.URL.RequestURI(), r.Header.Get("User-Agent")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	enricher := NewDexScreenerEnricher(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	snapshot, err := enricher.Enrich(context.Background(), domain.Candidate{ID: "solana:token-new", Chain: domain.ChainSolana, Address: "token-new"})
	if err != nil {
		t.Fatal(err)
	}
	if requestURI != "/token-pairs/v1/solana/token-new" || userAgent != "fomo-scanner/1.0" {
		t.Fatalf("request URI=%q user-agent=%q", requestURI, userAgent)
	}
	if snapshot.PairAddress != "pair-high" || snapshot.PriceUSD.Value != 0.025 || snapshot.LiquidityUSD.Value != 75000 || snapshot.FDVUSD.Value != 250000 {
		t.Fatalf("selected snapshot=%+v", snapshot)
	}
	if snapshot.VolumeM5USD.Value != 4321.5 || snapshot.VolumeH1USD.Value != 9876.25 || snapshot.PriceChangeM5.Value != 12.5 {
		t.Fatalf("market windows=%+v", snapshot)
	}
	if snapshot.BuysM5.Value != 17 || snapshot.SellsM5.Value != 6 {
		t.Fatalf("transaction counts buys=%+v sells=%+v", snapshot.BuysM5, snapshot.SellsM5)
	}
	if snapshot.BuyersM5.Quality != domain.QualityMissing || snapshot.SellersM5.Quality != domain.QualityMissing {
		t.Fatalf("unique-address evidence must be missing: buyers=%+v sellers=%+v", snapshot.BuyersM5, snapshot.SellersM5)
	}
	if snapshot.MarketCapUSD.Quality != domain.QualityMissing || snapshot.MarketCapUSD.Value != 0 {
		t.Fatalf("market cap=%+v; must not copy FDV", snapshot.MarketCapUSD)
	}
	wantCreated := time.Date(2026, 8, 16, 11, 35, 0, 0, time.UTC)
	if snapshot.PairCreatedAt == nil || !snapshot.PairCreatedAt.Equal(wantCreated) {
		t.Fatalf("PairCreatedAt=%v, want %s", snapshot.PairCreatedAt, wantCreated)
	}
	if len(snapshot.Links) != 3 || snapshot.BoostsActive.Value != 3 {
		t.Fatalf("links=%+v boosts=%+v", snapshot.Links, snapshot.BoostsActive)
	}
}

func TestDexPairParsesAllMomentumWindowsWithoutInventingUniqueAddresses(t *testing.T) {
	body := []byte(`[{"chainId":"bsc","pairAddress":"0xPool","baseToken":{"address":"0xToken","name":"牛来","symbol":"牛来"},"priceUsd":"0.12","txns":{"m5":{"buys":20,"sells":8},"h1":{"buys":200,"sells":80},"h6":{"buys":600,"sells":300},"h24":{"buys":1200,"sells":700}},"volume":{"m5":5000,"h1":25000,"h6":100000,"h24":350000},"priceChange":{"m5":2,"h1":30,"h6":80,"h24":120},"liquidity":{"usd":50000},"marketCap":1200000,"pairCreatedAt":1787443200000}]`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	enricher := NewDexScreenerEnricher(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	got, err := enricher.Enrich(context.Background(), domain.Candidate{Chain: domain.ChainBSC, Address: "0xtoken"})
	if err != nil {
		t.Fatal(err)
	}
	if got.VolumeH6USD.Value != 100000 || got.Volume24hUSD.Value != 350000 || got.PriceChangeH1.Value != 30 || got.PriceChangeH6.Value != 80 || got.PriceChangeH24.Value != 120 {
		t.Fatalf("Momentum float windows = %+v", got)
	}
	if got.BuysH1.Value != 200 || got.SellsH6.Value != 300 || got.BuysH24.Value != 1200 {
		t.Fatalf("Momentum transaction windows = %+v", got)
	}
	if got.BuyersM5.Quality != domain.QualityMissing || got.SellersM5.Quality != domain.QualityMissing {
		t.Fatalf("transaction counts leaked into unique-address fields: %+v/%+v", got.BuyersM5, got.SellersM5)
	}
}

func TestProviderReturnsTypedRateLimitAfterTwoRetries(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "0")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer server.Close()

	enricher := NewDexScreenerEnricher(NewHTTPClient(time.Second), server.URL, time.Now)
	_, err := enricher.Enrich(context.Background(), domain.Candidate{Chain: domain.ChainBSC, Address: "0xabc"})
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorRateLimited {
		t.Fatalf("error=%v, want rate_limited ProviderError", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d, want 3", attempts)
	}
}

func TestProviderRejectsResponseLargerThanTwoMiB(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 2*1024*1024+1)))
	}))
	defer server.Close()

	enricher := NewDexScreenerEnricher(NewHTTPClient(time.Second), server.URL, time.Now)
	_, err := enricher.Enrich(context.Background(), domain.Candidate{Chain: domain.ChainBSC, Address: "0xabc"})
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorBadResponse {
		t.Fatalf("error=%v, want bad_response ProviderError", err)
	}
}

func TestHTTPClientUsesSmallVPSDefaults(t *testing.T) {
	client := NewHTTPClient(0)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type=%T", client.Transport)
	}
	if client.Timeout != 8*time.Second || transport.MaxIdleConns != 8 || transport.MaxIdleConnsPerHost != 4 {
		t.Fatalf("client timeout=%s max-idle=%d per-host=%d", client.Timeout, transport.MaxIdleConns, transport.MaxIdleConnsPerHost)
	}
}

func TestProviderReturnsTypedTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	enricher := NewDexScreenerEnricher(NewHTTPClient(20*time.Millisecond), server.URL, time.Now)
	_, err := enricher.Enrich(context.Background(), domain.Candidate{Chain: domain.ChainBSC, Address: "0xabc"})
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorTimeout {
		t.Fatalf("error=%v, want timeout ProviderError", err)
	}
}

func TestProviderDoesNotRetryUnlistedStatus(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer server.Close()

	enricher := NewDexScreenerEnricher(NewHTTPClient(time.Second), server.URL, time.Now)
	_, err := enricher.Enrich(context.Background(), domain.Candidate{Chain: domain.ChainBSC, Address: "0xabc"})
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorBadResponse || attempts != 1 {
		t.Fatalf("error=%v attempts=%d, want one bad_response attempt", err, attempts)
	}
}

func TestDexScreenerEnricherKeepsAbsentTransactionWindowMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"chainId":"bsc","pairAddress":"pair","baseToken":{"address":"0xAbC"},"liquidity":{"usd":10}}]`))
	}))
	defer server.Close()

	enricher := NewDexScreenerEnricher(NewHTTPClient(time.Second), server.URL, time.Now)
	snapshot, err := enricher.Enrich(context.Background(), domain.Candidate{Chain: domain.ChainBSC, Address: "0xabc"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.BuysM5.Quality != domain.QualityMissing || snapshot.SellsM5.Quality != domain.QualityMissing {
		t.Fatalf("absent txns.m5 must stay missing: buys=%+v sells=%+v", snapshot.BuysM5, snapshot.SellsM5)
	}
}

func TestProviderPreservesCanceledRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	enricher := NewDexScreenerEnricher(NewHTTPClient(time.Second), server.URL, time.Now)
	_, err := enricher.Enrich(ctx, domain.Candidate{Chain: domain.ChainBSC, Address: "0xabc"})
	var providerErr *ProviderError
	if !errors.Is(err, context.Canceled) || errors.As(err, &providerErr) {
		t.Fatalf("error=%v, want unwrapped context.Canceled", err)
	}
}

func TestProviderPreservesCancellationDuringRetryWait(t *testing.T) {
	responseSent := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "slow down", http.StatusTooManyRequests)
		close(responseSent)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-responseSent
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	enricher := NewDexScreenerEnricher(NewHTTPClient(time.Second), server.URL, time.Now)
	_, err := enricher.Enrich(ctx, domain.Candidate{Chain: domain.ChainBSC, Address: "0xabc"})
	var providerErr *ProviderError
	if !errors.Is(err, context.Canceled) || errors.As(err, &providerErr) {
		t.Fatalf("error=%v, want unwrapped context.Canceled", err)
	}
}
