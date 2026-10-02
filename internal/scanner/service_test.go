package scanner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/config"
	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/providers"
	"github.com/alen1/fomo-radar/internal/score"
	"github.com/alen1/fomo-radar/internal/store"
	_ "modernc.org/sqlite"
)

func TestScanPersistsHealthyChainWhenOtherDiscoveryFails(t *testing.T) {
	now := scanTime()
	discoverer := fakeDiscoverer{
		candidates: map[domain.Chain][]domain.Candidate{domain.ChainSolana: {candidate("solana-one", domain.ChainSolana, now)}},
		errors:     map[domain.Chain]error{domain.ChainBSC: errors.New("bsc unavailable")},
	}
	database := &fakeStore{}
	service := newTestService(now, discoverer, fakeEnricher{markets: map[string]domain.MarketSnapshot{"solana-one": activeMarket("solana-one", now)}}, successfulTrades(now), missingSocial{}, database)

	result, err := service.Scan(context.Background(), ModeManual)

	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Status != StatusDegraded || len(result.Candidates) != 1 || result.Candidates[0].Candidate.Address != "solana-one" {
		t.Fatalf("Scan() result = %+v, want one degraded Solana candidate", result)
	}
	if len(database.snapshots) != 1 || database.snapshots[0].TokenID != "solana:solana-one" {
		t.Fatalf("persisted snapshots = %+v, want Solana snapshot", database.snapshots)
	}
	if !strings.Contains(result.ErrorSummary, "discover bsc: bsc unavailable") {
		t.Fatalf("error summary = %q, want BSC discovery failure", result.ErrorSummary)
	}
}

func TestScanNormalizesScoreWhenOptionalDeepEvidenceIsAbsent(t *testing.T) {
	now := scanTime()
	c := candidate("0xoptional", domain.ChainBSC, now)
	database := &fakeStore{}
	service := newTestService(
		now,
		fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: {c}}},
		fakeEnricher{markets: map[string]domain.MarketSnapshot{c.Address: activeMarket(c.Address, now)}},
		failingTrades{err: errors.New("trades unavailable")},
		failingSocial{err: errors.New("social unavailable")},
		database,
	)

	result, err := service.Scan(context.Background(), ModeManual)

	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Status != StatusDegraded || len(database.snapshots) != 1 {
		t.Fatalf("status/snapshots = %q/%d, want degraded/1", result.Status, len(database.snapshots))
	}
	got := database.snapshots[0]
	if got.Score.Quality != domain.QualityFresh || got.Score.Value <= 0 {
		t.Fatalf("persisted score = %+v, want available chain-only score", got.Score)
	}
	if factor := got.ScoreBreakdown.Factors["social_momentum"]; factor.Available {
		t.Fatalf("social factor = %+v, want unavailable", factor)
	}
	if got.ScoreBreakdown.AvailableMaximum >= 100 || got.ScoreBreakdown.NormalizedMomentum <= got.ScoreBreakdown.RawMomentum {
		t.Fatalf("score normalization = raw %v available %v normalized %v", got.ScoreBreakdown.RawMomentum, got.ScoreBreakdown.AvailableMaximum, got.ScoreBreakdown.NormalizedMomentum)
	}
}

func TestScanContinuesAfterOneEnrichmentTimeout(t *testing.T) {
	now := scanTime()
	timedOut := candidate("0xtimeout", domain.ChainBSC, now)
	healthy := candidate("0xhealthy", domain.ChainBSC, now)
	database := &fakeStore{}
	service := newTestService(
		now,
		fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: {timedOut, healthy}}},
		fakeEnricher{
			markets: map[string]domain.MarketSnapshot{healthy.Address: activeMarket(healthy.Address, now)},
			errors:  map[string]error{timedOut.Address: &providers.ProviderError{Kind: providers.ErrorTimeout, Provider: "dexscreener", Err: context.DeadlineExceeded}},
		},
		successfulTrades(now), missingSocial{}, database,
	)

	result, err := service.Scan(context.Background(), ModeManual)

	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Status != StatusDegraded || len(result.Candidates) != 1 || len(database.snapshots) != 1 {
		t.Fatalf("result/snapshots = %+v/%d, want one surviving candidate", result, len(database.snapshots))
	}
	if !strings.Contains(result.ErrorSummary, "enrich bsc:0xtimeout") {
		t.Fatalf("error summary = %q, want timed-out token", result.ErrorSummary)
	}
}

func TestScanTreatsSnapshotInsertFailureAsFatalAndRecordsFailedRun(t *testing.T) {
	now := scanTime()
	c := candidate("0xinsert", domain.ChainBSC, now)
	database := &fakeStore{insertErr: errors.New("disk full")}
	service := newTestService(
		now,
		fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: {c}}},
		fakeEnricher{markets: map[string]domain.MarketSnapshot{c.Address: activeMarket(c.Address, now)}},
		successfulTrades(now), missingSocial{}, database,
	)

	_, err := service.Scan(context.Background(), ModeManual)

	if err == nil || !strings.Contains(err.Error(), "insert snapshot") || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Scan() error = %v, want original insert failure", err)
	}
	if len(database.finishes) != 1 || database.finishes[0].status != StatusFailed || !strings.Contains(database.finishes[0].summary, "disk full") {
		t.Fatalf("FinishScanRun calls = %+v, want failed run with original error", database.finishes)
	}
}

func TestScanDeduplicatesCanonicalTokenIDs(t *testing.T) {
	now := scanTime()
	first := candidate("0xAbC", domain.ChainBSC, now)
	second := candidate("0xabc", domain.ChainBSC, now)
	var calls atomic.Int32
	enricher := enrichingFunc(func(_ context.Context, c domain.Candidate) (domain.MarketSnapshot, error) {
		calls.Add(1)
		return activeMarket(c.Address, now), nil
	})
	service := newTestService(now, fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: {first, second}}}, enricher, successfulTrades(now), missingSocial{}, &fakeStore{})

	result, err := service.Scan(context.Background(), ModeManual)

	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if calls.Load() != 1 || result.CandidatesSeen != 1 {
		t.Fatalf("enrichment calls/seen = %d/%d, want canonical dedupe", calls.Load(), result.CandidatesSeen)
	}
}

func TestFastScanCoordinatorPersistsPureMomentumEvidenceWithoutV1Score(t *testing.T) {
	now := scanTime()
	oldPair := now.Add(-42 * time.Hour)
	c := domain.Candidate{
		ID: "bsc:niu", Chain: domain.ChainBSC,
		Address: "0xbeea1d618e533a387d941f58a7d4c9b7bd377777",
		Name:    "牛来", Symbol: "牛来", Origin: domain.CandidateOriginMomentum,
		MomentumSources:    []domain.MomentumSource{domain.MomentumSourceGeckoTrending},
		TriggerPoolAddress: "0xtrigger", TriggerPoolCreatedAt: &oldPair, DetectedAt: now,
	}
	market := activeMarket(c.Address, now)
	market.PairCreatedAt = &oldPair
	market.SelectedPairCreatedAt = &oldPair
	market.PriceChangeH1 = freshFloat(30, now)
	market.Volume24hUSD = freshFloat(200_000, now)
	c.DiscoveryMarket = market
	database := &fakeStore{}
	coordinator := &fakeCandidateCoordinator{result: domain.DiscoveryResult{
		Candidates: []domain.Candidate{c}, Coverages: map[domain.Chain]domain.DiscoveryCoverage{},
	}}
	service := NewService(
		testConfig(), fakeDiscoverer{}, enrichingFunc(func(context.Context, domain.Candidate) (domain.MarketSnapshot, error) {
			t.Fatal("pure Momentum candidate must use coordinator evidence, not one-address Dex enrichment")
			return domain.MarketSnapshot{}, nil
		}),
		successfulTrades(now), missingSocial{}, score.NewEngine(), database, func() time.Time { return now }, coordinator,
	)

	result, tasks, err := service.FastScan(context.Background(), ModeWatch)
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.calls != 1 || result.CandidatesSeen != 1 || result.CandidatesScored != 0 || len(tasks) != 0 {
		t.Fatalf("calls/seen/scored/tasks = %d/%d/%d/%d", coordinator.calls, result.CandidatesSeen, result.CandidatesScored, len(tasks))
	}
	if len(database.snapshots) != 1 {
		t.Fatalf("snapshots = %d", len(database.snapshots))
	}
	snapshot := database.snapshots[0]
	if snapshot.Score.Quality != domain.QualityMissing || snapshot.ScoreVersion != "" || snapshot.Tier != "" {
		t.Fatalf("pure Momentum received a score: %#v", snapshot)
	}
	if snapshot.MomentumEvidence == nil || snapshot.MomentumEvidence.Version != "MOMENTUM_EVIDENCE_V1" {
		t.Fatalf("Momentum evidence = %#v", snapshot.MomentumEvidence)
	}
	if snapshot.LaunchType != domain.LaunchTypeReactivation || snapshot.CandidateOrigin != domain.CandidateOriginMomentum {
		t.Fatalf("classification/origin = %q/%q", snapshot.LaunchType, snapshot.CandidateOrigin)
	}
	if len(database.momentumStates) != 1 || database.momentumStates[0].TokenID != TokenID(c.Chain, c.Address) {
		t.Fatalf("Momentum states = %#v", database.momentumStates)
	}
}

func TestBuildMomentumEvidenceKeepsZeroSellPressureJSONSafe(t *testing.T) {
	now := scanTime()
	market := domain.MarketSnapshot{
		CollectedAt: now,
		BuysH1:      freshInt(12, now),
		SellsH1:     freshInt(0, now),
	}

	evidence := buildMomentumEvidence(market)
	if factor := evidence.Factors["buy_pressure"]; factor.Available {
		t.Fatalf("buy_pressure = %+v, want unavailable when sell denominator is zero", factor)
	}
	if _, err := json.Marshal(evidence); err != nil {
		t.Fatalf("Momentum evidence must remain JSON-safe: %v", err)
	}
}

func TestScanCapsEnrichmentConcurrencyAtFour(t *testing.T) {
	now := scanTime()
	candidates := make([]domain.Candidate, 8)
	for i := range candidates {
		candidates[i] = candidate(fmt.Sprintf("0x%d", i), domain.ChainBSC, now)
	}
	var active atomic.Int32
	var maximum atomic.Int32
	enricher := enrichingFunc(func(_ context.Context, c domain.Candidate) (domain.MarketSnapshot, error) {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		return activeMarket(c.Address, now), nil
	})
	service := newTestService(now, fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: candidates}}, enricher, successfulTrades(now), missingSocial{}, &fakeStore{})

	if _, err := service.Scan(context.Background(), ModeManual); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if maximum.Load() != 4 {
		t.Fatalf("maximum concurrent enrichment = %d, want exactly 4", maximum.Load())
	}
}

func TestScanEndToEndPersistsRealScoreAndPreservesFirstSeenMarketCap(t *testing.T) {
	ctx := context.Background()
	now := scanTime()
	created := now.Add(-90 * time.Minute)
	c := domain.Candidate{Chain: domain.ChainSolana, Address: "Exact72Mint", Name: "Fixture", Symbol: "FX", PairAddress: "Pool72", ChainCreatedAt: &created}
	dbPath := filepath.Join(t.TempDir(), "fomo.db")
	database, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}

	marketCap := 100_000.0
	enricher := enrichingFunc(func(_ context.Context, candidate domain.Candidate) (domain.MarketSnapshot, error) {
		market := activeMarket(candidate.Address, now)
		market.MarketCapUSD = freshFloat(marketCap, now)
		market.LiquidityUSD = freshFloat(20_000, now)
		market.BuysM5 = freshInt(4, now)
		market.SellsM5 = freshInt(1, now)
		return market, nil
	})
	tradeReader := tradeFunc(func(context.Context, domain.Chain, string) (domain.TradeWindow, error) {
		return exact72Trades(now), nil
	})
	social := socialFunc(func(context.Context, domain.Candidate, domain.MarketSnapshot) (domain.SocialEvidence, error) {
		return domain.SocialEvidence{CollectedAt: now, MentionCount: freshInt(1, now)}, nil
	})
	cfg := testConfig()
	service := NewService(cfg, fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainSolana: {c}}}, enricher, tradeReader, social, score.NewEngine(), database, func() time.Time { return now })

	first, err := service.Scan(ctx, ModeManual)
	if err != nil {
		_ = database.Close()
		t.Fatalf("first Scan() error = %v", err)
	}
	if len(first.Candidates) != 1 || first.Candidates[0].Snapshot.Score.Value != 72 || first.Candidates[0].Snapshot.Tier != string(domain.TierFastRising) {
		_ = database.Close()
		t.Fatalf("first candidate = %+v, want score 72 and FAST_RISING", first.Candidates)
	}
	if first.Candidates[0].Snapshot.BuyersM5.Value != 4 || first.Candidates[0].Snapshot.BuyersM5.Quality != domain.QualityFresh {
		_ = database.Close()
		t.Fatalf("persisted BuyersM5 = %+v, want Gecko unique buyers", first.Candidates[0].Snapshot.BuyersM5)
	}
	for name, want := range map[string]float64{"buyer_velocity": 25, "volume_velocity": 20} {
		factor := first.Candidates[0].Snapshot.ScoreBreakdown.Factors[name]
		if !factor.Available || factor.Points != want {
			_ = database.Close()
			t.Fatalf("first-scan deep factor %q = %+v, want available %v without DB history", name, factor, want)
		}
		if quality := first.Candidates[0].Snapshot.ScoreBreakdown.EvidenceQuality[name]; quality != domain.QualityFresh {
			_ = database.Close()
			t.Fatalf("first-scan deep quality %q = %q, want fresh", name, quality)
		}
	}

	marketCap = 400_000
	if _, err := service.Scan(ctx, ModeManual); err != nil {
		_ = database.Close()
		t.Fatalf("second Scan() error = %v", err)
	}
	snapshots, err := database.GetRecentSnapshots(ctx, TokenID(c.Chain, c.Address), 10)
	if err != nil {
		_ = database.Close()
		t.Fatalf("GetRecentSnapshots() error = %v", err)
	}
	if len(snapshots) != 4 {
		_ = database.Close()
		t.Fatalf("snapshot count = %d, want 4 (Fast + Deep per run)", len(snapshots))
	}
	if err := database.Close(); err != nil {
		t.Fatalf("store.Close() error = %v", err)
	}

	verifyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer verifyDB.Close()
	var firstSeenMarketCap, firstSeenScore float64
	if err := verifyDB.QueryRowContext(ctx, `SELECT first_seen_market_cap_usd, first_seen_score FROM tokens WHERE id = ?`, TokenID(c.Chain, c.Address)).Scan(&firstSeenMarketCap, &firstSeenScore); err != nil {
		t.Fatalf("query first seen market cap: %v", err)
	}
	if firstSeenMarketCap != 100_000 {
		t.Fatalf("first_seen_market_cap_usd = %v, want 100000", firstSeenMarketCap)
	}
	if firstSeenScore != 100 {
		t.Fatalf("first_seen_score = %v, want immutable initial Fast raw score 100", firstSeenScore)
	}
	var versionCount int
	var configJSON string
	if err := verifyDB.QueryRowContext(ctx, `SELECT COUNT(*), config_json FROM score_versions WHERE version = ?`, score.Version).Scan(&versionCount, &configJSON); err != nil {
		t.Fatalf("query score version: %v", err)
	}
	if versionCount != 1 || !strings.Contains(configJSON, `"factors"`) || !strings.Contains(configJSON, `"risk_penalties"`) || !strings.Contains(configJSON, `"tiers"`) {
		t.Fatalf("score version count/config = %d/%q, want one complete immutable V1 config", versionCount, configJSON)
	}
}

func TestScanLoadsOnlyHistoryBeforeCurrentObservation(t *testing.T) {
	now := scanTime()
	c := candidate("0xcutoff", domain.ChainBSC, now)
	market := activeMarket(c.Address, now)
	earlier := activeMarket(c.Address, now.Add(-time.Minute))
	earlier.TokenID = TokenID(c.Chain, c.Address)
	earlier.BuyersM5 = freshInt(1, earlier.CollectedAt)
	database := &fakeStore{history: []domain.MarketSnapshot{earlier}}
	service := newTestService(
		now,
		fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: {c}}},
		fakeEnricher{markets: map[string]domain.MarketSnapshot{c.Address: market}},
		successfulTrades(now), missingSocial{}, database,
	)

	result, err := service.Scan(context.Background(), ModeManual)

	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(database.historyCutoffs) != 2 || !database.historyCutoffs[0].Equal(market.CollectedAt) || !database.historyCutoffs[1].Equal(market.CollectedAt) {
		t.Fatalf("history cutoffs = %v, want Fast and Deep bounded at %s", database.historyCutoffs, market.CollectedAt)
	}
	if len(result.Candidates) != 1 || !result.Candidates[0].Snapshot.ScoreBreakdown.Factors["buyer_velocity"].Available {
		t.Fatalf("score breakdown = %+v, want earlier snapshot usable for buyer velocity", result.Candidates)
	}
}

func TestScanUsesLatestAdoptedEvidenceTimeForSignalAndHistoryCutoff(t *testing.T) {
	t0 := scanTime()
	t1 := t0.Add(45 * time.Second)
	c := candidate("0xevidence-time", domain.ChainBSC, t0.Add(-time.Minute))
	market := activeMarket(c.Address, t0)
	database := &fakeStore{}
	trades := tradeFunc(func(context.Context, domain.Chain, string) (domain.TradeWindow, error) {
		window := exact72Trades(t1)
		window.Trades = []domain.Trade{{
			FromAddress: "0xbuyer", Kind: domain.TradeBuy, VolumeUSD: 25,
			BlockTimestamp: t0.Add(20 * time.Second),
		}}
		return window, nil
	})
	social := socialFunc(func(context.Context, domain.Candidate, domain.MarketSnapshot) (domain.SocialEvidence, error) {
		return domain.SocialEvidence{CollectedAt: t1, MentionCount: freshInt(1, t1)}, nil
	})
	service := newTestService(
		t0,
		fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: {c}}},
		fakeEnricher{markets: map[string]domain.MarketSnapshot{c.Address: market}},
		trades, social, database,
	)

	result, err := service.Scan(context.Background(), ModeManual)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(result.Candidates))
	}
	snapshot := result.Candidates[0].Snapshot
	if !snapshot.CollectedAt.Equal(t1) || !snapshot.Score.CollectedAt.Equal(t1) {
		t.Fatalf("snapshot/score times = %s/%s, want latest evidence %s", snapshot.CollectedAt, snapshot.Score.CollectedAt, t1)
	}
	if len(database.historyCutoffs) != 2 || !database.historyCutoffs[0].Equal(t0) || !database.historyCutoffs[1].Equal(t1) {
		t.Fatalf("history cutoffs = %v, want Fast %s then Deep %s", database.historyCutoffs, t0, t1)
	}
	if snapshot.BuyersM5.CollectedAt.After(snapshot.CollectedAt) || snapshot.Score.CollectedAt.After(snapshot.CollectedAt) {
		t.Fatalf("adopted evidence exceeds signal time: snapshot=%+v", snapshot)
	}
}

func TestScanPreservesDiscoveryTimeAsFirstSeen(t *testing.T) {
	marketAt := scanTime()
	discoveredAt := marketAt.Add(-2 * time.Minute)
	c := candidate("0xfirst-seen", domain.ChainBSC, discoveredAt)
	database := &fakeStore{}
	service := newTestService(
		marketAt,
		fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: {c}}},
		fakeEnricher{markets: map[string]domain.MarketSnapshot{c.Address: activeMarket(c.Address, marketAt)}},
		successfulTrades(marketAt), missingSocial{}, database,
	)

	if _, err := service.Scan(context.Background(), ModeManual); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(database.tokens) != 1 || !database.tokens[0].DetectedAt.Equal(discoveredAt) {
		t.Fatalf("persisted first_seen_at = %v, want discovery time %s", database.tokens, discoveredAt)
	}
	if !database.tokens[0].FirstSeenPriceUSD.CollectedAt.Equal(marketAt) {
		t.Fatalf("first seen market evidence time = %s, want %s", database.tokens[0].FirstSeenPriceUSD.CollectedAt, marketAt)
	}
}

func TestFastScanPersistsGeckoOnlyCandidateWithoutCallingDeep(t *testing.T) {
	now := scanTime()
	discoveryCreated := now.Add(-5 * time.Minute)
	market := activeMarket("MintCase", now)
	market.PairAddress = "DiscoveryPool"
	market.AggregateBuyersM5 = freshInt(7, now)
	market.AggregateSellersM5 = freshInt(3, now)
	c := domain.Candidate{
		Chain: domain.ChainSolana, Address: "MintCase", Name: "极早币", Symbol: "早",
		PairAddress: "DiscoveryPool", DiscoveryPoolAddress: "DiscoveryPool",
		DetectedAt: now, ChainCreatedAt: &discoveryCreated, DiscoveryPoolCreatedAt: &discoveryCreated,
		DiscoveryMarket: market,
	}
	discoverer := coverageDiscoverer{fakeDiscoverer: fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainSolana: {c}}}, now: now}
	var deepCalls atomic.Int32
	database := &fakeStore{}
	service := newTestService(
		now,
		discoverer,
		fakeEnricher{errors: map[string]error{"MintCase": errors.New("not indexed")}},
		tradeFunc(func(context.Context, domain.Chain, string) (domain.TradeWindow, error) {
			deepCalls.Add(1)
			return domain.TradeWindow{}, errors.New("FastScan must not call Deep")
		}),
		missingSocial{},
		database,
	)

	result, tasks, err := service.FastScan(context.Background(), ModeManual)
	if err != nil {
		t.Fatalf("FastScan() error = %v", err)
	}
	if deepCalls.Load() != 0 {
		t.Fatalf("Deep calls = %d, want 0", deepCalls.Load())
	}
	if result.CandidatesSeen != 1 || result.CandidatesScored != 1 || len(result.Candidates) != 1 || len(tasks) != 1 {
		t.Fatalf("Fast result = seen %d scored %d candidates %d tasks %d", result.CandidatesSeen, result.CandidatesScored, len(result.Candidates), len(tasks))
	}
	got := result.Candidates[0].Snapshot
	if got.MarketDataSource != domain.MarketDataSourceGecko || got.MarketDataQuality != domain.MarketDataQualityPartial {
		t.Fatalf("market source/quality = %q/%q", got.MarketDataSource, got.MarketDataQuality)
	}
	if got.AggregateBuyersM5.Value != 7 || got.BuyersM5.Quality != domain.QualityMissing {
		t.Fatalf("participants = aggregate %+v unique %+v", got.AggregateBuyersM5, got.BuyersM5)
	}
	if got.ScoreBreakdown.EvidenceAvailable != 2 || got.Tier != string(domain.TierWatch) {
		t.Fatalf("confidence/tier = %d/%q", got.ScoreBreakdown.EvidenceAvailable, got.Tier)
	}
	if len(database.coverages) != 2 || len(database.snapshots) != 1 {
		t.Fatalf("persisted coverage/snapshots = %d/%d", len(database.coverages), len(database.snapshots))
	}
	if len(database.tokens) != 1 || database.tokens[0].FirstSeenScore.Quality != domain.QualityFresh || database.tokens[0].FirstSeenMarketCapUSD.Quality != domain.QualityFresh {
		t.Fatalf("first Fast insert did not carry immutable first-seen evidence: %+v", database.tokens)
	}
	if result.GeckoOnlyCandidates != 1 || result.MergedCandidates != 0 {
		t.Fatalf("source metrics = gecko %d merged %d", result.GeckoOnlyCandidates, result.MergedCandidates)
	}
}

func TestScoreInputMarksTruncatedDeepVelocitiesDegraded(t *testing.T) {
	now := scanTime()
	c := candidate("0xtruncated", domain.ChainBSC, now)
	trades := exact72Trades(now)
	trades.Truncated = true
	input := scoreInput(
		readyCandidate{candidate: c, market: activeMarket(c.Address, now)},
		nil,
		deepResult{trades: trades, tradeOK: true},
		now,
	)

	if input.BuyerVelocity.Value != 4 || input.BuyerVelocity.Quality != domain.QualityDegraded {
		t.Fatalf("BuyerVelocity = %+v, want degraded literal ratio 4", input.BuyerVelocity)
	}
	if !math.IsInf(input.VolumeVelocity.Value, 1) || input.VolumeVelocity.Quality != domain.QualityDegraded {
		t.Fatalf("VolumeVelocity = %+v, want degraded +Inf from positive/zero windows", input.VolumeVelocity)
	}
}

func TestScoreInputLabelsDiscoveryPoolAgeEvidence(t *testing.T) {
	now := scanTime()
	discoveryCreated := now.Add(-34 * time.Minute)
	selectedCreated := now.Add(-38 * time.Hour)
	item := readyCandidate{
		candidate: domain.Candidate{DiscoveryPoolCreatedAt: &discoveryCreated},
		market:    domain.MarketSnapshot{SelectedPairCreatedAt: &selectedCreated},
	}

	got := scoreInput(item, nil, deepResult{}, now)

	if got.TokenAge.Value != 34*time.Minute || got.TokenAge.Source != "discovery_pool_created_at" {
		t.Fatalf("token age evidence = %v from %q", got.TokenAge.Value, got.TokenAge.Source)
	}
}

func TestProcessDeepTaskUsesDiscoveryPoolAndPreservesAggregateParticipants(t *testing.T) {
	now := scanTime()
	discoveryCreated := now.Add(-10 * time.Minute)
	runID := int64(9)
	baseID := int64(41)
	market := activeMarket("token", now.Add(-time.Minute))
	market.ID = baseID
	market.TokenID = "bsc:0xtoken"
	market.PairAddress = "0xselected"
	market.DiscoveryPoolAddress = "0xdiscovery"
	market.DiscoveryPoolCreatedAt = &discoveryCreated
	market.SignalFirstSeenAt = now.Add(-time.Minute)
	market.ScanRunID = &runID
	market.AggregateBuyersM5 = freshInt(11, now)
	market.AggregateSellersM5 = freshInt(4, now)
	candidate := domain.Candidate{
		ID: "bsc:0xtoken", Chain: domain.ChainBSC, Address: "0xtoken",
		DiscoveryPoolAddress: "0xdiscovery", DiscoveryPoolCreatedAt: &discoveryCreated,
	}
	database := &fakeStore{currentFast: map[int64]bool{baseID: true}}
	var targetPool string
	service := newTestService(now, fakeDiscoverer{}, fakeEnricher{}, tradeFunc(func(_ context.Context, _ domain.Chain, pool string) (domain.TradeWindow, error) {
		targetPool = pool
		window := exact72Trades(now)
		window.CollectedAt = now
		return window, nil
	}), missingSocial{}, database)

	outcome, err := service.ProcessDeepTask(context.Background(), domain.DeepTask{
		TokenID: candidate.ID, BaseFastSnapshotID: baseID, Candidate: candidate, Snapshot: market,
	})
	if err != nil {
		t.Fatalf("ProcessDeepTask() error = %v", err)
	}
	if !outcome.Inserted || targetPool != "0xdiscovery" {
		t.Fatalf("inserted/target = %v/%q", outcome.Inserted, targetPool)
	}
	got := outcome.Result.Snapshot
	if got.BuyersM5.Value != 4 || got.SellersM5.Value != 1 || got.AggregateBuyersM5.Value != 11 || got.AggregateSellersM5.Value != 4 {
		t.Fatalf("participants = unique %d/%d aggregate %d/%d", got.BuyersM5.Value, got.SellersM5.Value, got.AggregateBuyersM5.Value, got.AggregateSellersM5.Value)
	}
	if got.SignalFirstSeenAt.Before(now) || got.BaseFastSnapshotID == nil || *got.BaseFastSnapshotID != baseID {
		t.Fatalf("Deep lineage/time = base %v signal %v", got.BaseFastSnapshotID, got.SignalFirstSeenAt)
	}
	if len(database.historyCutoffs) != 1 || !database.historyCutoffs[0].Equal(got.SignalFirstSeenAt) {
		t.Fatalf("history cutoff = %v, want Deep signal %v", database.historyCutoffs, got.SignalFirstSeenAt)
	}
}

func TestManualScanPersistsFastThenWaitsForDeepResult(t *testing.T) {
	now := scanTime()
	c := candidate("0xmanual-deep", domain.ChainBSC, now)
	database := &fakeStore{}
	service := newTestService(
		now,
		fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: {c}}},
		fakeEnricher{markets: map[string]domain.MarketSnapshot{c.Address: activeMarket(c.Address, now)}},
		successfulTrades(now), missingSocial{}, database,
	)

	result, err := service.Scan(context.Background(), ModeManual)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(database.snapshots) != 1 || database.snapshots[0].Stage != domain.SnapshotStageFast {
		t.Fatalf("Fast snapshots = %+v, want one FAST", database.snapshots)
	}
	if len(database.deepSnapshots) != 1 || database.deepSnapshots[0].Stage != domain.SnapshotStageDeep {
		t.Fatalf("Deep snapshots = %+v, want one DEEP", database.deepSnapshots)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Snapshot.Stage != domain.SnapshotStageDeep || result.DeepBacklog != 0 || result.DeepCompleted != 1 {
		t.Fatalf("manual result = %+v, want awaited DEEP with empty backlog", result)
	}
}

func TestScanRecomputesTokenAgeAtFinalSignalTime(t *testing.T) {
	t0 := scanTime()
	t1 := t0.Add(45 * time.Second)
	createdAt := t0.Add(-29*time.Minute - 59*time.Second)
	c := domain.Candidate{
		Chain: domain.ChainBSC, Address: "0xage-boundary", PairAddress: "0xage-boundary-pool",
		DetectedAt: t0, ChainCreatedAt: &createdAt,
	}
	database := &fakeStore{}
	engine := &recordingEngine{inner: score.NewEngine()}
	service := NewService(
		testConfig(),
		fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: {c}}},
		fakeEnricher{markets: map[string]domain.MarketSnapshot{c.Address: activeMarket(c.Address, t0)}},
		tradeFunc(func(context.Context, domain.Chain, string) (domain.TradeWindow, error) {
			return exact72Trades(t1), nil
		}),
		missingSocial{}, engine, database, func() time.Time { return t0 },
	)

	result, err := service.Scan(context.Background(), ModeManual)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Candidates) != 1 || !result.Candidates[0].Snapshot.CollectedAt.Equal(t1) {
		t.Fatalf("persisted signal = %+v, want collected_at %s", result.Candidates, t1)
	}
	breakdown := result.Candidates[0].Snapshot.ScoreBreakdown
	if breakdown.AgeBonus != 6 || breakdown.EvidenceQuality["token_age"] != domain.QualityFresh {
		t.Fatalf("age bonus/quality = %v/%q, want 6/fresh at 30m44s", breakdown.AgeBonus, breakdown.EvidenceQuality["token_age"])
	}
	if len(engine.inputs) != 2 {
		t.Fatalf("score inputs = %d, want Fast and Deep", len(engine.inputs))
	}
	if engine.inputs[0].TokenAge.Value != 29*time.Minute+59*time.Second || !engine.inputs[0].TokenAge.CollectedAt.Equal(t0) {
		t.Fatalf("Fast token age = %+v, want 29m59s at %s", engine.inputs[0].TokenAge, t0)
	}
	if engine.inputs[1].TokenAge.Value != 30*time.Minute+44*time.Second || !engine.inputs[1].TokenAge.CollectedAt.Equal(t1) {
		t.Fatalf("Deep token age = %+v, want 30m44s at %s", engine.inputs[1].TokenAge, t1)
	}
}

func newTestService(now time.Time, discoverer providers.Discoverer, enricher providers.Enricher, trades providers.TradeReader, social providers.SocialProvider, database scanStore) *Service {
	return NewService(testConfig(), discoverer, enricher, trades, social, score.NewEngine(), database, func() time.Time { return now })
}

func testConfig() config.Config {
	return config.Config{DeepScanThreshold: 40, CandidateMaxAge: 6 * time.Hour, EarlyMaxAge: 2 * time.Hour, PreferredMaxMCUSD: 500_000}
}

func scanTime() time.Time { return time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC) }

func candidate(address string, chain domain.Chain, now time.Time) domain.Candidate {
	created := now.Add(-90 * time.Minute)
	return domain.Candidate{Chain: chain, Address: address, PairAddress: address + "-pool", DetectedAt: now, ChainCreatedAt: &created}
}

func activeMarket(address string, now time.Time) domain.MarketSnapshot {
	return domain.MarketSnapshot{
		PairAddress: address + "-pool", CollectedAt: now,
		PriceUSD: freshFloat(.001, now), MarketCapUSD: freshFloat(100_000, now), LiquidityUSD: freshFloat(10_000, now),
		VolumeM5USD: freshFloat(1_000, now), VolumeH1USD: freshFloat(3_000, now),
		BuysM5: freshInt(20, now), SellsM5: freshInt(5, now),
		DataQuality: map[string]domain.Quality{
			"price_usd": domain.QualityFresh, "market_cap_usd": domain.QualityFresh, "liquidity_usd": domain.QualityFresh,
			"volume_m5_usd": domain.QualityFresh, "volume_h1_usd": domain.QualityFresh,
			"buys_m5": domain.QualityFresh, "sells_m5": domain.QualityFresh,
		},
	}
}

func successfulTrades(now time.Time) providers.TradeReader {
	return tradeFunc(func(context.Context, domain.Chain, string) (domain.TradeWindow, error) {
		return exact72Trades(now), nil
	})
}

func exact72Trades(now time.Time) domain.TradeWindow {
	return domain.TradeWindow{
		CollectedAt: now,
		Current:     domain.TradeStats{Start: now.Add(-5 * time.Minute), End: now, Buys: 4, Sells: 1, UniqueBuyers: 4, UniqueSellers: 1, BuyVolumeUSD: 400, SellVolumeUSD: 100, TopAddressVolumeShare: .6},
		Previous:    domain.TradeStats{Start: now.Add(-10 * time.Minute), End: now.Add(-5 * time.Minute), UniqueBuyers: 1},
	}
}

type fakeDiscoverer struct {
	candidates map[domain.Chain][]domain.Candidate
	errors     map[domain.Chain]error
}

type fakeCandidateCoordinator struct {
	result domain.DiscoveryResult
	calls  int
}

func (f *fakeCandidateCoordinator) Discover(context.Context, int64) domain.DiscoveryResult {
	f.calls++
	return f.result
}

type coverageDiscoverer struct {
	fakeDiscoverer
	now time.Time
}

func (f coverageDiscoverer) DiscoverWithCoverage(ctx context.Context, chain domain.Chain) (domain.DiscoveryBatch, error) {
	candidates, err := f.Discover(ctx, chain)
	coverage := domain.DiscoveryCoverage{Chain: chain, ScanStartedAt: f.now, PagesFetched: 1, UniqueCandidates: len(candidates)}
	if len(candidates) > 0 && candidates[0].DiscoveryPoolCreatedAt != nil {
		coverage.NewestPoolAt = *candidates[0].DiscoveryPoolCreatedAt
		coverage.OldestPoolAt = *candidates[0].DiscoveryPoolCreatedAt
	}
	return domain.DiscoveryBatch{Candidates: candidates, Coverage: coverage}, err
}

func (f fakeDiscoverer) Discover(_ context.Context, chain domain.Chain) ([]domain.Candidate, error) {
	return append([]domain.Candidate(nil), f.candidates[chain]...), f.errors[chain]
}

type fakeEnricher struct {
	markets map[string]domain.MarketSnapshot
	errors  map[string]error
}

func (f fakeEnricher) Enrich(_ context.Context, c domain.Candidate) (domain.MarketSnapshot, error) {
	return f.markets[c.Address], f.errors[c.Address]
}

type enrichingFunc func(context.Context, domain.Candidate) (domain.MarketSnapshot, error)

func (f enrichingFunc) Enrich(ctx context.Context, c domain.Candidate) (domain.MarketSnapshot, error) {
	return f(ctx, c)
}

type tradeFunc func(context.Context, domain.Chain, string) (domain.TradeWindow, error)

func (f tradeFunc) ReadRecentTrades(ctx context.Context, chain domain.Chain, pool string) (domain.TradeWindow, error) {
	return f(ctx, chain, pool)
}

type failingTrades struct{ err error }

func (f failingTrades) ReadRecentTrades(context.Context, domain.Chain, string) (domain.TradeWindow, error) {
	return domain.TradeWindow{}, f.err
}

type missingSocial struct{}

func (missingSocial) Evidence(_ context.Context, _ domain.Candidate, market domain.MarketSnapshot) (domain.SocialEvidence, error) {
	return domain.SocialEvidence{CollectedAt: market.CollectedAt, Links: market.Links, MentionCount: domain.DataValue[int]{Quality: domain.QualityMissing}}, nil
}

type failingSocial struct{ err error }

func (f failingSocial) Evidence(context.Context, domain.Candidate, domain.MarketSnapshot) (domain.SocialEvidence, error) {
	return domain.SocialEvidence{}, f.err
}

type socialFunc func(context.Context, domain.Candidate, domain.MarketSnapshot) (domain.SocialEvidence, error)

func (f socialFunc) Evidence(ctx context.Context, c domain.Candidate, market domain.MarketSnapshot) (domain.SocialEvidence, error) {
	return f(ctx, c, market)
}

type recordingEngine struct {
	inner  score.Engine
	inputs []score.Input
}

func (e *recordingEngine) Evaluate(input score.Input) domain.ScoreBreakdown {
	e.inputs = append(e.inputs, input)
	return e.inner.Evaluate(input)
}

func (e *recordingEngine) Version() string             { return e.inner.Version() }
func (e *recordingEngine) ConfigJSON() ([]byte, error) { return e.inner.ConfigJSON() }

type finishCall struct {
	status  string
	summary string
}

type fakeStore struct {
	mu             sync.Mutex
	tokens         []domain.Candidate
	snapshots      []domain.MarketSnapshot
	finishes       []finishCall
	history        []domain.MarketSnapshot
	historyCutoffs []time.Time
	insertErr      error
	finishErr      error
	upsertErr      error
	historyErr     error
	versions       []string
	coverages      []domain.DiscoveryCoverage
	nextFastID     int64
	currentFast    map[int64]bool
	deepSnapshots  []domain.MarketSnapshot
	deepStatuses   map[int64]domain.DeepStatus
	pendingDeep    []domain.DeepTask
	momentumStates []domain.MomentumState
	activeScores   []domain.ScoreAssignment
	snapshotScores map[int64][]domain.ScoreAssignment
	fastScores     map[int64][]domain.VersionedScore
	deepScores     map[int64][]domain.VersionedScore
}

func (f *fakeStore) UpsertMomentumState(_ context.Context, state domain.MomentumState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.momentumStates = append(f.momentumStates, state)
	return nil
}

func (f *fakeStore) UpdateMomentumUniverseEvidence(context.Context, string, *time.Time, domain.MarketSnapshot, time.Time) error {
	return nil
}

func (f *fakeStore) StartScanRun(context.Context, string) (int64, error) { return 1, nil }

func (f *fakeStore) EnsureScoreVersion(_ context.Context, version string, _ []byte, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions = append(f.versions, version)
	return nil
}

func (f *fakeStore) BootstrapChampion(_ context.Context, version string, configJSON []byte, changedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.activeScores) == 0 {
		f.activeScores = []domain.ScoreAssignment{{Version: version, Role: domain.ScoreRoleChampion, ConfigJSON: append([]byte(nil), configJSON...), ChangedAt: changedAt}}
	}
	return nil
}

func (f *fakeStore) ActiveScoreAssignments(context.Context) ([]domain.ScoreAssignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneScoreAssignments(f.activeScores), nil
}

func (f *fakeStore) SnapshotScoreAssignments(_ context.Context, snapshotID int64) ([]domain.ScoreAssignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneScoreAssignments(f.snapshotScores[snapshotID]), nil
}

func (f *fakeStore) FinishScanRun(_ context.Context, _ int64, status string, _, _ int, summary string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishes = append(f.finishes, finishCall{status: status, summary: summary})
	return f.finishErr
}

func (f *fakeStore) UpsertToken(_ context.Context, candidate domain.Candidate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, candidate)
	return f.upsertErr
}

func (f *fakeStore) InsertDiscoveryCoverage(_ context.Context, _ int64, coverage domain.DiscoveryCoverage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.coverages = append(f.coverages, coverage)
	return nil
}

func (f *fakeStore) InsertFastSnapshot(_ context.Context, candidate domain.Candidate, snapshot domain.MarketSnapshot) (int64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, candidate)
	f.nextFastID++
	snapshot.ID = f.nextFastID
	f.snapshots = append(f.snapshots, snapshot)
	return f.nextFastID, nil
}

func (f *fakeStore) InsertFastScoredSnapshot(_ context.Context, candidate domain.Candidate, snapshot domain.MarketSnapshot, scores []domain.VersionedScore) (int64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, candidate)
	f.nextFastID++
	snapshot.ID = f.nextFastID
	f.snapshots = append(f.snapshots, snapshot)
	if f.snapshotScores == nil {
		f.snapshotScores = map[int64][]domain.ScoreAssignment{}
	}
	f.snapshotScores[f.nextFastID] = cloneScoreAssignments(f.activeScores)
	if f.fastScores == nil {
		f.fastScores = map[int64][]domain.VersionedScore{}
	}
	f.fastScores[f.nextFastID] = append([]domain.VersionedScore(nil), scores...)
	return f.nextFastID, nil
}

func (f *fakeStore) GetTokenFirstSeen(_ context.Context, tokenID string) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, candidate := range f.tokens {
		if candidate.ID == tokenID {
			return candidate.DetectedAt, true, nil
		}
	}
	return time.Time{}, false, nil
}

func (f *fakeStore) IsCurrentFastSnapshot(_ context.Context, _ string, fastID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.currentFast == nil {
		return true, nil
	}
	return f.currentFast[fastID], nil
}

func (f *fakeStore) InsertDeepSnapshotIfCurrent(_ context.Context, fastID int64, snapshot domain.MarketSnapshot) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.currentFast != nil && !f.currentFast[fastID] {
		return false, nil
	}
	snapshot.Stage = domain.SnapshotStageDeep
	snapshot.BaseFastSnapshotID = &fastID
	f.deepSnapshots = append(f.deepSnapshots, snapshot)
	return true, nil
}

func (f *fakeStore) InsertDeepScoredSnapshotIfCurrent(_ context.Context, fastID int64, snapshot domain.MarketSnapshot, scores []domain.VersionedScore) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.currentFast != nil && !f.currentFast[fastID] {
		return false, nil
	}
	snapshot.Stage = domain.SnapshotStageDeep
	snapshot.BaseFastSnapshotID = &fastID
	f.deepSnapshots = append(f.deepSnapshots, snapshot)
	if f.deepScores == nil {
		f.deepScores = map[int64][]domain.VersionedScore{}
	}
	f.deepScores[fastID] = append([]domain.VersionedScore(nil), scores...)
	return true, nil
}

func (f *fakeStore) SetDeepStatus(_ context.Context, fastID int64, status domain.DeepStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deepStatuses == nil {
		f.deepStatuses = map[int64]domain.DeepStatus{}
	}
	f.deepStatuses[fastID] = status
	return nil
}

func (f *fakeStore) ListPendingDeep(context.Context, int) ([]domain.DeepTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.DeepTask(nil), f.pendingDeep...), nil
}

func (f *fakeStore) GetRecentSnapshots(context.Context, string, int) ([]domain.MarketSnapshot, error) {
	return append([]domain.MarketSnapshot(nil), f.history...), f.historyErr
}

func (f *fakeStore) GetRecentSnapshotsBefore(_ context.Context, _ string, cutoff time.Time, _ int) ([]domain.MarketSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.historyCutoffs = append(f.historyCutoffs, cutoff)
	return append([]domain.MarketSnapshot(nil), f.history...), f.historyErr
}

func cloneScoreAssignments(assignments []domain.ScoreAssignment) []domain.ScoreAssignment {
	cloned := make([]domain.ScoreAssignment, len(assignments))
	for index, assignment := range assignments {
		cloned[index] = assignment
		cloned[index].ConfigJSON = append([]byte(nil), assignment.ConfigJSON...)
	}
	return cloned
}
