package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestEnsureScoreVersionIsIdempotentAndRejectsConfigConflict(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "fomo.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	createdAt := time.Date(2026, time.August, 16, 7, 0, 0, 0, time.UTC)
	configJSON := []byte(`{"version":"FOMO_SCORE_V1.0","factors":{"buyer_velocity":{"maximum":25}},"risk_penalties":{"strong_dev_selling":20}}`)
	if err := s.EnsureScoreVersion(ctx, "FOMO_SCORE_V1.0", configJSON, createdAt); err != nil {
		t.Fatalf("first EnsureScoreVersion() error = %v", err)
	}
	if err := s.EnsureScoreVersion(ctx, "FOMO_SCORE_V1.0", configJSON, createdAt.Add(time.Hour)); err != nil {
		t.Fatalf("idempotent EnsureScoreVersion() error = %v", err)
	}
	if err := s.EnsureScoreVersion(ctx, "FOMO_SCORE_V1.0", []byte(`{"version":"changed"}`), createdAt); err == nil || !strings.Contains(err.Error(), "conflicting config") {
		t.Fatalf("conflicting EnsureScoreVersion() error = %v, want rejection", err)
	}

	var count int
	var stored string
	var storedAt int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), config_json, created_at FROM score_versions WHERE version = ?`, "FOMO_SCORE_V1.0").Scan(&count, &stored, &storedAt); err != nil {
		t.Fatalf("query score version: %v", err)
	}
	if count != 1 || stored != string(configJSON) || storedAt != createdAt.UnixMilli() {
		t.Fatalf("score version = count %d config %q created %d", count, stored, storedAt)
	}
}

func TestUpsertTokenPreservesAllFirstSeenFields(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "fomo.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	firstSeen := time.Date(2026, time.August, 16, 1, 0, 0, 0, time.UTC)
	if err := s.UpsertToken(ctx, domain.Candidate{
		ID:                    "bsc:0xabc",
		Chain:                 domain.ChainBSC,
		Address:               "0xabc",
		Name:                  "First Name",
		Symbol:                "ONE",
		PairAddress:           "0xpair-one",
		DetectedAt:            firstSeen,
		FirstSeenPriceUSD:     freshFloat(0.0002, firstSeen),
		FirstSeenMarketCapUSD: freshFloat(63000, firstSeen),
		FirstSeenScore:        freshFloat(73, firstSeen),
	}); err != nil {
		t.Fatalf("first UpsertToken() error = %v", err)
	}
	if err := s.UpsertToken(ctx, domain.Candidate{
		ID:                    "bsc:0xabc",
		Chain:                 domain.ChainBSC,
		Address:               "0xabc",
		Name:                  "Renamed Token",
		Symbol:                "TWO",
		PairAddress:           "0xpair-two",
		DetectedAt:            firstSeen.Add(time.Hour),
		FirstSeenPriceUSD:     freshFloat(0.0011, firstSeen.Add(time.Hour)),
		FirstSeenMarketCapUSD: freshFloat(410000, firstSeen.Add(time.Hour)),
		FirstSeenScore:        freshFloat(93, firstSeen.Add(time.Hour)),
	}); err != nil {
		t.Fatalf("second UpsertToken() error = %v", err)
	}
	if _, err := s.InsertSnapshot(ctx, domain.MarketSnapshot{
		TokenID: "bsc:0xabc", CollectedAt: firstSeen.Add(2 * time.Hour), Tier: "watch", ScoreVersion: "v1",
		Score: freshFloat(99, firstSeen.Add(2*time.Hour)), ScoreBreakdown: domain.ScoreBreakdown{}, RiskFlags: []string{},
		DataQuality: map[string]domain.Quality{"score": domain.QualityFresh},
	}); err != nil {
		t.Fatalf("later InsertSnapshot() error = %v", err)
	}

	var firstSeenAt int64
	var firstPrice, firstMarketCap, firstScore sql.NullFloat64
	var name, symbol, pairAddress string
	if err := s.db.QueryRowContext(ctx, `SELECT first_seen_at, first_seen_price_usd, first_seen_market_cap_usd, first_seen_score, name, symbol, pair_address FROM tokens WHERE id = ?`, "bsc:0xabc").Scan(&firstSeenAt, &firstPrice, &firstMarketCap, &firstScore, &name, &symbol, &pairAddress); err != nil {
		t.Fatalf("query token: %v", err)
	}
	if firstSeenAt != firstSeen.UnixMilli() || !firstPrice.Valid || firstPrice.Float64 != 0.0002 || !firstMarketCap.Valid || firstMarketCap.Float64 != 63000 || !firstScore.Valid || firstScore.Float64 != 73 {
		t.Fatalf("first discovery = (at=%d, price=%+v, market_cap=%+v, score=%+v), want original values", firstSeenAt, firstPrice, firstMarketCap, firstScore)
	}
	if name != "Renamed Token" || symbol != "TWO" || pairAddress != "0xpair-two" {
		t.Fatalf("metadata = (%q, %q, %q), want updated values", name, symbol, pairAddress)
	}

	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var busyTimeout int
	if err := s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}
}

func TestInsertSnapshotDoesNotBackfillMissingFirstSeenScore(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "fomo.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	collectedAt := time.Date(2026, time.August, 16, 3, 0, 0, 0, time.UTC)
	if err := s.UpsertToken(ctx, domain.Candidate{
		ID: "bsc:no-first-score", Chain: domain.ChainBSC, Address: "0xno-first-score", DetectedAt: collectedAt,
	}); err != nil {
		t.Fatalf("UpsertToken() error = %v", err)
	}
	if _, err := s.InsertSnapshot(ctx, domain.MarketSnapshot{
		TokenID: "bsc:no-first-score", CollectedAt: collectedAt.Add(time.Hour), Tier: "watch", ScoreVersion: "v1",
		Score: freshFloat(88, collectedAt.Add(time.Hour)), ScoreBreakdown: domain.ScoreBreakdown{}, RiskFlags: []string{},
		DataQuality: map[string]domain.Quality{"score": domain.QualityFresh},
	}); err != nil {
		t.Fatalf("InsertSnapshot() error = %v", err)
	}

	var firstScore sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `SELECT first_seen_score FROM tokens WHERE id = ?`, "bsc:no-first-score").Scan(&firstScore); err != nil {
		t.Fatalf("query first score: %v", err)
	}
	if firstScore.Valid {
		t.Fatalf("first_seen_score = %+v, want SQL NULL from first discovery", firstScore)
	}
}

func TestInsertSnapshotRoundTripsMissingMarketCap(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "fomo.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	collectedAt := time.Date(2026, time.August, 16, 2, 0, 0, 0, time.UTC)
	if err := s.UpsertToken(ctx, domain.Candidate{
		ID: "solana:mint", Chain: domain.ChainSolana, Address: "mint", DetectedAt: collectedAt,
	}); err != nil {
		t.Fatalf("UpsertToken() error = %v", err)
	}
	_, err = s.InsertSnapshot(ctx, domain.MarketSnapshot{
		TokenID:     "solana:mint",
		CollectedAt: collectedAt,
		MarketCapUSD: domain.DataValue[float64]{
			Quality:     domain.QualityMissing,
			CollectedAt: collectedAt,
		},
		Tier:           "watch",
		ScoreVersion:   "v1",
		ScoreBreakdown: domain.ScoreBreakdown{},
		RiskFlags:      []string{},
		DataQuality:    map[string]domain.Quality{"market_cap_usd": domain.QualityMissing},
	})
	if err != nil {
		t.Fatalf("InsertSnapshot() error = %v", err)
	}
	var storedMarketCap sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `SELECT market_cap_usd FROM snapshots WHERE token_id = ?`, "solana:mint").Scan(&storedMarketCap); err != nil {
		t.Fatalf("query stored market cap: %v", err)
	}
	if storedMarketCap.Valid {
		t.Fatalf("stored market_cap_usd = %+v, want SQL NULL", storedMarketCap)
	}

	snapshots, err := s.GetRecentSnapshots(ctx, "solana:mint", 1)
	if err != nil {
		t.Fatalf("GetRecentSnapshots() error = %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("GetRecentSnapshots() returned %d snapshots, want 1", len(snapshots))
	}
	if snapshots[0].MarketCapUSD.Quality != domain.QualityMissing {
		t.Fatalf("MarketCapUSD.Quality = %q, want %q", snapshots[0].MarketCapUSD.Quality, domain.QualityMissing)
	}
}

func TestGetRecentSnapshotsMakesSQLNULLMarketCapMissingDespiteFreshMetadata(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "fomo.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	collectedAt := time.Date(2026, time.August, 16, 4, 0, 0, 0, time.UTC)
	if err := s.UpsertToken(ctx, domain.Candidate{ID: "solana:null-quality", Chain: domain.ChainSolana, Address: "null-quality", DetectedAt: collectedAt}); err != nil {
		t.Fatalf("UpsertToken() error = %v", err)
	}
	if _, err := s.InsertSnapshot(ctx, domain.MarketSnapshot{
		TokenID: "solana:null-quality", CollectedAt: collectedAt, Tier: "watch", ScoreVersion: "v1",
		MarketCapUSD:   domain.DataValue[float64]{Quality: domain.QualityMissing, CollectedAt: collectedAt},
		ScoreBreakdown: domain.ScoreBreakdown{}, RiskFlags: []string{},
		DataQuality: map[string]domain.Quality{"market_cap_usd": domain.QualityFresh},
	}); err != nil {
		t.Fatalf("InsertSnapshot() error = %v", err)
	}

	snapshots, err := s.GetRecentSnapshots(ctx, "solana:null-quality", 1)
	if err != nil {
		t.Fatalf("GetRecentSnapshots() error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].MarketCapUSD.Quality != domain.QualityMissing {
		t.Fatalf("MarketCapUSD.Quality = %q, want missing when SQL value is NULL", snapshots[0].MarketCapUSD.Quality)
	}
}

func TestGetRecentSnapshotsMarksUnstoredFieldsMissing(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "fomo.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	collectedAt := time.Date(2026, time.August, 16, 5, 0, 0, 0, time.UTC)
	if err := s.UpsertToken(ctx, domain.Candidate{ID: "bsc:unstored-fields", Chain: domain.ChainBSC, Address: "0xunstored-fields", DetectedAt: collectedAt}); err != nil {
		t.Fatalf("UpsertToken() error = %v", err)
	}
	if _, err := s.InsertSnapshot(ctx, domain.MarketSnapshot{
		TokenID: "bsc:unstored-fields", CollectedAt: collectedAt, Tier: "watch", ScoreVersion: "v1",
		ScoreBreakdown: domain.ScoreBreakdown{}, RiskFlags: []string{}, DataQuality: map[string]domain.Quality{},
	}); err != nil {
		t.Fatalf("InsertSnapshot() error = %v", err)
	}

	snapshots, err := s.GetRecentSnapshots(ctx, "bsc:unstored-fields", 1)
	if err != nil {
		t.Fatalf("GetRecentSnapshots() error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Volume24hUSD.Quality != domain.QualityMissing || snapshots[0].Holders.Quality != domain.QualityMissing {
		t.Fatalf("unstored fields = (volume_24h=%q, holders=%q), want missing", snapshots[0].Volume24hUSD.Quality, snapshots[0].Holders.Quality)
	}
}

func TestGetRecentSnapshotsBeforeExcludesCurrentAndFutureRowsBeforeLimit(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "fomo.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	current := time.Date(2026, time.August, 16, 6, 0, 0, 0, time.UTC)
	tokenID := "bsc:cutoff"
	if err := s.UpsertToken(ctx, domain.Candidate{ID: tokenID, Chain: domain.ChainBSC, Address: "0xcutoff", DetectedAt: current.Add(-time.Hour)}); err != nil {
		t.Fatalf("UpsertToken() error = %v", err)
	}
	for _, collectedAt := range []time.Time{current.Add(-time.Minute), current, current.Add(time.Minute)} {
		if _, err := s.InsertSnapshot(ctx, domain.MarketSnapshot{
			TokenID: tokenID, CollectedAt: collectedAt, Tier: "watch", ScoreVersion: "v1",
			VolumeM5USD:    freshFloat(float64(collectedAt.Unix()), collectedAt),
			ScoreBreakdown: domain.ScoreBreakdown{}, RiskFlags: []string{},
			DataQuality: map[string]domain.Quality{"volume_m5_usd": domain.QualityFresh},
		}); err != nil {
			t.Fatalf("InsertSnapshot(%s) error = %v", collectedAt, err)
		}
	}

	snapshots, err := s.GetRecentSnapshotsBefore(ctx, tokenID, current, 1)
	if err != nil {
		t.Fatalf("GetRecentSnapshotsBefore() error = %v", err)
	}
	if len(snapshots) != 1 || !snapshots[0].CollectedAt.Equal(current.Add(-time.Minute)) {
		t.Fatalf("snapshots = %+v, want only latest observation strictly before cutoff", snapshots)
	}
}

func TestScanRunLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "fomo.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	id, err := s.StartScanRun(ctx, "scheduled")
	if err != nil {
		t.Fatalf("StartScanRun() error = %v", err)
	}
	if err := s.FinishScanRun(ctx, id, "completed", 12, 7, ""); err != nil {
		t.Fatalf("FinishScanRun() error = %v", err)
	}

	var status string
	var seen, scored int
	var finishedAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT status, candidates_seen, candidates_scored, finished_at FROM scan_runs WHERE id = ?`, id).Scan(&status, &seen, &scored, &finishedAt); err != nil {
		t.Fatalf("query scan run: %v", err)
	}
	if status != "completed" || seen != 12 || scored != 7 || !finishedAt.Valid {
		t.Fatalf("scan run = (status=%q, seen=%d, scored=%d, finished=%+v), want completed lifecycle", status, seen, scored, finishedAt)
	}
}

func TestOpenMigratesPlan1DatabaseToLiveHardeningSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(coreSchema); err != nil {
		t.Fatalf("apply Plan 1 schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tokens (id, chain, address, first_seen_at, created_at) VALUES ('bsc:legacy', 'bsc', '0xlegacy', 1, 1)`); err != nil {
		t.Fatalf("insert legacy token: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO snapshots (token_id, collected_at, tier, score_version, score_breakdown_json, risk_flags_json, data_quality_json) VALUES ('bsc:legacy', 2, 'WATCH', 'v1', '{}', '[]', '{}')`); err != nil {
		t.Fatalf("insert legacy snapshot: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() migrating Plan 1 database: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for table, columns := range map[string][]string{
		"tokens": {"latest_fast_snapshot_id"},
		"snapshots": {
			"stage", "base_fast_snapshot_id", "deep_status", "pair_address", "discovery_pool_address", "discovery_pool_created_at",
			"selected_pair_created_at", "signal_first_seen_at", "launch_type", "market_data_source",
			"market_data_quality", "aggregate_buyers_m5", "aggregate_sellers_m5", "raw_score",
			"raw_tier", "evidence_available", "evidence_total", "evidence_confidence", "effective_tier",
		},
	} {
		got := tableColumns(t, s.db, table)
		for _, column := range columns {
			if !got[column] {
				t.Errorf("%s.%s missing after migration", table, column)
			}
		}
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second idempotent Open() error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second store: %v", err)
	}
	var legacyCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE token_id = 'bsc:legacy'`).Scan(&legacyCount); err != nil || legacyCount != 1 {
		t.Fatalf("legacy snapshot count = %d, err=%v", legacyCount, err)
	}
}

func TestMarketMomentumMigrationIsAdditive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan11.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(coreSchema); err != nil {
		t.Fatalf("apply core schema: %v", err)
	}
	if err := applyLiveHardening(db); err != nil {
		t.Fatalf("apply live hardening schema: %v", err)
	}
	const v1Config = `{"version":"FOMO_SCORE_V1.0","factors":{"buyer_velocity":{"maximum":25}}}`
	if _, err := db.Exec(`INSERT INTO score_versions (version, config_json, created_at) VALUES ('FOMO_SCORE_V1.0', ?, 1)`, v1Config); err != nil {
		t.Fatalf("insert V1 config: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tokens (id, chain, address, first_seen_at) VALUES ('bsc:legacy', 'bsc', '0xlegacy', 1)`); err != nil {
		t.Fatalf("insert legacy token: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO snapshots (
		token_id, collected_at, tier, score_version, score_breakdown_json,
		risk_flags_json, data_quality_json
	) VALUES ('bsc:legacy', 2, 'WATCH', 'FOMO_SCORE_V1.0', '{}', '[]', '{}')`); err != nil {
		t.Fatalf("insert legacy snapshot: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() migrating Plan 1.1 database: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for _, column := range []string{
		"candidate_origin", "momentum_sources_json", "momentum_trigger_json",
		"trigger_pool_address", "trigger_pool_created_at", "token_age_seconds",
		"token_age_source", "volume_h6_usd", "volume_h24_usd", "price_change_h1",
		"price_change_h6", "price_change_h24", "launch_bonus",
		"momentum_baseline_at", "momentum_evidence_json",
	} {
		if !tableColumns(t, s.db, "snapshots")[column] {
			t.Errorf("snapshots.%s missing after Momentum migration", column)
		}
	}
	for _, table := range []string{"momentum_state", "momentum_universe", "momentum_scan_cursors", "discovery_source_runs"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("inspect table %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s count = %d, want 1", table, count)
		}
	}
	for _, column := range []string{"last_trade_at", "liquidity_usd", "volume_h24_usd"} {
		if !tableColumns(t, s.db, "momentum_universe")[column] {
			t.Errorf("momentum_universe.%s missing after Momentum migration", column)
		}
	}
	var origin sql.NullString
	if err := s.db.QueryRow(`SELECT candidate_origin FROM snapshots WHERE token_id = 'bsc:legacy'`).Scan(&origin); err != nil {
		t.Fatalf("read legacy Momentum column: %v", err)
	}
	if origin.Valid {
		t.Fatalf("legacy candidate_origin = %q, want SQL NULL", origin.String)
	}
	var gotConfig string
	if err := s.db.QueryRow(`SELECT config_json FROM score_versions WHERE version = 'FOMO_SCORE_V1.0'`).Scan(&gotConfig); err != nil {
		t.Fatalf("read V1 config: %v", err)
	}
	if gotConfig != v1Config {
		t.Fatalf("V1 config changed: got %q want %q", gotConfig, v1Config)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second idempotent Open() error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second store: %v", err)
	}
}

func TestMomentumUniverseAdmissionCapsChainAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "universe-cap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		id := fmt.Sprintf("bsc:seed-%04d", i)
		address := fmt.Sprintf("0x%04d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO tokens (id, chain, address, first_seen_at) VALUES (?, 'bsc', ?, ?)`, id, address, i+1); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seed token %d: %v", i, err)
		}
		lastMomentum := int64(100)
		if i == 0 {
			lastMomentum = 0
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO momentum_universe (
			token_id, chain, admission_source, admission_priority, first_added_at, last_seen_at,
			last_momentum_at, last_trade_at, liquidity_usd, volume_h24_usd
		) VALUES (?, 'bsc', 'HISTORICAL', 100, ?, ?, NULLIF(?, 0), ?, ?, ?)`,
			id, i+1, i+1, lastMomentum, 999999999, 999999999, 999999999); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seed universe %d: %v", i, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots (
		token_id, collected_at, tier, score_version, score_breakdown_json,
		risk_flags_json, data_quality_json
	) VALUES ('bsc:seed-0000', 2, 'WATCH', 'FOMO_SCORE_V1.0', '{}', '[]', '{}')`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed immutable snapshot: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	observedAt := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	admitted, err := s.AdmitMomentumUniverse(ctx, domain.Candidate{
		ID: "bsc:anomaly", Chain: domain.ChainBSC, Address: "0xanomaly",
		Name: "牛来", Symbol: "牛来", DetectedAt: observedAt,
	}, domain.UniverseAdmissionAnomaly, observedAt)
	if err != nil || !admitted {
		t.Fatalf("AdmitMomentumUniverse() admitted=%v err=%v", admitted, err)
	}

	var active, tokenHistory, snapshotHistory, evictedActive int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM momentum_universe WHERE chain = 'bsc'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE chain = 'bsc'`).Scan(&tokenHistory); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE token_id = 'bsc:seed-0000'`).Scan(&snapshotHistory); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM momentum_universe WHERE token_id = 'bsc:seed-0000'`).Scan(&evictedActive); err != nil {
		t.Fatal(err)
	}
	if active != 5000 || tokenHistory != 5001 || snapshotHistory != 1 || evictedActive != 0 {
		t.Fatalf("active=%d tokens=%d snapshots=%d evicted_active=%d", active, tokenHistory, snapshotHistory, evictedActive)
	}

	var name, symbol, source string
	var priority int
	if err := s.db.QueryRow(`
		SELECT t.name, t.symbol, u.admission_source, u.admission_priority
		FROM momentum_universe u JOIN tokens t ON t.id = u.token_id
		WHERE u.token_id = 'bsc:anomaly'`).Scan(&name, &symbol, &source, &priority); err != nil {
		t.Fatal(err)
	}
	if name != "牛来" || symbol != "牛来" || source != "ANOMALY" || priority != 300 {
		t.Fatalf("new member = %q %q %q %d", name, symbol, source, priority)
	}
	if _, err := s.AdmitMomentumUniverse(ctx, candidateWithDetectedAt(domain.Candidate{
		ID: "bsc:anomaly", Chain: domain.ChainBSC, Address: "0xanomaly",
	}, observedAt.Add(-time.Minute)), domain.UniverseAdmissionAnomaly, observedAt.Add(-time.Minute)); err != nil {
		t.Fatalf("late anomaly refresh: %v", err)
	}
	var lastSeenAt, lastMomentumAt int64
	if err := s.db.QueryRow(`SELECT last_seen_at, last_momentum_at FROM momentum_universe WHERE token_id = 'bsc:anomaly'`).Scan(&lastSeenAt, &lastMomentumAt); err != nil {
		t.Fatal(err)
	}
	if lastSeenAt != observedAt.UnixMilli() || lastMomentumAt != observedAt.UnixMilli() {
		t.Fatalf("late anomaly regressed times: last_seen=%d last_momentum=%d", lastSeenAt, lastMomentumAt)
	}
}

func TestMomentumUniverseRefreshesMarketEvidenceMonotonically(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "universe-evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	tradeAt := now.Add(-time.Minute)
	candidate := domain.Candidate{
		ID: "bsc:niu", Chain: domain.ChainBSC, Address: "0xniu", DetectedAt: now,
		DiscoveryMarket: domain.MarketSnapshot{
			LiquidityUSD: domain.DataValue[float64]{Value: 32000, Quality: domain.QualityFresh},
			Volume24hUSD: domain.DataValue[float64]{Value: 180000, Quality: domain.QualityFresh},
		},
	}
	if admitted, err := s.AdmitMomentumUniverse(ctx, candidate, domain.UniverseAdmissionTrending, now); err != nil || !admitted {
		t.Fatalf("admit: admitted=%v err=%v", admitted, err)
	}
	if err := s.UpdateMomentumUniverseEvidence(ctx, candidate.ID, &tradeAt, candidate.DiscoveryMarket, now); err != nil {
		t.Fatal(err)
	}
	olderTrade := tradeAt.Add(-time.Hour)
	olderMarket := candidate.DiscoveryMarket
	olderMarket.LiquidityUSD.Value = 1
	olderMarket.Volume24hUSD.Value = 2
	if err := s.UpdateMomentumUniverseEvidence(ctx, candidate.ID, &olderTrade, olderMarket, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var lastMomentum, lastTrade int64
	var liquidity, volume float64
	if err := s.db.QueryRow(`SELECT last_momentum_at, last_trade_at, liquidity_usd, volume_h24_usd
		FROM momentum_universe WHERE token_id = ?`, candidate.ID).Scan(&lastMomentum, &lastTrade, &liquidity, &volume); err != nil {
		t.Fatal(err)
	}
	if lastMomentum != now.UnixMilli() || lastTrade != tradeAt.UnixMilli() || liquidity != 32000 || volume != 180000 {
		t.Fatalf("evidence regressed: momentum=%d trade=%d liquidity=%v volume=%v", lastMomentum, lastTrade, liquidity, volume)
	}
}

func candidateWithDetectedAt(candidate domain.Candidate, detectedAt time.Time) domain.Candidate {
	candidate.DetectedAt = detectedAt
	return candidate
}

func TestMomentumUniverseCursorPersistsAndBatchClampsToThirty(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "universe-cursor.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 23, 13, 0, 0, 0, time.UTC)
	for i := 0; i < 31; i++ {
		id := fmt.Sprintf("solana:MintCase%02d", i)
		admitted, err := s.AdmitMomentumUniverse(ctx, domain.Candidate{
			ID: id, Chain: domain.ChainSolana, Address: fmt.Sprintf("MintCase%02d", i),
			Name: "币", Symbol: "币", DetectedAt: base.Add(time.Duration(i) * time.Second),
		}, domain.UniverseAdmissionHistorical, base.Add(time.Duration(i)*time.Second))
		if err != nil || !admitted {
			t.Fatalf("seed member %d admitted=%v err=%v", i, admitted, err)
		}
	}

	first, firstCursor, err := s.ListMomentumUniverse(ctx, domain.ChainSolana, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 30 || first[0].Address != "MintCase00" || first[29].Address != "MintCase29" || firstCursor != "solana:MintCase29" {
		t.Fatalf("first batch len=%d first=%+v last=%+v cursor=%q", len(first), first[0], first[len(first)-1], firstCursor)
	}
	if err := s.SaveMomentumCursor(ctx, domain.ChainSolana, firstCursor, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	second, secondCursor, err := s.ListMomentumUniverse(ctx, domain.ChainSolana, "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Address != "MintCase30" || secondCursor != "solana:MintCase30" {
		t.Fatalf("second batch=%+v cursor=%q", second, secondCursor)
	}
	if err := s.SaveMomentumCursor(ctx, domain.ChainSolana, secondCursor, base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wrapped, wrappedCursor, err := s.ListMomentumUniverse(ctx, domain.ChainSolana, "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(wrapped) != 30 || wrapped[0].Address != "MintCase00" || wrappedCursor != "solana:MintCase29" {
		t.Fatalf("wrapped batch len=%d first=%+v cursor=%q", len(wrapped), wrapped[0], wrappedCursor)
	}
}

func TestSeedMomentumUniverseFromHistoricalTokensIsBoundedAndDeterministic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "universe-history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	base := time.Date(2026, 8, 23, 13, 0, 0, 0, time.UTC)
	for i := 0; i < 35; i++ {
		candidate := domain.Candidate{
			ID: fmt.Sprintf("bsc:history-%02d", i), Chain: domain.ChainBSC,
			Address: fmt.Sprintf("0xhistory%02d", i), DetectedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := s.UpsertToken(ctx, candidate); err != nil {
			t.Fatal(err)
		}
	}

	seeded, err := s.SeedMomentumUniverseFromHistory(ctx, domain.ChainBSC, base.Add(time.Hour))
	if err != nil || seeded != 35 {
		t.Fatalf("seeded=%d err=%v", seeded, err)
	}
	seeded, err = s.SeedMomentumUniverseFromHistory(ctx, domain.ChainBSC, base.Add(2*time.Hour))
	if err != nil || seeded != 0 {
		t.Fatalf("second seed=%d err=%v", seeded, err)
	}
	items, _, err := s.ListMomentumUniverse(ctx, domain.ChainBSC, "", 30)
	if err != nil || len(items) != 30 || items[0].ID != "bsc:history-00" || items[29].ID != "bsc:history-29" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestMomentumSnapshotRoundTripsDiscoveryEvidence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "momentum-snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	at := time.Date(2026, 8, 23, 14, 0, 0, 0, time.UTC)
	triggerCreated := at.Add(-42 * time.Hour)
	baselineAt := at.Add(-2 * time.Minute)
	candidate := domain.Candidate{
		ID: "bsc:niu", Chain: domain.ChainBSC,
		Address: "0xbeea1d618e533a387d941f58a7d4c9b7bd377777",
		Name:    "牛来", Symbol: "牛来", DetectedAt: at,
		Origin: domain.CandidateOriginBoth,
		MomentumSources: []domain.MomentumSource{
			domain.MomentumSourceLocalUniverse,
			domain.MomentumSourceGeckoTrending,
		},
		TriggerPoolAddress: "trigger-pool", TriggerPoolCreatedAt: &triggerCreated,
	}
	snapshot := hardeningSnapshot(candidate.ID, at)
	snapshot.CandidateOrigin = domain.CandidateOriginBoth
	snapshot.MomentumSources = candidate.MomentumSources
	snapshot.MomentumTrigger = &domain.MomentumTrigger{
		GateVersion: "MOMENTUM_GATE_V1", Source: domain.MomentumSourceGeckoTrending,
		Reasons: []string{"price_h1", "volume_ratio"},
	}
	snapshot.TriggerPoolAddress = candidate.TriggerPoolAddress
	snapshot.TriggerPoolCreatedAt = candidate.TriggerPoolCreatedAt
	snapshot.TokenAgeSeconds = domain.DataValue[int64]{Value: 42 * 60 * 60, Quality: domain.QualityFresh, CollectedAt: at}
	snapshot.TokenAgeSource = domain.TokenAgeSourceEarliestPair
	snapshot.VolumeH6USD = freshFloat(240000, at)
	snapshot.Volume24hUSD = freshFloat(900000, at)
	snapshot.PriceChangeH1 = freshFloat(30, at)
	snapshot.PriceChangeH6 = freshFloat(80, at)
	snapshot.PriceChangeH24 = freshFloat(120, at)
	snapshot.BuysH1 = domain.DataValue[int]{Value: 200, Quality: domain.QualityFresh, CollectedAt: at}
	snapshot.SellsH1 = domain.DataValue[int]{Value: 80, Quality: domain.QualityFresh, CollectedAt: at}
	snapshot.AggregateBuyersH1 = domain.DataValue[int]{Value: 150, Quality: domain.QualityFresh, CollectedAt: at}
	snapshot.AggregateSellersH1 = domain.DataValue[int]{Value: 70, Quality: domain.QualityFresh, CollectedAt: at}
	snapshot.LaunchBonus = freshFloat(0, at)
	snapshot.MomentumBaselineAt = &baselineAt
	snapshot.MomentumEvidence = &domain.MomentumEvidence{
		Version: "MOMENTUM_EVIDENCE_V1",
		Factors: map[string]domain.MomentumFactorEvidence{
			"price_momentum": {
				Available: true, Value: 30, Unit: "percent", Source: "gecko",
				Window: "h1", ObservedAt: at,
			},
		},
	}
	snapshot.DataQuality["volume_h6_usd"] = domain.QualityFresh
	snapshot.DataQuality["volume_24h_usd"] = domain.QualityFresh
	snapshot.DataQuality["price_change_h1"] = domain.QualityFresh
	snapshot.DataQuality["price_change_h6"] = domain.QualityFresh
	snapshot.DataQuality["price_change_h24"] = domain.QualityFresh
	snapshot.DataQuality["buys_h1"] = domain.QualityFresh
	snapshot.DataQuality["sells_h1"] = domain.QualityFresh
	snapshot.DataQuality["aggregate_buyers_h1"] = domain.QualityFresh
	snapshot.DataQuality["aggregate_sellers_h1"] = domain.QualityFresh
	snapshot.DataQuality["launch_bonus"] = domain.QualityFresh
	snapshot.DataQuality["token_age_seconds"] = domain.QualityFresh
	if _, err := s.InsertFastSnapshot(ctx, candidate, snapshot); err != nil {
		t.Fatalf("InsertFastSnapshot() error = %v", err)
	}

	got, err := s.GetRecentSnapshots(ctx, candidate.ID, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("GetRecentSnapshots() len=%d err=%v", len(got), err)
	}
	m := got[0]
	if m.CandidateOrigin != domain.CandidateOriginBoth || m.TriggerPoolAddress != "trigger-pool" || m.TokenAgeSeconds.Value != 151200 || m.TokenAgeSource != domain.TokenAgeSourceEarliestPair {
		t.Fatalf("origin/trigger/age = %+v", m)
	}
	if len(m.MomentumSources) != 2 || m.MomentumSources[0] != domain.MomentumSourceGeckoTrending || m.MomentumSources[1] != domain.MomentumSourceLocalUniverse {
		t.Fatalf("Momentum sources = %+v", m.MomentumSources)
	}
	if m.MomentumTrigger == nil || m.MomentumTrigger.GateVersion != "MOMENTUM_GATE_V1" || m.MomentumEvidence == nil || m.MomentumEvidence.Version != "MOMENTUM_EVIDENCE_V1" {
		t.Fatalf("trigger/evidence = %+v / %+v", m.MomentumTrigger, m.MomentumEvidence)
	}
	if m.VolumeH6USD.Value != 240000 || m.Volume24hUSD.Value != 900000 || m.PriceChangeH1.Value != 30 || m.BuysH1.Value != 200 || m.AggregateBuyersH1.Value != 150 || m.LaunchBonus.Value != 0 {
		t.Fatalf("Momentum interval evidence = %+v", m)
	}
	if m.MomentumBaselineAt == nil || !m.MomentumBaselineAt.Equal(baselineAt) {
		t.Fatalf("baseline = %v, want %v", m.MomentumBaselineAt, baselineAt)
	}
}

func TestSnapshotStoresMissingMomentumSourcesAsEmptyJSONArray(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "empty-momentum-sources.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	at := time.Date(2026, 8, 23, 14, 30, 0, 0, time.UTC)
	candidate := domain.Candidate{ID: "bsc:no-momentum", Chain: domain.ChainBSC, Address: "0xnone", DetectedAt: at}
	id, err := s.InsertFastSnapshot(ctx, candidate, hardeningSnapshot(candidate.ID, at))
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	var origin sql.NullString
	if err := s.db.QueryRow(`SELECT momentum_sources_json, candidate_origin FROM snapshots WHERE id = ?`, id).Scan(&stored, &origin); err != nil {
		t.Fatal(err)
	}
	if stored != "[]" {
		t.Fatalf("momentum_sources_json = %q, want []", stored)
	}
	if origin.Valid {
		t.Fatalf("candidate_origin = %q, want SQL NULL before Discovery wiring", origin.String)
	}
}

func TestMomentumStateBeforeIsStrictAndOlderStateCannotOverwrite(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "momentum-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	at := time.Date(2026, 8, 23, 15, 0, 0, 0, time.UTC)
	candidate := domain.Candidate{ID: "bsc:state", Chain: domain.ChainBSC, Address: "0xstate", DetectedAt: at}
	if err := s.UpsertToken(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	current := domain.MomentumState{
		TokenID: candidate.ID, ObservedAt: at, PairAddress: "pool-current",
		PriceUSD: freshFloat(12, at), LiquidityUSD: freshFloat(30000, at),
		VolumeM5USD: freshFloat(5000, at), VolumeH1USD: freshFloat(25000, at),
		VolumeH6USD: freshFloat(100000, at), VolumeH24USD: freshFloat(350000, at),
		AggregateBuyersM5: domain.DataValue[int]{Value: 20, Quality: domain.QualityFresh, CollectedAt: at},
		AggregateBuyersH1: domain.DataValue[int]{Value: 90, Quality: domain.QualityFresh, CollectedAt: at},
		Sources:           []domain.MomentumSource{domain.MomentumSourceLocalUniverse, domain.MomentumSourceGeckoTrending},
	}
	if err := s.UpsertMomentumState(ctx, current); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.MomentumStateBefore(ctx, candidate.ID, at); err != nil || ok {
		t.Fatalf("equal-time state leaked: ok=%v err=%v", ok, err)
	}
	got, ok, err := s.MomentumStateBefore(ctx, candidate.ID, at.Add(time.Second))
	if err != nil || !ok || got.PairAddress != "pool-current" || got.PriceUSD.Value != 12 {
		t.Fatalf("strict earlier state = %+v ok=%v err=%v", got, ok, err)
	}
	if len(got.Sources) != 2 || got.Sources[0] != domain.MomentumSourceGeckoTrending || got.Sources[1] != domain.MomentumSourceLocalUniverse {
		t.Fatalf("state sources = %+v", got.Sources)
	}

	older := current
	older.ObservedAt = at.Add(-time.Minute)
	older.PairAddress = "pool-old"
	older.PriceUSD = freshFloat(9, older.ObservedAt)
	if err := s.UpsertMomentumState(ctx, older); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.MomentumStateBefore(ctx, candidate.ID, at.Add(time.Second))
	if err != nil || !ok || got.PairAddress != "pool-current" || got.PriceUSD.Value != 12 {
		t.Fatalf("older state overwrote current: %+v ok=%v err=%v", got, ok, err)
	}
}

func TestInsertDiscoverySourceReportPersistsAuditAndRejectsDuplicate(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "source-report.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	runID, err := s.StartScanRun(ctx, "watch")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 23, 16, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Second)
	newest := started.Add(-5 * time.Second)
	oldest := started.Add(-10 * time.Minute)
	report := domain.DiscoverySourceReport{
		Chain: domain.ChainBSC, Provider: "gecko", Source: "GECKO_TRENDING",
		StartedAt: started, FinishedAt: finished, PagesFetched: 1, ReturnedItems: 20,
		UniqueCandidates: 18, NewestPoolAt: &newest, OldestPoolAt: &oldest,
		CursorBefore: "before", CursorAfter: "after", Error: "",
	}
	if err := s.InsertDiscoverySourceReport(ctx, runID, report); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertDiscoverySourceReport(ctx, runID, report); err == nil {
		t.Fatal("duplicate source report was accepted")
	}
	var chain, provider, source, before, after string
	var pages, returned, unique int
	if err := s.db.QueryRow(`SELECT chain, provider, source, pages_fetched,
		returned_items, unique_candidates, cursor_before, cursor_after
		FROM discovery_source_runs WHERE scan_run_id = ?`, runID).Scan(
		&chain, &provider, &source, &pages, &returned, &unique, &before, &after,
	); err != nil {
		t.Fatal(err)
	}
	if chain != "bsc" || provider != "gecko" || source != "GECKO_TRENDING" || pages != 1 || returned != 20 || unique != 18 || before != "before" || after != "after" {
		t.Fatalf("stored source report = %s %s %s %d %d %d %s %s", chain, provider, source, pages, returned, unique, before, after)
	}
}

func TestInsertDiscoveryCoverageRoundTripsObservedWindow(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "coverage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	runID, err := s.StartScanRun(ctx, "watch")
	if err != nil {
		t.Fatal(err)
	}
	coverage := domain.DiscoveryCoverage{
		Chain: domain.ChainSolana, ScanStartedAt: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		NewestPoolAt: time.Date(2026, 8, 16, 11, 59, 59, 0, time.UTC),
		OldestPoolAt: time.Date(2026, 8, 16, 11, 57, 35, 0, time.UTC), PagesFetched: 3, UniqueCandidates: 50,
	}
	if err := s.InsertDiscoveryCoverage(ctx, runID, coverage); err != nil {
		t.Fatalf("InsertDiscoveryCoverage() error = %v", err)
	}
	var chain string
	var started, newest, oldest int64
	var pages, unique int
	if err := s.db.QueryRow(`SELECT chain, scan_started_at, newest_pool_at, oldest_pool_at, pages_fetched, unique_candidates FROM discovery_coverages WHERE scan_run_id = ?`, runID).Scan(&chain, &started, &newest, &oldest, &pages, &unique); err != nil {
		t.Fatal(err)
	}
	if chain != "solana" || started != coverage.ScanStartedAt.UnixMilli() || newest != coverage.NewestPoolAt.UnixMilli() || oldest != coverage.OldestPoolAt.UnixMilli() || pages != 3 || unique != 50 {
		t.Fatalf("stored coverage = %s %d %d %d %d %d", chain, started, newest, oldest, pages, unique)
	}
}

func TestFastAndDeepSnapshotsEnforceCurrentGeneration(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "generations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	candidate := domain.Candidate{ID: "bsc:0xabc", Chain: domain.ChainBSC, Address: "0xabc", Name: "币", Symbol: "币", DetectedAt: base}

	fastA, err := s.InsertFastSnapshot(ctx, candidate, hardeningSnapshot(candidate.ID, base))
	if err != nil {
		t.Fatalf("InsertFastSnapshot(A): %v", err)
	}
	fastB, err := s.InsertFastSnapshot(ctx, candidate, hardeningSnapshot(candidate.ID, base.Add(time.Minute)))
	if err != nil {
		t.Fatalf("InsertFastSnapshot(B): %v", err)
	}
	if fastA == fastB {
		t.Fatal("Fast generations must have distinct snapshot IDs")
	}
	current, err := s.IsCurrentFastSnapshot(ctx, candidate.ID, fastA)
	if err != nil || current {
		t.Fatalf("Fast A current = %v, err=%v, want false", current, err)
	}

	inserted, err := s.InsertDeepSnapshotIfCurrent(ctx, fastA, hardeningSnapshot(candidate.ID, base.Add(2*time.Minute)))
	if err != nil || inserted {
		t.Fatalf("late Deep(A) inserted=%v err=%v, want discarded", inserted, err)
	}
	inserted, err = s.InsertDeepSnapshotIfCurrent(ctx, fastB, hardeningSnapshot(candidate.ID, base.Add(3*time.Minute)))
	if err != nil || !inserted {
		t.Fatalf("Deep(B) inserted=%v err=%v, want true", inserted, err)
	}
	inserted, err = s.InsertDeepSnapshotIfCurrent(ctx, fastB, hardeningSnapshot(candidate.ID, base.Add(4*time.Minute)))
	if err != nil || inserted {
		t.Fatalf("duplicate Deep(B) inserted=%v err=%v, want idempotent false", inserted, err)
	}

	var deepRows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE stage = 'DEEP'`).Scan(&deepRows); err != nil || deepRows != 1 {
		t.Fatalf("Deep rows = %d, err=%v, want 1", deepRows, err)
	}
	var statusA, statusB string
	if err := s.db.QueryRow(`SELECT deep_status FROM snapshots WHERE id = ?`, fastA).Scan(&statusA); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT deep_status FROM snapshots WHERE id = ?`, fastB).Scan(&statusB); err != nil {
		t.Fatal(err)
	}
	if statusA != string(domain.DeepStatusStale) || statusB != string(domain.DeepStatusCompleted) {
		t.Fatalf("Fast statuses = %q/%q, want stale/completed", statusA, statusB)
	}
}

func TestInsertFastSnapshotPreservesNotRequiredDeepStatus(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "not-required.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	candidate := domain.Candidate{ID: "bsc:0xquiet", Chain: domain.ChainBSC, Address: "0xquiet", DetectedAt: at}
	snapshot := hardeningSnapshot(candidate.ID, at)
	snapshot.DeepStatus = domain.DeepStatusNotRequired
	id, err := s.InsertFastSnapshot(ctx, candidate, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.db.QueryRow(`SELECT deep_status FROM snapshots WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.DeepStatusNotRequired) {
		t.Fatalf("deep status = %q, want not_required", status)
	}
}

func TestSetDeepStatusPersistsQueueLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "queue-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	candidate := domain.Candidate{ID: "bsc:0xqueued", Chain: domain.ChainBSC, Address: "0xqueued", DetectedAt: at}
	id, err := s.InsertFastSnapshot(ctx, candidate, hardeningSnapshot(candidate.ID, at))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeepStatus(ctx, id, domain.DeepStatusQueued); err != nil {
		t.Fatalf("SetDeepStatus() error = %v", err)
	}
	var status string
	if err := s.db.QueryRow(`SELECT deep_status FROM snapshots WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.DeepStatusQueued) {
		t.Fatalf("status = %q, want queued", status)
	}
	tasks, err := s.ListPendingDeep(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].BaseFastSnapshotID != id {
		t.Fatalf("queued restart tasks = %+v, want Fast snapshot %d", tasks, id)
	}
}

func TestListPendingDeepReturnsOnlyLatestFastGeneration(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "pending.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	candidate := domain.Candidate{ID: "solana:MintCase", Chain: domain.ChainSolana, Address: "MintCase", Name: "币", Symbol: "币", PairAddress: "PoolCase", DetectedAt: base}
	if _, err := s.InsertFastSnapshot(ctx, candidate, hardeningSnapshot(candidate.ID, base)); err != nil {
		t.Fatal(err)
	}
	latestID, err := s.InsertFastSnapshot(ctx, candidate, hardeningSnapshot(candidate.ID, base.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	tasks, err := s.ListPendingDeep(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingDeep() error = %v", err)
	}
	if len(tasks) != 1 || tasks[0].BaseFastSnapshotID != latestID || tasks[0].TokenID != candidate.ID || tasks[0].Candidate.Address != "MintCase" {
		t.Fatalf("pending tasks = %+v", tasks)
	}
}

func TestGetTokenFirstSeenReturnsImmutableLocalDiscoveryTime(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "first-seen.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	first := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	candidate := domain.Candidate{ID: "bsc:0xfirst", Chain: domain.ChainBSC, Address: "0xfirst", DetectedAt: first}
	if err := s.UpsertToken(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	candidate.DetectedAt = first.Add(time.Hour)
	if err := s.UpsertToken(ctx, candidate); err != nil {
		t.Fatal(err)
	}

	got, exists, err := s.GetTokenFirstSeen(ctx, candidate.ID)
	if err != nil || !exists || !got.Equal(first) {
		t.Fatalf("GetTokenFirstSeen() = %v, %v, %v; want %v, true, nil", got, exists, err, first)
	}
}

func hardeningSnapshot(tokenID string, at time.Time) domain.MarketSnapshot {
	discovery := at.Add(-time.Minute)
	selected := at.Add(-24 * time.Hour)
	return domain.MarketSnapshot{
		TokenID: tokenID, PairAddress: "SelectedPoolCase", DiscoveryPoolAddress: "DiscoveryPoolCase", CollectedAt: at,
		DiscoveryPoolCreatedAt: &discovery, SelectedPairCreatedAt: &selected, SignalFirstSeenAt: at,
		LaunchType: domain.LaunchTypeReactivation, MarketDataSource: domain.MarketDataSourceMerged,
		MarketDataQuality: domain.MarketDataQualityFull, AggregateBuyersM5: domain.DataValue[int]{Value: 7, Quality: domain.QualityFresh, CollectedAt: at},
		AggregateSellersM5: domain.DataValue[int]{Value: 3, Quality: domain.QualityFresh, CollectedAt: at},
		Score:              freshFloat(90, at), Tier: string(domain.TierBreakout), ScoreVersion: "FOMO_SCORE_V1.0",
		ScoreBreakdown: domain.ScoreBreakdown{
			Version: "FOMO_SCORE_V1.0", Final: 90, Tier: domain.TierBreakout, RawScore: 90,
			RawTier: domain.TierBreakout, EvidenceAvailable: 4, EvidenceTotal: 5,
			EvidenceConfidence: domain.EvidenceConfidenceGood, EffectiveTier: domain.TierBreakout,
		},
		RiskFlags: []string{}, DataQuality: map[string]domain.Quality{"score": domain.QualityFresh, "aggregate_buyers_m5": domain.QualityFresh, "aggregate_sellers_m5": domain.QualityFresh},
	}
}

func tableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func freshFloat(value float64, collectedAt time.Time) domain.DataValue[float64] {
	return domain.DataValue[float64]{
		Value: value, Quality: domain.QualityFresh, CollectedAt: collectedAt,
	}
}
