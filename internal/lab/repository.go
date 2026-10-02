package lab

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const (
	labSchemaVersion = 1
	labBusyTimeoutMS = 5000
)

//go:embed schema/001_signal_lab.sql
var labSchema string

type Repository struct {
	db *sql.DB
}

func OpenRepository(path string) (*Repository, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve Lab database path: %w", err)
	}
	parent := filepath.Dir(absolute)
	info, err := os.Stat(parent)
	if err != nil {
		return nil, fmt.Errorf("open Lab database parent: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("Lab database parent is not a directory: %s", parent)
	}
	db, err := sql.Open("sqlite", absolute)
	if err != nil {
		return nil, fmt.Errorf("open Lab database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	closeOnError := func(err error) (*Repository, error) {
		_ = db.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		fmt.Sprintf("PRAGMA busy_timeout=%d", labBusyTimeoutMS),
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return closeOnError(fmt.Errorf("configure Lab database: %w", err))
		}
	}
	if _, err := db.ExecContext(ctx, labSchema); err != nil {
		return closeOnError(fmt.Errorf("apply Lab schema: %w", err))
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO lab_schema_migrations(version,applied_at)
		VALUES (?,?) ON CONFLICT(version) DO NOTHING`, labSchemaVersion, time.Now().UTC().UnixMilli()); err != nil {
		return closeOnError(fmt.Errorf("record Lab schema: %w", err))
	}
	return &Repository{db: db}, nil
}

func (r *Repository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

func (r *Repository) InitializeOrValidate(ctx context.Context, registration SourceRegistration) (LabState, error) {
	if registration.CanonicalPath == "" || registration.Identity == "" {
		return LabState{}, errors.New("source path and identity are required")
	}
	if registration.ActivationSnapshotID < 0 || registration.CurrentSourceMaximum < 0 {
		return LabState{}, errors.New("source snapshot maximum must not be negative")
	}
	initializedAt := registration.InitializedAt.UTC()
	if initializedAt.IsZero() {
		initializedAt = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO lab_state (
			singleton_id,schema_version,algorithm_version,canonical_source_path,
			source_db_identity,activation_snapshot_id,last_processed_snapshot_id,
			last_cycle_source_high_watermark,source_observation_frontier_at,
			initialized_at,updated_at
		) VALUES (1,?,?,?,?,?,0,0,NULL,?,?)
		ON CONFLICT(singleton_id) DO NOTHING`,
		labSchemaVersion, AlgorithmVersion, registration.CanonicalPath, registration.Identity,
		registration.ActivationSnapshotID, initializedAt.UnixMilli(), initializedAt.UnixMilli())
	if err != nil {
		return LabState{}, fmt.Errorf("initialize Lab state: %w", err)
	}
	state, err := r.State(ctx)
	if err != nil {
		return LabState{}, err
	}
	if state.AlgorithmVersion != AlgorithmVersion {
		return LabState{}, fmt.Errorf("algorithm version mismatch: stored %q current %q", state.AlgorithmVersion, AlgorithmVersion)
	}
	if state.CanonicalSourcePath != registration.CanonicalPath {
		return LabState{}, fmt.Errorf("source path mismatch: stored %q current %q", state.CanonicalSourcePath, registration.CanonicalPath)
	}
	if state.SourceDBIdentity != registration.Identity {
		return LabState{}, fmt.Errorf("source identity mismatch")
	}
	if registration.CurrentSourceMaximum < state.LastProcessedSnapshotID {
		return LabState{}, fmt.Errorf("source maximum %d is below Lab watermark %d", registration.CurrentSourceMaximum, state.LastProcessedSnapshotID)
	}
	return state, nil
}

func (r *Repository) State(ctx context.Context) (LabState, error) {
	var state LabState
	var frontier sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT algorithm_version,canonical_source_path,source_db_identity,activation_snapshot_id,
		       last_processed_snapshot_id,last_cycle_source_high_watermark,
		       source_observation_frontier_at
		FROM lab_state WHERE singleton_id=1`).Scan(
		&state.AlgorithmVersion, &state.CanonicalSourcePath, &state.SourceDBIdentity, &state.ActivationSnapshotID,
		&state.LastProcessedSnapshotID, &state.LastCycleSourceHighWatermark, &frontier)
	if err != nil {
		return LabState{}, fmt.Errorf("read Lab state: %w", err)
	}
	if frontier.Valid {
		value := time.UnixMilli(frontier.Int64).UTC()
		state.SourceObservationFrontierAt = &value
	}
	return state, nil
}

func (r *Repository) ApplySnapshotChunk(ctx context.Context, snapshots []SourceSnapshot, activationID int64, createdAt time.Time) (ApplyResult, error) {
	if len(snapshots) == 0 {
		return ApplyResult{}, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("begin Lab snapshot chunk: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	var storedActivation, previousWatermark int64
	if err := tx.QueryRowContext(ctx, `SELECT activation_snapshot_id,last_processed_snapshot_id FROM lab_state WHERE singleton_id=1`).Scan(&storedActivation, &previousWatermark); err != nil {
		return ApplyResult{}, fmt.Errorf("read Lab chunk state: %w", err)
	}
	if storedActivation != activationID {
		return ApplyResult{}, fmt.Errorf("activation snapshot changed from %d to %d", storedActivation, activationID)
	}
	lastID := previousWatermark
	result := ApplyResult{}
	for _, snapshot := range snapshots {
		if snapshot.ID <= previousWatermark || snapshot.ID < lastID {
			return ApplyResult{}, fmt.Errorf("source snapshots not strictly ascending: %d after %d", snapshot.ID, lastID)
		}
		if snapshot.RawScore != nil {
			if snapshot.ScoreConfigJSON == "" {
				return ApplyResult{}, fmt.Errorf("snapshot %d score config is unavailable", snapshot.ID)
			}
			for _, threshold := range Thresholds() {
				if *snapshot.RawScore < float64(threshold) {
					break
				}
				signalSource := SignalLive
				if snapshot.ID <= activationID {
					signalSource = SignalBackfill
				}
				var signalID int64
				err := tx.QueryRowContext(ctx, `
					INSERT INTO signals (
						token_id,chain,address,name,symbol,signal_source,threshold,
						signal_snapshot_id,signal_at,source_time,price_usd,market_cap_usd,
						liquidity_usd,token_age_seconds,raw_score,raw_tier,effective_tier,
						evidence_confidence,evidence_available,evidence_total,score_version,
						score_config_json,score_breakdown_json,risk_flags_json,data_quality_json,created_at
					) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
					ON CONFLICT(token_id,score_version,threshold) DO NOTHING RETURNING id`,
					snapshot.TokenID, snapshot.Chain, snapshot.Address, snapshot.Name, snapshot.Symbol,
					signalSource, threshold, snapshot.ID, snapshot.CollectedAt.UnixMilli(), timeOrNil(snapshot.SourceTime),
					floatOrNil(snapshot.PriceUSD), floatOrNil(snapshot.MarketCapUSD), floatOrNil(snapshot.LiquidityUSD),
					int64OrNil(snapshot.TokenAgeSeconds), *snapshot.RawScore, snapshot.RawTier, snapshot.EffectiveTier,
					snapshot.EvidenceConfidence, snapshot.EvidenceAvailable, snapshot.EvidenceTotal, snapshot.ScoreVersion,
					snapshot.ScoreConfigJSON, snapshot.ScoreBreakdownJSON, snapshot.RiskFlagsJSON, snapshot.DataQualityJSON,
					createdAt.UTC().UnixMilli()).Scan(&signalID)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return ApplyResult{}, fmt.Errorf("insert threshold %d signal for snapshot %d: %w", threshold, snapshot.ID, err)
				}
				if err := insertPaperEntryAndOutcomes(ctx, tx, signalID, snapshot, createdAt); err != nil {
					return ApplyResult{}, err
				}
				result.SignalsCreated++
				result.EntriesCreated++
			}
		}
		lastID = snapshot.ID
		result.SourceSnapshotsProcessed++
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE lab_state SET last_processed_snapshot_id=?,updated_at=?
		WHERE singleton_id=1 AND last_processed_snapshot_id=?`, lastID, createdAt.UTC().UnixMilli(), previousWatermark)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("advance Lab source watermark: %w", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return ApplyResult{}, fmt.Errorf("read Lab watermark update count: %w", err)
	}
	if changed != 1 {
		return ApplyResult{}, fmt.Errorf("Lab watermark changed concurrently")
	}
	if err := tx.Commit(); err != nil {
		return ApplyResult{}, fmt.Errorf("commit Lab snapshot chunk: %w", err)
	}
	result.LastSnapshotID = lastID
	return result, nil
}

func insertPaperEntryAndOutcomes(ctx context.Context, tx *sql.Tx, signalID int64, snapshot SourceSnapshot, createdAt time.Time) error {
	entryStatus := EntryUnavailablePrice
	var entryPrice any
	if snapshot.PriceUSD != nil && *snapshot.PriceUSD > 0 {
		entryStatus = EntryEvaluable
		entryPrice = *snapshot.PriceUSD
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO paper_entries(signal_id,entry_snapshot_id,entry_at,entry_status,entry_price_usd,created_at)
		VALUES (?,?,?,?,?,?)`, signalID, snapshot.ID, snapshot.CollectedAt.UnixMilli(), entryStatus, entryPrice, createdAt.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("insert paper entry for signal %d: %w", signalID, err)
	}
	for _, horizon := range Horizons() {
		targetAt := snapshot.CollectedAt.Add(horizon.Offset)
		windowEnd := targetAt.Add(horizon.Tolerance)
		status := OutcomePending
		reason := ""
		var finalizedAt any
		if entryStatus == EntryUnavailablePrice {
			status = OutcomeNotEvaluable
			reason = "UNAVAILABLE_ENTRY_PRICE"
			finalizedAt = createdAt.UTC().UnixMilli()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO horizon_outcomes(
				signal_id,horizon,target_at,tolerance_seconds,window_end_at,status,status_reason,finalized_at,created_at
			) VALUES (?,?,?,?,?,?,?,?,?)`,
			signalID, horizon.Key, targetAt.UnixMilli(), int64(horizon.Tolerance/time.Second),
			windowEnd.UnixMilli(), status, reason, finalizedAt, createdAt.UTC().UnixMilli()); err != nil {
			return fmt.Errorf("insert %s outcome for signal %d: %w", horizon.Key, signalID, err)
		}
	}
	return nil
}

func (r *Repository) RecordCycleFrontier(ctx context.Context, frontier SourceFrontier, updatedAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE lab_state SET last_cycle_source_high_watermark=?,source_observation_frontier_at=?,updated_at=?
		WHERE singleton_id=1`, frontier.HighWatermark, timeOrNil(frontier.ObservationAt), updatedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("record Lab cycle frontier: %w", err)
	}
	return nil
}

func (r *Repository) PendingOutcomes(ctx context.Context, afterID int64, limit int) ([]PendingOutcome, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("pending outcome limit must be positive")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT o.id,o.signal_id,s.token_id,o.horizon,s.signal_snapshot_id,s.signal_at,
		       o.target_at,o.window_end_at,e.entry_price_usd
		FROM horizon_outcomes o
		JOIN signals s ON s.id=o.signal_id
		JOIN paper_entries e ON e.signal_id=s.id
		WHERE o.status='PENDING' AND e.entry_status='EVALUABLE' AND o.id>?
		ORDER BY o.id ASC LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending Lab outcomes: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	result := make([]PendingOutcome, 0, limit)
	for rows.Next() {
		var pending PendingOutcome
		var signalAt, targetAt, windowEnd int64
		if err := rows.Scan(&pending.ID, &pending.SignalID, &pending.TokenID, &pending.Horizon,
			&pending.SignalSnapshotID, &signalAt, &targetAt, &windowEnd, &pending.EntryPriceUSD); err != nil {
			return nil, fmt.Errorf("scan pending Lab outcome: %w", err)
		}
		pending.SignalAt = time.UnixMilli(signalAt).UTC()
		pending.TargetAt = time.UnixMilli(targetAt).UTC()
		pending.WindowEndAt = time.UnixMilli(windowEnd).UTC()
		result = append(result, pending)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending Lab outcomes: %w", err)
	}
	return result, nil
}

func (r *Repository) MatureOutcome(ctx context.Context, pending PendingOutcome, selected PriceObservation, frontier SourceFrontier, returnDecimal, mfe, mae float64, finalizedAt time.Time) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE horizon_outcomes SET status='MATURED',status_reason='',selected_source_snapshot_id=?,
		selected_snapshot_at=?,selection_delay_seconds=?,return_decimal=?,mfe_decimal=?,mae_decimal=?,
		evaluation_source_high_watermark=?,source_observation_frontier_at=?,finalized_at=?
		WHERE id=? AND status='PENDING'`,
		selected.SnapshotID, selected.CollectedAt.UnixMilli(), int64(selected.CollectedAt.Sub(pending.TargetAt)/time.Second),
		returnDecimal, mfe, mae, frontier.HighWatermark, timeOrNil(frontier.ObservationAt), finalizedAt.UTC().UnixMilli(), pending.ID)
	if err != nil {
		return fmt.Errorf("mature %s outcome for signal %d: %w", pending.Horizon, pending.SignalID, err)
	}
	return requireOneOutcomeUpdate(result, pending)
}

func (r *Repository) MarkOutcomeInsufficient(ctx context.Context, pending PendingOutcome, frontier SourceFrontier, finalizedAt time.Time) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE horizon_outcomes SET status='INSUFFICIENT_DATA',status_reason='NO_VALID_PRICE_IN_WINDOW',
		selected_source_snapshot_id=NULL,selected_snapshot_at=NULL,selection_delay_seconds=NULL,
		return_decimal=NULL,mfe_decimal=NULL,mae_decimal=NULL,evaluation_source_high_watermark=?,
		source_observation_frontier_at=?,finalized_at=? WHERE id=? AND status='PENDING'`,
		frontier.HighWatermark, timeOrNil(frontier.ObservationAt), finalizedAt.UTC().UnixMilli(), pending.ID)
	if err != nil {
		return fmt.Errorf("mark %s outcome insufficient for signal %d: %w", pending.Horizon, pending.SignalID, err)
	}
	return requireOneOutcomeUpdate(result, pending)
}

func requireOneOutcomeUpdate(result sql.Result, pending PendingOutcome) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read %s outcome update count for signal %d: %w", pending.Horizon, pending.SignalID, err)
	}
	if changed != 1 {
		return fmt.Errorf("%s outcome for signal %d was not PENDING", pending.Horizon, pending.SignalID)
	}
	return nil
}

func (r *Repository) LatestAnalysis(ctx context.Context) (ThresholdReport, error) {
	var report ThresholdReport
	report.Title = "FomoRadar 信号实验室"
	var generatedAt int64
	var frontier sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT id,algorithm_version,generated_at,persisted_through_snapshot_id,source_observation_frontier_at
		FROM analysis_runs ORDER BY id DESC LIMIT 1`).Scan(
		&report.Run.ID, &report.Run.AlgorithmVersion, &generatedAt,
		&report.Run.PersistedThroughSnapshotID, &frontier)
	if err != nil {
		return ThresholdReport{}, fmt.Errorf("read latest Signal Lab analysis: %w", err)
	}
	report.Run.GeneratedAt = time.UnixMilli(generatedAt).UTC()
	if frontier.Valid {
		value := time.UnixMilli(frontier.Int64).UTC()
		report.Run.SourceObservationFrontier = &value
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT signal_source_scope,chain_scope,evidence_scope,score_version,threshold,horizon,
		       signals_count,evaluable_entries,not_evaluable,entry_price_coverage,pending_count,
		       matured_count,insufficient_data_count,maturity_coverage,result_coverage,
		       positive_return_rate,median_return_decimal,median_mfe_decimal,median_mae_decimal,
		       insufficient_sample
		FROM threshold_statistics WHERE analysis_run_id=?
		ORDER BY CASE signal_source_scope WHEN 'BACKFILL' THEN 1 WHEN 'LIVE' THEN 2 ELSE 3 END,
		         chain_scope,evidence_scope,score_version,threshold,horizon`, report.Run.ID)
	if err != nil {
		return ThresholdReport{}, fmt.Errorf("query Signal Lab threshold statistics: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var stat ThresholdStatistic
		var entryCoverage, maturityCoverage, resultCoverage, positiveRate sql.NullFloat64
		var medianReturn, medianMFE, medianMAE sql.NullFloat64
		if err := rows.Scan(
			&stat.SignalSourceScope, &stat.ChainScope, &stat.EvidenceScope, &stat.ScoreVersion,
			&stat.Threshold, &stat.Horizon, &stat.Signals, &stat.EvaluableEntries, &stat.NotEvaluable,
			&entryCoverage, &stat.Pending, &stat.Matured, &stat.InsufficientData, &maturityCoverage,
			&resultCoverage, &positiveRate, &medianReturn, &medianMFE, &medianMAE,
			&stat.InsufficientSample,
		); err != nil {
			return ThresholdReport{}, fmt.Errorf("scan Signal Lab threshold statistic: %w", err)
		}
		stat.EntryPriceCoverage = nullFloatPointer(entryCoverage)
		stat.MaturityCoverage = nullFloatPointer(maturityCoverage)
		stat.ResultCoverage = nullFloatPointer(resultCoverage)
		stat.PositiveReturnRate = nullFloatPointer(positiveRate)
		stat.MedianReturn = nullFloatPointer(medianReturn)
		stat.MedianMFE = nullFloatPointer(medianMFE)
		stat.MedianMAE = nullFloatPointer(medianMAE)
		report.Statistics = append(report.Statistics, stat)
	}
	if err := rows.Err(); err != nil {
		return ThresholdReport{}, fmt.Errorf("iterate Signal Lab threshold statistics: %w", err)
	}
	return report, nil
}

// LatestComparison 从最近一次不可变分析中筛选指定评分版本，不重新计算指标。
func (r *Repository) LatestComparison(ctx context.Context, versions []string) (ComparisonReport, error) {
	report, err := r.LatestAnalysis(ctx)
	if err != nil {
		return ComparisonReport{}, err
	}
	allowed := make(map[string]struct{}, len(versions))
	for _, version := range versions {
		allowed[version] = struct{}{}
	}
	comparison := ComparisonReport{Run: report.Run}
	for _, statistic := range report.Statistics {
		if len(allowed) > 0 {
			if _, ok := allowed[statistic.ScoreVersion]; !ok {
				continue
			}
		}
		comparison.Statistics = append(comparison.Statistics, statistic)
	}
	return comparison, nil
}

func nullFloatPointer(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func timeOrNil(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().UnixMilli()
}

func floatOrNil(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64OrNil(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
