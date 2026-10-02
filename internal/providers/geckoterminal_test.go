package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/testutil"
)

func TestGeckoTerminalDiscovererParsesRecentPoolsAndStopsAtOldPool(t *testing.T) {
	body := testutil.Fixture(t, "gecko_new_pools.json")
	var paths []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	discoverer := NewGeckoTerminalDiscoverer(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	candidates, err := discoverer.Discover(context.Background(), domain.ChainSolana)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates)=%d, want 1", len(candidates))
	}
	got := candidates[0]
	if got.Chain != domain.ChainSolana || got.Address != "token-new" || got.PairAddress != "pool-new" || got.Name != "New Coin" || got.Symbol != "NEW" {
		t.Fatalf("candidate=%+v", got)
	}
	wantCreated := time.Date(2026, 8, 16, 11, 15, 0, 0, time.UTC)
	if got.ChainCreatedAt == nil || !got.ChainCreatedAt.Equal(wantCreated) {
		t.Fatalf("ChainCreatedAt=%v, want %s", got.ChainCreatedAt, wantCreated)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/networks/solana/new_pools?include=base_token&page=1" {
		t.Fatalf("requests=%v", paths)
	}
}

func TestGeckoTerminalDiscovererCapsRecentPoolPagesAtThree(t *testing.T) {
	created := "2026-08-16T11:30:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		_, _ = fmt.Fprintf(w, `{"data":[{"id":"bsc_pool-%s","type":"pool","attributes":{"address":"pool-%s","pool_created_at":%q},"relationships":{"base_token":{"data":{"id":"bsc_token-%s","type":"token"}}}}],"included":[{"id":"bsc_token-%s","type":"token","attributes":{"address":"token-%s","name":"Coin","symbol":"COIN"}}]}`, page, page, created, page, page, page)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	discoverer := NewGeckoTerminalDiscoverer(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	candidates, err := discoverer.Discover(context.Background(), domain.ChainBSC)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("len(candidates)=%d, want 3", len(candidates))
	}
}

func TestGeckoTerminalDiscovererParsesLiveMarketEvidenceWithoutInventingUniqueAddresses(t *testing.T) {
	body := testutil.Fixture(t, "gecko_new_pools_live_v2.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"included":[]}`))
	}))
	defer server.Close()

	now := time.Date(2026, 8, 16, 9, 2, 0, 0, time.UTC)
	discoverer := NewGeckoTerminalDiscoverer(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	batch, err := discoverer.DiscoverWithCoverage(context.Background(), domain.ChainBSC)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(batch.Candidates))
	}
	got := batch.Candidates[0]
	created := time.Date(2026, 8, 16, 9, 1, 16, 0, time.UTC)
	if got.Name != "直接进作者钱包" || got.Symbol != "巨兽BEHEMOTH" {
		t.Fatalf("identity = %q/%q", got.Name, got.Symbol)
	}
	if got.DiscoveryPoolAddress != "0xdff399c5d0ffa1ef244e1a94f46f9f06e736ffff" || got.DiscoveryPoolCreatedAt == nil || !got.DiscoveryPoolCreatedAt.Equal(created) {
		t.Fatalf("discovery pool = %q/%v", got.DiscoveryPoolAddress, got.DiscoveryPoolCreatedAt)
	}
	market := got.DiscoveryMarket
	assertFreshFloat(t, market.PriceUSD, 0.00000355678261840511)
	assertFreshFloat(t, market.FDVUSD, 3556.782618)
	if market.MarketCapUSD.Quality != domain.QualityMissing {
		t.Fatalf("null market cap quality = %q, want missing", market.MarketCapUSD.Quality)
	}
	assertFreshFloat(t, market.LiquidityUSD, 2845.009677077)
	assertFreshFloat(t, market.VolumeM5USD, 42.0581823529)
	assertFreshFloat(t, market.VolumeH1USD, 42.0581823529)
	assertFreshFloat(t, market.Volume24hUSD, 42.0581823529)
	if market.BuysM5.Value != 1 || market.BuysM5.Quality != domain.QualityFresh || market.SellsM5.Value != 0 || market.SellsM5.Quality != domain.QualityFresh {
		t.Fatalf("transactions = buys %+v sells %+v", market.BuysM5, market.SellsM5)
	}
	if market.AggregateBuyersM5.Value != 1 || market.AggregateBuyersM5.Quality != domain.QualityFresh || market.AggregateSellersM5.Value != 0 || market.AggregateSellersM5.Quality != domain.QualityFresh {
		t.Fatalf("aggregate participants = buyers %+v sellers %+v", market.AggregateBuyersM5, market.AggregateSellersM5)
	}
	if market.BuyersM5.Quality != domain.QualityMissing || market.SellersM5.Quality != domain.QualityMissing {
		t.Fatalf("aggregate evidence leaked into unique participants: buyers=%+v sellers=%+v", market.BuyersM5, market.SellersM5)
	}
	if batch.Coverage.PagesFetched != 2 || batch.Coverage.UniqueCandidates != 1 {
		t.Fatalf("coverage = %+v, want two successful pages and one candidate", batch.Coverage)
	}
}

func TestGeckoPoolParsesAllMomentumWindowsWithoutInventingUniqueAddresses(t *testing.T) {
	body := []byte(`{"data":[{"attributes":{"address":"0xPool","pool_created_at":"2026-08-23T11:59:00Z","base_token_price_usd":"0.12","reserve_in_usd":"50000","price_change_percentage":{"m5":"2","h1":"30","h6":"80","h24":"120"},"transactions":{"m5":{"buys":20,"sells":8,"buyers":15,"sellers":7},"h1":{"buys":200,"sells":80,"buyers":150,"sellers":70},"h6":{"buys":600,"sells":300,"buyers":450,"sellers":260},"h24":{"buys":1200,"sells":700,"buyers":900,"sellers":600}},"volume_usd":{"m5":"5000","h1":"25000","h6":"100000","h24":"350000"}},"relationships":{"base_token":{"data":{"id":"bsc_0xToken"}}}}],"included":[{"id":"bsc_0xToken","attributes":{"address":"0xToken","name":"牛来","symbol":"牛来"}}]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"included":[]}`))
	}))
	defer server.Close()

	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	discoverer := NewGeckoTerminalDiscoverer(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	batch, err := discoverer.DiscoverWithCoverage(context.Background(), domain.ChainBSC)
	if err != nil || len(batch.Candidates) != 1 {
		t.Fatalf("DiscoverWithCoverage() candidates=%d err=%v", len(batch.Candidates), err)
	}
	got := batch.Candidates[0].DiscoveryMarket
	if got.VolumeH6USD.Value != 100000 || got.Volume24hUSD.Value != 350000 || got.PriceChangeH1.Value != 30 || got.PriceChangeH6.Value != 80 || got.PriceChangeH24.Value != 120 {
		t.Fatalf("Momentum float windows = %+v", got)
	}
	if got.BuysH1.Value != 200 || got.SellsH6.Value != 300 || got.BuysH24.Value != 1200 || got.AggregateBuyersH1.Value != 150 || got.AggregateSellersH24.Value != 600 {
		t.Fatalf("Momentum transaction windows = %+v", got)
	}
	if got.BuyersM5.Quality != domain.QualityMissing || got.SellersM5.Quality != domain.QualityMissing {
		t.Fatalf("aggregate participants leaked into address-deduplicated fields: %+v/%+v", got.BuyersM5, got.SellersM5)
	}
}

func TestGeckoTerminalDiscoveryCoverageMeasuresFetchedWindowAndUniqueCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		created := map[string]string{"1": "2026-08-16T11:59:00Z", "2": "2026-08-16T11:50:00Z", "3": "2026-08-16T11:40:00Z"}[page]
		_, _ = fmt.Fprintf(w, `{"data":[{"attributes":{"address":"pool-%s","pool_created_at":%q},"relationships":{"base_token":{"data":{"id":"solana_token-%s"}}}}],"included":[{"id":"solana_token-%s","attributes":{"address":"token-%s","name":"币%s","symbol":"币%s"}}]}`, page, created, page, page, page, page, page)
	}))
	defer server.Close()

	started := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	discoverer := NewGeckoTerminalDiscoverer(NewHTTPClient(time.Second), server.URL, func() time.Time { return started })
	batch, err := discoverer.DiscoverWithCoverage(context.Background(), domain.ChainSolana)
	if err != nil {
		t.Fatal(err)
	}
	newest := time.Date(2026, 8, 16, 11, 59, 0, 0, time.UTC)
	oldest := time.Date(2026, 8, 16, 11, 40, 0, 0, time.UTC)
	if batch.Coverage.Chain != domain.ChainSolana || !batch.Coverage.ScanStartedAt.Equal(started) || !batch.Coverage.NewestPoolAt.Equal(newest) || !batch.Coverage.OldestPoolAt.Equal(oldest) {
		t.Fatalf("coverage times = %+v", batch.Coverage)
	}
	if batch.Coverage.PagesFetched != 3 || batch.Coverage.UniqueCandidates != 3 || len(batch.Candidates) != 3 {
		t.Fatalf("coverage counts = %+v candidates=%d", batch.Coverage, len(batch.Candidates))
	}
}

func TestGeckoTerminalDiscovererNormalizesAndDeduplicatesBSCIdentities(t *testing.T) {
	body := []byte(`{"data":[{"attributes":{"address":"0xPoOl","pool_created_at":"2026-08-16T11:30:00Z"},"relationships":{"base_token":{"data":{"id":"bsc_token-a"}}}},{"attributes":{"address":"0xPOOL","pool_created_at":"2026-08-16T11:29:00Z"},"relationships":{"base_token":{"data":{"id":"bsc_token-b"}}}}],"included":[{"id":"bsc_token-a","attributes":{"address":"0xAbC","name":"Coin","symbol":"COIN"}},{"id":"bsc_token-b","attributes":{"address":"0xaBc","name":"Coin","symbol":"COIN"}}]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	discoverer := NewGeckoTerminalDiscoverer(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	candidates, err := discoverer.Discover(context.Background(), domain.ChainBSC)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates)=%d, want one normalized candidate", len(candidates))
	}
	if got := candidates[0]; got.ID != "bsc:0xabc" || got.Address != "0xabc" || got.PairAddress != "0xpool" {
		t.Fatalf("candidate=%+v", got)
	}
}

func TestGeckoTerminalDiscovererPreservesSolanaIdentityCase(t *testing.T) {
	body := []byte(`{"data":[{"attributes":{"address":"PoOlCase","pool_created_at":"2026-08-16T11:30:00Z"},"relationships":{"base_token":{"data":{"id":"solana_token"}}}}],"included":[{"id":"solana_token","attributes":{"address":"ToKeNCase","name":"Coin","symbol":"COIN"}}]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	discoverer := NewGeckoTerminalDiscoverer(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	candidates, err := discoverer.Discover(context.Background(), domain.ChainSolana)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Address != "ToKeNCase" || candidates[0].PairAddress != "PoOlCase" || candidates[0].ID != "solana:ToKeNCase" {
		t.Fatalf("candidates=%+v", candidates)
	}
}

func assertFreshFloat(t *testing.T, got domain.DataValue[float64], want float64) {
	t.Helper()
	if got.Quality != domain.QualityFresh || got.Source != "geckoterminal" || got.Value != want {
		t.Fatalf("value = %+v, want fresh geckoterminal %v", got, want)
	}
}
