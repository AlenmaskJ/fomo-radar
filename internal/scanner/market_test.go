package scanner

import (
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestMergeMarketKeepsGeckoCandidateWhenDexFails(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	discoveryCreated := now.Add(-5 * time.Minute)
	gecko := usableMarket(now.Add(-time.Second))
	gecko.PairAddress = "DiscoveryPool"
	gecko.Volume24hUSD = freshFloat(9_000, now)
	gecko.AggregateBuyersM5 = freshInt(7, now)
	gecko.AggregateSellersM5 = freshInt(3, now)
	gecko.BuyersM5 = domain.DataValue[int]{Quality: domain.QualityMissing}
	gecko.SellersM5 = domain.DataValue[int]{Quality: domain.QualityMissing}
	candidate := domain.Candidate{
		ID: "solana:MintCase", Chain: domain.ChainSolana, Address: "MintCase",
		DiscoveryPoolAddress: "DiscoveryPool", DiscoveryPoolCreatedAt: &discoveryCreated,
		DiscoveryMarket: gecko,
	}

	got := MergeMarket(candidate, domain.MarketSnapshot{}, false, now)

	if got.MarketDataSource != domain.MarketDataSourceGecko || got.MarketDataQuality != domain.MarketDataQualityPartial {
		t.Fatalf("source/quality = %q/%q", got.MarketDataSource, got.MarketDataQuality)
	}
	if got.PairAddress != "DiscoveryPool" || got.DiscoveryPoolAddress != "DiscoveryPool" {
		t.Fatalf("pair addresses = %q/%q", got.PairAddress, got.DiscoveryPoolAddress)
	}
	if got.AggregateBuyersM5.Value != 7 || got.BuyersM5.Quality != domain.QualityMissing {
		t.Fatalf("participant semantics = aggregate %+v unique %+v", got.AggregateBuyersM5, got.BuyersM5)
	}
}

func TestMergeMarketKeepsMomentumTriggerPoolWhenDexFails(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	gecko := usableMarket(now)
	gecko.PairAddress = "MomentumPool"
	candidate := domain.Candidate{
		ID: "solana:MintCase", Chain: domain.ChainSolana, Address: "MintCase",
		Origin: domain.CandidateOriginMomentum, TriggerPoolAddress: "MomentumPool",
		DiscoveryMarket: gecko,
	}

	got := MergeMarket(candidate, domain.MarketSnapshot{}, false, now)

	if got.PairAddress != "MomentumPool" || got.TriggerPoolAddress != "MomentumPool" || got.DiscoveryPoolAddress != "" {
		t.Fatalf("Momentum pool lineage = %#v", got)
	}
}

func TestMergeMarketPrefersDexAndFallsBackToGeckoFields(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	discoveryCreated := now.Add(-5 * time.Minute)
	selectedCreated := now.Add(-24 * time.Hour)
	gecko := usableMarket(now.Add(-time.Second))
	gecko.PriceUSD = freshFloat(1, now)
	gecko.Volume24hUSD = freshFloat(9_000, now)
	gecko.AggregateBuyersM5 = freshInt(7, now)
	candidate := domain.Candidate{
		ID: "bsc:0xabc", Chain: domain.ChainBSC, Address: "0xabc",
		DiscoveryPoolAddress: "0xdiscovery", DiscoveryPoolCreatedAt: &discoveryCreated,
		DiscoveryMarket: gecko,
	}
	dex := usableMarket(now)
	dex.PairAddress = "0xselected"
	dex.PairCreatedAt = &selectedCreated
	dex.PriceUSD = freshFloat(2, now)
	dex.Volume24hUSD = domain.DataValue[float64]{Quality: domain.QualityMissing}

	got := MergeMarket(candidate, dex, true, now)

	if got.MarketDataSource != domain.MarketDataSourceMerged || got.MarketDataQuality != domain.MarketDataQualityFull {
		t.Fatalf("source/quality = %q/%q", got.MarketDataSource, got.MarketDataQuality)
	}
	if got.PriceUSD.Value != 2 || got.Volume24hUSD.Value != 9_000 {
		t.Fatalf("merge precedence = price %+v volume24 %+v", got.PriceUSD, got.Volume24hUSD)
	}
	if got.PairAddress != "0xselected" || got.DiscoveryPoolAddress != "0xdiscovery" || got.SelectedPairCreatedAt == nil || !got.SelectedPairCreatedAt.Equal(selectedCreated) {
		t.Fatalf("pair lineage = selected %q discovery %q created %v", got.PairAddress, got.DiscoveryPoolAddress, got.SelectedPairCreatedAt)
	}
	if got.AggregateBuyersM5.Value != 7 || got.BuyersM5.Quality != domain.QualityMissing {
		t.Fatalf("participant semantics = aggregate %+v unique %+v", got.AggregateBuyersM5, got.BuyersM5)
	}
}

func TestMergeMarketPreservesAllMomentumWindowsAndLineage(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	triggerCreated := now.Add(-42 * time.Hour)
	candidate := domain.Candidate{
		ID: "bsc:niu", Chain: domain.ChainBSC, Address: "0xniu",
		Origin:             domain.CandidateOriginMomentum,
		MomentumSources:    []domain.MomentumSource{domain.MomentumSourceGeckoTrending},
		TriggerPoolAddress: "0xtrigger", TriggerPoolCreatedAt: &triggerCreated,
		DiscoveryMarket: usableMarket(now.Add(-time.Second)),
	}
	dex := usableMarket(now)
	dex.PairAddress = "0xselected"
	dex.VolumeH6USD = freshFloat(60_000, now)
	dex.Volume24hUSD = freshFloat(240_000, now)
	dex.PriceChangeH1 = freshFloat(20, now)
	dex.PriceChangeH6 = freshFloat(50, now)
	dex.PriceChangeH24 = freshFloat(90, now)
	dex.BuysH1, dex.SellsH1 = freshInt(80, now), freshInt(20, now)
	dex.BuysH6, dex.SellsH6 = freshInt(300, now), freshInt(100, now)
	dex.BuysH24, dex.SellsH24 = freshInt(900, now), freshInt(400, now)

	got := MergeMarket(candidate, dex, true, now)

	if got.VolumeH6USD.Value != 60_000 || got.Volume24hUSD.Value != 240_000 || got.PriceChangeH24.Value != 90 {
		t.Fatalf("Momentum windows dropped: %#v", got)
	}
	if got.BuysH1.Value != 80 || got.SellsH6.Value != 100 || got.BuysH24.Value != 900 {
		t.Fatalf("transaction windows dropped: %#v", got)
	}
	if got.CandidateOrigin != domain.CandidateOriginMomentum || got.TriggerPoolAddress != "0xtrigger" || got.PairAddress != "0xselected" {
		t.Fatalf("Momentum lineage = %#v", got)
	}
}
