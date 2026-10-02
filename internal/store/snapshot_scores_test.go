package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestInsertScoredSnapshotRollsBackParentWhenVersionConflicts(t *testing.T) {
	ctx := context.Background()
	s, snapshot := scoredSnapshotFixture(t)
	champion := versionedScore("v1", domain.ScoreRoleChampion, 81, domain.TierFastRising)

	before := rowCount(t, s, "snapshots")
	if _, err := s.InsertScoredSnapshot(ctx, snapshot, []domain.VersionedScore{champion, champion}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate InsertScoredSnapshot() error = %v, want duplicate rejection", err)
	}
	if got := rowCount(t, s, "snapshots"); got != before {
		t.Fatalf("snapshots after duplicate = %d, want %d", got, before)
	}

	unregistered := versionedScore("missing", domain.ScoreRoleChallenger, 70, domain.TierFastRising)
	if _, err := s.InsertScoredSnapshot(ctx, snapshot, []domain.VersionedScore{champion, unregistered}); err == nil {
		t.Fatal("unregistered InsertScoredSnapshot() error = nil, want foreign-key failure")
	}
	if got := rowCount(t, s, "snapshots"); got != before {
		t.Fatalf("snapshots after child failure = %d, want %d", got, before)
	}
	if got := rowCount(t, s, "snapshot_scores"); got != 0 {
		t.Fatalf("snapshot_scores after child failure = %d, want 0", got)
	}
}

func TestInsertScoredSnapshotProjectsChampionAndStoresAllVersions(t *testing.T) {
	ctx := context.Background()
	s, snapshot := scoredSnapshotFixture(t)
	now := snapshot.CollectedAt
	if _, err := s.EnableChallenger(ctx, "v2", []byte(`{"version":"v2","weight":2}`), "trial", now); err != nil {
		t.Fatal(err)
	}
	scores := []domain.VersionedScore{
		versionedScore("v2", domain.ScoreRoleChallenger, 92, domain.TierBreakout),
		versionedScore("v1", domain.ScoreRoleChampion, 61, domain.TierWatch),
	}
	id, err := s.InsertScoredSnapshot(ctx, snapshot, scores)
	if err != nil {
		t.Fatalf("InsertScoredSnapshot() error = %v", err)
	}

	var score float64
	var tier, version, risks string
	if err := s.db.QueryRow(`SELECT score, tier, score_version, risk_flags_json FROM snapshots WHERE id=?`, id).Scan(&score, &tier, &version, &risks); err != nil {
		t.Fatal(err)
	}
	if score != 61 || tier != string(domain.TierWatch) || version != "v1" || risks != `["risk-v1"]` {
		t.Fatalf("Champion projection = score %v tier %q version %q risks %q", score, tier, version, risks)
	}
	if got := rowCount(t, s, "snapshot_scores"); got != 2 {
		t.Fatalf("snapshot_scores = %d, want 2", got)
	}

	assignments, err := s.SnapshotScoreAssignments(ctx, id)
	if err != nil {
		t.Fatalf("SnapshotScoreAssignments() error = %v", err)
	}
	if len(assignments) != 2 || assignments[0].Version != "v1" || assignments[0].Role != domain.ScoreRoleChampion ||
		assignments[1].Version != "v2" || assignments[1].Role != domain.ScoreRoleChallenger {
		t.Fatalf("assignments = %+v, want Champion v1 then Challenger v2", assignments)
	}
	if string(assignments[0].ConfigJSON) != `{"version":"v1","weight":1}` || string(assignments[1].ConfigJSON) != `{"version":"v2","weight":2}` {
		t.Fatalf("assignment configs = %q, %q", assignments[0].ConfigJSON, assignments[1].ConfigJSON)
	}
}

func TestSnapshotScoresAndRoleEventsAreImmutable(t *testing.T) {
	ctx := context.Background()
	s, snapshot := scoredSnapshotFixture(t)
	id, err := s.InsertScoredSnapshot(ctx, snapshot, []domain.VersionedScore{
		versionedScore("v1", domain.ScoreRoleChampion, 61, domain.TierWatch),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE snapshot_scores SET raw_score=99 WHERE snapshot_id=?`,
		`DELETE FROM snapshot_scores WHERE snapshot_id=?`,
	} {
		if _, err := s.db.Exec(statement, id); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("%s error = %v, want immutable rejection", statement, err)
		}
	}
	for _, statement := range []string{
		`UPDATE score_role_events SET reason='changed' WHERE id=1`,
		`DELETE FROM score_role_events WHERE id=1`,
	} {
		if _, err := s.db.Exec(statement); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("%s error = %v, want immutable rejection", statement, err)
		}
	}
}

func scoredSnapshotFixture(t *testing.T) (*Store, domain.MarketSnapshot) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "scores.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Date(2026, time.September, 19, 5, 0, 0, 0, time.UTC)
	if err := s.BootstrapChampion(ctx, "v1", []byte(`{"version":"v1","weight":1}`), now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertToken(ctx, domain.Candidate{ID: "bsc:score", Chain: domain.ChainBSC, Address: "0xscore", DetectedAt: now}); err != nil {
		t.Fatal(err)
	}
	return s, domain.MarketSnapshot{
		TokenID:     "bsc:score",
		CollectedAt: now,
		DataQuality: map[string]domain.Quality{"score": domain.QualityFresh},
	}
}

func versionedScore(version string, role domain.ScoreRole, rawScore float64, tier domain.Tier) domain.VersionedScore {
	return domain.VersionedScore{
		Version: version,
		Role:    role,
		Breakdown: domain.ScoreBreakdown{
			Version: version, Final: rawScore, Tier: tier, RawScore: rawScore, RawTier: tier,
			EffectiveTier: tier, EvidenceAvailable: 5, EvidenceTotal: 5,
			EvidenceConfidence: domain.EvidenceConfidenceHigh, RiskFlags: []string{"risk-" + version},
		},
	}
}

func rowCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
