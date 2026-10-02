package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	_ "modernc.org/sqlite"
)

//go:embed migrations/001_core.sql
var coreSchema string

//go:embed migrations/002_live_hardening.sql
var liveHardeningSchema string

//go:embed migrations/003_market_momentum.sql
var marketMomentumSchema string

//go:embed migrations/004_champion_challenger.sql
var championChallengerSchema string

//go:embed migrations/005_derivatives_market.sql
var derivativesMarketSchema string

//go:embed migrations/006_market_opportunities.sql
var marketOpportunitiesSchema string

//go:embed migrations/007_stock_radar.sql
var stockRadarSchema string

var liveColumns = []struct {
	table      string
	name       string
	definition string
}{
	{"tokens", "latest_fast_snapshot_id", "INTEGER"},
	{"snapshots", "stage", "TEXT NOT NULL DEFAULT ''"},
	{"snapshots", "pair_address", "TEXT NOT NULL DEFAULT ''"},
	{"snapshots", "discovery_pool_address", "TEXT NOT NULL DEFAULT ''"},
	{"snapshots", "base_fast_snapshot_id", "INTEGER"},
	{"snapshots", "deep_status", "TEXT NOT NULL DEFAULT ''"},
	{"snapshots", "discovery_pool_created_at", "INTEGER"},
	{"snapshots", "selected_pair_created_at", "INTEGER"},
	{"snapshots", "signal_first_seen_at", "INTEGER"},
	{"snapshots", "launch_type", "TEXT NOT NULL DEFAULT ''"},
	{"snapshots", "market_data_source", "TEXT NOT NULL DEFAULT ''"},
	{"snapshots", "market_data_quality", "TEXT NOT NULL DEFAULT ''"},
	{"snapshots", "aggregate_buyers_m5", "INTEGER"},
	{"snapshots", "aggregate_sellers_m5", "INTEGER"},
	{"snapshots", "raw_score", "REAL"},
	{"snapshots", "raw_tier", "TEXT NOT NULL DEFAULT ''"},
	{"snapshots", "evidence_available", "INTEGER NOT NULL DEFAULT 0"},
	{"snapshots", "evidence_total", "INTEGER NOT NULL DEFAULT 0"},
	{"snapshots", "evidence_confidence", "TEXT NOT NULL DEFAULT ''"},
	{"snapshots", "effective_tier", "TEXT NOT NULL DEFAULT ''"},
}

var momentumColumns = []struct {
	table      string
	name       string
	definition string
}{
	{"snapshots", "candidate_origin", "TEXT"},
	{"snapshots", "momentum_sources_json", "TEXT"},
	{"snapshots", "momentum_trigger_json", "TEXT"},
	{"snapshots", "trigger_pool_address", "TEXT"},
	{"snapshots", "trigger_pool_created_at", "INTEGER"},
	{"snapshots", "token_age_seconds", "INTEGER"},
	{"snapshots", "token_age_source", "TEXT"},
	{"snapshots", "volume_h6_usd", "REAL"},
	{"snapshots", "volume_h24_usd", "REAL"},
	{"snapshots", "price_change_h1", "REAL"},
	{"snapshots", "price_change_h6", "REAL"},
	{"snapshots", "price_change_h24", "REAL"},
	{"snapshots", "buys_h1", "INTEGER"},
	{"snapshots", "sells_h1", "INTEGER"},
	{"snapshots", "buys_h6", "INTEGER"},
	{"snapshots", "sells_h6", "INTEGER"},
	{"snapshots", "buys_h24", "INTEGER"},
	{"snapshots", "sells_h24", "INTEGER"},
	{"snapshots", "aggregate_buyers_h1", "INTEGER"},
	{"snapshots", "aggregate_sellers_h1", "INTEGER"},
	{"snapshots", "aggregate_buyers_h6", "INTEGER"},
	{"snapshots", "aggregate_sellers_h6", "INTEGER"},
	{"snapshots", "aggregate_buyers_h24", "INTEGER"},
	{"snapshots", "aggregate_sellers_h24", "INTEGER"},
	{"snapshots", "launch_bonus", "REAL"},
	{"snapshots", "momentum_baseline_at", "INTEGER"},
	{"snapshots", "momentum_evidence_json", "TEXT"},
}

var momentumUniverseColumns = []struct {
	table      string
	name       string
	definition string
}{
	{"momentum_universe", "last_trade_at", "INTEGER"},
	{"momentum_universe", "liquidity_usd", "REAL"},
	{"momentum_universe", "volume_h24_usd", "REAL"},
}

var stockRadarColumns = []struct {
	table      string
	name       string
	definition string
}{
	{"market_opportunity_runs", "asset_class", "TEXT NOT NULL DEFAULT 'crypto' CHECK(asset_class IN ('crypto','stock'))"},
}

const snapshotSelectColumns = `
	id, token_id, scan_run_id, collected_at, source_time, price_usd, market_cap_usd,
	fdv_usd, liquidity_usd, volume_m5_usd, volume_h1_usd, buys_m5, sells_m5,
	buyers_m5, sellers_m5, price_change_m5, score, tier, score_version,
	score_breakdown_json, risk_flags_json, data_quality_json, pair_address,
	discovery_pool_address, stage, base_fast_snapshot_id, deep_status,
	discovery_pool_created_at, selected_pair_created_at, signal_first_seen_at,
	launch_type, market_data_source, market_data_quality, aggregate_buyers_m5,
	aggregate_sellers_m5, raw_score, raw_tier, evidence_available, evidence_total,
	evidence_confidence, effective_tier, candidate_origin, momentum_sources_json,
	momentum_trigger_json, trigger_pool_address, trigger_pool_created_at,
	token_age_seconds, token_age_source, volume_h6_usd, volume_h24_usd,
	price_change_h1, price_change_h6, price_change_h24, buys_h1, sells_h1,
	buys_h6, sells_h6, buys_h24, sells_h24, aggregate_buyers_h1,
	aggregate_sellers_h1, aggregate_buyers_h6, aggregate_sellers_h6,
	aggregate_buyers_h24, aggregate_sellers_h24, launch_bonus,
	momentum_baseline_at, momentum_evidence_json`

// Store persists scanner state in SQLite.
type Store struct {
	db             *sql.DB
	upsertToken    *sql.Stmt
	insertSnapshot *sql.Stmt
	startScanRun   *sql.Stmt
	finishScanRun  *sql.Stmt
}

const maxMomentumUniversePerChain = 5000

// Open opens a SQLite database and applies the core schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := configure(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(coreSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply core schema: %w", err)
	}
	if err := applyLiveHardening(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := applyMarketMomentum(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := applyChampionChallenger(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(derivativesMarketSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply derivatives market schema: %w", err)
	}
	if _, err := db.Exec(marketOpportunitiesSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply market opportunities schema: %w", err)
	}
	if err := applyStockRadar(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &Store{db: db}
	if s.upsertToken, err = db.Prepare(`
		INSERT INTO tokens (
			id, chain, address, name, symbol, pair_address, first_seen_at,
			first_seen_price_usd, first_seen_market_cap_usd, first_seen_score, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			chain = excluded.chain, address = excluded.address, name = excluded.name,
			symbol = excluded.symbol, pair_address = excluded.pair_address`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prepare token upsert: %w", err)
	}
	if s.insertSnapshot, err = db.Prepare(`
		INSERT INTO snapshots (
			token_id, scan_run_id, collected_at, source_time, price_usd, market_cap_usd,
			fdv_usd, liquidity_usd, volume_m5_usd, volume_h1_usd, buys_m5, sells_m5,
			buyers_m5, sellers_m5, price_change_m5, score, tier, score_version,
			score_breakdown_json, risk_flags_json, data_quality_json, pair_address, discovery_pool_address,
			stage, base_fast_snapshot_id,
			deep_status, discovery_pool_created_at, selected_pair_created_at, signal_first_seen_at,
			launch_type, market_data_source, market_data_quality, aggregate_buyers_m5,
			aggregate_sellers_m5, raw_score, raw_tier, evidence_available, evidence_total,
			evidence_confidence, effective_tier, candidate_origin, momentum_sources_json,
			momentum_trigger_json, trigger_pool_address, trigger_pool_created_at,
			token_age_seconds, token_age_source, volume_h6_usd, volume_h24_usd,
			price_change_h1, price_change_h6, price_change_h24, buys_h1, sells_h1,
			buys_h6, sells_h6, buys_h24, sells_h24, aggregate_buyers_h1,
			aggregate_sellers_h1, aggregate_buyers_h6, aggregate_sellers_h6,
			aggregate_buyers_h24, aggregate_sellers_h24, launch_bonus,
			momentum_baseline_at, momentum_evidence_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("prepare snapshot insert: %w", err)
	}
	if s.startScanRun, err = db.Prepare(`
		INSERT INTO scan_runs (mode, started_at, status) VALUES (?, ?, 'running')`); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("prepare scan start: %w", err)
	}
	if s.finishScanRun, err = db.Prepare(`
		UPDATE scan_runs SET finished_at = ?, status = ?, candidates_seen = ?, candidates_scored = ?, error_summary = ? WHERE id = ?`); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("prepare scan finish: %w", err)
	}
	return s, nil
}

func applyLiveHardening(db *sql.DB) error {
	for _, column := range liveColumns {
		exists, err := columnExists(db, column.table, column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.table, column.name, column.definition)
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("add %s.%s: %w", column.table, column.name, err)
		}
	}
	if _, err := db.Exec(liveHardeningSchema); err != nil {
		return fmt.Errorf("apply live hardening schema: %w", err)
	}
	return nil
}

func applyMarketMomentum(db *sql.DB) error {
	if err := addMissingColumns(db, momentumColumns); err != nil {
		return err
	}
	if _, err := db.Exec(marketMomentumSchema); err != nil {
		return fmt.Errorf("apply market momentum schema: %w", err)
	}
	return addMissingColumns(db, momentumUniverseColumns)
}

func applyChampionChallenger(db *sql.DB) error {
	if _, err := db.Exec(championChallengerSchema); err != nil {
		return fmt.Errorf("apply Champion/Challenger schema: %w", err)
	}
	return nil
}

func applyStockRadar(db *sql.DB) error {
	if err := addMissingColumns(db, stockRadarColumns); err != nil {
		return err
	}
	if _, err := db.Exec(stockRadarSchema); err != nil {
		return fmt.Errorf("apply stock radar schema: %w", err)
	}
	return nil
}

func addMissingColumns(db *sql.DB, columns []struct {
	table      string
	name       string
	definition string
}) error {
	for _, column := range columns {
		exists, err := columnExists(db, column.table, column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.table, column.name, column.definition)
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("add %s.%s: %w", column.table, column.name, err)
		}
	}
	return nil
}

func columnExists(db *sql.DB, table, want string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("scan %s columns: %w", table, err)
		}
		if name == want {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, fmt.Errorf("iterate %s columns: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close %s columns: %w", table, err)
	}
	return found, nil
}

func configure(db *sql.DB) error {
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("configure sqlite (%s): %w", pragma, err)
		}
	}
	return nil
}

// Close releases database and prepared statement resources.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var firstErr error
	for _, stmt := range []*sql.Stmt{s.upsertToken, s.insertSnapshot, s.startScanRun, s.finishScanRun} {
		if stmt != nil {
			if err := stmt.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// UpsertToken updates mutable metadata while preserving first-discovery fields.
func (s *Store) UpsertToken(ctx context.Context, candidate domain.Candidate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin token upsert: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Commit makes this a no-op.

	if err := s.upsertTokenTx(ctx, tx, candidate); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit token upsert: %w", err)
	}
	return nil
}

func (s *Store) upsertTokenTx(ctx context.Context, tx *sql.Tx, candidate domain.Candidate) error {
	_, err := tx.StmtContext(ctx, s.upsertToken).ExecContext(ctx,
		candidate.ID, candidate.Chain, candidate.Address, candidate.Name, candidate.Symbol, candidate.PairAddress,
		timestamp(candidate.DetectedAt), floatValueOrNil(candidate.FirstSeenPriceUSD),
		floatValueOrNil(candidate.FirstSeenMarketCapUSD), floatValueOrNil(candidate.FirstSeenScore), timestampOrNil(candidate.ChainCreatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert token: %w", err)
	}
	return nil
}

// GetTokenFirstSeen returns the immutable first local discovery time.
func (s *Store) GetTokenFirstSeen(ctx context.Context, tokenID string) (time.Time, bool, error) {
	var firstSeen int64
	if err := s.db.QueryRowContext(ctx, `SELECT first_seen_at FROM tokens WHERE id = ?`, tokenID).Scan(&firstSeen); err != nil {
		if err == sql.ErrNoRows {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("read token first seen: %w", err)
	}
	return fromTimestamp(firstSeen), true, nil
}

// AdmitMomentumUniverse adds or refreshes one token in the bounded active universe.
// Eviction affects membership only; immutable token and snapshot history is retained.
func (s *Store) AdmitMomentumUniverse(ctx context.Context, candidate domain.Candidate, source domain.UniverseAdmissionSource, observedAt time.Time) (bool, error) {
	priority, err := universeAdmissionPriority(source)
	if err != nil {
		return false, err
	}
	if candidate.ID == "" || candidate.Address == "" {
		return false, fmt.Errorf("admit Momentum universe: token ID and address are required")
	}
	if candidate.Chain != domain.ChainBSC && candidate.Chain != domain.ChainSolana {
		return false, fmt.Errorf("admit Momentum universe: unsupported chain %q", candidate.Chain)
	}
	if observedAt.IsZero() {
		return false, fmt.Errorf("admit Momentum universe: observed time is required")
	}
	if candidate.DetectedAt.IsZero() {
		candidate.DetectedAt = observedAt
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin Momentum universe admission: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := s.upsertTokenTx(ctx, tx, candidate); err != nil {
		return false, err
	}

	var currentPriority int
	var currentSource string
	var currentLastSeen int64
	err = tx.QueryRowContext(ctx, `
		SELECT admission_priority, admission_source, last_seen_at
		FROM momentum_universe WHERE token_id = ?`, candidate.ID).Scan(
		&currentPriority, &currentSource, &currentLastSeen,
	)
	if err == nil {
		newPriority := currentPriority
		newSource := currentSource
		if priority > currentPriority {
			newPriority = priority
			newSource = string(source)
		}
		lastSeen := timestamp(observedAt)
		if currentLastSeen > lastSeen {
			lastSeen = currentLastSeen
		}
		var lastMomentum any
		if source != domain.UniverseAdmissionHistorical {
			lastMomentum = timestamp(observedAt)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE momentum_universe
			SET admission_source = ?, admission_priority = ?, last_seen_at = ?,
				last_momentum_at = CASE
					WHEN ? IS NULL THEN last_momentum_at
					WHEN last_momentum_at IS NULL OR ? > last_momentum_at THEN ?
					ELSE last_momentum_at
				END,
				liquidity_usd = CASE WHEN ? >= last_seen_at AND ? IS NOT NULL THEN ? ELSE liquidity_usd END,
				volume_h24_usd = CASE WHEN ? >= last_seen_at AND ? IS NOT NULL THEN ? ELSE volume_h24_usd END
			WHERE token_id = ?`, newSource, newPriority, lastSeen,
			lastMomentum, lastMomentum, lastMomentum,
			timestamp(observedAt), floatValueOrNil(candidate.DiscoveryMarket.LiquidityUSD), floatValueOrNil(candidate.DiscoveryMarket.LiquidityUSD),
			timestamp(observedAt), floatValueOrNil(candidate.DiscoveryMarket.Volume24hUSD), floatValueOrNil(candidate.DiscoveryMarket.Volume24hUSD),
			candidate.ID); err != nil {
			return false, fmt.Errorf("refresh Momentum universe member: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit Momentum universe refresh: %w", err)
		}
		return true, nil
	}
	if err != sql.ErrNoRows {
		return false, fmt.Errorf("read Momentum universe member: %w", err)
	}

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM momentum_universe WHERE chain = ?`, candidate.Chain).Scan(&count); err != nil {
		return false, fmt.Errorf("count Momentum universe: %w", err)
	}
	if count >= maxMomentumUniversePerChain {
		var victim universeRank
		if err := tx.QueryRowContext(ctx, `
			SELECT token_id, last_momentum_at, last_trade_at, liquidity_usd, volume_h24_usd, last_seen_at
			FROM momentum_universe WHERE chain = ?
			ORDER BY last_momentum_at ASC, last_trade_at ASC, liquidity_usd ASC,
				volume_h24_usd ASC, last_seen_at ASC, token_id ASC LIMIT 1`, candidate.Chain).Scan(
			&victim.tokenID, &victim.lastMomentum, &victim.lastTrade, &victim.liquidity, &victim.volumeH24, &victim.lastSeen,
		); err != nil {
			return false, fmt.Errorf("select Momentum universe eviction: %w", err)
		}
		incoming := universeRank{
			tokenID: candidate.ID, lastSeen: timestamp(observedAt),
			liquidity: nullFloat(floatValueOrNil(candidate.DiscoveryMarket.LiquidityUSD)),
			volumeH24: nullFloat(floatValueOrNil(candidate.DiscoveryMarket.Volume24hUSD)),
		}
		if source != domain.UniverseAdmissionHistorical {
			incoming.lastMomentum = sql.NullInt64{Int64: timestamp(observedAt), Valid: true}
		}
		if !universeRankStronger(incoming, victim) {
			if err := tx.Commit(); err != nil {
				return false, fmt.Errorf("commit declined Momentum universe admission: %w", err)
			}
			return false, nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM momentum_universe WHERE token_id = ?`, victim.tokenID); err != nil {
			return false, fmt.Errorf("evict Momentum universe member: %w", err)
		}
	}

	var lastMomentum any
	if source != domain.UniverseAdmissionHistorical {
		lastMomentum = timestamp(observedAt)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO momentum_universe (
			token_id, chain, admission_source, admission_priority,
			first_added_at, last_seen_at, last_momentum_at, liquidity_usd, volume_h24_usd
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, candidate.ID, candidate.Chain, source, priority,
		timestamp(observedAt), timestamp(observedAt), lastMomentum,
		floatValueOrNil(candidate.DiscoveryMarket.LiquidityUSD), floatValueOrNil(candidate.DiscoveryMarket.Volume24hUSD)); err != nil {
		return false, fmt.Errorf("insert Momentum universe member: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit Momentum universe admission: %w", err)
	}
	return true, nil
}

type universeRank struct {
	tokenID      string
	lastMomentum sql.NullInt64
	lastTrade    sql.NullInt64
	liquidity    sql.NullFloat64
	volumeH24    sql.NullFloat64
	lastSeen     int64
}

func nullFloat(value any) sql.NullFloat64 {
	if value == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: value.(float64), Valid: true}
}

func universeRankStronger(left, right universeRank) bool {
	if comparison := compareNullInt(left.lastMomentum, right.lastMomentum); comparison != 0 {
		return comparison > 0
	}
	if comparison := compareNullInt(left.lastTrade, right.lastTrade); comparison != 0 {
		return comparison > 0
	}
	if comparison := compareNullFloat(left.liquidity, right.liquidity); comparison != 0 {
		return comparison > 0
	}
	if comparison := compareNullFloat(left.volumeH24, right.volumeH24); comparison != 0 {
		return comparison > 0
	}
	if left.lastSeen != right.lastSeen {
		return left.lastSeen > right.lastSeen
	}
	return left.tokenID > right.tokenID
}

func compareNullInt(left, right sql.NullInt64) int {
	if left.Valid != right.Valid {
		if left.Valid {
			return 1
		}
		return -1
	}
	if !left.Valid || left.Int64 == right.Int64 {
		return 0
	}
	if left.Int64 > right.Int64 {
		return 1
	}
	return -1
}

func compareNullFloat(left, right sql.NullFloat64) int {
	if left.Valid != right.Valid {
		if left.Valid {
			return 1
		}
		return -1
	}
	if !left.Valid || left.Float64 == right.Float64 {
		return 0
	}
	if left.Float64 > right.Float64 {
		return 1
	}
	return -1
}

// UpdateMomentumUniverseEvidence 单调更新活跃 Universe 的市场淘汰证据。
func (s *Store) UpdateMomentumUniverseEvidence(ctx context.Context, tokenID string, lastTradeAt *time.Time, market domain.MarketSnapshot, observedAt time.Time) error {
	if tokenID == "" || observedAt.IsZero() {
		return fmt.Errorf("update Momentum universe evidence: token ID and observed time are required")
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE momentum_universe SET
			last_trade_at = CASE
				WHEN ? IS NULL THEN last_trade_at
				WHEN last_trade_at IS NULL OR ? > last_trade_at THEN ? ELSE last_trade_at END,
			liquidity_usd = CASE WHEN ? >= last_seen_at AND ? IS NOT NULL THEN ? ELSE liquidity_usd END,
			volume_h24_usd = CASE WHEN ? >= last_seen_at AND ? IS NOT NULL THEN ? ELSE volume_h24_usd END,
			last_seen_at = CASE WHEN ? > last_seen_at THEN ? ELSE last_seen_at END
		WHERE token_id = ?`,
		timestampOrNil(lastTradeAt), timestampOrNil(lastTradeAt), timestampOrNil(lastTradeAt),
		timestamp(observedAt), floatValueOrNil(market.LiquidityUSD), floatValueOrNil(market.LiquidityUSD),
		timestamp(observedAt), floatValueOrNil(market.Volume24hUSD), floatValueOrNil(market.Volume24hUSD),
		timestamp(observedAt), timestamp(observedAt), tokenID,
	); err != nil {
		return fmt.Errorf("update Momentum universe evidence: %w", err)
	}
	return nil
}

func universeAdmissionPriority(source domain.UniverseAdmissionSource) (int, error) {
	switch source {
	case domain.UniverseAdmissionHistorical:
		return 100, nil
	case domain.UniverseAdmissionTrending:
		return 200, nil
	case domain.UniverseAdmissionAnomaly:
		return 300, nil
	default:
		return 0, fmt.Errorf("admit Momentum universe: unsupported admission source %q", source)
	}
}

// SeedMomentumUniverseFromHistory 用已有 Token 填充活跃 Universe 的空位；不改写历史。
func (s *Store) SeedMomentumUniverseFromHistory(ctx context.Context, chain domain.Chain, observedAt time.Time) (int, error) {
	if chain != domain.ChainBSC && chain != domain.ChainSolana {
		return 0, fmt.Errorf("seed Momentum universe: unsupported chain %q", chain)
	}
	if observedAt.IsZero() {
		return 0, fmt.Errorf("seed Momentum universe: observed time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin Momentum history seed: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM momentum_universe WHERE chain = ?`, chain).Scan(&active); err != nil {
		return 0, fmt.Errorf("count Momentum universe before history seed: %w", err)
	}
	remaining := maxMomentumUniversePerChain - active
	if remaining <= 0 {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("commit empty Momentum history seed: %w", err)
		}
		return 0, nil
	}
	result, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO momentum_universe (
			token_id, chain, admission_source, admission_priority, first_added_at,
			last_seen_at, last_momentum_at, liquidity_usd, volume_h24_usd
		)
		SELECT t.id, t.chain, ?, ?, ?, COALESCE(s.collected_at, t.first_seen_at),
			CASE WHEN s.candidate_origin IN (?, ?) THEN s.collected_at END,
			s.liquidity_usd, s.volume_h24_usd
		FROM tokens t
		LEFT JOIN momentum_universe u ON u.token_id = t.id
		LEFT JOIN snapshots s ON s.id = t.latest_fast_snapshot_id
		WHERE t.chain = ? AND u.token_id IS NULL
		ORDER BY
			CASE WHEN s.candidate_origin IN (?, ?) THEN 1 ELSE 0 END DESC,
			s.collected_at DESC, s.liquidity_usd DESC, s.volume_h24_usd DESC,
			COALESCE(s.collected_at, t.first_seen_at) DESC, t.id DESC
		LIMIT ?`,
		domain.UniverseAdmissionHistorical, 100, timestamp(observedAt),
		domain.CandidateOriginMomentum, domain.CandidateOriginBoth, chain,
		domain.CandidateOriginMomentum, domain.CandidateOriginBoth, remaining,
	)
	if err != nil {
		return 0, fmt.Errorf("seed Momentum universe from history: %w", err)
	}
	seeded, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count Momentum history seed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit Momentum history seed: %w", err)
	}
	return int(seeded), nil
}

// ListMomentumUniverse returns the next stable, bounded round-robin batch. An empty
// afterTokenID resumes from the persisted chain cursor.
func (s *Store) ListMomentumUniverse(ctx context.Context, chain domain.Chain, afterTokenID string, limit int) ([]domain.Candidate, string, error) {
	if chain != domain.ChainBSC && chain != domain.ChainSolana {
		return nil, "", fmt.Errorf("list Momentum universe: unsupported chain %q", chain)
	}
	if limit <= 0 {
		return []domain.Candidate{}, afterTokenID, nil
	}
	if limit > 30 {
		limit = 30
	}
	if afterTokenID == "" {
		err := s.db.QueryRowContext(ctx, `SELECT last_token_id FROM momentum_scan_cursors WHERE chain = ?`, chain).Scan(&afterTokenID)
		if err != nil && err != sql.ErrNoRows {
			return nil, "", fmt.Errorf("read Momentum cursor: %w", err)
		}
	}

	candidates, err := s.listMomentumUniverseAfter(ctx, chain, afterTokenID, limit)
	if err != nil {
		return nil, "", err
	}
	if len(candidates) == 0 && afterTokenID != "" {
		candidates, err = s.listMomentumUniverseAfter(ctx, chain, "", limit)
		if err != nil {
			return nil, "", err
		}
	}
	if len(candidates) == 0 {
		return candidates, "", nil
	}
	return candidates, candidates[len(candidates)-1].ID, nil
}

func (s *Store) listMomentumUniverseAfter(ctx context.Context, chain domain.Chain, afterTokenID string, limit int) ([]domain.Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.chain, t.address, t.name, t.symbol, t.pair_address,
			t.first_seen_at, t.created_at, t.first_seen_price_usd,
			t.first_seen_market_cap_usd, t.first_seen_score
		FROM momentum_universe u
		JOIN tokens t ON t.id = u.token_id
		WHERE u.chain = ? AND u.token_id > ?
		ORDER BY u.token_id ASC LIMIT ?`, chain, afterTokenID, limit)
	if err != nil {
		return nil, fmt.Errorf("query Momentum universe: %w", err)
	}
	defer rows.Close()

	candidates := make([]domain.Candidate, 0, limit)
	for rows.Next() {
		var candidate domain.Candidate
		var storedChain string
		var firstSeenAt int64
		var createdAt sql.NullInt64
		var firstPrice, firstMarketCap, firstScore sql.NullFloat64
		if err := rows.Scan(
			&candidate.ID, &storedChain, &candidate.Address, &candidate.Name,
			&candidate.Symbol, &candidate.PairAddress, &firstSeenAt, &createdAt,
			&firstPrice, &firstMarketCap, &firstScore,
		); err != nil {
			return nil, fmt.Errorf("scan Momentum universe candidate: %w", err)
		}
		candidate.Chain = domain.Chain(storedChain)
		candidate.DetectedAt = fromTimestamp(firstSeenAt)
		candidate.FirstSeenPriceUSD = dataFloat(firstPrice, domain.QualityFresh, firstSeenAt)
		candidate.FirstSeenMarketCapUSD = dataFloat(firstMarketCap, domain.QualityFresh, firstSeenAt)
		candidate.FirstSeenScore = dataFloat(firstScore, domain.QualityFresh, firstSeenAt)
		if createdAt.Valid {
			created := fromTimestamp(createdAt.Int64)
			candidate.ChainCreatedAt = &created
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Momentum universe: %w", err)
	}
	return candidates, nil
}

// SaveMomentumCursor advances one chain cursor after its batch has been persisted.
func (s *Store) SaveMomentumCursor(ctx context.Context, chain domain.Chain, lastTokenID string, updatedAt time.Time) error {
	if chain != domain.ChainBSC && chain != domain.ChainSolana {
		return fmt.Errorf("save Momentum cursor: unsupported chain %q", chain)
	}
	if updatedAt.IsZero() {
		return fmt.Errorf("save Momentum cursor: updated time is required")
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO momentum_scan_cursors (chain, last_token_id, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(chain) DO UPDATE SET
			last_token_id = excluded.last_token_id,
			updated_at = excluded.updated_at`, chain, lastTokenID, timestamp(updatedAt)); err != nil {
		return fmt.Errorf("save Momentum cursor: %w", err)
	}
	return nil
}

// UpsertMomentumState advances compact operational state monotonically.
func (s *Store) UpsertMomentumState(ctx context.Context, state domain.MomentumState) error {
	if state.TokenID == "" || state.ObservedAt.IsZero() {
		return fmt.Errorf("upsert Momentum state: token ID and observed time are required")
	}
	sources := append([]domain.MomentumSource(nil), state.Sources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i] < sources[j] })
	sourcesJSON, err := json.Marshal(sources)
	if err != nil {
		return fmt.Errorf("marshal Momentum state sources: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO momentum_state (
			token_id, observed_at, pair_address, price_usd, liquidity_usd,
			volume_m5_usd, volume_h1_usd, volume_h6_usd, volume_h24_usd,
			aggregate_buyers_m5, aggregate_buyers_h1, sources_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(token_id) DO UPDATE SET
			observed_at = excluded.observed_at,
			pair_address = excluded.pair_address,
			price_usd = excluded.price_usd,
			liquidity_usd = excluded.liquidity_usd,
			volume_m5_usd = excluded.volume_m5_usd,
			volume_h1_usd = excluded.volume_h1_usd,
			volume_h6_usd = excluded.volume_h6_usd,
			volume_h24_usd = excluded.volume_h24_usd,
			aggregate_buyers_m5 = excluded.aggregate_buyers_m5,
			aggregate_buyers_h1 = excluded.aggregate_buyers_h1,
			sources_json = excluded.sources_json
		WHERE excluded.observed_at > momentum_state.observed_at`,
		state.TokenID, timestamp(state.ObservedAt), state.PairAddress,
		floatValueOrNil(state.PriceUSD), floatValueOrNil(state.LiquidityUSD),
		floatValueOrNil(state.VolumeM5USD), floatValueOrNil(state.VolumeH1USD),
		floatValueOrNil(state.VolumeH6USD), floatValueOrNil(state.VolumeH24USD),
		intValueOrNil(state.AggregateBuyersM5), intValueOrNil(state.AggregateBuyersH1),
		string(sourcesJSON),
	); err != nil {
		return fmt.Errorf("upsert Momentum state: %w", err)
	}
	return nil
}

// MomentumStateBefore returns operational state only when it is strictly earlier
// than the current observation boundary.
func (s *Store) MomentumStateBefore(ctx context.Context, tokenID string, before time.Time) (domain.MomentumState, bool, error) {
	var state domain.MomentumState
	var observedAt int64
	var price, liquidity, volumeM5, volumeH1, volumeH6, volumeH24 sql.NullFloat64
	var aggregateBuyersM5, aggregateBuyersH1 sql.NullInt64
	var sourcesJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT token_id, observed_at, pair_address, price_usd, liquidity_usd,
			volume_m5_usd, volume_h1_usd, volume_h6_usd, volume_h24_usd,
			aggregate_buyers_m5, aggregate_buyers_h1, sources_json
		FROM momentum_state
		WHERE token_id = ? AND observed_at < ?`, tokenID, timestamp(before)).Scan(
		&state.TokenID, &observedAt, &state.PairAddress, &price, &liquidity,
		&volumeM5, &volumeH1, &volumeH6, &volumeH24, &aggregateBuyersM5,
		&aggregateBuyersH1, &sourcesJSON,
	)
	if err == sql.ErrNoRows {
		return domain.MomentumState{}, false, nil
	}
	if err != nil {
		return domain.MomentumState{}, false, fmt.Errorf("read Momentum state before cutoff: %w", err)
	}
	state.ObservedAt = fromTimestamp(observedAt)
	state.PriceUSD = dataFloat(price, domain.QualityFresh, observedAt)
	state.LiquidityUSD = dataFloat(liquidity, domain.QualityFresh, observedAt)
	state.VolumeM5USD = dataFloat(volumeM5, domain.QualityFresh, observedAt)
	state.VolumeH1USD = dataFloat(volumeH1, domain.QualityFresh, observedAt)
	state.VolumeH6USD = dataFloat(volumeH6, domain.QualityFresh, observedAt)
	state.VolumeH24USD = dataFloat(volumeH24, domain.QualityFresh, observedAt)
	state.AggregateBuyersM5 = dataInt(aggregateBuyersM5, domain.QualityFresh, observedAt)
	state.AggregateBuyersH1 = dataInt(aggregateBuyersH1, domain.QualityFresh, observedAt)
	if err := json.Unmarshal([]byte(sourcesJSON), &state.Sources); err != nil {
		return domain.MomentumState{}, false, fmt.Errorf("decode Momentum state sources: %w", err)
	}
	return state, true, nil
}

// InsertDiscoverySourceReport persists one source attempt for audit and degradation.
func (s *Store) InsertDiscoverySourceReport(ctx context.Context, runID int64, report domain.DiscoverySourceReport) error {
	if runID <= 0 || report.StartedAt.IsZero() || report.Provider == "" || report.Source == "" {
		return fmt.Errorf("insert discovery source report: run, start, provider, and source are required")
	}
	if report.Chain != domain.ChainBSC && report.Chain != domain.ChainSolana {
		return fmt.Errorf("insert discovery source report: unsupported chain %q", report.Chain)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO discovery_source_runs (
			scan_run_id, chain, provider, source, started_at, finished_at,
			pages_fetched, returned_items, unique_candidates, newest_pool_at,
			oldest_pool_at, cursor_before, cursor_after, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, report.Chain, report.Provider, report.Source, timestamp(report.StartedAt),
		timestampIfNonZero(report.FinishedAt), report.PagesFetched, report.ReturnedItems,
		report.UniqueCandidates, timestampOrNil(report.NewestPoolAt),
		timestampOrNil(report.OldestPoolAt), report.CursorBefore, report.CursorAfter,
		report.Error,
	); err != nil {
		return fmt.Errorf("insert discovery source report: %w", err)
	}
	return nil
}

// EnsureScoreVersion inserts an immutable score configuration or verifies that
// an existing version has the identical configuration.
func (s *Store) EnsureScoreVersion(ctx context.Context, version string, configJSON []byte, createdAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin score version: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Commit makes this a no-op.

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO score_versions (version, config_json, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(version) DO NOTHING`, version, string(configJSON), timestamp(createdAt)); err != nil {
		return fmt.Errorf("insert score version %s: %w", version, err)
	}
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT config_json FROM score_versions WHERE version = ?`, version).Scan(&stored); err != nil {
		return fmt.Errorf("read score version %s: %w", version, err)
	}
	if stored != string(configJSON) {
		return fmt.Errorf("score version %s has conflicting config", version)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit score version %s: %w", version, err)
	}
	return nil
}

// InsertSnapshot stores an observation and returns its generated ID.
func (s *Store) InsertSnapshot(ctx context.Context, snapshot domain.MarketSnapshot) (int64, error) {
	if snapshot.ScoreVersion != "" && snapshot.Score.Quality != domain.QualityMissing {
		var registered int
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM score_versions WHERE version=?)`, snapshot.ScoreVersion).Scan(&registered); err != nil {
			return 0, fmt.Errorf("check legacy snapshot score version: %w", err)
		}
		if registered == 1 {
			breakdown := snapshot.ScoreBreakdown
			if breakdown.Version == "" {
				breakdown.Version = snapshot.ScoreVersion
			}
			breakdown.RiskFlags = append([]string(nil), snapshot.RiskFlags...)
			return s.InsertScoredSnapshot(ctx, snapshot, []domain.VersionedScore{{
				Version: snapshot.ScoreVersion, Role: domain.ScoreRoleChampion, Breakdown: breakdown,
			}})
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin snapshot insert: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Commit makes this a no-op.
	id, err := s.insertSnapshotTx(ctx, tx, snapshot)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit snapshot insert: %w", err)
	}
	return id, nil
}

func (s *Store) insertSnapshotTx(ctx context.Context, tx *sql.Tx, snapshot domain.MarketSnapshot) (int64, error) {
	breakdownJSON, err := json.Marshal(snapshot.ScoreBreakdown)
	if err != nil {
		return 0, fmt.Errorf("marshal score breakdown: %w", err)
	}
	riskFlagsJSON, err := json.Marshal(nonNilStrings(snapshot.RiskFlags))
	if err != nil {
		return 0, fmt.Errorf("marshal risk flags: %w", err)
	}
	dataQualityJSON, err := json.Marshal(nonNilQuality(snapshot.DataQuality))
	if err != nil {
		return 0, fmt.Errorf("marshal data quality: %w", err)
	}
	momentumSources := append([]domain.MomentumSource(nil), snapshot.MomentumSources...)
	if momentumSources == nil {
		momentumSources = []domain.MomentumSource{}
	}
	sort.Slice(momentumSources, func(i, j int) bool { return momentumSources[i] < momentumSources[j] })
	momentumSourcesJSON, err := json.Marshal(momentumSources)
	if err != nil {
		return 0, fmt.Errorf("marshal Momentum sources: %w", err)
	}
	var momentumTriggerJSON any
	if snapshot.MomentumTrigger != nil {
		trigger := *snapshot.MomentumTrigger
		trigger.Reasons = append([]string(nil), trigger.Reasons...)
		sort.Strings(trigger.Reasons)
		encoded, err := json.Marshal(trigger)
		if err != nil {
			return 0, fmt.Errorf("marshal Momentum trigger: %w", err)
		}
		momentumTriggerJSON = string(encoded)
	}
	var momentumEvidenceJSON any
	if snapshot.MomentumEvidence != nil {
		encoded, err := json.Marshal(snapshot.MomentumEvidence)
		if err != nil {
			return 0, fmt.Errorf("marshal Momentum evidence: %w", err)
		}
		momentumEvidenceJSON = string(encoded)
	}
	selectedPairCreatedAt := snapshot.SelectedPairCreatedAt
	if selectedPairCreatedAt == nil {
		selectedPairCreatedAt = snapshot.PairCreatedAt
	}
	effectiveTier := snapshot.ScoreBreakdown.EffectiveTier
	if effectiveTier == "" {
		effectiveTier = domain.Tier(snapshot.Tier)
	}
	result, err := tx.StmtContext(ctx, s.insertSnapshot).ExecContext(ctx,
		snapshot.TokenID, snapshot.ScanRunID, timestamp(snapshot.CollectedAt), timestampOrNil(snapshot.SourceTime),
		floatValueOrNil(snapshot.PriceUSD), floatValueOrNil(snapshot.MarketCapUSD), floatValueOrNil(snapshot.FDVUSD),
		floatValueOrNil(snapshot.LiquidityUSD), floatValueOrNil(snapshot.VolumeM5USD), floatValueOrNil(snapshot.VolumeH1USD),
		intValueOrNil(snapshot.BuysM5), intValueOrNil(snapshot.SellsM5), intValueOrNil(snapshot.BuyersM5),
		intValueOrNil(snapshot.SellersM5), floatValueOrNil(snapshot.PriceChangeM5), floatValueOrNil(snapshot.Score),
		snapshot.Tier, snapshot.ScoreVersion, string(breakdownJSON), string(riskFlagsJSON), string(dataQualityJSON),
		snapshot.PairAddress, snapshot.DiscoveryPoolAddress, snapshot.Stage, snapshot.BaseFastSnapshotID,
		snapshot.DeepStatus, timestampOrNil(snapshot.DiscoveryPoolCreatedAt),
		timestampOrNil(selectedPairCreatedAt), timestampIfNonZero(snapshot.SignalFirstSeenAt), snapshot.LaunchType,
		snapshot.MarketDataSource, snapshot.MarketDataQuality, intValueOrNil(snapshot.AggregateBuyersM5),
		intValueOrNil(snapshot.AggregateSellersM5), floatValueOrNil(snapshot.Score), snapshot.ScoreBreakdown.RawTier,
		snapshot.ScoreBreakdown.EvidenceAvailable, snapshot.ScoreBreakdown.EvidenceTotal,
		snapshot.ScoreBreakdown.EvidenceConfidence, effectiveTier, stringOrNil(string(snapshot.CandidateOrigin)),
		string(momentumSourcesJSON), momentumTriggerJSON, snapshot.TriggerPoolAddress,
		timestampOrNil(snapshot.TriggerPoolCreatedAt), int64ValueOrNil(snapshot.TokenAgeSeconds),
		snapshot.TokenAgeSource, floatValueOrNil(snapshot.VolumeH6USD), floatValueOrNil(snapshot.Volume24hUSD),
		floatValueOrNil(snapshot.PriceChangeH1), floatValueOrNil(snapshot.PriceChangeH6),
		floatValueOrNil(snapshot.PriceChangeH24), intValueOrNil(snapshot.BuysH1), intValueOrNil(snapshot.SellsH1),
		intValueOrNil(snapshot.BuysH6), intValueOrNil(snapshot.SellsH6), intValueOrNil(snapshot.BuysH24),
		intValueOrNil(snapshot.SellsH24), intValueOrNil(snapshot.AggregateBuyersH1),
		intValueOrNil(snapshot.AggregateSellersH1), intValueOrNil(snapshot.AggregateBuyersH6),
		intValueOrNil(snapshot.AggregateSellersH6), intValueOrNil(snapshot.AggregateBuyersH24),
		intValueOrNil(snapshot.AggregateSellersH24), floatValueOrNil(snapshot.LaunchBonus),
		timestampOrNil(snapshot.MomentumBaselineAt), momentumEvidenceJSON,
	)
	if err != nil {
		return 0, fmt.Errorf("insert snapshot: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read snapshot ID: %w", err)
	}
	return id, nil
}

// InsertFastSnapshot atomically publishes a new Fast generation for a token.
func (s *Store) InsertFastSnapshot(ctx context.Context, candidate domain.Candidate, snapshot domain.MarketSnapshot) (int64, error) {
	return s.insertFastSnapshot(ctx, candidate, snapshot, nil)
}

// InsertFastScoredSnapshot 原子发布 Fast 快照及该代全部版本评分。
func (s *Store) InsertFastScoredSnapshot(ctx context.Context, candidate domain.Candidate, snapshot domain.MarketSnapshot, scores []domain.VersionedScore) (int64, error) {
	champion, err := validateVersionedScores(scores)
	if err != nil {
		return 0, err
	}
	return s.insertFastSnapshot(ctx, candidate, projectChampion(snapshot, champion), scores)
}

func (s *Store) insertFastSnapshot(ctx context.Context, candidate domain.Candidate, snapshot domain.MarketSnapshot, scores []domain.VersionedScore) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin fast snapshot: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := s.upsertTokenTx(ctx, tx, candidate); err != nil {
		return 0, err
	}
	snapshot.TokenID = candidate.ID
	snapshot.Stage = domain.SnapshotStageFast
	snapshot.BaseFastSnapshotID = nil
	if snapshot.DeepStatus == "" {
		snapshot.DeepStatus = domain.DeepStatusPending
	}
	var id int64
	if len(scores) == 0 {
		id, err = s.insertSnapshotTx(ctx, tx, snapshot)
	} else {
		id, err = s.insertScoredSnapshotTx(ctx, tx, snapshot, scores)
	}
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE snapshots SET deep_status = ?
		WHERE token_id = ? AND stage = ? AND id <> ? AND deep_status IN (?, ?, ?)`,
		domain.DeepStatusStale, candidate.ID, domain.SnapshotStageFast, id,
		domain.DeepStatusPending, domain.DeepStatusQueued, domain.DeepStatusDeferred); err != nil {
		return 0, fmt.Errorf("stale older Fast generations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tokens SET latest_fast_snapshot_id = ? WHERE id = ?`, id, candidate.ID); err != nil {
		return 0, fmt.Errorf("publish latest Fast generation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit fast snapshot: %w", err)
	}
	return id, nil
}

// IsCurrentFastSnapshot reports whether a Fast generation may still publish Deep evidence.
func (s *Store) IsCurrentFastSnapshot(ctx context.Context, tokenID string, fastSnapshotID int64) (bool, error) {
	var latest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT latest_fast_snapshot_id FROM tokens WHERE id = ?`, tokenID).Scan(&latest); err != nil {
		return false, fmt.Errorf("read latest Fast generation: %w", err)
	}
	return latest.Valid && latest.Int64 == fastSnapshotID, nil
}

// SetDeepStatus records queue lifecycle metadata for a Fast generation.
func (s *Store) SetDeepStatus(ctx context.Context, fastSnapshotID int64, status domain.DeepStatus) error {
	result, err := s.db.ExecContext(ctx, `UPDATE snapshots SET deep_status = ? WHERE id = ? AND stage = ?`, status, fastSnapshotID, domain.SnapshotStageFast)
	if err != nil {
		return fmt.Errorf("set Deep status: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Deep status update count: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("set Deep status for Fast snapshot %d: not found", fastSnapshotID)
	}
	return nil
}

// InsertDeepSnapshotIfCurrent appends Deep evidence only for the latest Fast generation.
func (s *Store) InsertDeepSnapshotIfCurrent(ctx context.Context, baseFastSnapshotID int64, snapshot domain.MarketSnapshot) (bool, error) {
	return s.insertDeepSnapshotIfCurrent(ctx, baseFastSnapshotID, snapshot, nil)
}

// InsertDeepScoredSnapshotIfCurrent 仅为仍是最新代的 Fast 快照原子追加 Deep 多版本评分。
func (s *Store) InsertDeepScoredSnapshotIfCurrent(ctx context.Context, baseFastSnapshotID int64, snapshot domain.MarketSnapshot, scores []domain.VersionedScore) (bool, error) {
	champion, err := validateVersionedScores(scores)
	if err != nil {
		return false, err
	}
	return s.insertDeepSnapshotIfCurrent(ctx, baseFastSnapshotID, projectChampion(snapshot, champion), scores)
}

func (s *Store) insertDeepSnapshotIfCurrent(ctx context.Context, baseFastSnapshotID int64, snapshot domain.MarketSnapshot, scores []domain.VersionedScore) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin deep snapshot: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	var tokenID string
	if err := tx.QueryRowContext(ctx, `SELECT token_id FROM snapshots WHERE id = ? AND stage = ?`, baseFastSnapshotID, domain.SnapshotStageFast).Scan(&tokenID); err != nil {
		return false, fmt.Errorf("read base Fast snapshot: %w", err)
	}
	var latest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT latest_fast_snapshot_id FROM tokens WHERE id = ?`, tokenID).Scan(&latest); err != nil {
		return false, fmt.Errorf("read current Fast snapshot: %w", err)
	}
	if !latest.Valid || latest.Int64 != baseFastSnapshotID {
		if _, err := tx.ExecContext(ctx, `UPDATE snapshots SET deep_status = ? WHERE id = ?`, domain.DeepStatusStale, baseFastSnapshotID); err != nil {
			return false, fmt.Errorf("mark stale Fast snapshot: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit stale Deep result: %w", err)
		}
		return false, nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM snapshots WHERE stage = ? AND base_fast_snapshot_id = ?`, domain.SnapshotStageDeep, baseFastSnapshotID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check existing Deep snapshot: %w", err)
	}
	if exists > 0 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit duplicate Deep check: %w", err)
		}
		return false, nil
	}
	snapshot.TokenID = tokenID
	snapshot.Stage = domain.SnapshotStageDeep
	snapshot.BaseFastSnapshotID = &baseFastSnapshotID
	snapshot.DeepStatus = domain.DeepStatusCompleted
	if len(scores) == 0 {
		_, err = s.insertSnapshotTx(ctx, tx, snapshot)
	} else {
		_, err = s.insertScoredSnapshotTx(ctx, tx, snapshot, scores)
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE snapshots SET deep_status = ? WHERE id = ?`, domain.DeepStatusCompleted, baseFastSnapshotID); err != nil {
		return false, fmt.Errorf("complete Fast generation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit Deep snapshot: %w", err)
	}
	return true, nil
}

// InsertDiscoveryCoverage stores the actual provider window for one chain/run.
func (s *Store) InsertDiscoveryCoverage(ctx context.Context, runID int64, coverage domain.DiscoveryCoverage) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO discovery_coverages (
			scan_run_id, chain, scan_started_at, newest_pool_at, oldest_pool_at, pages_fetched, unique_candidates
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scan_run_id, chain) DO UPDATE SET
			scan_started_at = excluded.scan_started_at, newest_pool_at = excluded.newest_pool_at,
			oldest_pool_at = excluded.oldest_pool_at, pages_fetched = excluded.pages_fetched,
			unique_candidates = excluded.unique_candidates`,
		runID, coverage.Chain, timestamp(coverage.ScanStartedAt), timestampIfNonZero(coverage.NewestPoolAt),
		timestampIfNonZero(coverage.OldestPoolAt), coverage.PagesFetched, coverage.UniqueCandidates)
	if err != nil {
		return fmt.Errorf("insert discovery coverage: %w", err)
	}
	return nil
}

// StartScanRun records a scan that is in progress.
func (s *Store) StartScanRun(ctx context.Context, mode string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin scan start: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Commit makes this a no-op.
	result, err := tx.StmtContext(ctx, s.startScanRun).ExecContext(ctx, mode, timestamp(time.Now().UTC()))
	if err != nil {
		return 0, fmt.Errorf("start scan run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit scan start: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read scan run ID: %w", err)
	}
	return id, nil
}

// FinishScanRun records the final state and counters for a scan.
func (s *Store) FinishScanRun(ctx context.Context, id int64, status string, candidatesSeen, candidatesScored int, errorSummary string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin scan finish: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Commit makes this a no-op.
	result, err := tx.StmtContext(ctx, s.finishScanRun).ExecContext(ctx, timestamp(time.Now().UTC()), status, candidatesSeen, candidatesScored, errorSummary, id)
	if err != nil {
		return fmt.Errorf("finish scan run: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("read finished scan count: %w", err)
	} else if changed != 1 {
		return fmt.Errorf("finish scan run %d: not found", id)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit scan finish: %w", err)
	}
	return nil
}

// GetRecentSnapshots returns the newest snapshots for a token.
func (s *Store) GetRecentSnapshots(ctx context.Context, tokenID string, limit int) ([]domain.MarketSnapshot, error) {
	if limit <= 0 {
		return []domain.MarketSnapshot{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+snapshotSelectColumns+`
		FROM snapshots WHERE token_id = ? ORDER BY collected_at DESC, id DESC LIMIT ?`, tokenID, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent snapshots: %w", err)
	}
	defer rows.Close()

	snapshots := make([]domain.MarketSnapshot, 0)
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent snapshots: %w", err)
	}
	return snapshots, nil
}

// GetRecentSnapshotsBefore returns the newest snapshots strictly before the
// current observation so delayed scans cannot consume future evidence.
func (s *Store) GetRecentSnapshotsBefore(ctx context.Context, tokenID string, before time.Time, limit int) ([]domain.MarketSnapshot, error) {
	if limit <= 0 {
		return []domain.MarketSnapshot{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+snapshotSelectColumns+`
		FROM snapshots
		WHERE token_id = ? AND collected_at < ?
		ORDER BY collected_at DESC, id DESC LIMIT ?`, tokenID, timestamp(before), limit)
	if err != nil {
		return nil, fmt.Errorf("query recent snapshots before cutoff: %w", err)
	}
	defer rows.Close()

	snapshots := make([]domain.MarketSnapshot, 0)
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent snapshots before cutoff: %w", err)
	}
	return snapshots, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSnapshot(row rowScanner) (domain.MarketSnapshot, error) {
	var tokenID, tier, scoreVersion, breakdownJSON, riskFlagsJSON, dataQualityJSON string
	var pairAddress, discoveryPoolAddress, stage, deepStatus string
	var launchType, marketSource, marketQuality, rawTier, evidenceConfidence, effectiveTier string
	var candidateOrigin, momentumSourcesJSON, momentumTriggerJSON, triggerPoolAddress sql.NullString
	var tokenAgeSource, momentumEvidenceJSON sql.NullString
	var id int64
	var scanRunID, baseFastSnapshotID sql.NullInt64
	var collectedAt int64
	var sourceTime, discoveryPoolCreatedAt, selectedPairCreatedAt, signalFirstSeenAt sql.NullInt64
	var triggerPoolCreatedAt, tokenAgeSeconds, momentumBaselineAt sql.NullInt64
	var price, marketCap, fdv, liquidity, volumeM5, volumeH1, priceChange, score, rawScore sql.NullFloat64
	var volumeH6, volumeH24, priceChangeH1, priceChangeH6, priceChangeH24, launchBonus sql.NullFloat64
	var buys, sells, buyers, sellers, aggregateBuyers, aggregateSellers sql.NullInt64
	var buysH1, sellsH1, buysH6, sellsH6, buysH24, sellsH24 sql.NullInt64
	var aggregateBuyersH1, aggregateSellersH1, aggregateBuyersH6, aggregateSellersH6 sql.NullInt64
	var aggregateBuyersH24, aggregateSellersH24 sql.NullInt64
	var evidenceAvailable, evidenceTotal int
	if err := row.Scan(
		&id, &tokenID, &scanRunID, &collectedAt, &sourceTime, &price, &marketCap, &fdv,
		&liquidity, &volumeM5, &volumeH1, &buys, &sells, &buyers, &sellers, &priceChange,
		&score, &tier, &scoreVersion, &breakdownJSON, &riskFlagsJSON, &dataQualityJSON,
		&pairAddress, &discoveryPoolAddress, &stage, &baseFastSnapshotID, &deepStatus,
		&discoveryPoolCreatedAt, &selectedPairCreatedAt, &signalFirstSeenAt, &launchType,
		&marketSource, &marketQuality, &aggregateBuyers, &aggregateSellers, &rawScore,
		&rawTier, &evidenceAvailable, &evidenceTotal, &evidenceConfidence, &effectiveTier,
		&candidateOrigin, &momentumSourcesJSON, &momentumTriggerJSON, &triggerPoolAddress,
		&triggerPoolCreatedAt, &tokenAgeSeconds, &tokenAgeSource, &volumeH6, &volumeH24,
		&priceChangeH1, &priceChangeH6, &priceChangeH24, &buysH1, &sellsH1, &buysH6,
		&sellsH6, &buysH24, &sellsH24, &aggregateBuyersH1, &aggregateSellersH1,
		&aggregateBuyersH6, &aggregateSellersH6, &aggregateBuyersH24, &aggregateSellersH24,
		&launchBonus, &momentumBaselineAt, &momentumEvidenceJSON,
	); err != nil {
		return domain.MarketSnapshot{}, fmt.Errorf("scan snapshot: %w", err)
	}
	quality := map[string]domain.Quality{}
	if err := json.Unmarshal([]byte(dataQualityJSON), &quality); err != nil {
		return domain.MarketSnapshot{}, fmt.Errorf("decode snapshot data quality: %w", err)
	}
	var breakdown domain.ScoreBreakdown
	if err := json.Unmarshal([]byte(breakdownJSON), &breakdown); err != nil {
		return domain.MarketSnapshot{}, fmt.Errorf("decode score breakdown: %w", err)
	}
	if rawScore.Valid {
		breakdown.RawScore = rawScore.Float64
	}
	if rawTier != "" {
		breakdown.RawTier = domain.Tier(rawTier)
	}
	breakdown.EvidenceAvailable = evidenceAvailable
	breakdown.EvidenceTotal = evidenceTotal
	if evidenceConfidence != "" {
		breakdown.EvidenceConfidence = domain.EvidenceConfidence(evidenceConfidence)
	}
	if effectiveTier != "" {
		breakdown.EffectiveTier = domain.Tier(effectiveTier)
	}
	var riskFlags []string
	if err := json.Unmarshal([]byte(riskFlagsJSON), &riskFlags); err != nil {
		return domain.MarketSnapshot{}, fmt.Errorf("decode risk flags: %w", err)
	}
	var momentumSources []domain.MomentumSource
	if momentumSourcesJSON.Valid && momentumSourcesJSON.String != "" {
		if err := json.Unmarshal([]byte(momentumSourcesJSON.String), &momentumSources); err != nil {
			return domain.MarketSnapshot{}, fmt.Errorf("decode Momentum sources: %w", err)
		}
	}
	var momentumTrigger *domain.MomentumTrigger
	if momentumTriggerJSON.Valid && momentumTriggerJSON.String != "" {
		momentumTrigger = &domain.MomentumTrigger{}
		if err := json.Unmarshal([]byte(momentumTriggerJSON.String), momentumTrigger); err != nil {
			return domain.MarketSnapshot{}, fmt.Errorf("decode Momentum trigger: %w", err)
		}
	}
	var momentumEvidence *domain.MomentumEvidence
	if momentumEvidenceJSON.Valid && momentumEvidenceJSON.String != "" {
		momentumEvidence = &domain.MomentumEvidence{}
		if err := json.Unmarshal([]byte(momentumEvidenceJSON.String), momentumEvidence); err != nil {
			return domain.MarketSnapshot{}, fmt.Errorf("decode Momentum evidence: %w", err)
		}
	}
	snapshot := domain.MarketSnapshot{
		ID: id, TokenID: tokenID, PairAddress: pairAddress, DiscoveryPoolAddress: discoveryPoolAddress,
		CollectedAt: fromTimestamp(collectedAt), Tier: tier, ScoreVersion: scoreVersion,
		Stage: domain.SnapshotStage(stage), DeepStatus: domain.DeepStatus(deepStatus),
		LaunchType: domain.LaunchType(launchType), MarketDataSource: domain.MarketDataSource(marketSource),
		MarketDataQuality:  domain.MarketDataQuality(marketQuality),
		CandidateOrigin:    domain.CandidateOrigin(candidateOrigin.String),
		MomentumSources:    momentumSources,
		MomentumTrigger:    momentumTrigger,
		TriggerPoolAddress: triggerPoolAddress.String,
		TokenAgeSeconds:    dataInt64(tokenAgeSeconds, quality["token_age_seconds"], collectedAt),
		TokenAgeSource:     domain.TokenAgeSource(tokenAgeSource.String),
		ScoreBreakdown:     breakdown, RiskFlags: riskFlags, DataQuality: quality,
		PriceUSD: dataFloat(price, quality["price_usd"], collectedAt), MarketCapUSD: dataFloat(marketCap, quality["market_cap_usd"], collectedAt),
		FDVUSD: dataFloat(fdv, quality["fdv_usd"], collectedAt), LiquidityUSD: dataFloat(liquidity, quality["liquidity_usd"], collectedAt),
		VolumeM5USD: dataFloat(volumeM5, quality["volume_m5_usd"], collectedAt), VolumeH1USD: dataFloat(volumeH1, quality["volume_h1_usd"], collectedAt),
		VolumeH6USD: dataFloat(volumeH6, quality["volume_h6_usd"], collectedAt), Volume24hUSD: dataFloat(volumeH24, quality["volume_24h_usd"], collectedAt),
		BuysM5: dataInt(buys, quality["buys_m5"], collectedAt), SellsM5: dataInt(sells, quality["sells_m5"], collectedAt),
		BuysH1: dataInt(buysH1, quality["buys_h1"], collectedAt), SellsH1: dataInt(sellsH1, quality["sells_h1"], collectedAt),
		BuysH6: dataInt(buysH6, quality["buys_h6"], collectedAt), SellsH6: dataInt(sellsH6, quality["sells_h6"], collectedAt),
		BuysH24: dataInt(buysH24, quality["buys_h24"], collectedAt), SellsH24: dataInt(sellsH24, quality["sells_h24"], collectedAt),
		BuyersM5: dataInt(buyers, quality["buyers_m5"], collectedAt), SellersM5: dataInt(sellers, quality["sellers_m5"], collectedAt),
		AggregateBuyersM5:   dataInt(aggregateBuyers, quality["aggregate_buyers_m5"], collectedAt),
		AggregateSellersM5:  dataInt(aggregateSellers, quality["aggregate_sellers_m5"], collectedAt),
		AggregateBuyersH1:   dataInt(aggregateBuyersH1, quality["aggregate_buyers_h1"], collectedAt),
		AggregateSellersH1:  dataInt(aggregateSellersH1, quality["aggregate_sellers_h1"], collectedAt),
		AggregateBuyersH6:   dataInt(aggregateBuyersH6, quality["aggregate_buyers_h6"], collectedAt),
		AggregateSellersH6:  dataInt(aggregateSellersH6, quality["aggregate_sellers_h6"], collectedAt),
		AggregateBuyersH24:  dataInt(aggregateBuyersH24, quality["aggregate_buyers_h24"], collectedAt),
		AggregateSellersH24: dataInt(aggregateSellersH24, quality["aggregate_sellers_h24"], collectedAt),
		PriceChangeM5:       dataFloat(priceChange, quality["price_change_m5"], collectedAt),
		PriceChangeH1:       dataFloat(priceChangeH1, quality["price_change_h1"], collectedAt),
		PriceChangeH6:       dataFloat(priceChangeH6, quality["price_change_h6"], collectedAt),
		PriceChangeH24:      dataFloat(priceChangeH24, quality["price_change_h24"], collectedAt),
		Score:               dataFloat(score, quality["score"], collectedAt),
		LaunchBonus:         dataFloat(launchBonus, quality["launch_bonus"], collectedAt),
		MomentumEvidence:    momentumEvidence,
		Holders:             domain.DataValue[int]{Quality: domain.QualityMissing, CollectedAt: fromTimestamp(collectedAt)},
	}
	if scanRunID.Valid {
		id := scanRunID.Int64
		snapshot.ScanRunID = &id
	}
	if baseFastSnapshotID.Valid {
		base := baseFastSnapshotID.Int64
		snapshot.BaseFastSnapshotID = &base
	}
	if sourceTime.Valid {
		t := fromTimestamp(sourceTime.Int64)
		snapshot.SourceTime = &t
	}
	if discoveryPoolCreatedAt.Valid {
		created := fromTimestamp(discoveryPoolCreatedAt.Int64)
		snapshot.DiscoveryPoolCreatedAt = &created
	}
	if selectedPairCreatedAt.Valid {
		created := fromTimestamp(selectedPairCreatedAt.Int64)
		snapshot.SelectedPairCreatedAt = &created
		snapshot.PairCreatedAt = &created
	}
	if signalFirstSeenAt.Valid {
		snapshot.SignalFirstSeenAt = fromTimestamp(signalFirstSeenAt.Int64)
	}
	if triggerPoolCreatedAt.Valid {
		created := fromTimestamp(triggerPoolCreatedAt.Int64)
		snapshot.TriggerPoolCreatedAt = &created
	}
	if momentumBaselineAt.Valid {
		baseline := fromTimestamp(momentumBaselineAt.Int64)
		snapshot.MomentumBaselineAt = &baseline
	}
	return snapshot, nil
}

// ListPendingDeep reloads only current Fast generations awaiting Deep evidence.
func (s *Store) ListPendingDeep(ctx context.Context, limit int) ([]domain.DeepTask, error) {
	if limit <= 0 {
		return []domain.DeepTask{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id
		FROM snapshots s JOIN tokens t ON t.id = s.token_id AND t.latest_fast_snapshot_id = s.id
		WHERE s.stage = ? AND s.deep_status IN (?, ?, ?)
		ORDER BY s.id LIMIT ?`, domain.SnapshotStageFast, domain.DeepStatusPending, domain.DeepStatusQueued, domain.DeepStatusDeferred, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending Deep tasks: %w", err)
	}
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan pending Deep task: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate pending Deep tasks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close pending Deep tasks: %w", err)
	}
	tasks := make([]domain.DeepTask, 0, len(ids))
	for _, id := range ids {
		snapshot, err := s.snapshotByID(ctx, id)
		if err != nil {
			return nil, err
		}
		candidate, err := s.candidateByID(ctx, snapshot.TokenID)
		if err != nil {
			return nil, err
		}
		candidate.PairAddress = snapshot.PairAddress
		candidate.DiscoveryPoolAddress = snapshot.DiscoveryPoolAddress
		candidate.DiscoveryPoolCreatedAt = snapshot.DiscoveryPoolCreatedAt
		assignments, err := s.SnapshotScoreAssignments(ctx, id)
		if err != nil {
			return nil, err
		}
		references := make([]domain.ScoreReference, 0, len(assignments))
		for _, assignment := range assignments {
			references = append(references, domain.ScoreReference{Version: assignment.Version, Role: assignment.Role})
		}
		tasks = append(tasks, domain.DeepTask{TokenID: snapshot.TokenID, BaseFastSnapshotID: id, Candidate: candidate, Snapshot: snapshot, ScoreReferences: references})
	}
	return tasks, nil
}

func (s *Store) snapshotByID(ctx context.Context, id int64) (domain.MarketSnapshot, error) {
	snapshot, err := scanSnapshot(s.db.QueryRowContext(ctx, `SELECT `+snapshotSelectColumns+` FROM snapshots WHERE id = ?`, id))
	if err != nil {
		return domain.MarketSnapshot{}, fmt.Errorf("load Fast snapshot %d: %w", id, err)
	}
	return snapshot, nil
}

func (s *Store) candidateByID(ctx context.Context, id string) (domain.Candidate, error) {
	var candidate domain.Candidate
	var chain string
	var firstSeenAt int64
	var createdAt sql.NullInt64
	var firstPrice, firstMarketCap, firstScore sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `
		SELECT id, chain, address, name, symbol, pair_address, first_seen_at, created_at,
			first_seen_price_usd, first_seen_market_cap_usd, first_seen_score
		FROM tokens WHERE id = ?`, id).Scan(
		&candidate.ID, &chain, &candidate.Address, &candidate.Name, &candidate.Symbol,
		&candidate.PairAddress, &firstSeenAt, &createdAt, &firstPrice, &firstMarketCap, &firstScore,
	); err != nil {
		return domain.Candidate{}, fmt.Errorf("load pending Deep candidate: %w", err)
	}
	candidate.Chain = domain.Chain(chain)
	candidate.DetectedAt = fromTimestamp(firstSeenAt)
	candidate.FirstSeenPriceUSD = dataFloat(firstPrice, domain.QualityFresh, firstSeenAt)
	candidate.FirstSeenMarketCapUSD = dataFloat(firstMarketCap, domain.QualityFresh, firstSeenAt)
	candidate.FirstSeenScore = dataFloat(firstScore, domain.QualityFresh, firstSeenAt)
	if createdAt.Valid {
		created := fromTimestamp(createdAt.Int64)
		candidate.ChainCreatedAt = &created
	}
	return candidate, nil
}

func timestamp(t time.Time) int64 { return t.UTC().UnixMilli() }

func timestampOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timestamp(*t)
}

func timestampIfNonZero(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return timestamp(t)
}

func stringOrNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func fromTimestamp(value int64) time.Time { return time.UnixMilli(value).UTC() }

func present(quality domain.Quality) bool {
	return quality == domain.QualityFresh || quality == domain.QualityCached || quality == domain.QualityDegraded
}

func floatValueOrNil(value domain.DataValue[float64]) any {
	if !present(value.Quality) {
		return nil
	}
	return value.Value
}

func intValueOrNil(value domain.DataValue[int]) any {
	if !present(value.Quality) {
		return nil
	}
	return value.Value
}

func int64ValueOrNil(value domain.DataValue[int64]) any {
	if !present(value.Quality) {
		return nil
	}
	return value.Value
}

func dataFloat(value sql.NullFloat64, quality domain.Quality, collectedAt int64) domain.DataValue[float64] {
	if !value.Valid {
		return domain.DataValue[float64]{Quality: domain.QualityMissing, CollectedAt: fromTimestamp(collectedAt)}
	}
	if quality == "" {
		quality = domain.QualityFresh
	}
	return domain.DataValue[float64]{Value: value.Float64, Quality: quality, CollectedAt: fromTimestamp(collectedAt)}
}

func dataInt(value sql.NullInt64, quality domain.Quality, collectedAt int64) domain.DataValue[int] {
	if !value.Valid {
		return domain.DataValue[int]{Quality: domain.QualityMissing, CollectedAt: fromTimestamp(collectedAt)}
	}
	if quality == "" {
		quality = domain.QualityFresh
	}
	return domain.DataValue[int]{Value: int(value.Int64), Quality: quality, CollectedAt: fromTimestamp(collectedAt)}
}

func dataInt64(value sql.NullInt64, quality domain.Quality, collectedAt int64) domain.DataValue[int64] {
	if !value.Valid {
		return domain.DataValue[int64]{Quality: domain.QualityMissing, CollectedAt: fromTimestamp(collectedAt)}
	}
	if quality == "" {
		quality = domain.QualityFresh
	}
	return domain.DataValue[int64]{Value: value.Int64, Quality: quality, CollectedAt: fromTimestamp(collectedAt)}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilQuality(values map[string]domain.Quality) map[string]domain.Quality {
	if values == nil {
		return map[string]domain.Quality{}
	}
	return values
}
