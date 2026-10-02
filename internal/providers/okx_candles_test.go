package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestOKXCandleReaderOrdersOldestFirstAndPreservesConfirmation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("bar"); got != "15m" {
			t.Fatalf("bar = %q", got)
		}
		_, _ = w.Write([]byte(`{"code":"0","data":[
			["1789879500000","101","103","100","102","12","1200","1224","0"],
			["1789878600000","100","102","99","101","10","1000","1010","1"]
		]}`))
	}))
	defer server.Close()

	reader := NewOKXCandleReader(NewHTTPClient(time.Second), server.URL, &countingPacer{})
	candles, err := reader.Read(context.Background(), "BTC-USDT-SWAP", domain.Timeframe15m, 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 2 || !candles[0].OpenTime.Before(candles[1].OpenTime) {
		t.Fatalf("candles = %+v", candles)
	}
	if !candles[0].Confirmed || candles[1].Confirmed {
		t.Fatalf("confirmation = %+v", candles)
	}
	if candles[0].Close != 101 || candles[0].VolumeQuote != 1010 {
		t.Fatalf("oldest candle = %+v", candles[0])
	}
}

func TestOKXCandleReaderRejectsMalformedOrDuplicateCandles(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"code":"0","data":[["1","2"]]}`},
		{name: "duplicate", body: `{"code":"0","data":[["1789878600000","100","102","99","101","10","1000","1010","1"],["1789878600000","100","102","99","101","10","1000","1010","1"]]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			reader := NewOKXCandleReader(NewHTTPClient(time.Second), server.URL, nil)
			_, err := reader.Read(context.Background(), "BTC-USDT-SWAP", domain.Timeframe15m, 300)
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || providerErr.Kind != ErrorBadResponse {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestOKXCandleReaderRejectsUnsupportedTimeframeBeforeRequest(t *testing.T) {
	reader := NewOKXCandleReader(NewHTTPClient(time.Second), "http://127.0.0.1:1", nil)
	_, err := reader.Read(context.Background(), "BTC-USDT-SWAP", domain.Timeframe("2H"), 100)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorBadResponse {
		t.Fatalf("error = %v", err)
	}
}
