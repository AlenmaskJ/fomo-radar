package lab

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"
)

func TestHorizonSelectionAndPathStopAtSourceHighWatermark(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath, source, repo := setupEvaluableSignal(t, t0)
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(4*time.Minute + 59*time.Second), Price: floatPtr(110)})
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(5*time.Minute + 30*time.Second), Price: floatPtr(120)})
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(6 * time.Minute), Price: floatPtr(150)})
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(-time.Minute), Price: floatPtr(1000)})
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(5*time.Minute + 10*time.Second), Price: floatPtr(130)})

	evaluator := NewEvaluator(source, repo, func() time.Time { return t0.Add(7 * time.Minute) })
	frontierAt := t0.Add(6 * time.Minute)
	result, err := evaluator.EvaluatePending(context.Background(), SourceFrontier{HighWatermark: 5, ObservationAt: &frontierAt}, 100)
	if err != nil {
		t.Fatalf("EvaluatePending() error = %v", err)
	}
	if result.Matured != 1 {
		t.Fatalf("outcome result = %+v", result)
	}
	outcome := readOutcome(t, repo, "5m")
	if outcome.Status != "MATURED" || outcome.SelectedSnapshotID.Int64 != 3 || outcome.EvaluationHighWatermark.Int64 != 5 {
		t.Fatalf("5m outcome = %+v", outcome)
	}
	assertDecimal(t, "return", outcome.ReturnDecimal, .20)
	assertDecimal(t, "mfe", outcome.MFEDecimal, .20)
	assertDecimal(t, "mae", outcome.MAEDecimal, 0)
}

func TestWindowDoesNotCloseFromWallClockOrEqualFrontier(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	_, source, repo := setupEvaluableSignal(t, t0)
	evaluator := NewEvaluator(source, repo, func() time.Time { return t0.Add(7 * 24 * time.Hour) })
	windowEnd := t0.Add(8 * time.Minute)

	if _, err := evaluator.EvaluatePending(context.Background(), SourceFrontier{HighWatermark: 1, ObservationAt: &t0}, 100); err != nil {
		t.Fatal(err)
	}
	if got := readOutcome(t, repo, "5m").Status; got != "PENDING" {
		t.Fatalf("wall clock closed outcome as %s", got)
	}
	if _, err := evaluator.EvaluatePending(context.Background(), SourceFrontier{HighWatermark: 1, ObservationAt: &windowEnd}, 100); err != nil {
		t.Fatal(err)
	}
	if got := readOutcome(t, repo, "5m").Status; got != "PENDING" {
		t.Fatalf("equal frontier closed outcome as %s", got)
	}
	afterEnd := windowEnd.Add(time.Millisecond)
	result, err := evaluator.EvaluatePending(context.Background(), SourceFrontier{HighWatermark: 1, ObservationAt: &afterEnd}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.Insufficient == 0 || readOutcome(t, repo, "5m").Status != "INSUFFICIENT_DATA" {
		t.Fatalf("past frontier result=%+v outcome=%+v", result, readOutcome(t, repo, "5m"))
	}
}

func TestPerHorizonMFEAndMAEUseIndependentPaths(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath, source, repo := setupEvaluableSignal(t, t0)
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(time.Minute), Price: floatPtr(90)})
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(5 * time.Minute), Price: floatPtr(140)})
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(14 * time.Minute), Price: floatPtr(80)})
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(15 * time.Minute), Price: floatPtr(180)})
	frontierAt := t0.Add(15 * time.Minute)
	evaluator := NewEvaluator(source, repo, func() time.Time { return frontierAt })
	result, err := evaluator.EvaluatePending(context.Background(), SourceFrontier{HighWatermark: 5, ObservationAt: &frontierAt}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.Matured != 2 {
		t.Fatalf("matured = %d, want 2", result.Matured)
	}
	five := readOutcome(t, repo, "5m")
	assertDecimal(t, "5m return", five.ReturnDecimal, .40)
	assertDecimal(t, "5m mfe", five.MFEDecimal, .40)
	assertDecimal(t, "5m mae", five.MAEDecimal, -.10)
	fifteen := readOutcome(t, repo, "15m")
	assertDecimal(t, "15m return", fifteen.ReturnDecimal, .80)
	assertDecimal(t, "15m mfe", fifteen.MFEDecimal, .80)
	assertDecimal(t, "15m mae", fifteen.MAEDecimal, -.20)
}

func TestHorizonIncludesWindowEndAndDoesNotReachOutside(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath, source, repo := setupEvaluableSignal(t, t0)
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(8 * time.Minute), Price: floatPtr(125)})
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(8*time.Minute + time.Millisecond), Price: floatPtr(200)})
	frontierAt := t0.Add(8*time.Minute + time.Millisecond)
	evaluator := NewEvaluator(source, repo, func() time.Time { return frontierAt })
	if _, err := evaluator.EvaluatePending(context.Background(), SourceFrontier{HighWatermark: 3, ObservationAt: &frontierAt}, 100); err != nil {
		t.Fatal(err)
	}
	outcome := readOutcome(t, repo, "5m")
	if outcome.SelectedSnapshotID.Int64 != 2 {
		t.Fatalf("selected snapshot = %d, want exact window-end ID 2", outcome.SelectedSnapshotID.Int64)
	}
	assertDecimal(t, "window-end return", outcome.ReturnDecimal, .25)
}

func TestUnavailableEntryIsNotEvaluableNotInsufficient(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{{At: t0, RawScore: floatPtr(55), EffectiveTier: "WATCH"}})
	_, repo, processor := openProcessorFixture(t, corePath, t0)
	if _, err := processor.ProcessCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	outcome := readOutcome(t, repo, "5m")
	if outcome.Status != "NOT_EVALUABLE" || outcome.StatusReason != "UNAVAILABLE_ENTRY_PRICE" || outcome.ReturnDecimal.Valid {
		t.Fatalf("unavailable outcome = %+v", outcome)
	}
}

type outcomeRow struct {
	Status                      string
	StatusReason                string
	SelectedSnapshotID          sql.NullInt64
	ReturnDecimal               sql.NullFloat64
	MFEDecimal                  sql.NullFloat64
	MAEDecimal                  sql.NullFloat64
	EvaluationHighWatermark     sql.NullInt64
	SourceObservationFrontierAt sql.NullInt64
}

func setupEvaluableSignal(t *testing.T, at time.Time) (string, *Source, *Repository) {
	t.Helper()
	corePath := buildCoreFixture(t, []fixtureSnapshot{{At: at, Price: floatPtr(100), RawScore: floatPtr(55), RawTier: "WATCH", EffectiveTier: "WATCH", EvidenceConfidence: "HIGH", EvidenceAvailable: 5, EvidenceTotal: 5}})
	source, repo, processor := openProcessorFixture(t, corePath, at)
	if _, err := processor.ProcessCycle(context.Background()); err != nil {
		t.Fatalf("seed ProcessCycle() error = %v", err)
	}
	return corePath, source, repo
}

func readOutcome(t *testing.T, repo *Repository, horizon string) outcomeRow {
	t.Helper()
	var result outcomeRow
	err := repo.db.QueryRow(`
		SELECT status,status_reason,selected_source_snapshot_id,return_decimal,mfe_decimal,mae_decimal,
		       evaluation_source_high_watermark,source_observation_frontier_at
		FROM horizon_outcomes WHERE horizon=? ORDER BY id LIMIT 1`, horizon).Scan(
		&result.Status, &result.StatusReason, &result.SelectedSnapshotID, &result.ReturnDecimal,
		&result.MFEDecimal, &result.MAEDecimal, &result.EvaluationHighWatermark,
		&result.SourceObservationFrontierAt)
	if err != nil {
		t.Fatalf("read %s outcome: %v", horizon, err)
	}
	return result
}

func assertDecimal(t *testing.T, label string, got sql.NullFloat64, want float64) {
	t.Helper()
	if !got.Valid || math.Abs(got.Float64-want) > 1e-9 {
		t.Fatalf("%s = %+v, want %.9f", label, got, want)
	}
}
