package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/markets"
	_ "modernc.org/sqlite"
)

const (
	sqliteBusyTimeout = 5000
	momentumWindow    = 5 * time.Minute
	watchCadence      = 2 * time.Minute
)

const currentTokenSelect = `
	WITH current_tokens AS (
		SELECT
			t.id, t.chain, t.address, t.name, t.symbol, t.first_seen_at,
			COALESCE(
				(SELECT d.id FROM snapshots d
				 WHERE d.stage = 'DEEP' AND d.base_fast_snapshot_id = t.latest_fast_snapshot_id
				 ORDER BY d.id DESC LIMIT 1),
				t.latest_fast_snapshot_id
			) AS snapshot_id
		FROM tokens t
		WHERE t.latest_fast_snapshot_id IS NOT NULL
	)
	SELECT
		c.id, c.chain, c.address, c.name, c.symbol, c.first_seen_at,
		s.id, s.collected_at, s.market_cap_usd, s.fdv_usd, s.liquidity_usd,
		s.volume_m5_usd, s.buys_m5, s.sells_m5, s.buyers_m5, s.sellers_m5,
		s.aggregate_buyers_m5, s.aggregate_sellers_m5, s.price_change_m5, s.raw_score,
		s.raw_tier, s.effective_tier, s.evidence_available, s.evidence_total,
		s.evidence_confidence, s.launch_type, s.market_data_source,
		s.market_data_quality, s.stage, s.deep_status, s.score_version,
		s.score_breakdown_json, s.risk_flags_json, s.pair_address,
		s.discovery_pool_created_at, s.selected_pair_created_at,
		p.raw_score, p.volume_m5_usd, p.buyers_m5, p.sellers_m5,
		p.aggregate_buyers_m5, p.aggregate_sellers_m5, p.collected_at
	FROM current_tokens c
	JOIN snapshots s ON s.id = c.snapshot_id
	LEFT JOIN snapshots p ON p.id = (
		SELECT previous.id
		FROM snapshots previous
		WHERE previous.token_id = c.id
		  AND previous.collected_at < s.collected_at
		  AND previous.collected_at >= s.collected_at - 300000
		ORDER BY previous.collected_at ASC, previous.id ASC
		LIMIT 1
	)`

const tokenOrderSQL = `
	CASE s.effective_tier
		WHEN 'BREAKOUT' THEN 1
		WHEN 'FAST_RISING' THEN 2
		WHEN 'WATCH' THEN 3
		ELSE 4 END,
	CASE WHEN s.raw_score IS NULL OR p.raw_score IS NULL THEN 1 ELSE 0 END,
	(s.raw_score - p.raw_score) DESC,
	s.raw_score DESC,
	CASE s.evidence_confidence
		WHEN 'HIGH' THEN 1
		WHEN 'GOOD' THEN 2
		WHEN 'MEDIUM' THEN 3
		WHEN 'LOW' THEN 4
		ELSE 5 END,
	COALESCE(s.discovery_pool_created_at, s.selected_pair_created_at) DESC,
	s.collected_at DESC, s.id DESC`

// SQLiteRepository reads the scanner database without acquiring write authority.
type SQLiteRepository struct {
	db *sql.DB
}

// OpenSQLiteRepository opens an existing Plan 1.1 database in read-only mode.
func OpenSQLiteRepository(path string) (*SQLiteRepository, error) {
	dsn, err := sqliteReadOnlyDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open dashboard database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	closeOnError := func(err error) (*SQLiteRepository, error) {
		_ = db.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(fmt.Errorf("open dashboard database read-only: %w", err))
	}
	if _, err := db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return closeOnError(fmt.Errorf("enable dashboard query-only mode: %w", err))
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", sqliteBusyTimeout)); err != nil {
		return closeOnError(fmt.Errorf("configure dashboard busy timeout: %w", err))
	}
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return closeOnError(fmt.Errorf("read dashboard journal mode: %w", err))
	}
	if !strings.EqualFold(journalMode, "wal") {
		return closeOnError(fmt.Errorf("dashboard database journal mode is %q, want wal", journalMode))
	}
	rows, err := db.QueryContext(ctx, currentTokenSelect+" LIMIT 0")
	if err != nil {
		return closeOnError(fmt.Errorf("validate dashboard Plan 1.1 schema: %w", err))
	}
	if err := rows.Close(); err != nil {
		return closeOnError(fmt.Errorf("close dashboard schema validation: %w", err))
	}
	return &SQLiteRepository{db: db}, nil
}

func sqliteReadOnlyDSN(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve dashboard database path: %w", err)
	}
	normalized := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" && !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	uri := url.URL{Scheme: "file", Path: normalized}
	query := uri.Query()
	query.Set("mode", "ro")
	uri.RawQuery = query.Encode()
	return uri.String(), nil
}

// Close releases the Dashboard's read-only database connection.
func (r *SQLiteRepository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// ListRadar returns only actionable current candidates in Radar priority order.
func (r *SQLiteRepository) ListRadar(ctx context.Context, limit int) ([]TokenView, error) {
	if limit <= 0 {
		return []TokenView{}, nil
	}
	if limit > 30 {
		limit = 30
	}
	return r.queryTokens(ctx, currentTokenSelect+`
		WHERE s.effective_tier IN ('BREAKOUT', 'FAST_RISING')
		   OR (s.effective_tier = 'WATCH' AND s.evidence_confidence IN ('HIGH', 'GOOD'))
		ORDER BY `+tokenOrderSQL+`
		LIMIT ?`, []any{limit}, false)
}

// MarketRadar 读取八个主流币的最新两轮快照，并复用命令行扫描器的分析规则。
func (r *SQLiteRepository) MarketRadar(ctx context.Context) (markets.Report, error) {
	rows, err := r.db.QueryContext(ctx, `
		WITH ranked AS (
			SELECT asset_class, instrument_id, symbol, collected_at, source_time,
			       price_usd, change_24h_pct, turnover_24h_usd, open_interest_usd, funding_rate,
			       ROW_NUMBER() OVER (PARTITION BY instrument_id ORDER BY collected_at DESC, id DESC) AS observation_rank
			FROM derivatives_market_snapshots
			WHERE asset_class = 'crypto'
			  AND symbol IN ('BTC','ETH','SOL','BNB','XRP','ADA','LINK','AVAX')
		)
		SELECT asset_class, instrument_id, symbol, collected_at, source_time,
		       price_usd, change_24h_pct, turnover_24h_usd, open_interest_usd, funding_rate,
		       observation_rank
		FROM ranked
		WHERE observation_rank <= 2
		ORDER BY instrument_id, observation_rank`)
	if err != nil {
		return markets.Report{}, fmt.Errorf("query dashboard market radar: %w", err)
	}
	defer rows.Close()

	current := make([]domain.DerivativesSnapshot, 0, 8)
	previous := make(map[string]domain.DerivativesSnapshot, 8)
	for rows.Next() {
		snapshot, rank, err := scanDerivativesSnapshot(rows)
		if err != nil {
			return markets.Report{}, err
		}
		if rank == 1 {
			current = append(current, snapshot)
		} else {
			previous[snapshot.InstrumentID] = snapshot
		}
	}
	if err := rows.Err(); err != nil {
		return markets.Report{}, fmt.Errorf("iterate dashboard market radar: %w", err)
	}
	return markets.Analyze(current, previous, nil), nil
}

// MarketOpportunities 只读取最近一次完整发布的动态机会报告。
func (r *SQLiteRepository) MarketOpportunities(ctx context.Context, assetClass domain.AssetClass) (domain.OpportunityReport, error) {
	if !assetClass.Valid() {
		return domain.OpportunityReport{}, fmt.Errorf("invalid opportunity asset class %q", assetClass)
	}
	var report domain.OpportunityReport
	var startedAt, finishedAt int64
	var storedClass, status string
	err := r.db.QueryRowContext(ctx, `
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
		return domain.OpportunityReport{}, fmt.Errorf("query dashboard opportunities: %w", err)
	}
	report.Run.StartedAt = time.UnixMilli(startedAt).UTC()
	report.Run.FinishedAt = time.UnixMilli(finishedAt).UTC()
	report.Run.AssetClass = domain.AssetClass(storedClass)
	report.Run.Status = domain.OpportunityRunStatus(status)
	rows, err := r.db.QueryContext(ctx, `
		SELECT payload_json FROM market_opportunities WHERE run_id = ?
		ORDER BY opportunity_score DESC, fomo_score DESC, instrument_id ASC LIMIT 10`, report.Run.ID)
	if err != nil {
		return domain.OpportunityReport{}, fmt.Errorf("query dashboard opportunity rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return domain.OpportunityReport{}, fmt.Errorf("scan dashboard opportunity: %w", err)
		}
		var opportunity domain.Opportunity
		if err := json.Unmarshal([]byte(payload), &opportunity); err != nil {
			return domain.OpportunityReport{}, fmt.Errorf("decode dashboard opportunity: %w", err)
		}
		report.Opportunities = append(report.Opportunities, opportunity)
	}
	if err := rows.Err(); err != nil {
		return domain.OpportunityReport{}, fmt.Errorf("iterate dashboard opportunities: %w", err)
	}
	return report, nil
}

func scanDerivativesSnapshot(row sqlRowScanner) (domain.DerivativesSnapshot, int, error) {
	var snapshot domain.DerivativesSnapshot
	var assetClass string
	var collectedAt, sourceTime int64
	var openInterest, funding sql.NullFloat64
	var rank int
	if err := row.Scan(
		&assetClass, &snapshot.InstrumentID, &snapshot.Symbol, &collectedAt, &sourceTime,
		&snapshot.PriceUSD, &snapshot.Change24hPct, &snapshot.Turnover24hUSD,
		&openInterest, &funding, &rank,
	); err != nil {
		return domain.DerivativesSnapshot{}, 0, fmt.Errorf("scan dashboard market snapshot: %w", err)
	}
	snapshot.AssetClass = domain.AssetClass(assetClass)
	snapshot.CollectedAt = time.UnixMilli(collectedAt).UTC()
	snapshot.SourceTime = time.UnixMilli(sourceTime).UTC()
	snapshot.OpenInterestUSD = openInterest.Float64
	snapshot.OpenInterestAvailable = openInterest.Valid
	snapshot.FundingRate = funding.Float64
	snapshot.FundingRateAvailable = funding.Valid
	return snapshot, rank, nil
}

// ListTokens returns the current archive, including low-confidence and cooled candidates.
func (r *SQLiteRepository) ListTokens(ctx context.Context, filter TokenFilter) ([]TokenView, error) {
	if filter.Limit <= 0 {
		return []TokenView{}, nil
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}
	conditions := make([]string, 0, 6)
	args := make([]any, 0, 14)
	if query := strings.TrimSpace(filter.Query); query != "" {
		conditions = append(conditions, `(instr(lower(c.name), lower(?)) > 0 OR instr(lower(c.symbol), lower(?)) > 0 OR instr(lower(c.address), lower(?)) > 0)`)
		args = append(args, query, query, query)
	}
	if filter.Chain != "" {
		conditions = append(conditions, `c.chain = ?`)
		args = append(args, strings.ToLower(filter.Chain))
	}
	if filter.EffectiveTier != "" {
		conditions = append(conditions, `s.effective_tier = ?`)
		args = append(args, strings.ToUpper(filter.EffectiveTier))
	}
	if filter.LaunchType != "" {
		conditions = append(conditions, `s.launch_type = ?`)
		args = append(args, strings.ToUpper(filter.LaunchType))
	}
	if filter.EvidenceConfidence != "" {
		conditions = append(conditions, `s.evidence_confidence = ?`)
		args = append(args, strings.ToUpper(filter.EvidenceConfidence))
	}
	if filter.DataQuality != "" {
		conditions = append(conditions, `s.market_data_quality = ?`)
		args = append(args, strings.ToLower(filter.DataQuality))
	}
	query := currentTokenSelect
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY " + tokenOrderSQL + " LIMIT ?"
	args = append(args, filter.Limit)
	return r.queryTokens(ctx, query, args, filter.IncludeDetails)
}

func (r *SQLiteRepository) queryTokens(ctx context.Context, query string, args []any, includeDetails bool) ([]TokenView, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list dashboard tokens: %w", err)
	}
	defer rows.Close()
	tokens := make([]TokenView, 0)
	for rows.Next() {
		token, err := scanTokenView(rows, includeDetails)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard tokens: %w", err)
	}
	return tokens, nil
}

// TokenByAddress returns one current token and its immutable stored timeline.
func (r *SQLiteRepository) TokenByAddress(ctx context.Context, address, chain string, timelineLimit int) (TokenDetail, error) {
	query := currentTokenSelect
	args := make([]any, 0, 2)
	switch chain {
	case "":
		query += ` WHERE
			(c.chain = 'bsc' AND lower(c.address) = lower(?)) OR
			(c.chain = 'solana' AND c.address = ?)
			LIMIT 2`
		args = append(args, address, address)
	case string(domain.ChainBSC):
		query += ` WHERE c.chain = 'bsc' AND lower(c.address) = lower(?) LIMIT 2`
		args = append(args, address)
	case string(domain.ChainSolana):
		query += ` WHERE c.chain = 'solana' AND c.address = ? LIMIT 2`
		args = append(args, address)
	default:
		return TokenDetail{}, ErrTokenNotFound
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return TokenDetail{}, fmt.Errorf("query dashboard token: %w", err)
	}
	tokens := make([]TokenView, 0, 2)
	for rows.Next() {
		token, scanErr := scanTokenView(rows, true)
		if scanErr != nil {
			_ = rows.Close()
			return TokenDetail{}, scanErr
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return TokenDetail{}, fmt.Errorf("iterate dashboard token lookup: %w", err)
	}
	if err := rows.Close(); err != nil {
		return TokenDetail{}, fmt.Errorf("close dashboard token lookup: %w", err)
	}
	if len(tokens) == 0 {
		return TokenDetail{}, ErrTokenNotFound
	}
	if len(tokens) > 1 {
		return TokenDetail{}, ErrAmbiguousToken
	}
	timeline, err := r.tokenTimeline(ctx, tokens[0].ID, timelineLimit)
	if err != nil {
		return TokenDetail{}, err
	}
	detail := TokenDetail{Token: tokens[0], Timeline: timeline}
	for left := 0; left < len(timeline); left++ {
		if timeline[left].RawScore == nil {
			continue
		}
		for right := len(timeline) - 1; right > left; right-- {
			if timeline[right].RawScore == nil {
				continue
			}
			change := *timeline[right].RawScore - *timeline[left].RawScore
			duration := int64(timeline[right].CollectedAt.Sub(timeline[left].CollectedAt) / time.Second)
			detail.ScoreChange = &change
			detail.ScoreChangeDuration = &duration
			detail.ScoreFallingFast = change <= -20
			return detail, nil
		}
	}
	return detail, nil
}

func (r *SQLiteRepository) tokenTimeline(ctx context.Context, tokenID string, limit int) ([]TimelinePoint, error) {
	if limit <= 0 {
		return []TimelinePoint{}, nil
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, collected_at, stage, market_cap_usd, liquidity_usd, volume_m5_usd,
			COALESCE(buyers_m5, aggregate_buyers_m5), COALESCE(sellers_m5, aggregate_sellers_m5),
			raw_score, effective_tier, evidence_confidence, deep_status
		FROM snapshots
		WHERE token_id = ?
		ORDER BY collected_at DESC, id DESC
		LIMIT ?`, tokenID, limit)
	if err != nil {
		return nil, fmt.Errorf("query dashboard token timeline: %w", err)
	}
	defer rows.Close()

	timeline := make([]TimelinePoint, 0, limit)
	for rows.Next() {
		var point TimelinePoint
		var collectedAt int64
		var marketCap, liquidity, volume, rawScore sql.NullFloat64
		var buyers, sellers sql.NullInt64
		if err := rows.Scan(
			&point.SnapshotID, &collectedAt, &point.Stage, &marketCap, &liquidity, &volume,
			&buyers, &sellers, &rawScore, &point.EffectiveTier, &point.EvidenceConfidence, &point.DeepStatus,
		); err != nil {
			return nil, fmt.Errorf("scan dashboard token timeline: %w", err)
		}
		point.CollectedAt = time.UnixMilli(collectedAt).UTC()
		point.MarketCapUSD = nullableFloat(marketCap)
		point.LiquidityUSD = nullableFloat(liquidity)
		point.VolumeM5USD = nullableFloat(volume)
		point.BuyersM5 = nullableInt(buyers)
		point.SellersM5 = nullableInt(sellers)
		point.RawScore = nullableFloat(rawScore)
		timeline = append(timeline, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard token timeline: %w", err)
	}
	for left, right := 0, len(timeline)-1; left < right; left, right = left+1, right-1 {
		timeline[left], timeline[right] = timeline[right], timeline[left]
	}
	return timeline, nil
}

// DashboardStatus reads the latest persisted scan, coverage, and Deep lifecycle state.
func (r *SQLiteRepository) DashboardStatus(ctx context.Context) (ScannerStatus, error) {
	status := ScannerStatus{
		BSC: CoverageView{Chain: string(domain.ChainBSC)}, Solana: CoverageView{Chain: string(domain.ChainSolana)},
		GeckoStatus: "unknown", DexStatus: "unknown",
	}
	var startedAt int64
	var finishedAt sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT mode, started_at, finished_at, status
		FROM scan_runs ORDER BY id DESC LIMIT 1`).Scan(
		&status.Mode, &startedAt, &finishedAt, &status.RunStatus,
	)
	if err != nil && err != sql.ErrNoRows {
		return ScannerStatus{}, fmt.Errorf("read dashboard scanner status: %w", err)
	}
	if err == nil {
		status.Available = true
		started := time.UnixMilli(startedAt).UTC()
		activity := started
		if finishedAt.Valid {
			activity = time.UnixMilli(finishedAt.Int64).UTC()
		}
		next := started.Add(watchCadence)
		status.NextScanAt = &next
		status.Monitoring = status.Mode == "watch" && !activity.Before(time.Now().UTC().Add(-3*watchCadence/2))
	}

	var completedAt int64
	var completedStatus string
	err = r.db.QueryRowContext(ctx, `
		SELECT finished_at, status, candidates_seen, candidates_scored, error_summary
		FROM scan_runs WHERE finished_at IS NOT NULL ORDER BY id DESC LIMIT 1`).Scan(
		&completedAt, &completedStatus, &status.CandidatesSeen, &status.CandidatesScored, &status.ErrorSummary,
	)
	if err != nil && err != sql.ErrNoRows {
		return ScannerStatus{}, fmt.Errorf("read dashboard last completed scan: %w", err)
	}
	if err == nil {
		last := time.UnixMilli(completedAt).UTC()
		status.LastScanAt = &last
		status.GeckoStatus = providerStatus(completedStatus, status.ErrorSummary, "gecko")
		status.DexStatus = providerStatus(completedStatus, status.ErrorSummary, "dex")
	}

	var coverageErr error
	if status.BSC, coverageErr = r.latestCoverage(ctx, domain.ChainBSC); coverageErr != nil {
		return ScannerStatus{}, coverageErr
	}
	if status.Solana, coverageErr = r.latestCoverage(ctx, domain.ChainSolana); coverageErr != nil {
		return ScannerStatus{}, coverageErr
	}
	if err := r.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN s.deep_status IN ('pending','queued','deferred') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN s.deep_status = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN s.deep_status = 'failed' THEN 1 ELSE 0 END), 0)
		FROM tokens t JOIN snapshots s ON s.id = t.latest_fast_snapshot_id`).Scan(
		&status.DeepQueued, &status.DeepCompleted, &status.DeepFailed,
	); err != nil {
		return ScannerStatus{}, fmt.Errorf("read dashboard Deep status: %w", err)
	}
	return status, nil
}

func (r *SQLiteRepository) latestCoverage(ctx context.Context, chain domain.Chain) (CoverageView, error) {
	coverage := CoverageView{Chain: string(chain)}
	var scanStartedAt, newestPoolAt, oldestPoolAt sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT scan_started_at, newest_pool_at, oldest_pool_at, pages_fetched, unique_candidates
		FROM discovery_coverages WHERE chain = ?
		ORDER BY scan_started_at DESC, id DESC LIMIT 1`, chain).Scan(
		&scanStartedAt, &newestPoolAt, &oldestPoolAt, &coverage.PagesFetched, &coverage.UniqueCandidates,
	)
	if err == sql.ErrNoRows {
		return coverage, nil
	}
	if err != nil {
		return CoverageView{}, fmt.Errorf("read dashboard %s coverage: %w", chain, err)
	}
	coverage.ScanStartedAt = nullableTime(scanStartedAt)
	coverage.NewestPoolAt = nullableTime(newestPoolAt)
	coverage.OldestPoolAt = nullableTime(oldestPoolAt)
	if coverage.NewestPoolAt != nil && coverage.OldestPoolAt != nil && !coverage.NewestPoolAt.Before(*coverage.OldestPoolAt) {
		seconds := int64(coverage.NewestPoolAt.Sub(*coverage.OldestPoolAt) / time.Second)
		coverage.DurationSeconds = &seconds
	}
	return coverage, nil
}

func providerStatus(runStatus, errorSummary, provider string) string {
	if strings.Contains(strings.ToLower(errorSummary), strings.ToLower(provider)) {
		return "degraded"
	}
	if runStatus == "completed" || runStatus == "degraded" {
		return "healthy"
	}
	return "unknown"
}

type sqlRowScanner interface {
	Scan(...any) error
}

func scanTokenView(row sqlRowScanner, includeDetails bool) (TokenView, error) {
	var token TokenView
	var firstSeenAt, collectedAt int64
	var marketCap, fdv, liquidity, volumeM5, priceChange, rawScore sql.NullFloat64
	var buys, sells, buyers, sellers, aggregateBuyers, aggregateSellers sql.NullInt64
	var discoveryPoolCreatedAt, selectedPairCreatedAt sql.NullInt64
	var previousRawScore, previousVolume sql.NullFloat64
	var previousBuyers, previousSellers, previousAggregateBuyers, previousAggregateSellers sql.NullInt64
	var previousCollectedAt sql.NullInt64
	var breakdownJSON, riskFlagsJSON string
	if err := row.Scan(
		&token.ID, &token.Chain, &token.Address, &token.Name, &token.Symbol, &firstSeenAt,
		&token.SnapshotID, &collectedAt, &marketCap, &fdv, &liquidity,
		&volumeM5, &buys, &sells, &buyers, &sellers, &aggregateBuyers, &aggregateSellers,
		&priceChange, &rawScore, &token.RawTier, &token.EffectiveTier,
		&token.EvidenceAvailable, &token.EvidenceTotal, &token.EvidenceConfidence,
		&token.LaunchType, &token.MarketDataSource, &token.DataQuality, &token.Stage,
		&token.DeepStatus, &token.ScoreVersion, &breakdownJSON, &riskFlagsJSON,
		&token.PairAddress, &discoveryPoolCreatedAt, &selectedPairCreatedAt,
		&previousRawScore, &previousVolume, &previousBuyers, &previousSellers,
		&previousAggregateBuyers, &previousAggregateSellers, &previousCollectedAt,
	); err != nil {
		return TokenView{}, fmt.Errorf("scan dashboard token: %w", err)
	}
	token.FirstSeenAt = time.UnixMilli(firstSeenAt).UTC()
	token.CollectedAt = time.UnixMilli(collectedAt).UTC()
	token.MarketCapUSD = nullableFloat(marketCap)
	token.FDVUSD = nullableFloat(fdv)
	token.LiquidityUSD = nullableFloat(liquidity)
	token.VolumeM5USD = nullableFloat(volumeM5)
	token.BuysM5 = nullableInt(buys)
	token.SellsM5 = nullableInt(sells)
	token.UniqueBuyersM5 = nullableInt(buyers)
	token.UniqueSellersM5 = nullableInt(sellers)
	token.AggregateBuyersM5 = nullableInt(aggregateBuyers)
	token.AggregateSellersM5 = nullableInt(aggregateSellers)
	token.PriceChangeM5 = nullableFloat(priceChange)
	token.RawScore = nullableFloat(rawScore)
	token.PreviousRawScore = nullableFloat(previousRawScore)
	token.PreviousVolumeM5USD = nullableFloat(previousVolume)
	if discoveryPoolCreatedAt.Valid {
		value := time.UnixMilli(discoveryPoolCreatedAt.Int64).UTC()
		token.DiscoveryPoolCreatedAt = &value
		token.DiscoveryPoolAge = ageAt(token.CollectedAt, &value)
	}
	if selectedPairCreatedAt.Valid {
		value := time.UnixMilli(selectedPairCreatedAt.Int64).UTC()
		token.SelectedPairCreatedAt = &value
		token.SelectedPairAge = ageAt(token.CollectedAt, &value)
	}
	token.AgeSeconds = tokenAgeSeconds(token.CollectedAt, token.DiscoveryPoolCreatedAt, token.SelectedPairCreatedAt)

	token.MomentumBuyersM5, token.MomentumSellersM5, token.ParticipantEvidence = participantValues(
		token.UniqueBuyersM5, token.UniqueSellersM5, token.AggregateBuyersM5, token.AggregateSellersM5,
	)
	previousUniqueBuyers := nullableInt(previousBuyers)
	previousUniqueSellers := nullableInt(previousSellers)
	previousAggregateBuyerView := nullableInt(previousAggregateBuyers)
	previousAggregateSellerView := nullableInt(previousAggregateSellers)
	token.PreviousBuyersM5, _, _ = participantValues(
		previousUniqueBuyers, previousUniqueSellers, previousAggregateBuyerView, previousAggregateSellerView,
	)
	if token.RawScore != nil && token.PreviousRawScore != nil {
		delta := *token.RawScore - *token.PreviousRawScore
		token.ScoreDelta = &delta
	}
	if token.VolumeM5USD != nil && token.PreviousVolumeM5USD != nil && *token.PreviousVolumeM5USD > 0 {
		change := (*token.VolumeM5USD - *token.PreviousVolumeM5USD) / *token.PreviousVolumeM5USD * 100
		token.VolumeChangePercent = &change
	}
	if token.MomentumBuyersM5 != nil && token.MomentumSellersM5 != nil && *token.MomentumSellersM5 > 0 {
		ratio := float64(*token.MomentumBuyersM5) / float64(*token.MomentumSellersM5)
		token.BuySellRatio = &ratio
	}
	if token.BuysM5 != nil && token.SellsM5 != nil {
		total := *token.BuysM5 + *token.SellsM5
		token.TotalTradesM5 = &total
	}

	if includeDetails {
		var breakdown domain.ScoreBreakdown
		if err := json.Unmarshal([]byte(breakdownJSON), &breakdown); err != nil {
			return TokenView{}, fmt.Errorf("decode dashboard score breakdown: %w", err)
		}
		token.Factors = factorViews(breakdown.Factors)
		token.AgeBonus = breakdown.AgeBonus
		token.RiskPenalty = breakdown.RiskPenalty
		if err := json.Unmarshal([]byte(riskFlagsJSON), &token.RiskFlags); err != nil {
			return TokenView{}, fmt.Errorf("decode dashboard risk flags: %w", err)
		}
		if token.RiskFlags == nil {
			token.RiskFlags = []string{}
		}
	}
	return token, nil
}

func participantValues(uniqueBuyers, uniqueSellers, aggregateBuyers, aggregateSellers *int) (*int, *int, string) {
	if uniqueBuyers != nil || uniqueSellers != nil {
		return uniqueBuyers, uniqueSellers, "address_deduplicated"
	}
	if aggregateBuyers != nil || aggregateSellers != nil {
		return aggregateBuyers, aggregateSellers, "gecko_aggregate"
	}
	return nil, nil, "unavailable"
}

func factorViews(factors map[string]domain.FactorScore) map[string]FactorView {
	views := make(map[string]FactorView, len(factors))
	for name, factor := range factors {
		view := FactorView{Maximum: factor.Maximum, Available: factor.Available}
		if factor.Available {
			points := factor.Points
			view.Points = &points
		}
		views[name] = view
	}
	return views
}

func nullableFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func nullableInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
}

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := time.UnixMilli(value.Int64).UTC()
	return &result
}

func tokenAgeSeconds(collectedAt time.Time, discoveryPoolCreatedAt, selectedPairCreatedAt *time.Time) *int64 {
	createdAt := discoveryPoolCreatedAt
	if createdAt == nil {
		createdAt = selectedPairCreatedAt
	}
	return ageAt(collectedAt, createdAt)
}

func ageAt(collectedAt time.Time, createdAt *time.Time) *int64 {
	if createdAt == nil || collectedAt.Before(*createdAt) {
		return nil
	}
	seconds := int64(collectedAt.Sub(*createdAt) / time.Second)
	return &seconds
}
