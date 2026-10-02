package markets

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestAnalyzeSeparatesWatchlistsAndBuildsLeverageSignals(t *testing.T) {
	now := time.Date(2026, time.September, 21, 8, 2, 0, 0, time.UTC)
	current := []domain.DerivativesSnapshot{
		{AssetClass: domain.AssetClassCrypto, InstrumentID: "BTC-USDT-SWAP", Symbol: "BTC", CollectedAt: now, PriceUSD: 102, Change24hPct: 5, OpenInterestUSD: 110, OpenInterestAvailable: true, FundingRate: 0.0006, FundingRateAvailable: true},
		{AssetClass: domain.AssetClassCrypto, InstrumentID: "BTC-USD-SWAP", Symbol: "BTC", Change24hPct: 99},
		{AssetClass: domain.AssetClassCrypto, InstrumentID: "ETH-USDT-SWAP", Symbol: "ETH", Change24hPct: -2},
		{AssetClass: domain.AssetClassCrypto, InstrumentID: "MEME-USDT-SWAP", Symbol: "MEME", Change24hPct: 50},
		{AssetClass: domain.AssetClassStock, InstrumentID: "AAPL-USDT-SWAP", Symbol: "AAPL", Change24hPct: 1.5},
		{AssetClass: domain.AssetClassStock, InstrumentID: "SPY-USDT-SWAP", Symbol: "SPY", Change24hPct: -0.5},
	}
	previous := map[string]domain.DerivativesSnapshot{
		"BTC-USDT-SWAP": {InstrumentID: "BTC-USDT-SWAP", CollectedAt: now.Add(-2 * time.Minute), PriceUSD: 100, OpenInterestUSD: 100, OpenInterestAvailable: true},
	}

	report := Analyze(current, previous, nil)
	if len(report.Crypto) != 2 || report.Crypto[0].Symbol != "BTC" || report.Crypto[1].Symbol != "ETH" {
		t.Fatalf("crypto rows = %+v", report.Crypto)
	}
	if len(report.Stocks) != 2 || report.Stocks[0].Symbol != "AAPL" || report.Stocks[1].Symbol != "SPY" {
		t.Fatalf("stock rows = %+v", report.Stocks)
	}
	btc := report.Crypto[0]
	if btc.OpenInterestChangePct == nil || math.Abs(*btc.OpenInterestChangePct-10) > 1e-9 {
		t.Fatalf("BTC OI change = %v", btc.OpenInterestChangePct)
	}
	if btc.PriceChangePct == nil || math.Abs(*btc.PriceChangePct-2) > 1e-9 {
		t.Fatalf("BTC short price change = %v", btc.PriceChangePct)
	}
	for _, want := range []string{"强势", "增仓", "多头拥挤"} {
		if !strings.Contains(btc.Signal, want) {
			t.Fatalf("BTC signal %q missing %q", btc.Signal, want)
		}
	}
	if report.Crypto[1].Signal != "偏弱" {
		t.Fatalf("ETH signal = %q", report.Crypto[1].Signal)
	}
	if !strings.Contains(report.CryptoSummary, "上涨 1/2") || !strings.Contains(report.CryptoSummary, "最强 BTC +5.00%") {
		t.Fatalf("crypto summary = %q", report.CryptoSummary)
	}
	if !strings.Contains(report.StockSummary, "上涨 1/2") || !strings.Contains(report.StockSummary, "最强 AAPL +1.50%") {
		t.Fatalf("stock summary = %q", report.StockSummary)
	}
}

func TestAnalyzeSkipsStaleComparisonBaseline(t *testing.T) {
	now := time.Date(2026, time.September, 21, 8, 10, 0, 0, time.UTC)
	current := []domain.DerivativesSnapshot{{
		AssetClass: domain.AssetClassCrypto, InstrumentID: "BTC-USDT-SWAP", Symbol: "BTC",
		CollectedAt: now, PriceUSD: 102, OpenInterestUSD: 110, OpenInterestAvailable: true,
	}}
	previous := map[string]domain.DerivativesSnapshot{
		"BTC-USDT-SWAP": {CollectedAt: now.Add(-10 * time.Minute), PriceUSD: 100, OpenInterestUSD: 100, OpenInterestAvailable: true},
	}

	row := Analyze(current, previous, nil).Crypto[0]
	if row.PriceChangePct != nil || row.OpenInterestChangePct != nil {
		t.Fatalf("stale baseline produced short changes: price=%v OI=%v", row.PriceChangePct, row.OpenInterestChangePct)
	}
}

func TestAnalyzeReportsMissingWatchlistDataAndProviderWarnings(t *testing.T) {
	report := Analyze(nil, nil, []string{"funding rate unavailable"})
	if len(report.Crypto) != 0 || len(report.Stocks) != 0 {
		t.Fatalf("rows = crypto %d stocks %d", len(report.Crypto), len(report.Stocks))
	}
	if report.CryptoSummary != "暂无主流加密行情" || report.StockSummary != "暂无美股行情" {
		t.Fatalf("summaries = %q / %q", report.CryptoSummary, report.StockSummary)
	}
	if len(report.Warnings) != 1 || report.Warnings[0] != "funding rate unavailable" {
		t.Fatalf("warnings = %v", report.Warnings)
	}
}
