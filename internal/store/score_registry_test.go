package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestChampionChallengerMigrationCreatesTables(t *testing.T) {
	s := openLegacyChampionChallengerFixture(t)
	defer s.Close() //nolint:errcheck

	for _, table := range []string{"snapshot_scores", "score_runtime_roles", "score_role_events"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatalf("inspect table %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s count = %d, want 1", table, count)
		}
	}
}

func TestLegacySnapshotScoreBackfillCopiesOnlyScoredRowsWithoutChangingSnapshot(t *testing.T) {
	s := openLegacyChampionChallengerFixture(t)
	defer s.Close() //nolint:errcheck

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM snapshot_scores`).Scan(&count); err != nil {
		t.Fatalf("count snapshot scores: %v", err)
	}
	if count != 1 {
		t.Fatalf("snapshot score count = %d, want 1", count)
	}

	var (
		version, role, rawTier, effectiveTier, confidence, breakdown, risks string
		rawScore                                                            float64
		available, total, createdAt                                         int64
	)
	if err := s.db.QueryRow(`
		SELECT score_version, evaluation_role, raw_score, raw_tier, effective_tier,
		       evidence_available, evidence_total, evidence_confidence,
		       score_breakdown_json, risk_flags_json, created_at
		FROM snapshot_scores WHERE snapshot_id=1`).Scan(
		&version, &role, &rawScore, &rawTier, &effectiveTier, &available, &total,
		&confidence, &breakdown, &risks, &createdAt,
	); err != nil {
		t.Fatalf("read backfilled score: %v", err)
	}
	if version != "FOMO_SCORE_V1.0" || role != "CHAMPION" || rawScore != 88.5 || rawTier != "TREND" ||
		effectiveTier != "WATCH" || available != 5 || total != 6 || confidence != "GOOD" ||
		breakdown != `{"raw_score":88.5}` || risks != `["legacy-risk"]` || createdAt != 2000 {
		t.Fatalf("unexpected backfill: version=%q role=%q score=%v rawTier=%q effectiveTier=%q evidence=%d/%d confidence=%q breakdown=%q risks=%q createdAt=%d",
			version, role, rawScore, rawTier, effectiveTier, available, total, confidence, breakdown, risks, createdAt)
	}

	var (
		collectedAt, scoreVersion, tier, originalBreakdown, originalRisks string
		originalScore                                                     float64
	)
	if err := s.db.QueryRow(`
		SELECT CAST(collected_at AS TEXT), score, tier, score_version,
		       score_breakdown_json, risk_flags_json
		FROM snapshots WHERE id=1`).Scan(
		&collectedAt, &originalScore, &tier, &scoreVersion, &originalBreakdown, &originalRisks,
	); err != nil {
		t.Fatalf("read original snapshot: %v", err)
	}
	if collectedAt != "2000" || originalScore != 88.5 || tier != "WATCH" || scoreVersion != "FOMO_SCORE_V1.0" ||
		originalBreakdown != `{"raw_score":88.5}` || originalRisks != `["legacy-risk"]` {
		t.Fatalf("legacy snapshot changed: collected=%q score=%v tier=%q version=%q breakdown=%q risks=%q",
			collectedAt, originalScore, tier, scoreVersion, originalBreakdown, originalRisks)
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM snapshot_scores WHERE snapshot_id=2`).Scan(&count); err != nil {
		t.Fatalf("count unscored snapshot rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("unscored snapshot rows = %d, want 0", count)
	}

	second, err := Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("open empty database: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close empty database: %v", err)
	}
}

func openLegacyChampionChallengerFixture(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(coreSchema); err != nil {
		t.Fatalf("apply core schema: %v", err)
	}
	if err := applyLiveHardening(db); err != nil {
		t.Fatalf("apply live hardening: %v", err)
	}
	if err := applyMarketMomentum(db); err != nil {
		t.Fatalf("apply market momentum: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO score_versions(version, config_json, created_at) VALUES ('FOMO_SCORE_V1.0', '{"version":"FOMO_SCORE_V1.0"}', 1000)`); err != nil {
		t.Fatalf("insert score version: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tokens(id, chain, address, first_seen_at) VALUES ('bsc:scored', 'bsc', '0x1', 1000), ('bsc:unscored', 'bsc', '0x2', 1000)`); err != nil {
		t.Fatalf("insert tokens: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO snapshots(
			id, token_id, collected_at, score, tier, score_version,
			score_breakdown_json, risk_flags_json, data_quality_json,
			raw_score, raw_tier, effective_tier, evidence_available,
			evidence_total, evidence_confidence
		) VALUES
			(1, 'bsc:scored', 2000, 88.5, 'WATCH', 'FOMO_SCORE_V1.0',
			 '{"raw_score":88.5}', '["legacy-risk"]', '{"score":"fresh"}',
			 88.5, 'TREND', 'WATCH', 5, 6, 'GOOD'),
			(2, 'bsc:unscored', 3000, NULL, '', 'FOMO_SCORE_V1.0',
			 '{}', '[]', '{}', NULL, '', '', 0, 0, '')`); err != nil {
		t.Fatalf("insert snapshots: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	return s
}

func TestBootstrapChampionIsIdempotentAndRejectsConfigConflict(t *testing.T) {
	ctx := context.Background()
	s := openScoreRegistryStore(t)
	now := time.Date(2026, time.September, 19, 1, 2, 3, 0, time.UTC)
	config := []byte(`{"version":"v1","weights":{"a":1}}`)

	if err := s.BootstrapChampion(ctx, "v1", config, now); err != nil {
		t.Fatalf("first BootstrapChampion() error = %v", err)
	}
	if err := s.BootstrapChampion(ctx, "v1", config, now.Add(time.Hour)); err != nil {
		t.Fatalf("idempotent BootstrapChampion() error = %v", err)
	}

	assignments, err := s.ActiveScoreAssignments(ctx)
	if err != nil {
		t.Fatalf("ActiveScoreAssignments() error = %v", err)
	}
	if len(assignments) != 1 || assignments[0].Version != "v1" || assignments[0].Role != domain.ScoreRoleChampion ||
		string(assignments[0].ConfigJSON) != string(config) || !assignments[0].ChangedAt.Equal(now) {
		t.Fatalf("assignments = %+v, want one original Champion", assignments)
	}
	if got := scoreRoleEventCount(t, s); got != 1 {
		t.Fatalf("event count = %d, want 1", got)
	}

	err = s.BootstrapChampion(ctx, "v1", []byte(`{"version":"v1","weights":{"a":2}}`), now)
	if err == nil || !strings.Contains(err.Error(), "conflicting config") {
		t.Fatalf("conflicting BootstrapChampion() error = %v, want conflict", err)
	}
	if got := scoreRoleEventCount(t, s); got != 1 {
		t.Fatalf("event count after conflict = %d, want 1", got)
	}
}

func TestEnableChallengerEnforcesCapacityIdentityAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s := openScoreRegistryStore(t)
	now := time.Date(2026, time.September, 19, 2, 0, 0, 0, time.UTC)
	if err := s.BootstrapChampion(ctx, "v1", []byte(`{"version":"v1","weight":1}`), now); err != nil {
		t.Fatal(err)
	}

	if _, err := s.EnableChallenger(ctx, "same", []byte(`{"version":"same","weight":1}`), "same numbers", now.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "numerically identical") {
		t.Fatalf("same-numbers EnableChallenger() error = %v, want rejection", err)
	}
	for i, version := range []string{"c1", "c2", "c3"} {
		changed, err := s.EnableChallenger(ctx, version, []byte(`{"version":"`+version+`","weight":`+string(rune('2'+i))+`}`), "experiment", now.Add(time.Duration(i+1)*time.Minute))
		if err != nil || !changed {
			t.Fatalf("EnableChallenger(%s) = changed %v, err %v", version, changed, err)
		}
	}
	if changed, err := s.EnableChallenger(ctx, "c1", []byte(`{"version":"c1","weight":2}`), "duplicate", now.Add(time.Hour)); err != nil || changed {
		t.Fatalf("duplicate EnableChallenger() = changed %v, err %v, want no-op", changed, err)
	}
	if _, err := s.EnableChallenger(ctx, "c4", []byte(`{"version":"c4","weight":5}`), "too many", now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "three") {
		t.Fatalf("fourth EnableChallenger() error = %v, want capacity rejection", err)
	}
	if got := scoreRoleEventCount(t, s); got != 4 {
		t.Fatalf("event count = %d, want bootstrap plus three enables", got)
	}

	changed, err := s.DisableChallenger(ctx, "c1", "pause", now.Add(2*time.Hour))
	if err != nil || !changed {
		t.Fatalf("DisableChallenger(c1) = changed %v, err %v", changed, err)
	}
	changed, err = s.EnableChallenger(ctx, "c1", []byte(`{"version":"c1","weight":2}`), "retry", now.Add(3*time.Hour))
	if err != nil || !changed {
		t.Fatalf("re-enable retired c1 = changed %v, err %v", changed, err)
	}
	if got := scoreRoleEventCount(t, s); got != 6 {
		t.Fatalf("event count after disable/re-enable = %d, want 6", got)
	}
}

func TestDisableChampionIsRejected(t *testing.T) {
	ctx := context.Background()
	s := openScoreRegistryStore(t)
	now := time.Date(2026, time.September, 19, 3, 0, 0, 0, time.UTC)
	if err := s.BootstrapChampion(ctx, "v1", []byte(`{"version":"v1","weight":1}`), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DisableChallenger(ctx, "v1", "stop", now.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "Champion") {
		t.Fatalf("DisableChallenger(Champion) error = %v, want rejection", err)
	}
	if got := scoreRoleEventCount(t, s); got != 1 {
		t.Fatalf("event count = %d, want 1", got)
	}
}

func TestPromoteChallengerUsesCompareAndSwapAndOneAuditEvent(t *testing.T) {
	ctx := context.Background()
	s := openScoreRegistryStore(t)
	now := time.Date(2026, time.September, 19, 4, 0, 0, 0, time.UTC)
	if err := s.BootstrapChampion(ctx, "v1", []byte(`{"version":"v1","weight":1}`), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnableChallenger(ctx, "v2", []byte(`{"version":"v2","weight":2}`), "trial", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	before := scoreRoleEventCount(t, s)
	if _, err := s.PromoteChallenger(ctx, "v2", "stale", "winner", now.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "current Champion is v1") {
		t.Fatalf("stale PromoteChallenger() error = %v, want current Champion", err)
	}
	assertScoreRole(t, s, "v1", domain.ScoreRoleChampion)
	assertScoreRole(t, s, "v2", domain.ScoreRoleChallenger)
	if got := scoreRoleEventCount(t, s); got != before {
		t.Fatalf("event count after stale promotion = %d, want %d", got, before)
	}

	eventID, err := s.PromoteChallenger(ctx, "v2", "v1", "winner", now.Add(3*time.Minute))
	if err != nil || eventID <= 0 {
		t.Fatalf("PromoteChallenger() = event %d, err %v", eventID, err)
	}
	assertScoreRole(t, s, "v1", domain.ScoreRoleRetired)
	assertScoreRole(t, s, "v2", domain.ScoreRoleChampion)
	if got := scoreRoleEventCount(t, s); got != before+1 {
		t.Fatalf("event count after promotion = %d, want %d", got, before+1)
	}
	var eventType, subject, fromRole, toRole, previous, next, reason string
	if err := s.db.QueryRow(`
		SELECT event_type, subject_version, from_role, to_role,
		       previous_champion_version, new_champion_version, reason
		FROM score_role_events WHERE id=?`, eventID).Scan(
		&eventType, &subject, &fromRole, &toRole, &previous, &next, &reason,
	); err != nil {
		t.Fatalf("read promotion event: %v", err)
	}
	if eventType != "PROMOTE" || subject != "v2" || fromRole != "CHALLENGER" || toRole != "CHAMPION" || previous != "v1" || next != "v2" || reason != "winner" {
		t.Fatalf("promotion event = %q %q %q %q %q %q %q", eventType, subject, fromRole, toRole, previous, next, reason)
	}
}

func openScoreRegistryStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func scoreRoleEventCount(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM score_role_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertScoreRole(t *testing.T, s *Store, version string, want domain.ScoreRole) {
	t.Helper()
	var got domain.ScoreRole
	if err := s.db.QueryRow(`SELECT role FROM score_runtime_roles WHERE score_version=?`, version).Scan(&got); err != nil {
		t.Fatalf("read role for %s: %v", version, err)
	}
	if got != want {
		t.Fatalf("role for %s = %q, want %q", version, got, want)
	}
}
