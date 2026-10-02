package providers

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestNewHTTPClientWithProxyUsesExplicitProxy(t *testing.T) {
	client, err := NewHTTPClientWithProxy(time.Second, "http://127.0.0.1:10808")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://www.okx.com/api/v5/market/tickers", nil)
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL.String() != "http://127.0.0.1:10808" {
		t.Fatalf("proxy URL = %q", proxyURL)
	}

	if _, err := NewHTTPClientWithProxy(time.Second, "127.0.0.1:10808"); err == nil {
		t.Fatal("proxy without scheme should be rejected")
	}
}

func TestOKXMarketReaderJoinsBulkDataAndSeparatesCryptoFromStocks(t *testing.T) {
	responses := map[string]string{
		"/api/v5/public/instruments?instType=SWAP": `{"code":"0","data":[
			{"instId":"BTC-USDT-SWAP","baseCcy":"BTC","instCategory":"1","ruleType":"normal","state":"live","listTime":"1780000000000","tickSz":"0.1"},
			{"instId":"AAPL-USDT-SWAP","baseCcy":"AAPL","instCategory":"3","ruleType":"normal","state":"live","listTime":"1780000001000","tickSz":"0.01"},
			{"instId":"OPENAI-USDT-SWAP","baseCcy":"OPENAI","instCategory":"3","ruleType":"pre_market","state":"live"},
			{"instId":"DOGE-USDT-SWAP","baseCcy":"DOGE","instCategory":"1","ruleType":"normal","state":"suspend"}
		]}`,
		"/api/v5/market/tickers?instType=SWAP": `{"code":"0","data":[
			{"instId":"BTC-USDT-SWAP","last":"110","open24h":"100","volCcy24h":"20","ts":"1789879381000"},
			{"instId":"AAPL-USDT-SWAP","last":"198","open24h":"200","volCcy24h":"10","ts":"1789879382000"}
		]}`,
		"/api/v5/public/open-interest?instType=SWAP": `{"code":"0","data":[
			{"instId":"BTC-USDT-SWAP","oiUsd":"5000000"},
			{"instId":"AAPL-USDT-SWAP","oiUsd":"250000"}
		]}`,
		"/api/v5/public/funding-rate?instId=ANY": `{"code":"0","data":[
			{"instId":"BTC-USDT-SWAP","fundingRate":"0.0001"},
			{"instId":"AAPL-USDT-SWAP","fundingRate":"-0.0002"}
		]}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := responses[r.URL.RequestURI()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	collectedAt := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	reader := NewOKXMarketReader(NewHTTPClient(time.Second), server.URL, func() time.Time { return collectedAt })
	result, err := reader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 || len(result.Snapshots) != 2 {
		t.Fatalf("result = %+v, want two complete snapshots", result)
	}
	metadata := result.Instruments["BTC-USDT-SWAP"]
	if metadata.TickSize != 0.1 || metadata.ListedAt.UnixMilli() != 1780000000000 {
		t.Fatalf("BTC instrument metadata = %+v", metadata)
	}

	btc := result.Snapshots[0]
	if btc.AssetClass != domain.AssetClassCrypto || btc.Symbol != "BTC" || btc.InstrumentID != "BTC-USDT-SWAP" {
		t.Fatalf("BTC identity = %+v", btc)
	}
	if btc.PriceUSD != 110 || math.Abs(btc.Change24hPct-10) > 1e-9 || btc.Turnover24hUSD != 2200 {
		t.Fatalf("BTC ticker fields = %+v", btc)
	}
	if !btc.OpenInterestAvailable || btc.OpenInterestUSD != 5000000 || !btc.FundingRateAvailable || btc.FundingRate != 0.0001 {
		t.Fatalf("BTC derivatives fields = %+v", btc)
	}
	if !btc.CollectedAt.Equal(collectedAt) || btc.SourceTime.UnixMilli() != 1789879381000 {
		t.Fatalf("BTC times = collected %s source %s", btc.CollectedAt, btc.SourceTime)
	}

	aapl := result.Snapshots[1]
	if aapl.AssetClass != domain.AssetClassStock || aapl.Symbol != "AAPL" || math.Abs(aapl.Change24hPct+1) > 1e-9 {
		t.Fatalf("AAPL snapshot = %+v", aapl)
	}
}

func TestOKXMarketReaderKeepsTickerDataWhenOptionalMetricsFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","data":[{"instId":"ETH-USDT-SWAP","baseCcy":"ETH","instCategory":"1","ruleType":"normal","state":"live","listTime":"1700000000000","tickSz":"0.01"}]}`))
		case "/api/v5/market/tickers":
			_, _ = w.Write([]byte(`{"code":"0","data":[{"instId":"ETH-USDT-SWAP","last":"2000","open24h":"2000","volCcy24h":"5","ts":"1789879381000"}]}`))
		default:
			http.Error(w, "unavailable", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	result, err := NewOKXMarketReader(NewHTTPClient(time.Second), server.URL, time.Now).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshots) != 1 || result.Snapshots[0].OpenInterestAvailable || result.Snapshots[0].FundingRateAvailable {
		t.Fatalf("degraded snapshots = %+v", result.Snapshots)
	}
	if len(result.Warnings) != 2 || !strings.Contains(strings.Join(result.Warnings, ";"), "open interest") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}
