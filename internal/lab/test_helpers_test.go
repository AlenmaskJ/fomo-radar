package lab

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/store"
)

type fixtureSnapshot struct {
	At                 time.Time
	Price              *float64
	RawScore           *float64
	RawTier            string
	EffectiveTier      string
	EvidenceConfidence string
	EvidenceAvailable  int
	EvidenceTotal      int
}

func buildCoreFixture(t *testing.T, snapshots []fixtureSnapshot) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fomo.db")
	core, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	ctx := context.Background()
	if err := core.EnsureScoreVersion(ctx, "FOMO_SCORE_V1.0", []byte(`{"version":"FOMO_SCORE_V1.0","weights":{"buyer_velocity":25}}`), time.Date(2026, time.August, 22, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("EnsureScoreVersion() error = %v", err)
	}
	firstSeen := time.Date(2026, time.August, 22, 1, 0, 0, 0, time.UTC)
	if len(snapshots) > 0 {
		firstSeen = snapshots[0].At
	}
	if err := core.UpsertToken(ctx, domain.Candidate{
		ID: "bsc:0xabc", Chain: domain.ChainBSC, Address: "0xabc",
		Name: "牛来", Symbol: "🐂", PairAddress: "0xpair", DetectedAt: firstSeen,
	}); err != nil {
		t.Fatalf("UpsertToken() error = %v", err)
	}
	for _, snapshot := range snapshots {
		insertFixtureSnapshot(t, core, snapshot)
	}
	if err := core.Close(); err != nil {
		t.Fatalf("close Core fixture: %v", err)
	}
	return path
}

func appendSourceSnapshot(t *testing.T, path string, snapshot fixtureSnapshot) int64 {
	t.Helper()
	core, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen Core fixture: %v", err)
	}
	defer core.Close() //nolint:errcheck
	return insertFixtureSnapshot(t, core, snapshot)
}

func insertFixtureSnapshot(t *testing.T, core *store.Store, fixture fixtureSnapshot) int64 {
	t.Helper()
	quality := domain.QualityMissing
	scoreValue := 0.0
	if fixture.RawScore != nil {
		quality = domain.QualityFresh
		scoreValue = *fixture.RawScore
	}
	price := domain.DataValue[float64]{Quality: domain.QualityMissing, CollectedAt: fixture.At}
	if fixture.Price != nil {
		price = domain.DataValue[float64]{Value: *fixture.Price, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: fixture.At}
	}
	discoveryCreated := fixture.At.Add(-10 * time.Minute)
	id, err := core.InsertSnapshot(context.Background(), domain.MarketSnapshot{
		TokenID:                "bsc:0xabc",
		CollectedAt:            fixture.At,
		PriceUSD:               price,
		MarketCapUSD:           domain.DataValue[float64]{Value: 100000, Quality: domain.QualityFresh, CollectedAt: fixture.At},
		LiquidityUSD:           domain.DataValue[float64]{Value: 25000, Quality: domain.QualityFresh, CollectedAt: fixture.At},
		Score:                  domain.DataValue[float64]{Value: scoreValue, Quality: quality, CollectedAt: fixture.At},
		Tier:                   fixture.EffectiveTier,
		ScoreVersion:           "FOMO_SCORE_V1.0",
		DiscoveryPoolCreatedAt: &discoveryCreated,
		ScoreBreakdown: domain.ScoreBreakdown{
			Version: "FOMO_SCORE_V1.0", RawScore: scoreValue, RawTier: domain.Tier(fixture.RawTier), EffectiveTier: domain.Tier(fixture.EffectiveTier),
			EvidenceConfidence: domain.EvidenceConfidence(fixture.EvidenceConfidence),
			EvidenceAvailable:  fixture.EvidenceAvailable, EvidenceTotal: fixture.EvidenceTotal,
		},
		RiskFlags:   []string{},
		DataQuality: map[string]domain.Quality{"price": price.Quality},
	})
	if err != nil {
		t.Fatalf("InsertSnapshot() error = %v", err)
	}
	return id
}

func floatPtr(value float64) *float64 { return &value }
