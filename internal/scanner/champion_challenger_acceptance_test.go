package scanner

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/lab"
	"github.com/alen1/fomo-radar/internal/score"
	"github.com/alen1/fomo-radar/internal/store"
	_ "modernc.org/sqlite"
)

func TestChampionChallengerEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	corePath := filepath.Join(dir, "core.db")
	labPath := filepath.Join(dir, "lab.db")
	now := scanTime()
	database, err := store.Open(corePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	builtInConfig, err := score.NewEngine().ConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.BootstrapChampion(ctx, score.Version, builtInConfig, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	legacyToken := domain.Candidate{ID: "bsc:legacy", Chain: domain.ChainBSC, Address: "0xlegacy", DetectedAt: now.Add(-time.Hour)}
	if err := database.UpsertToken(ctx, legacyToken); err != nil {
		t.Fatal(err)
	}
	legacyID, err := database.InsertSnapshot(ctx, domain.MarketSnapshot{
		TokenID: "bsc:legacy", CollectedAt: now.Add(-time.Hour), Score: freshFloat(66, now.Add(-time.Hour)),
		Tier: string(domain.TierWatch), ScoreVersion: score.Version,
		ScoreBreakdown: domain.ScoreBreakdown{
			Version: score.Version, Final: 66, RawScore: 66, Tier: domain.TierWatch,
			RawTier: domain.TierWatch, EffectiveTier: domain.TierWatch,
			EvidenceAvailable: 5, EvidenceTotal: 5, EvidenceConfidence: domain.EvidenceConfidenceHigh,
		},
		RiskFlags: []string{"legacy"}, DataQuality: map[string]domain.Quality{"score": domain.QualityFresh},
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyHashBefore := coreSnapshotHash(t, corePath, legacyID)

	challengerConfig := scoreConfigForTest(t, "FOMO_SCORE_V1.1", 24)
	if _, err := database.EnableChallenger(ctx, "FOMO_SCORE_V1.1", challengerConfig, "端到端验收", now); err != nil {
		t.Fatal(err)
	}
	candidate := candidate("0xe2e", domain.ChainBSC, now)
	service := NewService(
		testConfig(),
		fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: {candidate}}},
		fakeEnricher{markets: map[string]domain.MarketSnapshot{candidate.Address: activeMarket(candidate.Address, now)}},
		successfulTrades(now), missingSocial{}, score.NewEngine(), database, func() time.Time { return now },
	)

	_, tasks, err := service.FastScan(ctx, ModeWatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || len(tasks[0].ScoreReferences) != 2 {
		t.Fatalf("Fast Deep tasks = %+v", tasks)
	}
	if outcome, err := service.ProcessDeepTask(ctx, tasks[0]); err != nil || !outcome.Inserted {
		t.Fatalf("Deep outcome = %+v, err %v", outcome, err)
	}
	assertSnapshotScoreCount(t, corePath, tasks[0].BaseFastSnapshotID, 2)

	source, err := lab.OpenSource(corePath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := lab.OpenRepository(labPath)
	if err != nil {
		t.Fatal(err)
	}
	processor := lab.NewProcessor(source, repository, func() time.Time { return now.Add(time.Minute) })
	if _, err := processor.ProcessCycle(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := lab.NewAnalyzer(repository, func() time.Time { return now.Add(2 * time.Minute) }).Materialize(ctx); err != nil {
		t.Fatal(err)
	}
	comparison, err := repository.LatestComparison(ctx, []string{score.Version, "FOMO_SCORE_V1.1"})
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]bool{}
	for _, statistic := range comparison.Statistics {
		versions[statistic.ScoreVersion] = true
	}
	if !versions[score.Version] || !versions["FOMO_SCORE_V1.1"] {
		t.Fatalf("Lab comparison versions = %v", versions)
	}
	_ = repository.Close()
	_ = source.Close()

	if _, err := database.PromoteChallenger(ctx, "FOMO_SCORE_V1.1", score.Version, "验收晋升", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	next, _, err := service.FastScan(ctx, ModeWatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Candidates) != 1 || next.Candidates[0].Snapshot.ScoreVersion != "FOMO_SCORE_V1.1" {
		t.Fatalf("next generation Champion projection = %+v", next.Candidates)
	}
	legacyHashAfter := coreSnapshotHash(t, corePath, legacyID)
	if legacyHashAfter != legacyHashBefore {
		t.Fatalf("legacy snapshot hash changed: %x -> %x", legacyHashBefore, legacyHashAfter)
	}
}

func coreSnapshotHash(t *testing.T, path string, snapshotID int64) [32]byte {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck
	var tokenID, tier, version, breakdown, risks, quality string
	var collectedAt int64
	var value float64
	if err := db.QueryRow(`SELECT token_id,collected_at,score,tier,score_version,score_breakdown_json,risk_flags_json,data_quality_json FROM snapshots WHERE id=?`, snapshotID).Scan(
		&tokenID, &collectedAt, &value, &tier, &version, &breakdown, &risks, &quality,
	); err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%.17g|%s|%s|%s|%s|%s", tokenID, collectedAt, value, tier, version, breakdown, risks, quality)))
}

func assertSnapshotScoreCount(t *testing.T, path string, snapshotID int64, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM snapshot_scores WHERE snapshot_id=?`, snapshotID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("snapshot %d score rows = %d, want %d", snapshotID, got, want)
	}
}
