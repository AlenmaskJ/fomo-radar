package web

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/store"
	_ "modernc.org/sqlite"
)

func TestOpenSQLiteRepositoryEnforcesReadOnlyWAL(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fomo.db")
	writer, err := store.Open(databasePath)
	if err != nil {
		t.Fatalf("create Plan 1.1 database: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	repository, err := OpenSQLiteRepository(databasePath)
	if err != nil {
		t.Fatalf("OpenSQLiteRepository() error = %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })

	if _, err := repository.db.Exec(`CREATE TABLE forbidden (id INTEGER)`); err == nil {
		t.Fatal("read-only repository accepted CREATE")
	}
	if _, err := repository.db.Exec(`UPDATE tokens SET symbol = 'changed'`); err == nil {
		t.Fatal("read-only repository accepted UPDATE")
	}

	var queryOnly, busyTimeout int
	var journalMode string
	if err := repository.db.QueryRow(`PRAGMA query_only`).Scan(&queryOnly); err != nil {
		t.Fatalf("read query_only: %v", err)
	}
	if err := repository.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if err := repository.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if queryOnly != 1 || busyTimeout != 5000 || journalMode != "wal" {
		t.Fatalf("SQLite mode = query_only %d, busy_timeout %d, journal_mode %q; want 1, 5000, wal", queryOnly, busyTimeout, journalMode)
	}
}

func TestSQLiteRepository_MarketOpportunitiesReturnsLatestPublishedRun(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "market-opportunities.db")
	writer, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 9, 22, 5, 0, 0, 0, time.UTC)
	cryptoOpportunities := []domain.Opportunity{
		{Instrument: domain.MarketInstrument{InstrumentID: "BBB-USDT-SWAP", Symbol: "BBB"}, FOMOScore: 90, OpportunityScore: 70, Stage: domain.StageConfirmed},
		{Instrument: domain.MarketInstrument{InstrumentID: "AAA-USDT-SWAP", Symbol: "AAA"}, FOMOScore: 75, OpportunityScore: 88, Stage: domain.StageStarting},
	}
	publish := func(assetClass domain.AssetClass, at time.Time, poolSize int, opportunities []domain.Opportunity) int64 {
		t.Helper()
		id, err := writer.StartOpportunityRun(ctx, assetClass, "manual", at)
		if err != nil {
			t.Fatal(err)
		}
		run := domain.OpportunityRun{ID: id, AssetClass: assetClass, StartedAt: at, FinishedAt: at.Add(time.Minute), Status: domain.OpportunityRunCompleted, Trigger: "manual", PoolSize: poolSize}
		if err := writer.FinishOpportunityRun(ctx, run, opportunities); err != nil {
			t.Fatal(err)
		}
		return id
	}
	cryptoID := publish(domain.AssetClassCrypto, startedAt, 42, cryptoOpportunities)
	stockID := publish(domain.AssetClassStock, startedAt.Add(2*time.Minute), 31, []domain.Opportunity{{
		Instrument: domain.MarketInstrument{InstrumentID: "AAPL-USDT-SWAP", Symbol: "AAPL"}, FOMOScore: 82, OpportunityScore: 91, Stage: domain.StageConfirmed,
	}})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err := OpenSQLiteRepository(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	crypto, err := repository.MarketOpportunities(ctx, domain.AssetClassCrypto)
	if err != nil {
		t.Fatal(err)
	}
	stock, err := repository.MarketOpportunities(ctx, domain.AssetClassStock)
	if err != nil {
		t.Fatal(err)
	}
	if crypto.Run.ID != cryptoID || crypto.Run.AssetClass != domain.AssetClassCrypto || crypto.Run.PoolSize != 42 || len(crypto.Opportunities) != 2 || crypto.Opportunities[0].Instrument.Symbol != "AAA" {
		t.Fatalf("crypto report = %+v", crypto)
	}
	if stock.Run.ID != stockID || stock.Run.AssetClass != domain.AssetClassStock || stock.Run.PoolSize != 31 || len(stock.Opportunities) != 1 || stock.Opportunities[0].Instrument.Symbol != "AAPL" {
		t.Fatalf("stock report = %+v", stock)
	}
	if _, err := repository.MarketOpportunities(ctx, domain.AssetClass("forex")); err == nil {
		t.Fatal("unknown asset class should be rejected")
	}
}

func TestOpenSQLiteRepositoryRejectsMissingPlan11Schema(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "incomplete.db")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open incomplete database: %v", err)
	}
	if _, err := database.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	if _, err := database.Exec(`CREATE TABLE tokens (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create incomplete tokens: %v", err)
	}
	if _, err := database.Exec(`CREATE TABLE snapshots (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create incomplete snapshots: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close incomplete database: %v", err)
	}

	repository, err := OpenSQLiteRepository(databasePath)
	if repository != nil {
		_ = repository.Close()
	}
	if err == nil {
		t.Fatal("OpenSQLiteRepository() accepted a database without Plan 1.1 columns")
	}
}

func TestSQLiteRepository_CurrentGenerationIgnoresOlderDeep(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "fomo.db")
	writer := openWriter(t, databasePath)
	base := time.Date(2026, time.August, 22, 6, 0, 0, 0, time.UTC)
	candidate := testCandidate("bsc:0xcurrent", domain.ChainBSC, "0xcurrent", "CURRENT", base)

	oldFastID, err := writer.InsertFastSnapshot(ctx, candidate, testSnapshot(base, 48, domain.TierWatch, domain.DeepStatusQueued, 48000))
	if err != nil {
		t.Fatalf("insert old Fast: %v", err)
	}
	inserted, err := writer.InsertDeepSnapshotIfCurrent(ctx, oldFastID, testSnapshot(base.Add(time.Minute), 99, domain.TierBreakout, domain.DeepStatusCompleted, 99000))
	if err != nil || !inserted {
		t.Fatalf("insert old Deep = %v, %v", inserted, err)
	}
	newFastID, err := writer.InsertFastSnapshot(ctx, candidate, testSnapshot(base.Add(2*time.Minute), 71, domain.TierFastRising, domain.DeepStatusDeferred, 71000))
	if err != nil {
		t.Fatalf("insert current Fast: %v", err)
	}
	if newFastID == oldFastID {
		t.Fatal("expected a new Fast generation")
	}

	repository := openReader(t, databasePath)
	tokens, err := repository.ListTokens(ctx, TokenFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListTokens() error = %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("ListTokens() len = %d, want 1", len(tokens))
	}
	got := tokens[0]
	if got.SnapshotID != newFastID || got.Stage != "FAST" || got.RawScore == nil || *got.RawScore != 71 {
		t.Fatalf("current token = %+v, want current Fast score 71", got)
	}
	if got.DeepStatus != "deferred" {
		t.Fatalf("DeepStatus = %q, want deferred preserved for UI", got.DeepStatus)
	}
}

func TestSQLiteRepository_ListTokensUsesMatchingDeepAndTierOrder(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "fomo.db")
	writer := openWriter(t, databasePath)
	base := time.Date(2026, time.August, 22, 7, 0, 0, 0, time.UTC)

	breakout := testCandidate("solana:breakout", domain.ChainSolana, "breakout", "BREAK", base)
	breakoutFast, err := writer.InsertFastSnapshot(ctx, breakout, testSnapshot(base, 82, domain.TierFastRising, domain.DeepStatusQueued, 82000))
	if err != nil {
		t.Fatalf("insert breakout Fast: %v", err)
	}
	inserted, err := writer.InsertDeepSnapshotIfCurrent(ctx, breakoutFast, testSnapshot(base.Add(time.Minute), 91, domain.TierBreakout, domain.DeepStatusCompleted, 91000))
	if err != nil || !inserted {
		t.Fatalf("insert matching Deep = %v, %v", inserted, err)
	}

	fastRising := testCandidate("bsc:fast", domain.ChainBSC, "0xfast", "FAST", base)
	if _, err := writer.InsertFastSnapshot(ctx, fastRising, testSnapshot(base.Add(2*time.Minute), 76, domain.TierFastRising, domain.DeepStatusFailed, 76000)); err != nil {
		t.Fatalf("insert Fast Rising: %v", err)
	}

	watch := testCandidate("bsc:watch", domain.ChainBSC, "0xwatch", "WATCH", base)
	missingMarketCap := testSnapshot(base.Add(3*time.Minute), 61, domain.TierWatch, domain.DeepStatusPending, 0)
	missingMarketCap.MarketCapUSD = domain.DataValue[float64]{Quality: domain.QualityMissing, CollectedAt: missingMarketCap.CollectedAt}
	if _, err := writer.InsertFastSnapshot(ctx, watch, missingMarketCap); err != nil {
		t.Fatalf("insert Watch: %v", err)
	}

	hidden := testCandidate("bsc:hidden", domain.ChainBSC, "0xhidden", "HIDDEN", base)
	if _, err := writer.InsertFastSnapshot(ctx, hidden, testSnapshot(base.Add(4*time.Minute), 20, domain.TierHidden, domain.DeepStatusNotRequired, 20000)); err != nil {
		t.Fatalf("insert Hidden: %v", err)
	}

	repository := openReader(t, databasePath)
	tokens, err := repository.ListRadar(ctx, 30)
	if err != nil {
		t.Fatalf("ListTokens() error = %v", err)
	}
	if len(tokens) != 3 {
		t.Fatalf("ListTokens() len = %d, want 3 visible tokens", len(tokens))
	}
	wantTiers := []string{"BREAKOUT", "FAST_RISING", "WATCH"}
	for i, want := range wantTiers {
		if tokens[i].EffectiveTier != want {
			t.Fatalf("token[%d].EffectiveTier = %q, want %q", i, tokens[i].EffectiveTier, want)
		}
	}
	if tokens[0].Stage != "DEEP" || tokens[0].RawScore == nil || *tokens[0].RawScore != 91 || tokens[0].DeepStatus != "completed" {
		t.Fatalf("matching Deep token = %+v", tokens[0])
	}
	if tokens[1].DeepStatus != "failed" {
		t.Fatalf("failed Fast DeepStatus = %q, want failed", tokens[1].DeepStatus)
	}
	if tokens[2].MarketCapUSD != nil {
		t.Fatalf("missing market cap = %v, want nil", *tokens[2].MarketCapUSD)
	}
}

func TestSQLiteRepository_MarketRadarUsesLatestAndPreviousMainstreamSnapshots(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "fomo.db")
	writer := openWriter(t, databasePath)
	base := time.Date(2026, time.September, 21, 8, 0, 0, 0, time.UTC)

	btcBefore := derivativesSnapshot("BTC", base, 60_000, 2, 100_000_000)
	btcCurrent := derivativesSnapshot("BTC", base.Add(2*time.Minute), 61_800, 4, 110_000_000)
	eth := derivativesSnapshot("ETH", base.Add(2*time.Minute), 2_400, -1.5, 50_000_000)
	doge := derivativesSnapshot("DOGE", base.Add(2*time.Minute), 0.2, 9, 30_000_000)
	stock := derivativesSnapshot("AAPL", base.Add(2*time.Minute), 230, 3, 20_000_000)
	stock.AssetClass = domain.AssetClassStock
	if err := writer.InsertDerivativesSnapshots(ctx, []domain.DerivativesSnapshot{btcBefore, btcCurrent, eth, doge, stock}); err != nil {
		t.Fatalf("InsertDerivativesSnapshots() error = %v", err)
	}

	repository := openReader(t, databasePath)
	report, err := repository.MarketRadar(ctx)
	if err != nil {
		t.Fatalf("MarketRadar() error = %v", err)
	}
	if len(report.Crypto) != 2 {
		t.Fatalf("MarketRadar() symbols = %+v, want BTC and ETH only", report.Crypto)
	}
	if report.Crypto[0].Symbol != "BTC" || report.Crypto[1].Symbol != "ETH" {
		t.Fatalf("MarketRadar() order = %s, %s; want BTC, ETH", report.Crypto[0].Symbol, report.Crypto[1].Symbol)
	}
	if report.Crypto[0].OpenInterestChangePct == nil || math.Abs(*report.Crypto[0].OpenInterestChangePct-10) > 0.000001 {
		if report.Crypto[0].OpenInterestChangePct == nil {
			t.Fatal("BTC open interest change = nil, want 10%")
		}
		t.Fatalf("BTC open interest change = %.17f, want 10%%", *report.Crypto[0].OpenInterestChangePct)
	}
	if report.Crypto[0].PriceChangePct == nil || math.Abs(*report.Crypto[0].PriceChangePct-3) > 0.000001 {
		t.Fatalf("BTC short price change = %v, want 3%%", report.Crypto[0].PriceChangePct)
	}
	if report.Crypto[0].CollectedAt != btcCurrent.CollectedAt {
		t.Fatalf("BTC collected at = %s, want latest %s", report.Crypto[0].CollectedAt, btcCurrent.CollectedAt)
	}
}

func TestSQLiteRepository_TokenByAddressPreservesStoredEvidenceAndTimeline(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "fomo.db")
	writer := openWriter(t, databasePath)
	base := time.Date(2026, time.August, 22, 8, 0, 0, 0, time.UTC)
	candidate := testCandidate("bsc:0xabc", domain.ChainBSC, "0xAbC", "牛来", base)
	current := testSnapshot(base.Add(3*time.Minute), 88, domain.TierBreakout, domain.DeepStatusCompleted, 120000)
	current.ScoreBreakdown.AgeBonus = 10
	current.ScoreBreakdown.RiskPenalty = 3
	current.ScoreBreakdown.RiskFlags = []string{"EARLY_LIQUIDITY"}
	current.ScoreBreakdown.Factors = map[string]domain.FactorScore{
		"buyer_velocity":  {Points: 21, Maximum: 25, Available: true},
		"social_momentum": {Maximum: 15, Available: false},
	}
	current.RiskFlags = []string{"EARLY_LIQUIDITY"}
	if _, err := writer.InsertFastSnapshot(ctx, candidate, current); err != nil {
		t.Fatalf("insert current Fast: %v", err)
	}
	for index, score := range []float64{61, 74} {
		snapshot := testSnapshot(base.Add(time.Duration(index+1)*time.Minute), score, domain.TierWatch, domain.DeepStatusPending, 60000+score*100)
		snapshot.TokenID = candidate.ID
		snapshot.Stage = domain.SnapshotStageFast
		if _, err := writer.InsertSnapshot(ctx, snapshot); err != nil {
			t.Fatalf("insert timeline snapshot %d: %v", index, err)
		}
	}

	repository := openReader(t, databasePath)
	detail, err := repository.TokenByAddress(ctx, "0xABC", "", 2)
	if err != nil {
		t.Fatalf("TokenByAddress() error = %v", err)
	}
	if detail.Token.Name != "牛来 name" || detail.Token.RawScore == nil || *detail.Token.RawScore != 88 {
		t.Fatalf("detail current token = %+v", detail.Token)
	}
	buyer := detail.Token.Factors["buyer_velocity"]
	if !buyer.Available || buyer.Points == nil || *buyer.Points != 21 {
		t.Fatalf("buyer factor = %+v, want stored 21/25", buyer)
	}
	social := detail.Token.Factors["social_momentum"]
	if social.Available || social.Points != nil {
		t.Fatalf("social factor = %+v, want unavailable with nil points", social)
	}
	if len(detail.Timeline) != 2 {
		t.Fatalf("timeline len = %d, want bounded 2", len(detail.Timeline))
	}
	if detail.Timeline[0].RawScore == nil || *detail.Timeline[0].RawScore != 74 || detail.Timeline[1].RawScore == nil || *detail.Timeline[1].RawScore != 88 {
		t.Fatalf("timeline scores = %+v, want stored [74,88] oldest-to-newest", detail.Timeline)
	}
}

func TestSQLiteRepository_TokenByAddressHonorsChainCaseAndAmbiguity(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "fomo.db")
	writer := openWriter(t, databasePath)
	base := time.Date(2026, time.August, 22, 9, 0, 0, 0, time.UTC)
	for _, candidate := range []domain.Candidate{
		testCandidate("bsc:shared", domain.ChainBSC, "SharedAddress", "BSC", base),
		testCandidate("solana:shared", domain.ChainSolana, "SharedAddress", "SOL", base),
		testCandidate("solana:case", domain.ChainSolana, "MintCase", "CASE", base),
	} {
		if _, err := writer.InsertFastSnapshot(ctx, candidate, testSnapshot(base, 65, domain.TierWatch, domain.DeepStatusPending, 65000)); err != nil {
			t.Fatalf("insert %s: %v", candidate.ID, err)
		}
	}

	repository := openReader(t, databasePath)
	if _, err := repository.TokenByAddress(ctx, "SharedAddress", "", 10); !errors.Is(err, ErrAmbiguousToken) {
		t.Fatalf("ambiguous lookup error = %v, want ErrAmbiguousToken", err)
	}
	bsc, err := repository.TokenByAddress(ctx, "sharedaddress", "bsc", 10)
	if err != nil || bsc.Token.Chain != "bsc" {
		t.Fatalf("BSC case-insensitive lookup = %+v, %v", bsc, err)
	}
	if _, err := repository.TokenByAddress(ctx, "mintcase", "solana", 10); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("Solana wrong-case lookup error = %v, want ErrTokenNotFound", err)
	}
	if _, err := repository.TokenByAddress(ctx, "missing", "", 10); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("missing lookup error = %v, want ErrTokenNotFound", err)
	}
}

func openWriter(t *testing.T, path string) *store.Store {
	t.Helper()
	writer, err := store.Open(path)
	if err != nil {
		t.Fatalf("create Plan 1.1 database: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	return writer
}

func openReader(t *testing.T, path string) *SQLiteRepository {
	t.Helper()
	repository, err := OpenSQLiteRepository(path)
	if err != nil {
		t.Fatalf("OpenSQLiteRepository() error = %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	return repository
}

func testCandidate(id string, chain domain.Chain, address, symbol string, detectedAt time.Time) domain.Candidate {
	return domain.Candidate{
		ID: id, Chain: chain, Address: address, Name: symbol + " name", Symbol: symbol,
		PairAddress: "pair-" + address, DetectedAt: detectedAt,
	}
}

func testSnapshot(collectedAt time.Time, score float64, tier domain.Tier, deepStatus domain.DeepStatus, marketCap float64) domain.MarketSnapshot {
	createdAt := collectedAt.Add(-5 * time.Minute)
	return domain.MarketSnapshot{
		PairAddress: "pair", DiscoveryPoolAddress: "pool", DiscoveryPoolCreatedAt: &createdAt,
		SelectedPairCreatedAt: &createdAt, SignalFirstSeenAt: createdAt, CollectedAt: collectedAt,
		DeepStatus: deepStatus, LaunchType: domain.LaunchTypeNewLaunch,
		MarketDataSource: domain.MarketDataSourceMerged, MarketDataQuality: domain.MarketDataQualityFull,
		MarketCapUSD: testFloat(marketCap, collectedAt), LiquidityUSD: testFloat(30000, collectedAt),
		Score: testFloat(score, collectedAt), Tier: string(tier), ScoreVersion: "FOMO_SCORE_V1.0",
		ScoreBreakdown: domain.ScoreBreakdown{
			Version: "FOMO_SCORE_V1.0", Final: score, Tier: tier, RawScore: score, RawTier: tier,
			EvidenceAvailable: 5, EvidenceTotal: 5, EvidenceConfidence: domain.EvidenceConfidenceHigh,
			EffectiveTier: tier, Factors: map[string]domain.FactorScore{},
		},
		RiskFlags: []string{}, DataQuality: map[string]domain.Quality{
			"market_cap_usd": domain.QualityFresh, "liquidity_usd": domain.QualityFresh, "score": domain.QualityFresh,
		},
	}
}

func testFloat(value float64, collectedAt time.Time) domain.DataValue[float64] {
	return domain.DataValue[float64]{Value: value, Quality: domain.QualityFresh, CollectedAt: collectedAt}
}

func derivativesSnapshot(symbol string, collectedAt time.Time, price, change, openInterest float64) domain.DerivativesSnapshot {
	return domain.DerivativesSnapshot{
		AssetClass: domain.AssetClassCrypto, InstrumentID: symbol + "-USDT-SWAP", Symbol: symbol,
		CollectedAt: collectedAt, SourceTime: collectedAt, PriceUSD: price,
		Change24hPct: change, Turnover24hUSD: 1_000_000_000,
		OpenInterestUSD: openInterest, OpenInterestAvailable: true,
		FundingRate: 0.0001, FundingRateAvailable: true,
	}
}
