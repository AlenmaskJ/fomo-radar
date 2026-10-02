package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func (s *Store) StartOpportunityRun(ctx context.Context, assetClass domain.AssetClass, trigger string, startedAt time.Time) (int64, error) {
	if !assetClass.Valid() {
		return 0, fmt.Errorf("invalid opportunity asset class %q", assetClass)
	}
	switch trigger {
	case "startup", "scheduled", "manual", "cli":
	default:
		return 0, fmt.Errorf("invalid opportunity trigger %q", trigger)
	}
	if startedAt.IsZero() {
		return 0, fmt.Errorf("opportunity start time is required")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO market_opportunity_runs (asset_class, started_at, status, trigger) VALUES (?, ?, 'running', ?)`, assetClass, startedAt.UnixMilli(), trigger)
	if err != nil {
		return 0, fmt.Errorf("start opportunity run: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read opportunity run ID: %w", err)
	}
	return id, nil
}

func (s *Store) FinishOpportunityRun(ctx context.Context, run domain.OpportunityRun, opportunities []domain.Opportunity) error {
	if run.ID <= 0 || run.FinishedAt.IsZero() {
		return fmt.Errorf("opportunity run ID and finish time are required")
	}
	if !run.AssetClass.Valid() {
		return fmt.Errorf("invalid opportunity asset class %q", run.AssetClass)
	}
	if run.Status != domain.OpportunityRunCompleted && run.Status != domain.OpportunityRunDegraded && run.Status != domain.OpportunityRunFailed {
		return fmt.Errorf("invalid final opportunity status %q", run.Status)
	}
	if run.Status == domain.OpportunityRunFailed && len(opportunities) != 0 {
		return fmt.Errorf("failed opportunity run cannot publish candidates")
	}
	payloads := make([][]byte, len(opportunities))
	for index, opportunity := range opportunities {
		payload, err := json.Marshal(opportunity)
		if err != nil {
			return fmt.Errorf("encode opportunity %s: %w", opportunity.Instrument.InstrumentID, err)
		}
		payloads[index] = payload
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin opportunity publish: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM market_opportunities WHERE run_id = ?`, run.ID); err != nil {
		return fmt.Errorf("clear opportunity results: %w", err)
	}
	for index, opportunity := range opportunities {
		highVolatility := 0
		if opportunity.HighVolatility {
			highVolatility = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO market_opportunities (
				run_id, instrument_id, symbol, fomo_score, opportunity_score, stage, high_volatility, payload_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			run.ID, opportunity.Instrument.InstrumentID, opportunity.Instrument.Symbol,
			opportunity.FOMOScore, opportunity.OpportunityScore, opportunity.Stage, highVolatility, string(payloads[index])); err != nil {
			return fmt.Errorf("insert opportunity %s: %w", opportunity.Instrument.InstrumentID, err)
		}
	}
	run.CandidateCount = len(opportunities)
	result, err := tx.ExecContext(ctx, `
		UPDATE market_opportunity_runs
		SET finished_at = ?, status = ?, pool_size = ?, candidate_count = ?, error_summary = ?
		WHERE id = ? AND asset_class = ? AND status = 'running'`, run.FinishedAt.UnixMilli(), run.Status, run.PoolSize, run.CandidateCount, run.ErrorSummary, run.ID, run.AssetClass)
	if err != nil {
		return fmt.Errorf("finish opportunity run: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return fmt.Errorf("finish opportunity run %d: expected one running row", run.ID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit opportunity run: %w", err)
	}
	return nil
}

func (s *Store) LatestOpportunityReport(ctx context.Context, assetClass domain.AssetClass) (domain.OpportunityReport, error) {
	if !assetClass.Valid() {
		return domain.OpportunityReport{}, fmt.Errorf("invalid opportunity asset class %q", assetClass)
	}
	var report domain.OpportunityReport
	var startedAt, finishedAt int64
	var storedClass, status string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, asset_class, started_at, finished_at, status, trigger, pool_size, candidate_count, error_summary
		FROM market_opportunity_runs
		WHERE asset_class = ? AND status IN ('completed','degraded')
		ORDER BY finished_at DESC, id DESC LIMIT 1`, assetClass).Scan(
		&report.Run.ID, &storedClass, &startedAt, &finishedAt, &status, &report.Run.Trigger,
		&report.Run.PoolSize, &report.Run.CandidateCount, &report.Run.ErrorSummary,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.OpportunityReport{}, sql.ErrNoRows
		}
		return domain.OpportunityReport{}, fmt.Errorf("read latest opportunity run: %w", err)
	}
	report.Run.StartedAt = time.UnixMilli(startedAt).UTC()
	report.Run.FinishedAt = time.UnixMilli(finishedAt).UTC()
	report.Run.AssetClass = domain.AssetClass(storedClass)
	report.Run.Status = domain.OpportunityRunStatus(status)
	rows, err := s.db.QueryContext(ctx, `
		SELECT payload_json FROM market_opportunities WHERE run_id = ?
		ORDER BY opportunity_score DESC, fomo_score DESC, instrument_id ASC LIMIT 10`, report.Run.ID)
	if err != nil {
		return domain.OpportunityReport{}, fmt.Errorf("read latest opportunities: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return domain.OpportunityReport{}, fmt.Errorf("scan opportunity payload: %w", err)
		}
		var opportunity domain.Opportunity
		if err := json.Unmarshal([]byte(payload), &opportunity); err != nil {
			return domain.OpportunityReport{}, fmt.Errorf("decode opportunity payload: %w", err)
		}
		report.Opportunities = append(report.Opportunities, opportunity)
	}
	if err := rows.Err(); err != nil {
		return domain.OpportunityReport{}, fmt.Errorf("iterate opportunities: %w", err)
	}
	return report, nil
}
