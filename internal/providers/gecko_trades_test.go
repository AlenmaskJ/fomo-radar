package providers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/testutil"
)

func TestGeckoTradeReaderParsesTradesAndAggregatesAddressWindows(t *testing.T) {
	body := testutil.Fixture(t, "gecko_trades.json")
	var requestURI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI = r.URL.RequestURI()
		_, _ = w.Write(body)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	reader := NewGeckoTradeReader(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	window, err := reader.ReadRecentTrades(context.Background(), domain.ChainBSC, "pool/address")
	if err != nil {
		t.Fatal(err)
	}
	if requestURI != "/networks/bsc/pools/pool%2Faddress/trades" {
		t.Fatalf("request URI=%q", requestURI)
	}
	if len(window.Trades) != 6 || window.Trades[0].FromAddress != "addr-a" || window.Trades[0].Kind != domain.TradeBuy || window.Trades[0].VolumeUSD != 100 {
		t.Fatalf("trades=%+v", window.Trades)
	}
	if window.Current.Buys != 2 || window.Current.Sells != 1 || window.Current.UniqueBuyers != 1 || window.Current.UniqueSellers != 1 {
		t.Fatalf("current counts=%+v", window.Current)
	}
	if window.Current.BuyVolumeUSD != 150 || window.Current.SellVolumeUSD != 25 || math.Abs(window.Current.TopAddressVolumeShare-150.0/175.0) > 1e-9 {
		t.Fatalf("current volume=%+v", window.Current)
	}
	if window.Previous.Buys != 1 || window.Previous.Sells != 1 || window.Previous.UniqueBuyers != 1 || window.Previous.UniqueSellers != 1 || window.Previous.TopAddressVolumeShare != 0.6 {
		t.Fatalf("previous=%+v", window.Previous)
	}
	if window.Truncated {
		t.Fatal("six-trade sample must not be truncated")
	}
}

func TestGeckoTradeReaderMarksExactlyThreeHundredTradesTruncated(t *testing.T) {
	trade := map[string]any{"id": "trade", "type": "trade", "attributes": map[string]any{"tx_from_address": "addr", "kind": "buy", "volume_in_usd": "1", "block_timestamp": "2026-08-16T11:59:00Z"}}
	data := make([]any, 300)
	for i := range data {
		data[i] = trade
	}
	body, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	reader := NewGeckoTradeReader(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	window, err := reader.ReadRecentTrades(context.Background(), domain.ChainSolana, "pool")
	if err != nil {
		t.Fatal(err)
	}
	if !window.Truncated || len(window.Trades) != 300 {
		t.Fatalf("truncated=%v len=%d", window.Truncated, len(window.Trades))
	}
}

func TestGeckoTradeReaderNormalizesBSCTraderAndPoolIdentities(t *testing.T) {
	body := []byte(`{"data":[{"attributes":{"tx_from_address":"0xAbC","kind":"buy","volume_in_usd":"40","block_timestamp":"2026-08-16T11:59:00Z"}},{"attributes":{"tx_from_address":"0xaBc","kind":"buy","volume_in_usd":"60","block_timestamp":"2026-08-16T11:58:00Z"}}]}`)
	var requestURI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI = r.URL.RequestURI()
		_, _ = w.Write(body)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	reader := NewGeckoTradeReader(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	window, err := reader.ReadRecentTrades(context.Background(), domain.ChainBSC, "0xPoOl")
	if err != nil {
		t.Fatal(err)
	}
	if requestURI != "/networks/bsc/pools/0xpool/trades" || window.Trades[0].FromAddress != "0xabc" || window.Trades[1].FromAddress != "0xabc" {
		t.Fatalf("request=%q trades=%+v", requestURI, window.Trades)
	}
	if window.Current.UniqueBuyers != 1 || window.Current.TopAddressVolumeShare != 1 {
		t.Fatalf("current=%+v", window.Current)
	}
}

func TestGeckoTradeReaderPreservesSolanaTraderCase(t *testing.T) {
	body := []byte(`{"data":[{"attributes":{"tx_from_address":"TraderA","kind":"buy","volume_in_usd":"40","block_timestamp":"2026-08-16T11:59:00Z"}},{"attributes":{"tx_from_address":"tradera","kind":"buy","volume_in_usd":"60","block_timestamp":"2026-08-16T11:58:00Z"}}]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	reader := NewGeckoTradeReader(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	window, err := reader.ReadRecentTrades(context.Background(), domain.ChainSolana, "PoOl")
	if err != nil {
		t.Fatal(err)
	}
	if window.Trades[0].FromAddress != "TraderA" || window.Trades[1].FromAddress != "tradera" || window.Current.UniqueBuyers != 2 || window.Current.TopAddressVolumeShare != 0.6 {
		t.Fatalf("window=%+v", window)
	}
}

func TestGeckoTradeReaderRejectsMissingOrUnknownTradeIdentity(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "blank address", body: `{"data":[{"attributes":{"tx_from_address":" ","kind":"buy","volume_in_usd":"1","block_timestamp":"2026-08-16T11:59:00Z"}}]}`},
		{name: "missing kind", body: `{"data":[{"attributes":{"tx_from_address":"trader","volume_in_usd":"1","block_timestamp":"2026-08-16T11:59:00Z"}}]}`},
		{name: "unknown kind", body: `{"data":[{"attributes":{"tx_from_address":"trader","kind":"swap","volume_in_usd":"1","block_timestamp":"2026-08-16T11:59:00Z"}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tt.body)) }))
			defer server.Close()

			now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
			reader := NewGeckoTradeReader(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
			_, err := reader.ReadRecentTrades(context.Background(), domain.ChainSolana, "pool")
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || providerErr.Kind != ErrorBadResponse {
				t.Fatalf("error=%v, want bad_response ProviderError", err)
			}
		})
	}
}

func TestGeckoTradeReaderIncludesCollectedAtAndExcludesFutureTrade(t *testing.T) {
	body := []byte(`{"data":[{"attributes":{"tx_from_address":"at-now","kind":"buy","volume_in_usd":"10","block_timestamp":"2026-08-16T12:00:00Z"}},{"attributes":{"tx_from_address":"future","kind":"buy","volume_in_usd":"20","block_timestamp":"2026-08-16T12:00:00.000000001Z"}}]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	reader := NewGeckoTradeReader(NewHTTPClient(time.Second), server.URL, func() time.Time { return now })
	window, err := reader.ReadRecentTrades(context.Background(), domain.ChainSolana, "pool")
	if err != nil {
		t.Fatal(err)
	}
	if window.Current.Buys != 1 || window.Current.UniqueBuyers != 1 || window.Current.BuyVolumeUSD != 10 {
		t.Fatalf("current=%+v", window.Current)
	}
}
