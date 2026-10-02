package lab

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHorizonsFreezeOffsetsAndTolerance(t *testing.T) {
	want := []Horizon{
		{Key: "5m", Offset: 5 * time.Minute, Tolerance: 3 * time.Minute},
		{Key: "15m", Offset: 15 * time.Minute, Tolerance: 3 * time.Minute},
		{Key: "30m", Offset: 30 * time.Minute, Tolerance: 3 * time.Minute},
		{Key: "1h", Offset: time.Hour, Tolerance: 10 * time.Minute},
		{Key: "3h", Offset: 3 * time.Hour, Tolerance: 10 * time.Minute},
		{Key: "6h", Offset: 6 * time.Hour, Tolerance: 10 * time.Minute},
		{Key: "24h", Offset: 24 * time.Hour, Tolerance: time.Hour},
	}
	if !reflect.DeepEqual(Horizons(), want) {
		t.Fatalf("Horizons() = %+v, want %+v", Horizons(), want)
	}
	got := Horizons()
	got[0].Offset = time.Second
	if Horizons()[0].Offset != 5*time.Minute {
		t.Fatal("Horizons() exposed mutable package state")
	}
}

func TestThresholdsAreFrozen(t *testing.T) {
	want := []int{55, 60, 65, 70, 75, 80, 85, 90}
	if !reflect.DeepEqual(Thresholds(), want) {
		t.Fatalf("Thresholds() = %v, want %v", Thresholds(), want)
	}
	got := Thresholds()
	got[0] = 0
	if Thresholds()[0] != 55 {
		t.Fatal("Thresholds() exposed mutable package state")
	}
}

func TestRepositoryInitializesOnceAndValidatesSourceIdentity(t *testing.T) {
	repo := openTestRepository(t)
	ctx := context.Background()
	initialized := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	registration := SourceRegistration{
		CanonicalPath:        `C:\data\fomo.db`,
		Identity:             "source-a",
		ActivationSnapshotID: 42,
		CurrentSourceMaximum: 42,
		InitializedAt:        initialized,
	}
	state, err := repo.InitializeOrValidate(ctx, registration)
	if err != nil {
		t.Fatalf("InitializeOrValidate() error = %v", err)
	}
	if state.ActivationSnapshotID != 42 || state.SourceDBIdentity != "source-a" || state.LastProcessedSnapshotID != 0 {
		t.Fatalf("state = %+v", state)
	}

	registration.ActivationSnapshotID = 99
	registration.CurrentSourceMaximum = 99
	state, err = repo.InitializeOrValidate(ctx, registration)
	if err != nil {
		t.Fatalf("restart InitializeOrValidate() error = %v", err)
	}
	if state.ActivationSnapshotID != 42 {
		t.Fatalf("restart activation = %d, want immutable 42", state.ActivationSnapshotID)
	}

	badIdentity := registration
	badIdentity.Identity = "source-b"
	if _, err := repo.InitializeOrValidate(ctx, badIdentity); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("identity mismatch error = %v", err)
	}
	badPath := registration
	badPath.CanonicalPath = `C:\other\fomo.db`
	if _, err := repo.InitializeOrValidate(ctx, badPath); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("path mismatch error = %v", err)
	}
	tooSmall := registration
	tooSmall.CurrentSourceMaximum = -1
	if _, err := repo.InitializeOrValidate(ctx, tooSmall); err == nil {
		t.Fatal("source maximum below watermark accepted")
	}

	if _, err := repo.db.ExecContext(ctx, `UPDATE lab_state SET algorithm_version='SIGNAL_LAB_OLD' WHERE singleton_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InitializeOrValidate(ctx, registration); err == nil || !strings.Contains(err.Error(), "algorithm version") {
		t.Fatalf("algorithm version mismatch error = %v", err)
	}
}

func TestRepositoryEnforcesEntrySnapshotAndTerminalOutcome(t *testing.T) {
	repo := openTestRepository(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC).UnixMilli()
	signalID := insertRawSignal(t, repo, 10, now)

	if _, err := repo.db.ExecContext(ctx, `
		INSERT INTO paper_entries
		(signal_id, entry_snapshot_id, entry_at, entry_status, entry_price_usd, created_at)
		VALUES (?, ?, ?, 'EVALUABLE', 1.0, ?)`, signalID, 11, now, now); err == nil {
		t.Fatal("mismatched entry snapshot accepted")
	}
	if _, err := repo.db.ExecContext(ctx, `
		INSERT INTO paper_entries
		(signal_id, entry_snapshot_id, entry_at, entry_status, entry_price_usd, created_at)
		VALUES (?, ?, ?, 'UNAVAILABLE_PRICE', 1.0, ?)`, signalID, 10, now, now); err == nil {
		t.Fatal("UNAVAILABLE_PRICE with non-NULL price accepted")
	}
	if _, err := repo.db.ExecContext(ctx, `
		INSERT INTO paper_entries
		(signal_id, entry_snapshot_id, entry_at, entry_status, entry_price_usd, created_at)
		VALUES (?, ?, ?, 'EVALUABLE', NULL, ?)`, signalID, 10, now, now); err == nil {
		t.Fatal("EVALUABLE with NULL price accepted")
	}
	if _, err := repo.db.ExecContext(ctx, `
		INSERT INTO paper_entries
		(signal_id, entry_snapshot_id, entry_at, entry_status, entry_price_usd, created_at)
		VALUES (?, ?, ?, 'EVALUABLE', 1.0, ?)`, signalID, 10, now, now); err != nil {
		t.Fatalf("insert valid entry: %v", err)
	}
	if _, err := repo.db.ExecContext(ctx, `
		INSERT INTO horizon_outcomes
		(signal_id,horizon,target_at,tolerance_seconds,window_end_at,status,
		 selected_source_snapshot_id,selected_snapshot_at,selection_delay_seconds,
		 return_decimal,mfe_decimal,mae_decimal,evaluation_source_high_watermark,
		 source_observation_frontier_at,finalized_at,created_at)
		VALUES (?, '5m', ?, 180, ?, 'MATURED', 12, ?, 30, .1, .2, -.05, 12, ?, ?, ?)`,
		signalID, now+300000, now+480000, now+330000, now+500000, now+500000, now); err != nil {
		t.Fatalf("insert matured outcome: %v", err)
	}
	if _, err := repo.db.ExecContext(ctx, `UPDATE horizon_outcomes SET return_decimal=.9 WHERE signal_id=? AND horizon='5m'`, signalID); err == nil {
		t.Fatal("terminal MATURED outcome overwrite accepted")
	}

	for _, status := range []string{"INSUFFICIENT_DATA", "NOT_EVALUABLE"} {
		otherSignalID := insertRawSignal(t, repo, int64(20+len(status)), now+int64(len(status)))
		if _, err := repo.db.ExecContext(ctx, `
			INSERT INTO horizon_outcomes
			(signal_id,horizon,target_at,tolerance_seconds,window_end_at,status,created_at)
			VALUES (?, '5m', ?, 180, ?, ?, ?)`, otherSignalID, now+300000, now+480000, status, now); err != nil {
			t.Fatalf("insert %s outcome: %v", status, err)
		}
		if _, err := repo.db.ExecContext(ctx, `UPDATE horizon_outcomes SET status='PENDING' WHERE signal_id=?`, otherSignalID); err == nil {
			t.Fatalf("terminal %s outcome overwrite accepted", status)
		}
	}
}

func TestRepositoryKeepsAnalysisRunsImmutable(t *testing.T) {
	repo := openTestRepository(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC).UnixMilli()
	result, err := repo.db.ExecContext(ctx, `
		INSERT INTO analysis_runs
		(algorithm_version,generated_at,persisted_through_snapshot_id,source_observation_frontier_at)
		VALUES (?,?,?,?)`, AlgorithmVersion, now, 10, now)
	if err != nil {
		t.Fatalf("insert analysis run: %v", err)
	}
	runID, _ := result.LastInsertId()
	if _, err := repo.db.ExecContext(ctx, `
		INSERT INTO threshold_statistics
		(analysis_run_id,signal_source_scope,chain_scope,evidence_scope,score_version,threshold,horizon,
		 signals_count,evaluable_entries,not_evaluable,pending_count,matured_count,insufficient_data_count,
		 insufficient_sample)
		VALUES (?,'BACKFILL','BSC','HIGH_GOOD','FOMO_SCORE_V1.0',55,'5m',1,1,0,0,1,0,1)`, runID); err != nil {
		t.Fatalf("insert statistic: %v", err)
	}
	if _, err := repo.db.ExecContext(ctx, `UPDATE analysis_runs SET persisted_through_snapshot_id=11 WHERE id=?`, runID); err == nil {
		t.Fatal("analysis run update accepted")
	}
	if _, err := repo.db.ExecContext(ctx, `DELETE FROM threshold_statistics WHERE analysis_run_id=?`, runID); err == nil {
		t.Fatal("threshold statistic delete accepted")
	}
}

func openTestRepository(t *testing.T) *Repository {
	t.Helper()
	repo, err := OpenRepository(filepath.Join(t.TempDir(), "fomo-lab.db"))
	if err != nil {
		t.Fatalf("OpenRepository() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func insertRawSignal(t *testing.T, repo *Repository, snapshotID, signalAt int64) int64 {
	t.Helper()
	result, err := repo.db.Exec(`
		INSERT INTO signals
		(token_id,chain,address,name,symbol,signal_source,threshold,signal_snapshot_id,signal_at,
		 raw_score,raw_tier,effective_tier,evidence_confidence,evidence_available,evidence_total,
		 score_version,score_config_json,score_breakdown_json,risk_flags_json,data_quality_json,created_at)
		VALUES (?,?,?,?,?,'BACKFILL',55,?,?,60,'WATCH','WATCH','LOW',2,5,
		 'FOMO_SCORE_V1.0','{}','{}','[]','{}',?)`,
		"bsc:token:"+time.UnixMilli(signalAt).Format(time.RFC3339Nano), "bsc", "0xabc", "牛来", "🐂", snapshotID, signalAt, signalAt)
	if err != nil {
		t.Fatalf("insert signal: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("signal LastInsertId: %v", err)
	}
	return id
}
