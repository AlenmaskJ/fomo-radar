package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestGeckoMomentumKeepsTrendingWhenTopVolumeFails(t *testing.T) {
	body := []byte(`{"data":[{"attributes":{"address":"0xPool","pool_created_at":"2026-08-23T11:59:00Z","base_token_price_usd":"0.12","reserve_in_usd":"50000","price_change_percentage":{"h1":"30"},"transactions":{"h1":{"buys":200,"sells":80,"buyers":150,"sellers":70}},"volume_usd":{"h1":"25000","h24":"350000"}},"relationships":{"base_token":{"data":{"id":"bsc_0xbeea1d618e533a387d941f58a7d4c9b7bd377777"}}}}],"included":[{"id":"bsc_0xbeea1d618e533a387d941f58a7d4c9b7bd377777","attributes":{"address":"0xbeea1d618e533a387d941f58a7d4c9b7bd377777","name":"牛来","symbol":"牛来"}}]}`)
	var paths []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		if r.URL.Path == "/networks/bsc/trending_pools" {
			_, _ = w.Write(body)
			return
		}
		http.Error(w, "top volume unavailable", http.StatusBadRequest)
	}))
	defer server.Close()

	pacer := &countingPacer{}
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	discoverer := NewGeckoMomentumDiscoverer(NewHTTPClient(time.Second), server.URL, func() time.Time { return now }, pacer)
	result := discoverer.DiscoverMomentum(context.Background(), domain.ChainBSC)

	if len(result.Candidates) != 1 || len(result.Reports) != 2 || len(result.Errors) != 1 {
		t.Fatalf("result candidates=%d reports=%d errors=%v", len(result.Candidates), len(result.Reports), result.Errors)
	}
	got := result.Candidates[0]
	if got.ID != "bsc:0xbeea1d618e533a387d941f58a7d4c9b7bd377777" || got.Name != "牛来" || got.Symbol != "牛来" || got.Origin != domain.CandidateOriginMomentum {
		t.Fatalf("candidate = %+v", got)
	}
	if got.TriggerPoolAddress != "0xpool" || len(got.MomentumSources) != 1 || got.MomentumSources[0] != domain.MomentumSourceGeckoTrending {
		t.Fatalf("trigger/source = %+v", got)
	}
	if got.DiscoveryPoolAddress != "" || got.ChainCreatedAt != nil {
		t.Fatalf("Momentum pool was mislabeled as Launch/token creation: %+v", got)
	}
	if got.DiscoveryMarket.PriceChangeH1.Value != 30 || got.DiscoveryMarket.Volume24hUSD.Value != 350000 {
		t.Fatalf("market evidence = %+v", got.DiscoveryMarket)
	}
	if pacer.calls != 2 {
		t.Fatalf("pacer calls = %d, want 2", pacer.calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 || paths[0] != "/networks/bsc/trending_pools?include=base_token&page=1" || paths[1] != "/networks/bsc/pools?include=base_token&page=1&order=h24_volume_usd_desc" {
		t.Fatalf("paths = %v", paths)
	}
	if result.Reports[0].Source != string(domain.MomentumSourceGeckoTrending) || result.Reports[0].UniqueCandidates != 1 || result.Reports[1].Error == "" {
		t.Fatalf("reports = %+v", result.Reports)
	}
}

func TestGeckoMomentumSkipsMalformedPoolAndKeepsValidPool(t *testing.T) {
	body := []byte(`{"data":[
		{"attributes":{"address":"bad","pool_created_at":"not-a-time"},"relationships":{"base_token":{"data":{"id":"bsc_bad"}}}},
		{"attributes":{"address":"0xPool","pool_created_at":"2026-08-23T11:59:00Z","reserve_in_usd":"50000"},"relationships":{"base_token":{"data":{"id":"bsc_0xGood"}}}}
	],"included":[
		{"id":"bsc_bad","attributes":{"address":"0xBad","name":"坏","symbol":"坏"}},
		{"id":"bsc_0xGood","attributes":{"address":"0xGood","name":"好币","symbol":"好🚀"}}
	]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/networks/bsc/trending_pools" {
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"included":[]}`))
	}))
	defer server.Close()

	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	result := NewGeckoMomentumDiscoverer(server.Client(), server.URL, func() time.Time { return now }, nil).
		DiscoverMomentum(context.Background(), domain.ChainBSC)

	if len(result.Candidates) != 1 || result.Candidates[0].ID != "bsc:0xgood" || result.Candidates[0].Name != "好币" {
		t.Fatalf("valid candidate lost: %#v", result.Candidates)
	}
	if len(result.Errors) != 1 || result.Reports[0].Error == "" {
		t.Fatalf("malformed row was not audited: errors=%#v reports=%#v", result.Errors, result.Reports)
	}
}
