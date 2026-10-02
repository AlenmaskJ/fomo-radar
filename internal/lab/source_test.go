package lab

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSourceIsReadOnlyAndReadsSnapshotsAscending(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{
		{At: t0, Price: floatPtr(.001), RawScore: floatPtr(60), RawTier: "WATCH", EffectiveTier: "WATCH", EvidenceConfidence: "LOW", EvidenceAvailable: 2, EvidenceTotal: 5},
		{At: t0.Add(time.Minute), Price: floatPtr(.002), RawScore: floatPtr(70), RawTier: "FAST_RISING", EffectiveTier: "FAST_RISING", EvidenceConfidence: "GOOD", EvidenceAvailable: 4, EvidenceTotal: 5},
		{At: t0.Add(2 * time.Minute), Price: floatPtr(.003), RawScore: floatPtr(80), RawTier: "FAST_RISING", EffectiveTier: "FAST_RISING", EvidenceConfidence: "HIGH", EvidenceAvailable: 5, EvidenceTotal: 5},
	})
	source, err := OpenSource(corePath)
	if err != nil {
		t.Fatalf("OpenSource() error = %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })

	rows, err := source.Snapshots(context.Background(), 0, 3, 500)
	if err != nil {
		t.Fatalf("Snapshots() error = %v", err)
	}
	gotIDs := make([]int64, len(rows))
	for i, row := range rows {
		gotIDs[i] = row.ID
		if row.Name != "牛来" || row.Symbol != "🐂" {
			t.Fatalf("UTF-8 row = name %q symbol %q", row.Name, row.Symbol)
		}
	}
	if !slices.Equal(gotIDs, []int64{1, 2, 3}) {
		t.Fatalf("snapshot IDs = %v", gotIDs)
	}
	if _, err := source.db.Exec(`UPDATE snapshots SET raw_score=100 WHERE id=1`); err == nil {
		t.Fatal("read-only source accepted write")
	}
	if !filepath.IsAbs(source.CanonicalPath()) {
		t.Fatalf("canonical path is not absolute: %q", source.CanonicalPath())
	}
}

func TestSourceIdentityUsesPersistedActivationAnchors(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{
		{At: t0, Price: floatPtr(.001), RawScore: floatPtr(60), RawTier: "WATCH", EffectiveTier: "WATCH", EvidenceConfidence: "LOW", EvidenceAvailable: 2, EvidenceTotal: 5},
		{At: t0.Add(time.Minute), Price: floatPtr(.002), RawScore: floatPtr(70), RawTier: "FAST_RISING", EffectiveTier: "FAST_RISING", EvidenceConfidence: "GOOD", EvidenceAvailable: 4, EvidenceTotal: 5},
	})
	source, err := OpenSource(corePath)
	if err != nil {
		t.Fatalf("OpenSource() error = %v", err)
	}
	activation, err := source.Activation(context.Background())
	if err != nil {
		t.Fatalf("Activation() error = %v", err)
	}
	before, err := source.Identity(context.Background(), activation)
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}
	appendSourceSnapshot(t, corePath, fixtureSnapshot{At: t0.Add(2 * time.Minute), Price: floatPtr(.003), RawScore: floatPtr(80), RawTier: "FAST_RISING", EffectiveTier: "FAST_RISING", EvidenceConfidence: "HIGH", EvidenceAvailable: 5, EvidenceTotal: 5})
	after, err := source.Identity(context.Background(), activation)
	if err != nil {
		t.Fatalf("Identity() after growth error = %v", err)
	}
	if before != after {
		t.Fatalf("identity changed after source growth: %s -> %s", before, after)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}

	db, err := sql.Open("sqlite", corePath)
	if err != nil {
		t.Fatalf("open fixture for anchor mutation: %v", err)
	}
	if _, err := db.Exec(`UPDATE tokens SET address='0xchanged' WHERE id='bsc:0xabc'`); err != nil {
		t.Fatalf("mutate identity anchor: %v", err)
	}
	_ = db.Close()
	mutated, err := OpenSource(corePath)
	if err != nil {
		t.Fatalf("OpenSource() mutated error = %v", err)
	}
	defer mutated.Close() //nolint:errcheck
	changed, err := mutated.Identity(context.Background(), activation)
	if err != nil {
		t.Fatalf("Identity() mutated error = %v", err)
	}
	if changed == before {
		t.Fatal("identity ignored immutable anchor change")
	}
}

func TestSourceFrontierIsBoundToHighWatermark(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{
		{At: t0, RawScore: floatPtr(60), EffectiveTier: "WATCH"},
		{At: t0.Add(2 * time.Minute), RawScore: floatPtr(70), EffectiveTier: "WATCH"},
	})
	source, err := OpenSource(corePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close() //nolint:errcheck
	frontier, err := source.Frontier(context.Background())
	if err != nil {
		t.Fatalf("Frontier() error = %v", err)
	}
	if frontier.HighWatermark != 2 || frontier.ObservationAt == nil || !frontier.ObservationAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("frontier = %+v", frontier)
	}
}

func TestSourceRejectsMissingScoreConfigForScoredSnapshot(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{{At: t0, RawScore: floatPtr(60), EffectiveTier: "WATCH"}})
	db, err := sql.Open("sqlite", corePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM score_versions WHERE version='FOMO_SCORE_V1.0'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	source, err := OpenSource(corePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close() //nolint:errcheck
	_, err = source.Snapshots(context.Background(), 0, 1, 500)
	if err == nil || !strings.Contains(err.Error(), "score config") {
		t.Fatalf("Snapshots() missing config error = %v", err)
	}
}

func TestSourcePagesByParentSnapshotAndReturnsEveryScoreVersion(t *testing.T) {
	t0 := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	corePath := buildCoreFixture(t, []fixtureSnapshot{
		{At: t0, Price: floatPtr(.001), RawScore: floatPtr(60), RawTier: "WATCH", EffectiveTier: "WATCH", EvidenceConfidence: "HIGH", EvidenceAvailable: 5, EvidenceTotal: 5},
		{At: t0.Add(time.Minute), Price: floatPtr(.002), RawScore: floatPtr(70), RawTier: "FAST_RISING", EffectiveTier: "FAST_RISING", EvidenceConfidence: "HIGH", EvidenceAvailable: 5, EvidenceTotal: 5},
	})
	db, err := sql.Open("sqlite", corePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"V2", "V3", "V4"} {
		if _, err := db.Exec(`INSERT OR IGNORE INTO score_versions(version,config_json,created_at) VALUES (?, ?, 1)`, version, `{"version":"`+version+`"}`); err != nil {
			t.Fatalf("insert version %s: %v", version, err)
		}
		for snapshotID := int64(1); snapshotID <= 2; snapshotID++ {
			if _, err := db.Exec(`INSERT INTO snapshot_scores(
				snapshot_id,score_version,evaluation_role,raw_score,raw_tier,effective_tier,
				evidence_available,evidence_total,evidence_confidence,score_breakdown_json,
				risk_flags_json,created_at
			) VALUES (?, ?, ?, ?, 'WATCH', 'WATCH', 5, 5, 'HIGH', '{}', '[]', ?)`,
				snapshotID, version, "CHALLENGER", 59+snapshotID, t0.UnixMilli()); err != nil {
				t.Fatalf("insert snapshot %d score %s: %v", snapshotID, version, err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	source, err := OpenSource(corePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close() //nolint:errcheck
	rows, err := source.Snapshots(context.Background(), 0, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want all four versions from first parent", len(rows))
	}
	for index, row := range rows {
		if row.ID != 1 || row.ScoreVersion != []string{"FOMO_SCORE_V1.0", "V2", "V3", "V4"}[index] {
			t.Fatalf("row[%d] = snapshot %d version %q", index, row.ID, row.ScoreVersion)
		}
	}
}
