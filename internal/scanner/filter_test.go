package scanner

import (
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/config"
	"github.com/alen1/fomo-radar/internal/domain"
)

func TestFastFilterRejectsCandidateOlderThanSixHours(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	created := now.Add(-7 * time.Hour)

	got := FastFilter(domain.Candidate{ChainCreatedAt: &created}, domain.MarketSnapshot{}, now, filterConfig())

	if got.Accepted || got.RejectReason != RejectTooOld {
		t.Fatalf("FastFilter() = %+v, want rejection %q", got, RejectTooOld)
	}
}

func TestFastFilterDoesNotRejectOrDeprioritizeOldMomentumCandidate(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	created := now.Add(-42 * time.Hour)
	market := usableMarket(now)
	market.MarketCapUSD = freshFloat(900_000, now)
	candidate := domain.Candidate{
		ID: "bsc:niu", Chain: domain.ChainBSC,
		Address: "0xbeea1d618e533a387d941f58a7d4c9b7bd377777",
		Name:    "牛来", Symbol: "牛来", Origin: domain.CandidateOriginMomentum,
		TriggerPoolCreatedAt: &created,
	}
	market.PairCreatedAt = &created
	market.SelectedPairCreatedAt = &created

	got := FastFilter(candidate, market, now, filterConfig())

	if !got.Accepted || got.RejectReason != "" || got.Deprioritized {
		t.Fatalf("Momentum FastFilter() = %+v, want accepted without old-token deprioritization", got)
	}
}

func TestFastFilterKeepsLowLiquiditySafetyRuleForMomentum(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	created := now.Add(-42 * time.Hour)
	market := usableMarket(now)
	market.LiquidityUSD = freshFloat(1_999, now)

	got := FastFilter(domain.Candidate{Origin: domain.CandidateOriginBoth, TriggerPoolCreatedAt: &created}, market, now, filterConfig())

	if got.Accepted || got.RejectReason != RejectLowLiquidity {
		t.Fatalf("Momentum FastFilter() = %+v, want low-liquidity rejection", got)
	}
}

func TestFastFilterClassifiesNinetyMinuteCandidateAsEarlyPool(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	created := now.Add(-90 * time.Minute)

	got := FastFilter(domain.Candidate{ChainCreatedAt: &created}, usableMarket(now), now, filterConfig())

	if !got.Accepted || got.AgeBand != AgeEarlyPool {
		t.Fatalf("FastFilter() = %+v, want accepted %q", got, AgeEarlyPool)
	}
}

func TestFastFilterClassifiesThreeHourCandidateAsEarlyLate(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	created := now.Add(-3 * time.Hour)

	got := FastFilter(domain.Candidate{ChainCreatedAt: &created}, usableMarket(now), now, filterConfig())

	if !got.Accepted || got.AgeBand != AgeEarlyLate {
		t.Fatalf("FastFilter() = %+v, want accepted %q", got, AgeEarlyLate)
	}
}

func TestFastFilterDeprioritizesHighMarketCapWithoutRejecting(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	market := usableMarket(now)
	market.MarketCapUSD = freshFloat(700_000, now)

	got := FastFilter(domain.Candidate{}, market, now, filterConfig())

	if !got.Accepted || !got.Deprioritized {
		t.Fatalf("FastFilter() = %+v, want accepted but deprioritized", got)
	}
}

func TestFastFilterRejectsKnownLiquidityBelowTwoThousand(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	market := usableMarket(now)
	market.LiquidityUSD = freshFloat(1_999, now)

	got := FastFilter(domain.Candidate{}, market, now, filterConfig())

	if got.Accepted || got.RejectReason != RejectLowLiquidity {
		t.Fatalf("FastFilter() = %+v, want rejection %q", got, RejectLowLiquidity)
	}
}

func TestFastFilterRetainsMissingLiquidityAndMarketCap(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	market := usableMarket(now)
	market.LiquidityUSD = domain.DataValue[float64]{Quality: domain.QualityMissing}
	market.MarketCapUSD = domain.DataValue[float64]{Quality: domain.QualityMissing}

	got := FastFilter(domain.Candidate{}, market, now, filterConfig())

	if !got.Accepted || got.Deprioritized || got.MarketCapQuality != domain.QualityMissing {
		t.Fatalf("FastFilter() = %+v, want retained missing market cap", got)
	}
}

func TestFastFilterPrefersRecentDiscoveryPoolOverOldSelectedPair(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	discoveryCreated := now.Add(-34 * time.Minute)
	selectedCreated := now.Add(-38 * time.Hour)
	candidate := domain.Candidate{DiscoveryPoolCreatedAt: &discoveryCreated}
	market := usableMarket(now)
	market.PairCreatedAt = &selectedCreated
	market.SelectedPairCreatedAt = &selectedCreated

	got := FastFilter(candidate, market, now, filterConfig())

	if !got.Accepted || got.AgeBand != AgeEarlyPool {
		t.Fatalf("FastFilter() = %+v, want recent discovery pool accepted", got)
	}
}

func TestClassifyLaunchExplainsGoldenReactivation(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	discoveryCreated := now.Add(-34 * time.Minute)
	selectedCreated := now.Add(-38 * time.Hour)
	candidate := domain.Candidate{DiscoveryPoolCreatedAt: &discoveryCreated}
	market := usableMarket(now)
	market.SelectedPairCreatedAt = &selectedCreated

	launchType, reason := ClassifyLaunch(candidate, market, TokenState{}, now, filterConfig())

	if launchType != domain.LaunchTypeReactivation || !strings.Contains(reason, "older selected pair") {
		t.Fatalf("classification = %q (%q), want explained REACTIVATION", launchType, reason)
	}
}

func TestCalculatePreliminaryUsesOnlyCheapEvidence(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	created := now.Add(-45 * time.Minute)
	market := usableMarket(now)
	market.VolumeM5USD = freshFloat(2_000, now)
	market.VolumeH1USD = freshFloat(6_000, now)
	market.BuysM5 = freshInt(18, now)
	market.SellsM5 = freshInt(6, now)

	got := CalculatePreliminary(domain.Candidate{ChainCreatedAt: &created}, market, now)

	if got.Total < 40 {
		t.Fatalf("CalculatePreliminary().Total = %v, want deep-scan eligible", got.Total)
	}
	if market.Score.Quality != "" || market.Score.Value != 0 {
		t.Fatalf("preliminary score mutated final score field: %+v", market.Score)
	}
}

func TestTokenIDNormalizesOnlyBSCAddresses(t *testing.T) {
	if got := TokenID(domain.ChainBSC, "0xAbC"); got != "bsc:0xabc" {
		t.Fatalf("BSC TokenID() = %q, want bsc:0xabc", got)
	}
	if got := TokenID(domain.ChainSolana, "AbCd"); got != "solana:AbCd" {
		t.Fatalf("Solana TokenID() = %q, want case preserved", got)
	}
}

func filterConfig() config.Config {
	return config.Config{
		CandidateMaxAge:   6 * time.Hour,
		EarlyMaxAge:       2 * time.Hour,
		PreferredMaxMCUSD: 500_000,
	}
}

func usableMarket(at time.Time) domain.MarketSnapshot {
	return domain.MarketSnapshot{
		MarketCapUSD: freshFloat(100_000, at),
		LiquidityUSD: freshFloat(10_000, at),
	}
}

func freshFloat(value float64, at time.Time) domain.DataValue[float64] {
	return domain.DataValue[float64]{Value: value, Quality: domain.QualityFresh, CollectedAt: at}
}

func freshInt(value int, at time.Time) domain.DataValue[int] {
	return domain.DataValue[int]{Value: value, Quality: domain.QualityFresh, CollectedAt: at}
}
