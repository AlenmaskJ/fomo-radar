package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

const historyRunPageSize = 512

type effectiveHistoryRun struct {
	ID         int64
	FinishedAt time.Time
}

// MarketOpportunityHistory 从既有扫描记录重建机会轮次，不写入额外汇总数据。
func (r *SQLiteRepository) MarketOpportunityHistory(ctx context.Context, assetClass domain.AssetClass, filter OpportunityHistoryFilter) (OpportunityHistoryPage, error) {
	if !assetClass.Valid() {
		return OpportunityHistoryPage{}, fmt.Errorf("invalid opportunity asset class %q", assetClass)
	}
	if filter.AsOf.IsZero() {
		filter.AsOf = time.Now().UTC()
	} else {
		filter.AsOf = filter.AsOf.UTC()
	}
	windowStart := filter.AsOf.Add(-historyWindowDuration(filter.Window))

	runs, err := r.loadAllEffectiveRuns(ctx, assetClass, filter.AsOf)
	if err != nil {
		return OpportunityHistoryPage{}, err
	}
	scans := make([]historyScan, len(runs))
	runIndex := make(map[int64]int, len(runs))
	for index, run := range runs {
		scans[index] = historyScan{RunID: run.ID, FinishedAt: run.FinishedAt, Opportunities: make(map[string]historyPresence)}
		runIndex[run.ID] = index
	}
	if err := r.loadHistoryOpportunities(ctx, scans, runIndex); err != nil {
		return OpportunityHistoryPage{}, err
	}

	effectiveScans := 0
	var generatedAt time.Time
	for _, scan := range scans {
		if !scan.FinishedAt.Before(windowStart) && !scan.FinishedAt.After(filter.AsOf) {
			effectiveScans++
			generatedAt = scan.FinishedAt
		}
	}
	var historyRows []OpportunityHistoryRow
	if effectiveScans > 0 {
		historyRows = buildOpportunityEpisodes(scans, windowStart, filter.AsOf)
		if err := r.fillHistoryPrices(ctx, assetClass, historyRows, filter.AsOf); err != nil {
			return OpportunityHistoryPage{}, err
		}
		historyRows = filterAndSortHistoryRows(historyRows, filter)
	}
	return OpportunityHistoryPage{
		Title:          "历史榜单",
		Filter:         filter,
		GeneratedAt:    generatedAt,
		EffectiveScans: effectiveScans,
		Rows:           historyRows,
	}, nil
}

func (r *SQLiteRepository) loadAllEffectiveRuns(ctx context.Context, assetClass domain.AssetClass, asOf time.Time) ([]effectiveHistoryRun, error) {
	before := asOf.Add(time.Millisecond)
	var descending []effectiveHistoryRun
	for {
		batch, err := r.loadEffectiveRuns(ctx, assetClass, before, historyRunPageSize)
		if err != nil {
			return nil, err
		}
		descending = append(descending, batch...)
		if len(batch) < historyRunPageSize {
			break
		}
		// 游标退到整个 5 分钟桶之前，避免桶内较早的手动扫描在下一页重新成为第一名。
		earliestMillis := batch[len(batch)-1].FinishedAt.UnixMilli()
		before = time.UnixMilli(earliestMillis / (5 * 60 * 1000) * (5 * 60 * 1000)).UTC()
	}

	ascending := make([]effectiveHistoryRun, len(descending))
	for index := range descending {
		ascending[len(descending)-1-index] = descending[index]
	}
	return ascending, nil
}

func (r *SQLiteRepository) loadEffectiveRuns(ctx context.Context, assetClass domain.AssetClass, beforeExclusive time.Time, limit int) ([]effectiveHistoryRun, error) {
	rows, err := r.db.QueryContext(ctx, `
		WITH ranked_runs AS (
			SELECT id, finished_at,
			       ROW_NUMBER() OVER (
				   PARTITION BY CAST(finished_at / 300000 AS INTEGER)
				   ORDER BY finished_at DESC, id DESC
			       ) AS bucket_rank
			FROM market_opportunity_runs
			WHERE asset_class = ?
			  AND status IN ('completed','degraded')
			  AND finished_at IS NOT NULL
			  AND finished_at < ?
		)
		SELECT id, finished_at
		FROM ranked_runs
		WHERE bucket_rank = 1
		ORDER BY finished_at DESC, id DESC
		LIMIT ?`, assetClass, beforeExclusive.UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("query effective opportunity runs: %w", err)
	}
	defer rows.Close()

	runs := make([]effectiveHistoryRun, 0, limit)
	for rows.Next() {
		var run effectiveHistoryRun
		var finishedAt int64
		if err := rows.Scan(&run.ID, &finishedAt); err != nil {
			return nil, fmt.Errorf("scan effective opportunity run: %w", err)
		}
		run.FinishedAt = time.UnixMilli(finishedAt).UTC()
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate effective opportunity runs: %w", err)
	}
	return runs, nil
}

func (r *SQLiteRepository) loadHistoryOpportunities(ctx context.Context, scans []historyScan, runIndex map[int64]int) error {
	const batchSize = 400
	for start := 0; start < len(scans); start += batchSize {
		end := min(start+batchSize, len(scans))
		args := make([]any, end-start)
		for index := start; index < end; index++ {
			args[index-start] = scans[index].RunID
		}
		query := `SELECT run_id, instrument_id, symbol, payload_json
			FROM market_opportunities
			WHERE run_id IN (` + sqlPlaceholders(len(args)) + `)
			ORDER BY run_id, instrument_id`
		rows, err := r.db.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("query historical opportunities: %w", err)
		}
		for rows.Next() {
			var runID int64
			var instrumentID, symbol, payload string
			if err := rows.Scan(&runID, &instrumentID, &symbol, &payload); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan historical opportunity: %w", err)
			}
			var opportunity domain.Opportunity
			if err := json.Unmarshal([]byte(payload), &opportunity); err != nil {
				_ = rows.Close()
				return fmt.Errorf("decode historical opportunity run %d instrument %s: %w", runID, instrumentID, err)
			}
			index, exists := runIndex[runID]
			if !exists {
				continue
			}
			scans[index].Opportunities[instrumentID] = historyPresence{
				InstrumentID: instrumentID,
				Symbol:       symbol,
				Price:        opportunity.CurrentPrice,
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate historical opportunities: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close historical opportunities: %w", err)
		}
	}
	return nil
}

func sqlPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func (r *SQLiteRepository) fillHistoryPrices(ctx context.Context, assetClass domain.AssetClass, rows []OpportunityHistoryRow, asOf time.Time) error {
	for rowIndex := range rows {
		row := &rows[rowIndex]
		for episodeIndex := range row.Episodes {
			episode := &row.Episodes[episodeIndex]
			at := asOf
			if episode.EndedAt != nil {
				at = *episode.EndedAt
			}
			price, err := r.historyPriceAtOrBefore(ctx, assetClass, row.InstrumentID, at)
			if err != nil {
				return err
			}
			episode.EvaluationPrice = price
			episode.ReturnPct = returnPercent(episode.FirstPrice, price)
		}
		latest := row.Episodes[len(row.Episodes)-1]
		row.EvaluationPrice = latest.EvaluationPrice
		row.ReturnPct = latest.ReturnPct
	}
	return nil
}

func (r *SQLiteRepository) historyPriceAtOrBefore(ctx context.Context, assetClass domain.AssetClass, instrumentID string, at time.Time) (*float64, error) {
	var price float64
	err := r.db.QueryRowContext(ctx, `
		SELECT price_usd
		FROM derivatives_market_snapshots
		WHERE asset_class = ? AND instrument_id = ? AND collected_at <= ?
		ORDER BY collected_at DESC, id DESC LIMIT 1`, assetClass, instrumentID, at.UnixMilli()).Scan(&price)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query historical price %s: %w", instrumentID, err)
	}
	return &price, nil
}
