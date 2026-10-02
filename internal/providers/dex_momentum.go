package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

// DexMomentumDiscoverer 将 Boost 当作地址种子，并通过批量 Token API 获取市场证据。
type DexMomentumDiscoverer struct {
	client  *http.Client
	baseURL string
	now     func() time.Time
}

func NewDexMomentumDiscoverer(client *http.Client, baseURL string, now func() time.Time) *DexMomentumDiscoverer {
	if baseURL == "" {
		baseURL = dexScreenerBaseURL
	}
	return &DexMomentumDiscoverer{client: client, baseURL: strings.TrimRight(baseURL, "/"), now: now}
}

type dexBoost struct {
	ChainID      string `json:"chainId"`
	TokenAddress string `json:"tokenAddress"`
}

func (d *DexMomentumDiscoverer) DiscoverBoosts(ctx context.Context) domain.DiscoveryResult {
	started := providerNow(d.now)
	result := newDiscoveryResult()
	var boosts []dexBoost
	err := requestJSON(ctx, d.client, "dexscreener", d.baseURL+"/token-boosts/top/v1", &boosts)
	finished := providerNow(d.now)
	for _, chain := range []domain.Chain{domain.ChainBSC, domain.ChainSolana} {
		report := domain.DiscoverySourceReport{
			Chain: chain, Provider: "dexscreener", Source: string(domain.MomentumSourceDexBoostTop),
			StartedAt: started, FinishedAt: finished, PagesFetched: 1, ReturnedItems: len(boosts),
		}
		if err != nil {
			report.Error = err.Error()
			result.Errors = append(result.Errors, fmt.Sprintf("%s %s: %v", chain, domain.MomentumSourceDexBoostTop, err))
			result.Reports = append(result.Reports, report)
			continue
		}
		for _, boost := range boosts {
			if boost.ChainID != string(chain) || boost.TokenAddress == "" {
				continue
			}
			address := normalizeAddress(chain, boost.TokenAddress)
			candidate := domain.Candidate{
				ID: chainID(chain, address), Chain: chain, Address: address,
				Origin:          domain.CandidateOriginMomentum,
				MomentumSources: []domain.MomentumSource{domain.MomentumSourceDexBoostTop},
				DetectedAt:      started,
			}
			candidate.DiscoveryMarket = emptyMomentumMarket(candidate.ID, started)
			candidate.DiscoveryMarket.CandidateOrigin = candidate.Origin
			candidate.DiscoveryMarket.MomentumSources = append([]domain.MomentumSource(nil), candidate.MomentumSources...)
			result.Candidates = append(result.Candidates, candidate)
			report.UniqueCandidates++
		}
		result.Reports = append(result.Reports, report)
	}
	return result
}

func (d *DexMomentumDiscoverer) DiscoverAddresses(ctx context.Context, chain domain.Chain, seeds []domain.Candidate) domain.DiscoveryResult {
	result := newDiscoveryResult()
	if len(seeds) == 0 {
		return result
	}
	if len(seeds) > 30 {
		result.Errors = append(result.Errors, fmt.Sprintf("Dex batch contains %d addresses; maximum is 30", len(seeds)))
		return result
	}
	chainName, err := dexChain(chain)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result
	}
	started := providerNow(d.now)
	addresses := make([]string, 0, len(seeds))
	seedByID := make(map[string]domain.Candidate, len(seeds))
	for _, seed := range seeds {
		address := normalizeAddress(chain, seed.Address)
		addresses = append(addresses, address)
		seedByID[chainID(chain, address)] = seed
	}
	endpoint := fmt.Sprintf("%s/tokens/v1/%s/%s", d.baseURL, url.PathEscape(chainName), strings.Join(addresses, ","))
	var pairs []dexPair
	requestErr := requestJSON(ctx, d.client, "dexscreener", endpoint, &pairs)
	report := domain.DiscoverySourceReport{
		Chain: chain, Provider: "dexscreener", Source: string(domain.MomentumSourceLocalUniverse),
		StartedAt: started, FinishedAt: providerNow(d.now), PagesFetched: 1, ReturnedItems: len(pairs),
	}
	if requestErr != nil {
		report.Error = requestErr.Error()
		result.Errors = append(result.Errors, fmt.Sprintf("%s %s: %v", chain, domain.MomentumSourceLocalUniverse, requestErr))
		result.Reports = append(result.Reports, report)
		return result
	}

	selected := make(map[string]dexPair, len(seeds))
	earliestPair := make(map[string]time.Time, len(seeds))
	for _, pair := range pairs {
		if pair.ChainID != chainName || pair.BaseToken.Address == "" {
			continue
		}
		id := chainID(chain, normalizeAddress(chain, pair.BaseToken.Address))
		if _, wanted := seedByID[id]; !wanted {
			continue
		}
		if pair.PairCreatedAt != nil {
			created := time.UnixMilli(*pair.PairCreatedAt).UTC()
			if current, exists := earliestPair[id]; !exists || created.Before(current) {
				earliestPair[id] = created
			}
		}
		if pair.Liquidity.USD == nil {
			continue
		}
		current, exists := selected[id]
		if !exists || current.Liquidity.USD == nil || *pair.Liquidity.USD > *current.Liquidity.USD {
			selected[id] = pair
		}
	}
	for _, seed := range seeds {
		id := chainID(chain, normalizeAddress(chain, seed.Address))
		pair, ok := selected[id]
		if !ok {
			continue
		}
		seed.ID, seed.Chain, seed.Address = id, chain, normalizeAddress(chain, seed.Address)
		seed.Name, seed.Symbol = pair.BaseToken.Name, pair.BaseToken.Symbol
		seed.Origin = domain.CandidateOriginMomentum
		seed.MomentumSources = mergeMomentumSources(seed.MomentumSources, domain.MomentumSourceLocalUniverse)
		seed.PairAddress = normalizeAddress(chain, pair.PairAddress)
		seed.TriggerPoolAddress = seed.PairAddress
		market, mapErr := mapDexPair(seed, pair, started)
		if mapErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s %s %s: %v", chain, domain.MomentumSourceLocalUniverse, id, mapErr))
			continue
		}
		if pair.PairCreatedAt != nil {
			created := time.UnixMilli(*pair.PairCreatedAt).UTC()
			seed.TriggerPoolCreatedAt = &created
		}
		market.CandidateOrigin = seed.Origin
		market.MomentumSources = append([]domain.MomentumSource(nil), seed.MomentumSources...)
		market.MarketDataSource = domain.MarketDataSourceDex
		market.MarketDataQuality = domain.MarketDataQualityFull
		market.TriggerPoolAddress = seed.TriggerPoolAddress
		market.TriggerPoolCreatedAt = seed.TriggerPoolCreatedAt
		if earliest, exists := earliestPair[id]; exists {
			age := started.Sub(earliest)
			if age < 0 {
				age = 0
			}
			market.TokenAgeSeconds = domain.DataValue[int64]{
				Value: int64(age / time.Second), Quality: domain.QualityFresh,
				Source: "dexscreener", CollectedAt: started,
			}
			market.TokenAgeSource = domain.TokenAgeSourceEarliestPair
			if market.DataQuality == nil {
				market.DataQuality = map[string]domain.Quality{}
			}
			market.DataQuality["token_age_seconds"] = domain.QualityFresh
		}
		seed.DiscoveryMarket = market
		result.Candidates = append(result.Candidates, seed)
	}
	report.UniqueCandidates = len(result.Candidates)
	result.Reports = append(result.Reports, report)
	return result
}

func newDiscoveryResult() domain.DiscoveryResult {
	return domain.DiscoveryResult{
		Candidates: []domain.Candidate{}, Reports: []domain.DiscoverySourceReport{},
		Errors: []string{}, Coverages: map[domain.Chain]domain.DiscoveryCoverage{},
	}
}

func mergeMomentumSources(existing []domain.MomentumSource, additions ...domain.MomentumSource) []domain.MomentumSource {
	seen := make(map[domain.MomentumSource]struct{}, len(existing)+len(additions))
	for _, source := range append(append([]domain.MomentumSource(nil), existing...), additions...) {
		if source != "" {
			seen[source] = struct{}{}
		}
	}
	merged := make([]domain.MomentumSource, 0, len(seen))
	for source := range seen {
		merged = append(merged, source)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i] < merged[j] })
	return merged
}

func emptyMomentumMarket(tokenID string, at time.Time) domain.MarketSnapshot {
	return domain.MarketSnapshot{
		TokenID: tokenID, CollectedAt: at,
		PriceUSD: missingFloat("dexscreener", at), MarketCapUSD: missingFloat("dexscreener", at),
		FDVUSD: missingFloat("dexscreener", at), LiquidityUSD: missingFloat("dexscreener", at),
		VolumeM5USD: missingFloat("dexscreener", at), VolumeH1USD: missingFloat("dexscreener", at),
		VolumeH6USD: missingFloat("dexscreener", at), Volume24hUSD: missingFloat("dexscreener", at),
		PriceChangeM5: missingFloat("dexscreener", at), PriceChangeH1: missingFloat("dexscreener", at),
		PriceChangeH6: missingFloat("dexscreener", at), PriceChangeH24: missingFloat("dexscreener", at),
		BuysM5: missingInt("dexscreener", at), SellsM5: missingInt("dexscreener", at),
		BuyersM5: missingInt("dexscreener", at), SellersM5: missingInt("dexscreener", at),
		Score: missingFloat("", at), DataQuality: map[string]domain.Quality{},
	}
}
