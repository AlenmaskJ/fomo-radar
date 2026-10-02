package scanner

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

type launchDiscoverySource interface {
	DiscoverWithCoverage(context.Context, domain.Chain) (domain.DiscoveryBatch, error)
}

type geckoMomentumSource interface {
	DiscoverMomentum(context.Context, domain.Chain) domain.DiscoveryResult
}

type dexMomentumSource interface {
	DiscoverBoosts(context.Context) domain.DiscoveryResult
	DiscoverAddresses(context.Context, domain.Chain, []domain.Candidate) domain.DiscoveryResult
}

type discoveryStore interface {
	SeedMomentumUniverseFromHistory(context.Context, domain.Chain, time.Time) (int, error)
	AdmitMomentumUniverse(context.Context, domain.Candidate, domain.UniverseAdmissionSource, time.Time) (bool, error)
	ListMomentumUniverse(context.Context, domain.Chain, string, int) ([]domain.Candidate, string, error)
	SaveMomentumCursor(context.Context, domain.Chain, string, time.Time) error
	InsertDiscoverySourceReport(context.Context, int64, domain.DiscoverySourceReport) error
	UpdateMomentumUniverseEvidence(context.Context, string, *time.Time, domain.MarketSnapshot, time.Time) error
}

// DiscoveryCoordinator 在一个 Fast 周期内协调 Launch、Momentum 和本地 Universe。
type DiscoveryCoordinator struct {
	launch launchDiscoverySource
	gecko  geckoMomentumSource
	dex    dexMomentumSource
	store  discoveryStore
	now    func() time.Time
}

func NewDiscoveryCoordinator(launch launchDiscoverySource, gecko geckoMomentumSource, dex dexMomentumSource, store discoveryStore, now func() time.Time) *DiscoveryCoordinator {
	if now == nil {
		now = time.Now
	}
	return &DiscoveryCoordinator{launch: launch, gecko: gecko, dex: dex, store: store, now: now}
}

func (d *DiscoveryCoordinator) Discover(ctx context.Context, runID int64) domain.DiscoveryResult {
	now := d.now().UTC()
	result := domain.DiscoveryResult{
		Candidates: []domain.Candidate{}, Reports: []domain.DiscoverySourceReport{},
		Errors: []string{}, Coverages: map[domain.Chain]domain.DiscoveryCoverage{},
	}
	external := make([]domain.Candidate, 0, 128)
	for _, chain := range []domain.Chain{domain.ChainBSC, domain.ChainSolana} {
		if _, err := d.store.SeedMomentumUniverseFromHistory(ctx, chain, now); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("seed historical universe %s: %v", chain, err))
		}
		started := d.now().UTC()
		batch, err := d.launch.DiscoverWithCoverage(ctx, chain)
		report := domain.DiscoverySourceReport{
			Chain: chain, Provider: "geckoterminal", Source: "GECKO_NEW_POOLS",
			StartedAt: started, FinishedAt: d.now().UTC(), PagesFetched: batch.Coverage.PagesFetched,
			ReturnedItems: len(batch.Candidates), UniqueCandidates: len(batch.Candidates),
		}
		if !batch.Coverage.NewestPoolAt.IsZero() {
			newest := batch.Coverage.NewestPoolAt
			report.NewestPoolAt = &newest
		}
		if !batch.Coverage.OldestPoolAt.IsZero() {
			oldest := batch.Coverage.OldestPoolAt
			report.OldestPoolAt = &oldest
		}
		if err != nil {
			report.Error = err.Error()
			result.Errors = append(result.Errors, fmt.Sprintf("discover launch %s: %v", chain, err))
		} else {
			result.Coverages[chain] = batch.Coverage
			for _, candidate := range batch.Candidates {
				candidate.Origin = domain.CandidateOriginLaunch
				external = append(external, normalizeDiscoveredCandidate(candidate))
			}
		}
		d.persistReport(ctx, runID, report, &result)

		momentum := d.gecko.DiscoverMomentum(ctx, chain)
		external = append(external, momentum.Candidates...)
		result.Errors = append(result.Errors, momentum.Errors...)
		for _, sourceReport := range momentum.Reports {
			d.persistReport(ctx, runID, sourceReport, &result)
		}
	}

	boosts := d.dex.DiscoverBoosts(ctx)
	external = append(external, boosts.Candidates...)
	result.Errors = append(result.Errors, boosts.Errors...)
	for _, report := range boosts.Reports {
		d.persistReport(ctx, runID, report, &result)
	}

	external = MergeDiscoveredCandidates(external)
	for _, candidate := range external {
		source := universeAdmissionFor(candidate)
		admitted, err := d.store.AdmitMomentumUniverse(ctx, candidate, source, now)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("admit universe %s: %v", candidate.ID, err))
			continue
		}
		if admitted && candidate.Origin != domain.CandidateOriginLaunch {
			if err := d.store.UpdateMomentumUniverseEvidence(ctx, candidate.ID, nil, candidate.DiscoveryMarket, now); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("update universe evidence %s: %v", candidate.ID, err))
			}
		}
	}

	all := append([]domain.Candidate(nil), external...)
	for _, chain := range []domain.Chain{domain.ChainBSC, domain.ChainSolana} {
		seeds, cursor, err := d.store.ListMomentumUniverse(ctx, chain, "", 30)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("list universe %s: %v", chain, err))
			continue
		}
		if len(seeds) == 0 {
			continue
		}
		batch := d.dex.DiscoverAddresses(ctx, chain, seeds)
		all = append(all, batch.Candidates...)
		result.Errors = append(result.Errors, batch.Errors...)
		batchSucceeded := len(batch.Reports) > 0
		for _, report := range batch.Reports {
			report.CursorAfter = cursor
			if report.Error != "" {
				batchSucceeded = false
			}
			if !d.persistReport(ctx, runID, report, &result) {
				batchSucceeded = false
			}
		}
		if batchSucceeded {
			if err := d.store.SaveMomentumCursor(ctx, chain, cursor, now); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("save universe cursor %s: %v", chain, err))
			}
		}
		for _, candidate := range batch.Candidates {
			if err := d.store.UpdateMomentumUniverseEvidence(ctx, candidate.ID, nil, candidate.DiscoveryMarket, now); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("update universe evidence %s: %v", candidate.ID, err))
			}
		}
	}

	result.Candidates = MergeDiscoveredCandidates(all)
	sort.Strings(result.Errors)
	return result
}

func (d *DiscoveryCoordinator) persistReport(ctx context.Context, runID int64, report domain.DiscoverySourceReport, result *domain.DiscoveryResult) bool {
	result.Reports = append(result.Reports, report)
	if err := d.store.InsertDiscoverySourceReport(ctx, runID, report); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("persist discovery report %s/%s: %v", report.Chain, report.Source, err))
		return false
	}
	return true
}

func universeAdmissionFor(candidate domain.Candidate) domain.UniverseAdmissionSource {
	for _, source := range candidate.MomentumSources {
		if source == domain.MomentumSourceGeckoTopVolume || source == domain.MomentumSourceLocalUniverse {
			return domain.UniverseAdmissionAnomaly
		}
	}
	if candidate.Origin == domain.CandidateOriginMomentum || candidate.Origin == domain.CandidateOriginBoth {
		return domain.UniverseAdmissionTrending
	}
	return domain.UniverseAdmissionHistorical
}

// MergeDiscoveredCandidates 按规范化 chain+address 确定性合并同轮候选。
func MergeDiscoveredCandidates(candidates []domain.Candidate) []domain.Candidate {
	byID := make(map[string]domain.Candidate, len(candidates))
	for _, raw := range candidates {
		candidate := normalizeDiscoveredCandidate(raw)
		if candidate.ID == "" || candidate.Address == "" {
			continue
		}
		current, exists := byID[candidate.ID]
		if !exists {
			candidate.MomentumSources = sortedMomentumSources(candidate.MomentumSources)
			byID[candidate.ID] = candidate
			continue
		}
		byID[candidate.ID] = mergeDiscoveredCandidate(current, candidate)
	}
	result := make([]domain.Candidate, 0, len(byID))
	for _, candidate := range byID {
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func normalizeDiscoveredCandidate(candidate domain.Candidate) domain.Candidate {
	if candidate.Chain == domain.ChainBSC {
		candidate.Address = strings.ToLower(candidate.Address)
		candidate.PairAddress = strings.ToLower(candidate.PairAddress)
		candidate.DiscoveryPoolAddress = strings.ToLower(candidate.DiscoveryPoolAddress)
		candidate.TriggerPoolAddress = strings.ToLower(candidate.TriggerPoolAddress)
	}
	if candidate.Address != "" {
		candidate.ID = TokenID(candidate.Chain, candidate.Address)
	}
	if candidate.Origin == "" {
		candidate.Origin = domain.CandidateOriginLaunch
	}
	candidate.DiscoveryMarket.CandidateOrigin = candidate.Origin
	candidate.DiscoveryMarket.MomentumSources = sortedMomentumSources(candidate.MomentumSources)
	return candidate
}

func mergeDiscoveredCandidate(left, right domain.Candidate) domain.Candidate {
	merged := left
	merged.Origin = mergeCandidateOrigin(left.Origin, right.Origin)
	merged.MomentumSources = sortedMomentumSources(append(append([]domain.MomentumSource(nil), left.MomentumSources...), right.MomentumSources...))
	if merged.Name == "" {
		merged.Name = right.Name
	}
	if merged.Symbol == "" {
		merged.Symbol = right.Symbol
	}
	if merged.DiscoveryPoolAddress == "" && right.DiscoveryPoolAddress != "" {
		merged.DiscoveryPoolAddress = right.DiscoveryPoolAddress
		merged.DiscoveryPoolCreatedAt = right.DiscoveryPoolCreatedAt
	}
	if merged.TriggerPoolAddress == "" && right.TriggerPoolAddress != "" {
		merged.TriggerPoolAddress = right.TriggerPoolAddress
		merged.TriggerPoolCreatedAt = right.TriggerPoolCreatedAt
	}
	if right.PairAddress != "" && (merged.PairAddress == "" || right.Origin != domain.CandidateOriginLaunch) {
		merged.PairAddress = right.PairAddress
	}
	if merged.ChainCreatedAt == nil {
		merged.ChainCreatedAt = right.ChainCreatedAt
	}
	if merged.DetectedAt.IsZero() || (!right.DetectedAt.IsZero() && right.DetectedAt.Before(merged.DetectedAt)) {
		merged.DetectedAt = right.DetectedAt
	}
	if preferCandidateMarket(right.DiscoveryMarket, merged.DiscoveryMarket) {
		merged.DiscoveryMarket = right.DiscoveryMarket
	}
	merged.DiscoveryMarket.CandidateOrigin = merged.Origin
	merged.DiscoveryMarket.MomentumSources = append([]domain.MomentumSource(nil), merged.MomentumSources...)
	merged.DiscoveryMarket.DiscoveryPoolAddress = merged.DiscoveryPoolAddress
	merged.DiscoveryMarket.DiscoveryPoolCreatedAt = merged.DiscoveryPoolCreatedAt
	merged.DiscoveryMarket.TriggerPoolAddress = merged.TriggerPoolAddress
	merged.DiscoveryMarket.TriggerPoolCreatedAt = merged.TriggerPoolCreatedAt
	return merged
}

func preferCandidateMarket(candidate, current domain.MarketSnapshot) bool {
	qualityRank := func(quality domain.MarketDataQuality) int {
		switch quality {
		case domain.MarketDataQualityFull:
			return 2
		case domain.MarketDataQualityPartial:
			return 1
		default:
			return 0
		}
	}
	if left, right := qualityRank(candidate.MarketDataQuality), qualityRank(current.MarketDataQuality); left != right {
		return left > right
	}
	if left, right := marketEvidenceCount(candidate), marketEvidenceCount(current); left != right {
		return left > right
	}
	if !candidate.CollectedAt.Equal(current.CollectedAt) {
		return candidate.CollectedAt.After(current.CollectedAt)
	}
	return candidate.PairAddress > current.PairAddress
}

func mergeCandidateOrigin(left, right domain.CandidateOrigin) domain.CandidateOrigin {
	if left == right {
		return left
	}
	if left == domain.CandidateOriginBoth || right == domain.CandidateOriginBoth {
		return domain.CandidateOriginBoth
	}
	return domain.CandidateOriginBoth
}

func sortedMomentumSources(sources []domain.MomentumSource) []domain.MomentumSource {
	seen := make(map[domain.MomentumSource]struct{}, len(sources))
	for _, source := range sources {
		if source != "" {
			seen[source] = struct{}{}
		}
	}
	result := make([]domain.MomentumSource, 0, len(seen))
	for source := range seen {
		result = append(result, source)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func marketEvidenceCount(market domain.MarketSnapshot) int {
	qualities := []domain.Quality{
		market.PriceUSD.Quality, market.MarketCapUSD.Quality, market.FDVUSD.Quality,
		market.LiquidityUSD.Quality, market.VolumeM5USD.Quality, market.VolumeH1USD.Quality,
		market.VolumeH6USD.Quality, market.Volume24hUSD.Quality, market.PriceChangeM5.Quality,
		market.PriceChangeH1.Quality, market.PriceChangeH6.Quality, market.PriceChangeH24.Quality,
	}
	count := 0
	for _, quality := range qualities {
		if dataAvailable(quality) {
			count++
		}
	}
	return count
}
