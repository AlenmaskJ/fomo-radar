package scanner

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/config"
	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/score"
	"github.com/alen1/fomo-radar/internal/store"
	_ "modernc.org/sqlite"
)

func TestCoreScannerBoundsEnrichmentAndPersistsOnlyFastFilterSurvivors(t *testing.T) {
	if workerLimit != 4 {
		t.Fatalf("workerLimit = %d, want literal acceptance limit 4", workerLimit)
	}
	baselineGoroutines := runtime.NumGoroutine()
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	discovered, survivorIDs := acceptanceCandidates(now)
	dbPath := filepath.Join(t.TempDir(), "fomo.db")
	database, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	databaseOpen := true
	t.Cleanup(func() {
		if databaseOpen {
			_ = database.Close()
		}
	})

	enricher := &acceptanceEnricher{now: now}
	trades := &acceptanceTrades{now: now}
	social := &acceptanceSocial{now: now}
	service := NewService(
		config.Config{
			DeepScanThreshold: 0,
			CandidateMaxAge:   6 * time.Hour,
			EarlyMaxAge:       2 * time.Hour,
			PreferredMaxMCUSD: 500_000,
		},
		acceptanceDiscoverer{candidates: discovered},
		enricher,
		trades,
		social,
		score.NewEngine(),
		database,
		func() time.Time { return now },
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := service.Scan(ctx, ModeManual)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.CandidatesSeen != 2_000 {
		t.Fatalf("CandidatesSeen = %d, want 2000", result.CandidatesSeen)
	}
	if result.CandidatesScored != 50 || len(result.Candidates) != 50 {
		t.Fatalf("scored/result candidates = %d/%d, want 50/50", result.CandidatesScored, len(result.Candidates))
	}
	if calls := enricher.calls.Load(); calls != 50 {
		t.Fatalf("enrichment calls = %d, want only 50 fast-filter survivors", calls)
	}
	if maximum := enricher.maximum.Load(); maximum > 4 {
		t.Fatalf("maximum in-flight enrichment = %d, want <= 4", maximum)
	}
	if active := enricher.active.Load(); active != 0 {
		t.Fatalf("in-flight enrichment after Scan() = %d, want 0", active)
	}
	if calls := trades.calls.Load(); calls != 50 {
		t.Fatalf("deep trade calls = %d, want 50", calls)
	}
	if calls := social.calls.Load(); calls != 50 {
		t.Fatalf("deep social calls = %d, want 50", calls)
	}

	if err := database.Close(); err != nil {
		t.Fatalf("store.Close() error = %v", err)
	}
	databaseOpen = false
	verifyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	var snapshotCount int
	if err := verifyDB.QueryRow(`SELECT COUNT(*) FROM snapshots`).Scan(&snapshotCount); err != nil {
		_ = verifyDB.Close()
		t.Fatalf("count snapshots: %v", err)
	}
	if snapshotCount != 100 {
		_ = verifyDB.Close()
		t.Fatalf("Fast + Deep snapshot count = %d, want 100", snapshotCount)
	}
	rows, err := verifyDB.Query(`
		SELECT token_id, score, score_version, score_breakdown_json, buyers_m5, sellers_m5
		FROM snapshots WHERE stage = 'DEEP'`)
	if err != nil {
		_ = verifyDB.Close()
		t.Fatalf("query detailed snapshots: %v", err)
	}
	persistedIDs := make(map[string]int, 50)
	for rows.Next() {
		var tokenID, scoreVersion, breakdownJSON string
		var scoreValue float64
		var buyersM5, sellersM5 int
		if err := rows.Scan(&tokenID, &scoreValue, &scoreVersion, &breakdownJSON, &buyersM5, &sellersM5); err != nil {
			_ = rows.Close()
			_ = verifyDB.Close()
			t.Fatalf("scan detailed snapshot: %v", err)
		}
		if _, survived := survivorIDs[tokenID]; !survived {
			_ = rows.Close()
			_ = verifyDB.Close()
			t.Fatalf("snapshot persisted for rejected candidate %q", tokenID)
		}
		persistedIDs[tokenID]++
		if persistedIDs[tokenID] != 1 {
			_ = rows.Close()
			_ = verifyDB.Close()
			t.Fatalf("snapshot count for survivor %q = %d, want exactly 1", tokenID, persistedIDs[tokenID])
		}
		if scoreVersion != "FOMO_SCORE_V1.0" {
			_ = rows.Close()
			_ = verifyDB.Close()
			t.Fatalf("score_version for %q = %q, want literal V1 version", tokenID, scoreVersion)
		}
		if scoreValue != 100 {
			_ = rows.Close()
			_ = verifyDB.Close()
			t.Fatalf("score for %q = %v, want 100 from explicit fixture", tokenID, scoreValue)
		}
		if buyersM5 != 40 || sellersM5 != 10 {
			_ = rows.Close()
			_ = verifyDB.Close()
			t.Fatalf("deep participants for %q = %d/%d, want 40 buyers and 10 sellers", tokenID, buyersM5, sellersM5)
		}
		var breakdown struct {
			Factors map[string]struct {
				Points    float64 `json:"points"`
				Available bool    `json:"available"`
			} `json:"factors"`
		}
		if err := json.Unmarshal([]byte(breakdownJSON), &breakdown); err != nil {
			_ = rows.Close()
			_ = verifyDB.Close()
			t.Fatalf("decode score breakdown for %q: %v", tokenID, err)
		}
		if len(breakdown.Factors) != 6 {
			_ = rows.Close()
			_ = verifyDB.Close()
			t.Fatalf("score factors for %q = %d, want 6", tokenID, len(breakdown.Factors))
		}
		for _, factorName := range []string{"buy_pressure", "participant_growth", "social_momentum"} {
			factor, found := breakdown.Factors[factorName]
			if !found || !factor.Available || factor.Points != 15 {
				_ = rows.Close()
				_ = verifyDB.Close()
				t.Fatalf("factor %q for %q = %+v (found %v), want available 15 points", factorName, tokenID, factor, found)
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		_ = verifyDB.Close()
		t.Fatalf("iterate snapshot token IDs: %v", err)
	}
	if err := rows.Close(); err != nil {
		_ = verifyDB.Close()
		t.Fatalf("close snapshot rows: %v", err)
	}
	if len(persistedIDs) != 50 || len(survivorIDs) != 50 {
		_ = verifyDB.Close()
		t.Fatalf("unique persisted/expected survivors = %d/%d, want 50/50", len(persistedIDs), len(survivorIDs))
	}
	for tokenID := range survivorIDs {
		if persistedIDs[tokenID] != 1 {
			_ = verifyDB.Close()
			t.Fatalf("snapshot count for expected survivor %q = %d, want exactly 1", tokenID, persistedIDs[tokenID])
		}
	}
	if err := verifyDB.Close(); err != nil {
		t.Fatalf("close verification database: %v", err)
	}

	waitForGoroutinesNearBaseline(t, baselineGoroutines, 2, 2*time.Second)
}

func acceptanceCandidates(now time.Time) (map[domain.Chain][]domain.Candidate, map[string]struct{}) {
	byChain := map[domain.Chain][]domain.Candidate{
		domain.ChainBSC:    make([]domain.Candidate, 0, 1_000),
		domain.ChainSolana: make([]domain.Candidate, 0, 1_000),
	}
	survivors := make(map[string]struct{}, 50)
	for index := 0; index < 2_000; index++ {
		chain := domain.ChainBSC
		address := fmt.Sprintf("0x%040x", index)
		if index%2 == 1 {
			chain = domain.ChainSolana
			address = fmt.Sprintf("Mint%04d", index)
		}
		createdAt := now.Add(-24 * time.Hour)
		if index < 50 {
			createdAt = now.Add(-90 * time.Minute)
			prefix := "bsc:"
			if chain == domain.ChainSolana {
				prefix = "solana:"
			}
			survivors[prefix+address] = struct{}{}
		}
		byChain[chain] = append(byChain[chain], domain.Candidate{
			Chain:          chain,
			Address:        address,
			Name:           fmt.Sprintf("Fixture %d", index),
			Symbol:         fmt.Sprintf("F%04d", index),
			PairAddress:    fmt.Sprintf("pool-%04d", index),
			DetectedAt:     now,
			ChainCreatedAt: &createdAt,
		})
	}
	return byChain, survivors
}

type acceptanceDiscoverer struct {
	candidates map[domain.Chain][]domain.Candidate
}

func (d acceptanceDiscoverer) Discover(_ context.Context, chain domain.Chain) ([]domain.Candidate, error) {
	return append([]domain.Candidate(nil), d.candidates[chain]...), nil
}

type acceptanceEnricher struct {
	now     time.Time
	active  atomic.Int32
	maximum atomic.Int32
	calls   atomic.Int32
}

func (e *acceptanceEnricher) Enrich(ctx context.Context, candidate domain.Candidate) (domain.MarketSnapshot, error) {
	e.calls.Add(1)
	current := e.active.Add(1)
	defer e.active.Add(-1)
	for {
		observed := e.maximum.Load()
		if current <= observed || e.maximum.CompareAndSwap(observed, current) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return domain.MarketSnapshot{}, ctx.Err()
	}
	return acceptanceMarket(candidate, e.now), nil
}

func acceptanceMarket(candidate domain.Candidate, now time.Time) domain.MarketSnapshot {
	freshFloat := func(value float64) domain.DataValue[float64] {
		return domain.DataValue[float64]{Value: value, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now}
	}
	freshInt := func(value int) domain.DataValue[int] {
		return domain.DataValue[int]{Value: value, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now}
	}
	return domain.MarketSnapshot{
		PairAddress:  candidate.PairAddress,
		CollectedAt:  now,
		PriceUSD:     freshFloat(0.001),
		MarketCapUSD: freshFloat(100_000),
		LiquidityUSD: freshFloat(20_000),
		VolumeM5USD:  freshFloat(4_000),
		VolumeH1USD:  freshFloat(12_000),
		BuysM5:       freshInt(40),
		SellsM5:      freshInt(10),
		DataQuality: map[string]domain.Quality{
			"price_usd": domain.QualityFresh, "market_cap_usd": domain.QualityFresh,
			"liquidity_usd": domain.QualityFresh, "volume_m5_usd": domain.QualityFresh,
			"volume_h1_usd": domain.QualityFresh, "buys_m5": domain.QualityFresh,
			"sells_m5": domain.QualityFresh,
		},
	}
}

type acceptanceTrades struct {
	now   time.Time
	calls atomic.Int32
}

func (a *acceptanceTrades) ReadRecentTrades(context.Context, domain.Chain, string) (domain.TradeWindow, error) {
	a.calls.Add(1)
	return domain.TradeWindow{
		CollectedAt: a.now,
		Current: domain.TradeStats{
			Start: a.now.Add(-5 * time.Minute), End: a.now,
			Buys: 40, Sells: 10, UniqueBuyers: 40, UniqueSellers: 10,
			BuyVolumeUSD: 4_000, SellVolumeUSD: 1_000,
		},
		Previous: domain.TradeStats{
			Start: a.now.Add(-10 * time.Minute), End: a.now.Add(-5 * time.Minute),
			UniqueBuyers: 10,
		},
	}, nil
}

type acceptanceSocial struct {
	now   time.Time
	calls atomic.Int32
}

func (a *acceptanceSocial) Evidence(context.Context, domain.Candidate, domain.MarketSnapshot) (domain.SocialEvidence, error) {
	a.calls.Add(1)
	return domain.SocialEvidence{
		CollectedAt: a.now,
		MentionCount: domain.DataValue[int]{
			Value: 4, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: a.now,
		},
	}, nil
}

func waitForGoroutinesNearBaseline(t *testing.T, baseline, tolerance int, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	last := runtime.NumGoroutine()
	for last > baseline+tolerance {
		select {
		case <-poll.C:
			last = runtime.NumGoroutine()
		case <-deadline.C:
			t.Fatalf("goroutines after scan = %d, want <= baseline %d + tolerance %d", last, baseline, tolerance)
		}
	}
}
