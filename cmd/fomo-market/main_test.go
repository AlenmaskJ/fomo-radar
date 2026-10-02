package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/markets"
	"github.com/alen1/fomo-radar/internal/providers"
)

type fakeMarketReader struct {
	result providers.OKXMarketResult
	err    error
}

func (f fakeMarketReader) Read(context.Context) (providers.OKXMarketResult, error) {
	return f.result, f.err
}

type fakeSnapshotStore struct {
	previous map[string]domain.DerivativesSnapshot
	inserted []domain.DerivativesSnapshot
}

func (f *fakeSnapshotStore) LatestDerivativesSnapshotBefore(_ context.Context, _ domain.AssetClass, instrumentID string, _ time.Time) (domain.DerivativesSnapshot, bool, error) {
	value, ok := f.previous[instrumentID]
	return value, ok, nil
}

func (f *fakeSnapshotStore) InsertDerivativesSnapshots(_ context.Context, snapshots []domain.DerivativesSnapshot) error {
	f.inserted = append(f.inserted, snapshots...)
	return nil
}

func TestScanOnceUsesPreviousOpenInterestAndPersistsCurrentSnapshot(t *testing.T) {
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	current := domain.DerivativesSnapshot{
		AssetClass: domain.AssetClassCrypto, InstrumentID: "BTC-USDT-SWAP", Symbol: "BTC",
		CollectedAt: at, PriceUSD: 60_000, Change24hPct: 4,
		OpenInterestUSD: 110, OpenInterestAvailable: true,
	}
	reader := fakeMarketReader{result: providers.OKXMarketResult{
		Snapshots: []domain.DerivativesSnapshot{current, {
			AssetClass: domain.AssetClassCrypto, InstrumentID: "MEME-USDT-SWAP", Symbol: "MEME", CollectedAt: at,
		}}, Warnings: []string{"funding rate unavailable"},
	}}
	database := &fakeSnapshotStore{previous: map[string]domain.DerivativesSnapshot{
		"BTC-USDT-SWAP": {CollectedAt: at.Add(-2 * time.Minute), PriceUSD: 59_000, OpenInterestUSD: 100, OpenInterestAvailable: true},
	}}

	report, err := scanOnce(context.Background(), reader, database)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Crypto) != 1 || report.Crypto[0].OpenInterestChangePct == nil || *report.Crypto[0].OpenInterestChangePct < 9.99 {
		t.Fatalf("crypto report = %+v", report.Crypto)
	}
	if !strings.Contains(report.Crypto[0].Signal, "增仓") || len(report.Warnings) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(database.inserted) != 1 || database.inserted[0].InstrumentID != current.InstrumentID {
		t.Fatalf("inserted = %+v", database.inserted)
	}
}

func TestWriteReportPrintsChineseCryptoAndStockSections(t *testing.T) {
	change := 10.0
	report := markets.Report{
		Crypto: []markets.Row{{DerivativesSnapshot: domain.DerivativesSnapshot{
			Symbol: "BTC", PriceUSD: 60_000, Change24hPct: 5, Turnover24hUSD: 1_500_000_000,
			OpenInterestUSD: 500_000_000, OpenInterestAvailable: true,
			FundingRate: 0.0006, FundingRateAvailable: true,
		}, OpenInterestChangePct: &change, Signal: "强势 / 增仓 / 多头拥挤"}},
		Stocks: []markets.Row{{DerivativesSnapshot: domain.DerivativesSnapshot{
			Symbol: "AAPL", PriceUSD: 230, Change24hPct: -1.2, Turnover24hUSD: 25_000_000,
		}, Signal: "偏弱"}},
		CryptoSummary: "上涨 1/1", StockSummary: "上涨 0/1",
		Warnings: []string{"funding rate unavailable"},
	}
	var output bytes.Buffer
	if err := writeReport(&output, report); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"主流加密", "BTC", "+5.00%", "+10.00%", "0.0600%", "美股", "AAPL", "偏弱", "降级提示"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestWriteOpportunityReportPrintsDynamicSymbolAndTradePlan(t *testing.T) {
	report := domain.OpportunityReport{Run: domain.OpportunityRun{PoolSize: 45, CandidateCount: 1, Status: domain.OpportunityRunCompleted}, Opportunities: []domain.Opportunity{{
		Instrument: domain.MarketInstrument{InstrumentID: "SUI-USDT-SWAP", Symbol: "SUI"}, FOMOScore: 78, OpportunityScore: 81, Stage: domain.StageStarting,
		TradePlan: &domain.TradePlan{Entries: []domain.EntryLevel{{Price: 3.1, Weight: 0.3}}, StopPrice: 2.8, Targets: []domain.TargetLevel{{Price: 3.8}, {Price: 4.2}}},
	}}}
	var output bytes.Buffer
	if err := writeOpportunityReport(&output, report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SUI", "FOMO 78", "机会 81", "$3.1000", "$2.8000", "$3.8000"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q: %s", want, output.String())
		}
	}
}

func TestParseOptionsRequiresExactlyOneModeAndReadsEnvironment(t *testing.T) {
	getenv := func(name string) string {
		switch name {
		case "FOMO_DB":
			return "env.db"
		case "FOMO_PROXY":
			return "http://127.0.0.1:10808"
		default:
			return ""
		}
	}
	got, err := parseOptions([]string{"-scan"}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if got.mode != modeScan || got.databasePath != "env.db" || got.proxy != "http://127.0.0.1:10808" {
		t.Fatalf("options = %+v", got)
	}
	if _, err := parseOptions(nil, getenv); err == nil {
		t.Fatal("missing mode should fail")
	}
	if _, err := parseOptions([]string{"-scan", "-watch"}, getenv); err == nil {
		t.Fatal("two modes should fail")
	}
	if _, err := parseOptions([]string{"-watch", "-interval", "0s"}, getenv); err == nil {
		t.Fatal("non-positive watch interval should fail")
	}
}

func TestScanOnceDoesNotPersistWhenReadFails(t *testing.T) {
	database := &fakeSnapshotStore{}
	_, err := scanOnce(context.Background(), fakeMarketReader{err: errors.New("offline")}, database)
	if err == nil || len(database.inserted) != 0 {
		t.Fatalf("scan error = %v, inserted = %d", err, len(database.inserted))
	}
}
