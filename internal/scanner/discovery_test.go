package scanner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestMergeDiscoveredCandidatesPreservesLaunchAndMomentumLineage(t *testing.T) {
	now := time.Date(2026, 8, 23, 5, 0, 0, 0, time.UTC)
	launchAt := now.Add(-10 * time.Minute)
	triggerAt := now.Add(-42 * time.Hour)
	launch := domain.Candidate{
		ID: "bsc:0xbeef", Chain: domain.ChainBSC, Address: "0xBEEF", Name: "牛来", Symbol: "牛🚀",
		Origin: domain.CandidateOriginLaunch, DiscoveryPoolAddress: "0xlaunch",
		DiscoveryPoolCreatedAt: &launchAt, DetectedAt: now,
	}
	launch.DiscoveryMarket = domain.MarketSnapshot{
		MarketDataSource: domain.MarketDataSourceGecko, MarketDataQuality: domain.MarketDataQualityPartial,
		PriceUSD: domain.DataValue[float64]{Value: 1.1, Quality: domain.QualityFresh, CollectedAt: now},
	}
	momentum := domain.Candidate{
		ID: "bsc:0xbeef", Chain: domain.ChainBSC, Address: "0xbeef", Name: "牛来", Symbol: "牛🚀",
		Origin: domain.CandidateOriginMomentum, PairAddress: "0xselected",
		TriggerPoolAddress: "0xtrigger", TriggerPoolCreatedAt: &triggerAt,
		MomentumSources: []domain.MomentumSource{domain.MomentumSourceLocalUniverse, domain.MomentumSourceGeckoTrending, domain.MomentumSourceLocalUniverse},
		DetectedAt:      now,
	}
	momentum.DiscoveryMarket = domain.MarketSnapshot{
		PairAddress: "0xselected", MarketDataSource: domain.MarketDataSourceDex,
		MarketDataQuality: domain.MarketDataQualityFull,
		PriceUSD:          domain.DataValue[float64]{Value: 1.2, Quality: domain.QualityFresh, CollectedAt: now},
	}

	got := MergeDiscoveredCandidates([]domain.Candidate{launch, momentum})
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	c := got[0]
	if c.ID != "bsc:0xbeef" || c.Address != "0xbeef" || c.Origin != domain.CandidateOriginBoth {
		t.Fatalf("identity/origin = %#v", c)
	}
	if c.DiscoveryPoolAddress != "0xlaunch" || c.TriggerPoolAddress != "0xtrigger" || c.PairAddress != "0xselected" {
		t.Fatalf("pool lineage = %#v", c)
	}
	if c.Name != "牛来" || c.Symbol != "牛🚀" {
		t.Fatalf("Unicode changed: %#v", c)
	}
	if len(c.MomentumSources) != 2 || c.MomentumSources[0] != domain.MomentumSourceGeckoTrending || c.MomentumSources[1] != domain.MomentumSourceLocalUniverse {
		t.Fatalf("sources = %#v", c.MomentumSources)
	}
	if c.DiscoveryMarket.MarketDataSource != domain.MarketDataSourceDex || c.DiscoveryMarket.PriceUSD.Value != 1.2 {
		t.Fatalf("Dex batch evidence lost: %#v", c.DiscoveryMarket)
	}
}

func TestMergeDiscoveredCandidatesPreservesSolanaCase(t *testing.T) {
	got := MergeDiscoveredCandidates([]domain.Candidate{
		{Chain: domain.ChainSolana, Address: "MintCase", Origin: domain.CandidateOriginMomentum},
		{Chain: domain.ChainSolana, Address: "mintcase", Origin: domain.CandidateOriginMomentum},
	})
	if len(got) != 2 || got[0].Address == got[1].Address {
		t.Fatalf("Solana candidates = %#v", got)
	}
}

func TestDiscoveryCoordinatorIsolatesSourcesPersistsReportsAndAdvancesOnlySuccessfulCursor(t *testing.T) {
	now := time.Date(2026, 8, 23, 5, 10, 0, 0, time.UTC)
	launch := &fakeLaunchSource{
		batches: map[domain.Chain]domain.DiscoveryBatch{
			domain.ChainBSC: {
				Candidates: []domain.Candidate{{Chain: domain.ChainBSC, Address: "0xNIU", Name: "牛来", Symbol: "牛来", DiscoveryPoolAddress: "0xlaunch"}},
				Coverage:   domain.DiscoveryCoverage{Chain: domain.ChainBSC, ScanStartedAt: now, UniqueCandidates: 1},
			},
		},
		errors: map[domain.Chain]error{domain.ChainSolana: errors.New("launch unavailable")},
	}
	gecko := &fakeGeckoMomentumSource{results: map[domain.Chain]domain.DiscoveryResult{
		domain.ChainBSC: {
			Candidates: []domain.Candidate{{ID: "bsc:0xniu", Chain: domain.ChainBSC, Address: "0xniu", Name: "牛来", Symbol: "牛来", Origin: domain.CandidateOriginMomentum, TriggerPoolAddress: "0xtrigger", MomentumSources: []domain.MomentumSource{domain.MomentumSourceGeckoTrending}}},
			Reports:    []domain.DiscoverySourceReport{{Chain: domain.ChainBSC, Provider: "geckoterminal", Source: string(domain.MomentumSourceGeckoTrending), StartedAt: now, FinishedAt: now}},
		},
		domain.ChainSolana: {
			Candidates: []domain.Candidate{{ID: "solana:Mint", Chain: domain.ChainSolana, Address: "Mint", Origin: domain.CandidateOriginMomentum}},
			Reports:    []domain.DiscoverySourceReport{{Chain: domain.ChainSolana, Provider: "geckoterminal", Source: string(domain.MomentumSourceGeckoTrending), StartedAt: now, FinishedAt: now}},
		},
	}}
	dex := &fakeDexMomentumSource{
		boosts: domain.DiscoveryResult{Reports: []domain.DiscoverySourceReport{
			{Chain: domain.ChainBSC, Provider: "dexscreener", Source: string(domain.MomentumSourceDexBoostTop), StartedAt: now, FinishedAt: now},
			{Chain: domain.ChainSolana, Provider: "dexscreener", Source: string(domain.MomentumSourceDexBoostTop), StartedAt: now, FinishedAt: now},
		}},
		batches: map[domain.Chain]domain.DiscoveryResult{
			domain.ChainBSC: {
				Candidates: []domain.Candidate{{ID: "bsc:0xniu", Chain: domain.ChainBSC, Address: "0xniu", Origin: domain.CandidateOriginMomentum, PairAddress: "0xselected", MomentumSources: []domain.MomentumSource{domain.MomentumSourceLocalUniverse}}},
				Reports:    []domain.DiscoverySourceReport{{Chain: domain.ChainBSC, Provider: "dexscreener", Source: string(domain.MomentumSourceLocalUniverse), StartedAt: now, FinishedAt: now, CursorAfter: "bsc:0xniu"}},
			},
			domain.ChainSolana: {
				Errors:  []string{"solana dex batch unavailable"},
				Reports: []domain.DiscoverySourceReport{{Chain: domain.ChainSolana, Provider: "dexscreener", Source: string(domain.MomentumSourceLocalUniverse), StartedAt: now, FinishedAt: now, CursorAfter: "solana:Mint", Error: "unavailable"}},
			},
		},
	}
	store := &fakeDiscoveryStore{universe: map[domain.Chain][]domain.Candidate{
		domain.ChainBSC:    {{ID: "bsc:0xniu", Chain: domain.ChainBSC, Address: "0xniu"}},
		domain.ChainSolana: {{ID: "solana:Mint", Chain: domain.ChainSolana, Address: "Mint"}},
	}}
	coordinator := NewDiscoveryCoordinator(launch, gecko, dex, store, func() time.Time { return now })

	result := coordinator.Discover(context.Background(), 77)
	if dex.boostCalls != 1 {
		t.Fatalf("boost calls = %d", dex.boostCalls)
	}
	if len(result.Errors) < 2 {
		t.Fatalf("errors = %#v", result.Errors)
	}
	var niu *domain.Candidate
	for i := range result.Candidates {
		if result.Candidates[i].ID == "bsc:0xniu" {
			niu = &result.Candidates[i]
		}
	}
	if niu == nil || niu.Origin != domain.CandidateOriginBoth || niu.DiscoveryPoolAddress == "" || niu.TriggerPoolAddress != "0xtrigger" || niu.PairAddress != "0xselected" {
		t.Fatalf("merged 牛来 = %#v", niu)
	}
	if len(store.reports) != 8 {
		t.Fatalf("persisted reports = %d: %#v", len(store.reports), store.reports)
	}
	if len(store.cursors) != 1 || store.cursors[0].chain != domain.ChainBSC || store.cursors[0].value != "bsc:0xniu" {
		t.Fatalf("cursors = %#v", store.cursors)
	}
}

type fakeLaunchSource struct {
	batches map[domain.Chain]domain.DiscoveryBatch
	errors  map[domain.Chain]error
}

func (f *fakeLaunchSource) DiscoverWithCoverage(_ context.Context, chain domain.Chain) (domain.DiscoveryBatch, error) {
	return f.batches[chain], f.errors[chain]
}

type fakeGeckoMomentumSource struct {
	results map[domain.Chain]domain.DiscoveryResult
}

func (f *fakeGeckoMomentumSource) DiscoverMomentum(_ context.Context, chain domain.Chain) domain.DiscoveryResult {
	return f.results[chain]
}

type fakeDexMomentumSource struct {
	boosts     domain.DiscoveryResult
	batches    map[domain.Chain]domain.DiscoveryResult
	boostCalls int
}

func (f *fakeDexMomentumSource) DiscoverBoosts(context.Context) domain.DiscoveryResult {
	f.boostCalls++
	return f.boosts
}

func (f *fakeDexMomentumSource) DiscoverAddresses(_ context.Context, chain domain.Chain, _ []domain.Candidate) domain.DiscoveryResult {
	return f.batches[chain]
}

type fakeDiscoveryStore struct {
	universe map[domain.Chain][]domain.Candidate
	reports  []domain.DiscoverySourceReport
	cursors  []struct {
		chain domain.Chain
		value string
	}
}

func (f *fakeDiscoveryStore) SeedMomentumUniverseFromHistory(context.Context, domain.Chain, time.Time) (int, error) {
	return 0, nil
}

func (f *fakeDiscoveryStore) AdmitMomentumUniverse(context.Context, domain.Candidate, domain.UniverseAdmissionSource, time.Time) (bool, error) {
	return true, nil
}

func (f *fakeDiscoveryStore) ListMomentumUniverse(_ context.Context, chain domain.Chain, _ string, _ int) ([]domain.Candidate, string, error) {
	items := f.universe[chain]
	cursor := ""
	if len(items) > 0 {
		cursor = items[len(items)-1].ID
	}
	return items, cursor, nil
}

func (f *fakeDiscoveryStore) SaveMomentumCursor(_ context.Context, chain domain.Chain, value string, _ time.Time) error {
	f.cursors = append(f.cursors, struct {
		chain domain.Chain
		value string
	}{chain, value})
	return nil
}

func (f *fakeDiscoveryStore) InsertDiscoverySourceReport(_ context.Context, _ int64, report domain.DiscoverySourceReport) error {
	f.reports = append(f.reports, report)
	return nil
}

func (f *fakeDiscoveryStore) UpdateMomentumUniverseEvidence(context.Context, string, *time.Time, domain.MarketSnapshot, time.Time) error {
	return nil
}
