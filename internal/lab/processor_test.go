package lab

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestProcessorCreatesSignalsPerVersionFromOneParentSnapshot(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{{
		At: t0, Price: floatPtr(.001), RawScore: floatPtr(54), RawTier: "WATCH",
		EffectiveTier: "WATCH", EvidenceConfidence: "HIGH", EvidenceAvailable: 5, EvidenceTotal: 5,
	}})
	db, err := sql.Open("sqlite", corePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO score_versions(version,config_json,created_at) VALUES ('V1.1','{"version":"V1.1"}',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO snapshot_scores(
		snapshot_id,score_version,evaluation_role,raw_score,raw_tier,effective_tier,
		evidence_available,evidence_total,evidence_confidence,score_breakdown_json,risk_flags_json,created_at
	) VALUES (1,'V1.1','CHALLENGER',70,'FAST_RISING','FAST_RISING',5,5,'HIGH','{}','[]',?)`, t0.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, repo, processor := openProcessorFixture(t, corePath, t0)
	result, err := processor.ProcessCycle(context.Background())
	if err != nil {
		t.Fatalf("ProcessCycle() error = %v", err)
	}
	if result.SourceSnapshotsProcessed != 2 || result.SignalsCreated != 4 {
		t.Fatalf("cycle result = %+v, want two version rows and four Challenger signals", result)
	}
	var championSignals, challengerSignals, distinctParents int
	if err := repo.db.QueryRow(`SELECT
		SUM(CASE WHEN score_version='FOMO_SCORE_V1.0' THEN 1 ELSE 0 END),
		SUM(CASE WHEN score_version='V1.1' THEN 1 ELSE 0 END),
		COUNT(DISTINCT signal_snapshot_id)
		FROM signals`).Scan(&championSignals, &challengerSignals, &distinctParents); err != nil {
		t.Fatal(err)
	}
	if championSignals != 0 || challengerSignals != 4 || distinctParents != 1 {
		t.Fatalf("signals champion/challenger/parents = %d/%d/%d", championSignals, challengerSignals, distinctParents)
	}
}

func TestProcessCyclePersistsActivationOnceAndClassifiesBoundary(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{
		{At: t0, Price: floatPtr(.001), RawScore: floatPtr(54), RawTier: "WATCH", EffectiveTier: "WATCH", EvidenceConfidence: "LOW", EvidenceAvailable: 2, EvidenceTotal: 5},
		{At: t0.Add(time.Minute), Price: floatPtr(.002), RawScore: floatPtr(70), RawTier: "FAST_RISING", EffectiveTier: "FAST_RISING", EvidenceConfidence: "GOOD", EvidenceAvailable: 4, EvidenceTotal: 5},
	})
	source, repo, processor := openProcessorFixture(t, corePath, t0.Add(2*time.Minute))
	first, err := processor.ProcessCycle(context.Background())
	if err != nil {
		t.Fatalf("first ProcessCycle() error = %v", err)
	}
	if first.ActivationSnapshotID != 2 || first.SourceSnapshotsProcessed != 2 || first.SignalsCreated != 4 {
		t.Fatalf("first cycle = %+v", first)
	}

	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(3 * time.Minute), Price: floatPtr(.003), RawScore: floatPtr(91), RawTier: "BREAKOUT", EffectiveTier: "BREAKOUT", EvidenceConfidence: "HIGH", EvidenceAvailable: 5, EvidenceTotal: 5})
	second, err := processor.ProcessCycle(context.Background())
	if err != nil {
		t.Fatalf("second ProcessCycle() error = %v", err)
	}
	if second.ActivationSnapshotID != 2 || second.SourceSnapshotsProcessed != 1 || second.SignalsCreated != 4 {
		t.Fatalf("second cycle = %+v", second)
	}
	wantSources := map[int]string{55: "BACKFILL", 60: "BACKFILL", 65: "BACKFILL", 70: "BACKFILL", 75: "LIVE", 80: "LIVE", 85: "LIVE", 90: "LIVE"}
	rows, err := repo.db.Query(`SELECT threshold,signal_source FROM signals ORDER BY threshold`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var threshold int
		var signalSource string
		if err := rows.Scan(&threshold, &signalSource); err != nil {
			t.Fatal(err)
		}
		if wantSources[threshold] != signalSource {
			t.Fatalf("threshold %d source = %s", threshold, signalSource)
		}
	}

	reopened, err := NewProcessor(source, repo, func() time.Time { return t0.Add(4 * time.Minute) }).ProcessCycle(context.Background())
	if err != nil {
		t.Fatalf("restart ProcessCycle() error = %v", err)
	}
	if reopened.ActivationSnapshotID != 2 || reopened.SignalsCreated != 0 {
		t.Fatalf("restart cycle = %+v", reopened)
	}
	assertCount(t, repo, "signals", 8)
}

func TestRaw88CreatesSevenSignalsOnSameSnapshot(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{{At: t0, Price: floatPtr(.001), RawScore: floatPtr(88), RawTier: "BREAKOUT", EffectiveTier: "WATCH", EvidenceConfidence: "LOW", EvidenceAvailable: 2, EvidenceTotal: 5}})
	_, repo, processor := openProcessorFixture(t, corePath, t0)
	result, err := processor.ProcessCycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SignalsCreated != 7 {
		t.Fatalf("signals created = %d, want 7", result.SignalsCreated)
	}
	var distinctSnapshots, maximumThreshold int
	if err := repo.db.QueryRow(`SELECT COUNT(DISTINCT signal_snapshot_id),MAX(threshold) FROM signals`).Scan(&distinctSnapshots, &maximumThreshold); err != nil {
		t.Fatal(err)
	}
	if distinctSnapshots != 1 || maximumThreshold != 85 {
		t.Fatalf("distinct snapshots=%d max threshold=%d", distinctSnapshots, maximumThreshold)
	}
	var effectiveTier, confidence string
	var available, total int
	if err := repo.db.QueryRow(`SELECT effective_tier,evidence_confidence,evidence_available,evidence_total FROM signals LIMIT 1`).Scan(&effectiveTier, &confidence, &available, &total); err != nil {
		t.Fatal(err)
	}
	if effectiveTier != "WATCH" || confidence != "LOW" || available != 2 || total != 5 {
		t.Fatalf("saved gate evidence = %s/%s/%d/%d", effectiveTier, confidence, available, total)
	}
}

func TestRecrossDoesNotCreateAnotherSignal(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{
		{At: t0, Price: floatPtr(.001), RawScore: floatPtr(60), EffectiveTier: "WATCH"},
		{At: t0.Add(time.Minute), Price: floatPtr(.0008), RawScore: floatPtr(40), EffectiveTier: "HIDDEN"},
		{At: t0.Add(2 * time.Minute), Price: floatPtr(.0012), RawScore: floatPtr(60), EffectiveTier: "WATCH"},
	})
	_, repo, processor := openProcessorFixture(t, corePath, t0)
	if _, err := processor.ProcessCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, "signals", 2)
	var maximumSnapshot int64
	if err := repo.db.QueryRow(`SELECT MAX(signal_snapshot_id) FROM signals`).Scan(&maximumSnapshot); err != nil {
		t.Fatal(err)
	}
	if maximumSnapshot != 1 {
		t.Fatalf("recross replaced first signal snapshot with %d", maximumSnapshot)
	}
}

func TestEntryUsesSignalSnapshotPriceOrUnavailable(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{{At: t0, RawScore: floatPtr(60), EffectiveTier: "WATCH"}})
	_, repo, processor := openProcessorFixture(t, corePath, t0)
	if _, err := processor.ProcessCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	var unavailable, notEvaluable int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM paper_entries WHERE entry_status='UNAVAILABLE_PRICE' AND entry_price_usd IS NULL`).Scan(&unavailable); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM horizon_outcomes WHERE status='NOT_EVALUABLE' AND status_reason='UNAVAILABLE_ENTRY_PRICE'`).Scan(&notEvaluable); err != nil {
		t.Fatal(err)
	}
	if unavailable != 2 || notEvaluable != 14 {
		t.Fatalf("unavailable entries=%d not evaluable outcomes=%d", unavailable, notEvaluable)
	}
	var mismatch int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM paper_entries e JOIN signals s ON s.id=e.signal_id WHERE e.entry_snapshot_id<>s.signal_snapshot_id`).Scan(&mismatch); err != nil {
		t.Fatal(err)
	}
	if mismatch != 0 {
		t.Fatalf("entry/signal snapshot mismatches = %d", mismatch)
	}
}

func TestProcessChunkRollbackDoesNotAdvanceWatermark(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{{At: t0, Price: floatPtr(.001), RawScore: floatPtr(60), EffectiveTier: "WATCH"}})
	_, repo, processor := openProcessorFixture(t, corePath, t0)
	if _, err := repo.db.Exec(`CREATE TRIGGER fail_signal BEFORE INSERT ON signals WHEN NEW.token_id='bsc:0xabc' BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := processor.ProcessCycle(context.Background()); err == nil {
		t.Fatal("ProcessCycle() succeeded through injected transaction failure")
	}
	state, err := repo.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.LastProcessedSnapshotID != 0 {
		t.Fatalf("watermark advanced to %d", state.LastProcessedSnapshotID)
	}
	assertCount(t, repo, "signals", 0)
	if _, err := repo.db.Exec(`DROP TRIGGER fail_signal`); err != nil {
		t.Fatal(err)
	}
	if _, err := processor.ProcessCycle(context.Background()); err != nil {
		t.Fatalf("retry ProcessCycle() error = %v", err)
	}
	assertCount(t, repo, "signals", 2)
	if _, err := processor.ProcessCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, "signals", 2)
}

func openProcessorFixture(t *testing.T, corePath string, now time.Time) (*Source, *Repository, *Processor) {
	t.Helper()
	source, err := OpenSource(corePath)
	if err != nil {
		t.Fatalf("OpenSource() error = %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })
	repo := openTestRepository(t)
	processor := NewProcessor(source, repo, func() time.Time { return now })
	return source, repo, processor
}

func assertCount(t *testing.T, repo *Repository, table string, want int) {
	t.Helper()
	allowed := map[string]bool{"signals": true, "paper_entries": true, "horizon_outcomes": true, "analysis_runs": true, "threshold_statistics": true}
	if !allowed[table] {
		t.Fatalf("unsupported count table %q", table)
	}
	var got int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}
