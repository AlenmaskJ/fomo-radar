package lab

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

type Analyzer struct {
	repo *Repository
	now  func() time.Time
}

func NewAnalyzer(repo *Repository, now func() time.Time) *Analyzer {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Analyzer{repo: repo, now: now}
}

type statisticAccumulator struct {
	signals, evaluable, notEvaluable int
	pending, matured, insufficient   int
	returns, mfes, maes              []float64
}

type scopeDefinition struct {
	name      string
	predicate string
	args      []any
}

func (a *Analyzer) Materialize(ctx context.Context) (AnalysisRun, error) {
	if a == nil || a.repo == nil {
		return AnalysisRun{}, fmt.Errorf("Signal Lab analyzer is not configured")
	}
	tx, err := a.repo.db.BeginTx(ctx, nil)
	if err != nil {
		return AnalysisRun{}, fmt.Errorf("begin Signal Lab analysis: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	var stateHighWatermark int64
	var stateFrontier sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT last_cycle_source_high_watermark,source_observation_frontier_at FROM lab_state WHERE singleton_id=1`).Scan(&stateHighWatermark, &stateFrontier); err != nil {
		return AnalysisRun{}, fmt.Errorf("read persisted Lab state for analysis: %w", err)
	}
	generatedAt := a.now().UTC()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO analysis_runs(algorithm_version,generated_at,persisted_through_snapshot_id,source_observation_frontier_at)
		VALUES (?,?,?,?)`, AlgorithmVersion, generatedAt.UnixMilli(), stateHighWatermark, nullInt64Value(stateFrontier))
	if err != nil {
		return AnalysisRun{}, fmt.Errorf("insert Signal Lab analysis run: %w", err)
	}
	runID, err := result.LastInsertId()
	if err != nil {
		return AnalysisRun{}, fmt.Errorf("read Signal Lab analysis run ID: %w", err)
	}
	versions, err := analysisScoreVersions(ctx, tx, stateHighWatermark)
	if err != nil {
		return AnalysisRun{}, err
	}
	for _, sourceScope := range signalSourceScopes() {
		for _, chainScope := range chainScopes() {
			for _, evidenceScope := range evidenceScopes() {
				for _, version := range versions {
					accumulators, err := collectStatistics(ctx, tx, stateHighWatermark, version, sourceScope, chainScope, evidenceScope)
					if err != nil {
						return AnalysisRun{}, err
					}
					for _, threshold := range Thresholds() {
						for _, horizon := range Horizons() {
							stat := buildStatistic(sourceScope.name, chainScope.name, evidenceScope.name, version, threshold, horizon.Key, accumulators[statisticKey(threshold, horizon.Key)])
							if err := insertStatistic(ctx, tx, runID, stat); err != nil {
								return AnalysisRun{}, err
							}
						}
					}
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return AnalysisRun{}, fmt.Errorf("commit Signal Lab analysis: %w", err)
	}
	run := AnalysisRun{ID: runID, AlgorithmVersion: AlgorithmVersion, GeneratedAt: generatedAt, PersistedThroughSnapshotID: stateHighWatermark}
	if stateFrontier.Valid {
		value := time.UnixMilli(stateFrontier.Int64).UTC()
		run.SourceObservationFrontier = &value
	}
	return run, nil
}

func analysisScoreVersions(ctx context.Context, tx *sql.Tx, highWatermark int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT score_version FROM signals WHERE signal_snapshot_id<=? ORDER BY score_version`, highWatermark)
	if err != nil {
		return nil, fmt.Errorf("query Signal Lab score versions: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var result []string
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan Signal Lab score version: %w", err)
		}
		result = append(result, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Signal Lab score versions: %w", err)
	}
	return result, nil
}

func collectStatistics(ctx context.Context, tx *sql.Tx, highWatermark int64, version string, sourceScope, chainScope, evidenceScope scopeDefinition) (map[string]*statisticAccumulator, error) {
	query := `SELECT s.threshold,o.horizon,e.entry_status,o.status,o.return_decimal,o.mfe_decimal,o.mae_decimal,
		       o.evaluation_source_high_watermark
		FROM signals s JOIN paper_entries e ON e.signal_id=s.id JOIN horizon_outcomes o ON o.signal_id=s.id
		WHERE s.score_version=? AND s.signal_snapshot_id<=?`
	args := []any{version, highWatermark}
	for _, scope := range []scopeDefinition{sourceScope, chainScope, evidenceScope} {
		if scope.predicate != "" {
			query += " AND " + scope.predicate
			args = append(args, scope.args...)
		}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query threshold observations: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	result := make(map[string]*statisticAccumulator)
	for rows.Next() {
		var threshold int
		var horizon, entryStatus, outcomeStatus string
		var returnValue, mfe, mae sql.NullFloat64
		var evaluationHighWatermark sql.NullInt64
		if err := rows.Scan(&threshold, &horizon, &entryStatus, &outcomeStatus, &returnValue, &mfe, &mae, &evaluationHighWatermark); err != nil {
			return nil, fmt.Errorf("scan threshold observation: %w", err)
		}
		if evaluationHighWatermark.Valid && evaluationHighWatermark.Int64 > highWatermark {
			outcomeStatus = string(OutcomePending)
			returnValue, mfe, mae = sql.NullFloat64{}, sql.NullFloat64{}, sql.NullFloat64{}
		}
		key := statisticKey(threshold, horizon)
		accumulator := result[key]
		if accumulator == nil {
			accumulator = &statisticAccumulator{}
			result[key] = accumulator
		}
		accumulator.signals++
		if entryStatus != string(EntryEvaluable) {
			accumulator.notEvaluable++
			continue
		}
		accumulator.evaluable++
		switch OutcomeStatus(outcomeStatus) {
		case OutcomePending:
			accumulator.pending++
		case OutcomeInsufficientData:
			accumulator.insufficient++
		case OutcomeMatured:
			if !returnValue.Valid || !mfe.Valid || !mae.Valid {
				return nil, fmt.Errorf("MATURED %d/%s outcome has NULL metrics", threshold, horizon)
			}
			accumulator.matured++
			accumulator.returns = append(accumulator.returns, returnValue.Float64)
			accumulator.mfes = append(accumulator.mfes, mfe.Float64)
			accumulator.maes = append(accumulator.maes, mae.Float64)
		default:
			return nil, fmt.Errorf("evaluable entry has outcome status %q", outcomeStatus)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate threshold observations: %w", err)
	}
	return result, nil
}

func buildStatistic(sourceScope, chainScope, evidenceScope, version string, threshold int, horizon string, accumulator *statisticAccumulator) ThresholdStatistic {
	if accumulator == nil {
		accumulator = &statisticAccumulator{}
	}
	terminal := accumulator.matured + accumulator.insufficient
	positive := 0
	for _, value := range accumulator.returns {
		if value > 0 {
			positive++
		}
	}
	return ThresholdStatistic{
		SignalSourceScope: sourceScope, ChainScope: chainScope, EvidenceScope: evidenceScope,
		ScoreVersion: version, Threshold: threshold, Horizon: horizon,
		Signals: accumulator.signals, EvaluableEntries: accumulator.evaluable, NotEvaluable: accumulator.notEvaluable,
		EntryPriceCoverage: ratioPointer(accumulator.evaluable, accumulator.signals),
		Pending:            accumulator.pending, Matured: accumulator.matured, InsufficientData: accumulator.insufficient,
		MaturityCoverage: ratioPointer(terminal, accumulator.evaluable), ResultCoverage: ratioPointer(accumulator.matured, terminal),
		PositiveReturnRate: ratioPointer(positive, accumulator.matured), MedianReturn: median(accumulator.returns),
		MedianMFE: median(accumulator.mfes), MedianMAE: median(accumulator.maes), InsufficientSample: accumulator.matured < 20,
	}
}

func insertStatistic(ctx context.Context, tx *sql.Tx, runID int64, stat ThresholdStatistic) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO threshold_statistics(
		analysis_run_id,signal_source_scope,chain_scope,evidence_scope,score_version,threshold,horizon,
		signals_count,evaluable_entries,not_evaluable,entry_price_coverage,pending_count,matured_count,
		insufficient_data_count,maturity_coverage,result_coverage,positive_return_rate,
		median_return_decimal,median_mfe_decimal,median_mae_decimal,insufficient_sample
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		runID, stat.SignalSourceScope, stat.ChainScope, stat.EvidenceScope, stat.ScoreVersion, stat.Threshold, stat.Horizon,
		stat.Signals, stat.EvaluableEntries, stat.NotEvaluable, floatOrNil(stat.EntryPriceCoverage), stat.Pending, stat.Matured,
		stat.InsufficientData, floatOrNil(stat.MaturityCoverage), floatOrNil(stat.ResultCoverage), floatOrNil(stat.PositiveReturnRate),
		floatOrNil(stat.MedianReturn), floatOrNil(stat.MedianMFE), floatOrNil(stat.MedianMAE), stat.InsufficientSample)
	if err != nil {
		return fmt.Errorf("insert threshold statistic: %w", err)
	}
	return nil
}

func signalSourceScopes() []scopeDefinition {
	return []scopeDefinition{{name: "BACKFILL", predicate: "s.signal_source=?", args: []any{"BACKFILL"}}, {name: "LIVE", predicate: "s.signal_source=?", args: []any{"LIVE"}}, {name: "ALL"}}
}

func chainScopes() []scopeDefinition {
	return []scopeDefinition{{name: "BSC", predicate: "UPPER(s.chain)=?", args: []any{"BSC"}}, {name: "SOLANA", predicate: "UPPER(s.chain)=?", args: []any{"SOLANA"}}, {name: "ALL"}}
}

func evidenceScopes() []scopeDefinition {
	return []scopeDefinition{
		{name: "HIGH_GOOD", predicate: "UPPER(s.evidence_confidence) IN ('HIGH','GOOD')"},
		{name: "MEDIUM_LOW", predicate: "UPPER(s.evidence_confidence) IN ('MEDIUM','LOW')"},
		{name: "UNKNOWN", predicate: "UPPER(COALESCE(s.evidence_confidence,'')) NOT IN ('HIGH','GOOD','MEDIUM','LOW')"},
		{name: "ALL"},
	}
}

func statisticKey(threshold int, horizon string) string {
	return fmt.Sprintf("%d/%s", threshold, horizon)
}

func ratioPointer(numerator, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}

func median(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	value := ordered[middle]
	if len(ordered)%2 == 0 {
		value = (ordered[middle-1] + ordered[middle]) / 2
	}
	return &value
}

func nullInt64Value(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}
