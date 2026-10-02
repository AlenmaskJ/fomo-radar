package web

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/store"
)

func TestSQLiteRepositoryMarketOpportunityHistoryIsScopedByAssetClass(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "scoped-history.db")
	writer, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 30, 4, 0, 0, 0, time.UTC)
	writeOpportunityRunForClass(t, writer, domain.AssetClassCrypto, base, domain.OpportunityRunCompleted, historyOpportunity("AAA", 100))
	writeOpportunityRunForClass(t, writer, domain.AssetClassStock, base.Add(time.Minute), domain.OpportunityRunCompleted, historyOpportunity("AAPL", 200))
	writeOpportunityRunForClass(t, writer, domain.AssetClassCrypto, base.Add(5*time.Minute), domain.OpportunityRunCompleted, historyOpportunity("AAA", 101))
	writeOpportunityRunForClass(t, writer, domain.AssetClassStock, base.Add(6*time.Minute), domain.OpportunityRunDegraded, historyOpportunity("AAPL", 201))
	writeOpportunityRunForClass(t, writer, domain.AssetClassStock, base.Add(10*time.Minute), domain.OpportunityRunFailed)
	if err := writer.InsertDerivativesSnapshots(ctx, []domain.DerivativesSnapshot{
		historyDerivativesForClass(domain.AssetClassCrypto, "AAA", base.Add(7*time.Minute), 110),
		historyDerivativesForClass(domain.AssetClassStock, "AAPL", base.Add(7*time.Minute), 220),
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err := OpenSQLiteRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	filter := OpportunityHistoryFilter{Window: historyWindow24H, Status: historyStatusAll, Sort: historySortRate, Direction: "desc", AsOf: base.Add(20 * time.Minute)}
	crypto, err := repository.MarketOpportunityHistory(ctx, domain.AssetClassCrypto, filter)
	if err != nil {
		t.Fatal(err)
	}
	stock, err := repository.MarketOpportunityHistory(ctx, domain.AssetClassStock, filter)
	if err != nil {
		t.Fatal(err)
	}
	if crypto.EffectiveScans != 2 || len(crypto.Rows) != 1 || crypto.Rows[0].Symbol != "AAA" || crypto.Rows[0].EvaluationPrice == nil || *crypto.Rows[0].EvaluationPrice != 110 {
		t.Fatalf("crypto history = %+v", crypto)
	}
	if stock.EffectiveScans != 2 || len(stock.Rows) != 1 || stock.Rows[0].Symbol != "AAPL" || stock.Rows[0].EvaluationPrice == nil || *stock.Rows[0].EvaluationPrice != 220 {
		t.Fatalf("stock history = %+v", stock)
	}
	if _, err := repository.MarketOpportunityHistory(ctx, domain.AssetClass("forex"), filter); err == nil {
		t.Fatal("unknown asset class should be rejected")
	}
}

func TestSQLiteRepositoryMarketOpportunityHistoryDeduplicatesBucketsAndFixesEndedReturn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	writer, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)

	writeOpportunityRun(t, writer, base, domain.OpportunityRunCompleted, historyOpportunity("AAA", 100))
	writeOpportunityRun(t, writer, base.Add(time.Minute), domain.OpportunityRunCompleted, historyOpportunity("AAA", 101))
	writeOpportunityRun(t, writer, base.Add(5*time.Minute), domain.OpportunityRunDegraded, historyOpportunity("AAA", 102))
	writeOpportunityRun(t, writer, base.Add(10*time.Minute), domain.OpportunityRunCompleted)
	writeOpportunityRun(t, writer, base.Add(15*time.Minute), domain.OpportunityRunCompleted)
	writeOpportunityRun(t, writer, base.Add(20*time.Minute), domain.OpportunityRunCompleted)
	if err := writer.InsertDerivativesSnapshots(ctx, []domain.DerivativesSnapshot{
		historyDerivatives("AAA", base.Add(19*time.Minute), 109),
		historyDerivatives("AAA", base.Add(21*time.Minute), 130),
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err := OpenSQLiteRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	page, err := repository.MarketOpportunityHistory(ctx, domain.AssetClassCrypto, OpportunityHistoryFilter{
		Window: historyWindow24H, Status: historyStatusAll, Sort: historySortRate, Direction: "desc", AsOf: base.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.EffectiveScans != 5 || len(page.Rows) != 1 {
		t.Fatalf("page = %+v", page)
	}
	row := page.Rows[0]
	if row.Status != historyStatusEnded || row.Appearances != 2 || row.FirstPrice != 101 || row.EvaluationPrice == nil || *row.EvaluationPrice != 109 {
		t.Fatalf("row = %+v", row)
	}
	if row.ReturnPct == nil || math.Abs(*row.ReturnPct-7.920792) > 0.0001 {
		t.Fatalf("return = %v", row.ReturnPct)
	}
}

func TestSQLiteRepositoryMarketOpportunityHistoryKeepsPreWindowEpisodeStart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pre-window.db")
	writer, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	windowStart := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	writeOpportunityRun(t, writer, windowStart.Add(-10*time.Minute), domain.OpportunityRunCompleted, historyOpportunity("AAA", 90))
	writeOpportunityRun(t, writer, windowStart.Add(-5*time.Minute), domain.OpportunityRunCompleted, historyOpportunity("AAA", 95))
	writeOpportunityRun(t, writer, windowStart, domain.OpportunityRunCompleted, historyOpportunity("AAA", 100))
	writeOpportunityRun(t, writer, windowStart.Add(5*time.Minute), domain.OpportunityRunCompleted)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err := OpenSQLiteRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	page, err := repository.MarketOpportunityHistory(ctx, domain.AssetClassCrypto, OpportunityHistoryFilter{
		Window: historyWindow24H, Status: historyStatusAll, Sort: historySortRate, Direction: "desc", AsOf: windowStart.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.EffectiveScans != 2 || len(page.Rows) != 1 {
		t.Fatalf("page = %+v", page)
	}
	if want := windowStart.Add(5*time.Minute + time.Second); !page.GeneratedAt.Equal(want) {
		t.Fatalf("GeneratedAt = %v, want %v", page.GeneratedAt, want)
	}
	row := page.Rows[0]
	if !row.FirstListedAt.Equal(windowStart.Add(-10*time.Minute).Add(time.Second)) || row.FirstPrice != 90 || row.Appearances != 1 || row.ExperiencedScans != 2 {
		t.Fatalf("row = %+v", row)
	}
}

func TestSQLiteRepositoryMarketOpportunityHistoryReturnsNoRowsWithoutWindowScans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-window-scans.db")
	writer, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	writeOpportunityRun(t, writer, base, domain.OpportunityRunCompleted, historyOpportunity("AAA", 100))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err := OpenSQLiteRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	page, err := repository.MarketOpportunityHistory(context.Background(), domain.AssetClassCrypto, OpportunityHistoryFilter{
		Window: historyWindow24H, Status: historyStatusAll, Sort: historySortRate, Direction: "desc", AsOf: base.Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.EffectiveScans != 0 || len(page.Rows) != 0 || !page.GeneratedAt.IsZero() {
		t.Fatalf("page = %+v", page)
	}
}

func TestSQLiteRepositoryMarketOpportunityHistoryDoesNotRepeatBucketAcrossPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paged-history.db")
	writer, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < historyRunPageSize+1; index++ {
		writeOpportunityRun(t, writer, base.Add(time.Duration(index)*5*time.Minute+time.Second), domain.OpportunityRunCompleted)
	}
	// 该条记录与分页边界上的有效扫描同桶，但完成时间更早。
	writeOpportunityRun(t, writer, base.Add(5*time.Minute), domain.OpportunityRunCompleted)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err := OpenSQLiteRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	runs, err := repository.loadAllEffectiveRuns(context.Background(), domain.AssetClassCrypto, base.Add(60*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != historyRunPageSize+1 {
		t.Fatalf("effective runs = %d, want %d", len(runs), historyRunPageSize+1)
	}
}

func writeOpportunityRun(t *testing.T, writer *store.Store, at time.Time, status domain.OpportunityRunStatus, opportunities ...domain.Opportunity) {
	writeOpportunityRunForClass(t, writer, domain.AssetClassCrypto, at, status, opportunities...)
}

func writeOpportunityRunForClass(t *testing.T, writer *store.Store, assetClass domain.AssetClass, at time.Time, status domain.OpportunityRunStatus, opportunities ...domain.Opportunity) {
	t.Helper()
	id, err := writer.StartOpportunityRun(context.Background(), assetClass, "manual", at)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.OpportunityRun{
		ID: id, AssetClass: assetClass, StartedAt: at, FinishedAt: at.Add(time.Second), Status: status, Trigger: "manual", PoolSize: 40,
	}
	if err := writer.FinishOpportunityRun(context.Background(), run, opportunities); err != nil {
		t.Fatal(err)
	}
}

func historyOpportunity(symbol string, price float64) domain.Opportunity {
	return domain.Opportunity{
		Instrument:       domain.MarketInstrument{InstrumentID: symbol + "-USDT-SWAP", Symbol: symbol},
		GeneratedAt:      time.Now().UTC(),
		CurrentPrice:     price,
		FOMOScore:        80,
		OpportunityScore: 80,
		Stage:            domain.StageStarting,
	}
}

func historyDerivatives(symbol string, at time.Time, price float64) domain.DerivativesSnapshot {
	return historyDerivativesForClass(domain.AssetClassCrypto, symbol, at, price)
}

func historyDerivativesForClass(assetClass domain.AssetClass, symbol string, at time.Time, price float64) domain.DerivativesSnapshot {
	return domain.DerivativesSnapshot{
		AssetClass: assetClass, InstrumentID: symbol + "-USDT-SWAP", Symbol: symbol,
		CollectedAt: at, SourceTime: at, PriceUSD: price, Turnover24hUSD: 1_000_000,
	}
}
