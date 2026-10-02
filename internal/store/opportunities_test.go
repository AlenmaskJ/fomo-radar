package store

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestStockRadarMigrationBackfillsCryptoAndRejectsUnknownClass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(marketOpportunitiesSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO market_opportunity_runs (started_at, finished_at, status, trigger) VALUES (1, 2, 'completed', 'manual')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	var assetClass string
	if err := migrated.db.QueryRow(`SELECT asset_class FROM market_opportunity_runs WHERE id = 1`).Scan(&assetClass); err != nil {
		t.Fatal(err)
	}
	if assetClass != string(domain.AssetClassCrypto) {
		t.Fatalf("legacy asset class = %q, want crypto", assetClass)
	}
	var indexes int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('market_opportunity_runs') WHERE name = 'idx_market_runs_class_finished'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 1 {
		t.Fatalf("stock radar index count = %d, want 1", indexes)
	}
	if _, err := migrated.db.Exec(`INSERT INTO market_opportunity_runs (asset_class, started_at, status, trigger) VALUES ('forex', 3, 'running', 'manual')`); err == nil {
		t.Fatal("unknown asset class should violate the database constraint")
	}
}

func TestLatestOpportunityReportIsScopedByAssetClass(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "scoped-opportunities.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	base := time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC)

	publish := func(assetClass domain.AssetClass, at time.Time, status domain.OpportunityRunStatus, opportunities []domain.Opportunity) int64 {
		t.Helper()
		id, err := s.StartOpportunityRun(ctx, assetClass, "manual", at)
		if err != nil {
			t.Fatal(err)
		}
		run := domain.OpportunityRun{ID: id, AssetClass: assetClass, StartedAt: at, FinishedAt: at.Add(time.Second), Status: status, Trigger: "manual"}
		if err := s.FinishOpportunityRun(ctx, run, opportunities); err != nil {
			t.Fatal(err)
		}
		return id
	}

	cryptoID := publish(domain.AssetClassCrypto, base, domain.OpportunityRunCompleted, testOpportunities())
	stockID := publish(domain.AssetClassStock, base.Add(time.Minute), domain.OpportunityRunDegraded, testOpportunities()[:1])
	publish(domain.AssetClassStock, base.Add(2*time.Minute), domain.OpportunityRunFailed, nil)

	crypto, err := s.LatestOpportunityReport(ctx, domain.AssetClassCrypto)
	if err != nil {
		t.Fatal(err)
	}
	stock, err := s.LatestOpportunityReport(ctx, domain.AssetClassStock)
	if err != nil {
		t.Fatal(err)
	}
	if crypto.Run.ID != cryptoID || crypto.Run.AssetClass != domain.AssetClassCrypto || len(crypto.Opportunities) != 2 {
		t.Fatalf("crypto report = %+v", crypto)
	}
	if stock.Run.ID != stockID || stock.Run.AssetClass != domain.AssetClassStock || len(stock.Opportunities) != 1 {
		t.Fatalf("stock report = %+v", stock)
	}

	emptyID := publish(domain.AssetClassStock, base.Add(3*time.Minute), domain.OpportunityRunCompleted, nil)
	stock, err = s.LatestOpportunityReport(ctx, domain.AssetClassStock)
	if err != nil || stock.Run.ID != emptyID || len(stock.Opportunities) != 0 {
		t.Fatalf("empty stock report/error = %+v/%v", stock, err)
	}
	if _, err := s.LatestOpportunityReport(ctx, domain.AssetClass("forex")); err == nil {
		t.Fatal("unknown asset class should be rejected")
	}
}

func TestFinishOpportunityRunPublishesOnlyCompleteRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "opportunities.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	startedAt := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	firstID, err := store.StartOpportunityRun(ctx, domain.AssetClassCrypto, "manual", startedAt)
	if err != nil {
		t.Fatal(err)
	}
	first := domain.OpportunityRun{ID: firstID, AssetClass: domain.AssetClassCrypto, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute), Status: domain.OpportunityRunCompleted, Trigger: "manual", PoolSize: 2, CandidateCount: 2}
	if err := store.FinishOpportunityRun(ctx, first, testOpportunities()); err != nil {
		t.Fatal(err)
	}
	failedID, err := store.StartOpportunityRun(ctx, domain.AssetClassCrypto, "scheduled", startedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	failed := domain.OpportunityRun{ID: failedID, AssetClass: domain.AssetClassCrypto, StartedAt: startedAt.Add(2 * time.Minute), FinishedAt: startedAt.Add(3 * time.Minute), Status: domain.OpportunityRunFailed, Trigger: "scheduled", ErrorSummary: "batch unavailable"}
	if err := store.FinishOpportunityRun(ctx, failed, nil); err != nil {
		t.Fatal(err)
	}
	report, err := store.LatestOpportunityReport(ctx, domain.AssetClassCrypto)
	if err != nil {
		t.Fatal(err)
	}
	if report.Run.ID != firstID || len(report.Opportunities) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if report.Opportunities[0].Instrument.InstrumentID != "AAA-USDT-SWAP" {
		t.Fatalf("ordering = %+v", report.Opportunities)
	}
}

func TestFinishOpportunityRunRollsBackInvalidPayload(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	startedAt := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	id, err := store.StartOpportunityRun(ctx, domain.AssetClassCrypto, "manual", startedAt)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.OpportunityRun{ID: id, AssetClass: domain.AssetClassCrypto, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute), Status: domain.OpportunityRunCompleted, Trigger: "manual", CandidateCount: 1}
	bad := testOpportunities()[0]
	bad.CurrentPrice = math.NaN()
	if err := store.FinishOpportunityRun(ctx, run, []domain.Opportunity{bad}); err == nil {
		t.Fatal("NaN payload should fail")
	}
	var status string
	var count int
	if err := store.db.QueryRow(`SELECT status FROM market_opportunity_runs WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM market_opportunities WHERE run_id = ?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if status != "running" || count != 0 {
		t.Fatalf("status/count = %q/%d", status, count)
	}
}

func TestLatestOpportunityReportAllowsEmptyCompletedRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	startedAt := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	id, err := store.StartOpportunityRun(ctx, domain.AssetClassCrypto, "startup", startedAt)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.OpportunityRun{ID: id, AssetClass: domain.AssetClassCrypto, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute), Status: domain.OpportunityRunCompleted, Trigger: "startup"}
	if err := store.FinishOpportunityRun(ctx, run, nil); err != nil {
		t.Fatal(err)
	}
	report, err := store.LatestOpportunityReport(ctx, domain.AssetClassCrypto)
	if err != nil || report.Run.ID != id || len(report.Opportunities) != 0 {
		t.Fatalf("report/error = %+v/%v", report, err)
	}
}

func TestDerivativesSnapshotAtOrBeforeIncludesExactBoundary(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "oi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	snapshot := domain.DerivativesSnapshot{AssetClass: domain.AssetClassCrypto, InstrumentID: "BTC-USDT-SWAP", Symbol: "BTC", CollectedAt: at, SourceTime: at, PriceUSD: 100, Turnover24hUSD: 10, OpenInterestUSD: 5, OpenInterestAvailable: true}
	if err := store.InsertDerivativesSnapshots(ctx, []domain.DerivativesSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.DerivativesSnapshotAtOrBefore(ctx, snapshot.AssetClass, snapshot.InstrumentID, at)
	if err != nil || !ok || got.OpenInterestUSD != 5 {
		t.Fatalf("snapshot/ok/error = %+v/%v/%v", got, ok, err)
	}
	if _, ok, err := store.DerivativesSnapshotAtOrBefore(ctx, snapshot.AssetClass, snapshot.InstrumentID, at.Add(-time.Millisecond)); err != nil || ok {
		t.Fatalf("unexpected earlier result: ok=%v err=%v", ok, err)
	}
}

func testOpportunities() []domain.Opportunity {
	return []domain.Opportunity{
		{Instrument: domain.MarketInstrument{InstrumentID: "BBB-USDT-SWAP", Symbol: "BBB", TickSize: 0.01}, FOMOScore: 80, OpportunityScore: 70, Stage: domain.StageConfirmed},
		{Instrument: domain.MarketInstrument{InstrumentID: "AAA-USDT-SWAP", Symbol: "AAA", TickSize: 0.01}, FOMOScore: 75, OpportunityScore: 90, Stage: domain.StageStarting},
	}
}
