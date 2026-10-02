package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestDexMomentumBoostsAreSeedsOnly(t *testing.T) {
	now := time.Date(2026, 8, 23, 4, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token-boosts/top/v1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		fmt.Fprint(w, `[
			{"chainId":"bsc","tokenAddress":"0xABC","amount":100,"totalAmount":200},
			{"chainId":"solana","tokenAddress":"SoLCase","amount":50,"totalAmount":50},
			{"chainId":"ethereum","tokenAddress":"0xignored","amount":999,"totalAmount":999}
		]`)
	}))
	defer server.Close()

	d := NewDexMomentumDiscoverer(server.Client(), server.URL, func() time.Time { return now })
	result := d.DiscoverBoosts(context.Background())
	if len(result.Errors) != 0 || len(result.Candidates) != 2 || len(result.Reports) != 2 {
		t.Fatalf("result = %#v", result)
	}
	bsc := result.Candidates[0]
	if bsc.ID != "bsc:0xabc" || bsc.Address != "0xabc" || bsc.Origin != domain.CandidateOriginMomentum {
		t.Fatalf("BSC candidate = %#v", bsc)
	}
	if len(bsc.MomentumSources) != 1 || bsc.MomentumSources[0] != domain.MomentumSourceDexBoostTop {
		t.Fatalf("BSC sources = %#v", bsc.MomentumSources)
	}
	if bsc.DiscoveryMarket.PriceUSD.Quality != domain.QualityMissing || bsc.DiscoveryMarket.PairAddress != "" {
		t.Fatalf("boost invented market evidence: %#v", bsc.DiscoveryMarket)
	}
	if result.Candidates[1].ID != "solana:SoLCase" || result.Candidates[1].Address != "SoLCase" {
		t.Fatalf("Solana address lost case: %#v", result.Candidates[1])
	}
}

func TestDexMomentumBatchSelectsHighestLiquidityMatchingBasePair(t *testing.T) {
	now := time.Date(2026, 8, 23, 4, 5, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokens/v1/bsc/0xabc,0xdef" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		fmt.Fprint(w, `[
			{"chainId":"bsc","pairAddress":"0xlow","baseToken":{"address":"0xABC","name":"牛来","symbol":"牛🚀"},"priceUsd":"1.2","liquidity":{"usd":1000},"volume":{"h24":9000},"priceChange":{"h1":12},"txns":{"h1":{"buys":11,"sells":3}},"pairCreatedAt":1787450000000},
			{"chainId":"bsc","pairAddress":"0xhigh","baseToken":{"address":"0xABC","name":"牛来","symbol":"牛🚀"},"priceUsd":"1.3","liquidity":{"usd":5000},"volume":{"h24":10000},"priceChange":{"h1":15},"txns":{"h1":{"buys":13,"sells":4}},"pairCreatedAt":1787451000000},
			{"chainId":"bsc","pairAddress":"0xquote","baseToken":{"address":"0x999","name":"wrong","symbol":"NO"},"liquidity":{"usd":999999}},
			{"chainId":"bsc","pairAddress":"0xdefpair","baseToken":{"address":"0xdef","name":"Emoji 😀","symbol":"😀"},"priceUsd":"0.2","liquidity":{"usd":2000},"volume":{"h24":5000}}
		]`)
	}))
	defer server.Close()

	d := NewDexMomentumDiscoverer(server.Client(), server.URL, func() time.Time { return now })
	seeds := []domain.Candidate{
		{ID: "bsc:0xabc", Chain: domain.ChainBSC, Address: "0xABC", Origin: domain.CandidateOriginMomentum},
		{ID: "bsc:0xdef", Chain: domain.ChainBSC, Address: "0xdef", Origin: domain.CandidateOriginMomentum},
	}
	result := d.DiscoverAddresses(context.Background(), domain.ChainBSC, seeds)
	if len(result.Errors) != 0 || len(result.Candidates) != 2 || len(result.Reports) != 1 {
		t.Fatalf("result = %#v", result)
	}
	got := result.Candidates[0]
	if got.ID != "bsc:0xabc" || got.Name != "牛来" || got.Symbol != "牛🚀" || got.TriggerPoolAddress != "0xhigh" {
		t.Fatalf("selected candidate = %#v", got)
	}
	if got.DiscoveryPoolAddress != "" || got.DiscoveryPoolCreatedAt != nil {
		t.Fatalf("momentum pair became launch pool: %#v", got)
	}
	if got.DiscoveryMarket.LiquidityUSD.Value != 5000 || got.DiscoveryMarket.PriceChangeH1.Value != 15 {
		t.Fatalf("selected market = %#v", got.DiscoveryMarket)
	}
	if got.DiscoveryMarket.TokenAgeSource != domain.TokenAgeSourceEarliestPair || got.DiscoveryMarket.TokenAgeSeconds.Value != 7900 {
		t.Fatalf("earliest pair age = %#v", got.DiscoveryMarket.TokenAgeSeconds)
	}
	if got.DiscoveryMarket.BuyersM5.Quality != domain.QualityMissing || got.DiscoveryMarket.SellersM5.Quality != domain.QualityMissing {
		t.Fatal("Dex transaction counts must not become unique participants")
	}
	if result.Reports[0].Source != string(domain.MomentumSourceLocalUniverse) || result.Reports[0].ReturnedItems != 4 {
		t.Fatalf("report = %#v", result.Reports[0])
	}
}

func TestDexMomentumBatchRejectsMoreThanThirtyAddresses(t *testing.T) {
	d := NewDexMomentumDiscoverer(http.DefaultClient, "http://unused", time.Now)
	seeds := make([]domain.Candidate, 31)
	for i := range seeds {
		address := fmt.Sprintf("0x%02d", i)
		seeds[i] = domain.Candidate{ID: "bsc:" + address, Chain: domain.ChainBSC, Address: address}
	}
	result := d.DiscoverAddresses(context.Background(), domain.ChainBSC, seeds)
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "30") {
		t.Fatalf("errors = %#v", result.Errors)
	}
}
