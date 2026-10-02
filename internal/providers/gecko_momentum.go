package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

// GeckoMomentumDiscoverer 使用官方 Trending 和 Top Volume 首页面发现老币异动。
type GeckoMomentumDiscoverer struct {
	client  *http.Client
	baseURL string
	now     func() time.Time
	pacer   Pacer
}

func NewGeckoMomentumDiscoverer(client *http.Client, baseURL string, now func() time.Time, pacer Pacer) *GeckoMomentumDiscoverer {
	if baseURL == "" {
		baseURL = geckoTerminalBaseURL
	}
	return &GeckoMomentumDiscoverer{
		client: client, baseURL: strings.TrimRight(baseURL, "/"), now: now, pacer: pacer,
	}
}

func (d *GeckoMomentumDiscoverer) DiscoverMomentum(ctx context.Context, chain domain.Chain) domain.DiscoveryResult {
	network, err := geckoNetwork(chain)
	if err != nil {
		return domain.DiscoveryResult{Errors: []string{err.Error()}, Coverages: map[domain.Chain]domain.DiscoveryCoverage{}}
	}
	now := providerNow(d.now)
	result := domain.DiscoveryResult{
		Candidates: []domain.Candidate{}, Reports: []domain.DiscoverySourceReport{},
		Errors: []string{}, Coverages: map[domain.Chain]domain.DiscoveryCoverage{},
	}
	sources := []struct {
		source   domain.MomentumSource
		endpoint string
	}{
		{domain.MomentumSourceGeckoTrending, fmt.Sprintf("%s/networks/%s/trending_pools?include=base_token&page=1", d.baseURL, url.PathEscape(network))},
		{domain.MomentumSourceGeckoTopVolume, fmt.Sprintf("%s/networks/%s/pools?include=base_token&page=1&order=h24_volume_usd_desc", d.baseURL, url.PathEscape(network))},
	}
	for _, source := range sources {
		started := providerNow(d.now)
		report := domain.DiscoverySourceReport{
			Chain: chain, Provider: "geckoterminal", Source: string(source.source),
			StartedAt: started, PagesFetched: 1,
		}
		var response geckoPoolsResponse
		requestErr := requestJSONPacedPriority(ctx, d.client, "geckoterminal", source.endpoint, &response, d.pacer, RequestPriorityDiscovery)
		report.FinishedAt = providerNow(d.now)
		if requestErr != nil {
			report.Error = requestErr.Error()
			result.Errors = append(result.Errors, fmt.Sprintf("%s %s: %v", chain, source.source, requestErr))
			result.Reports = append(result.Reports, report)
			continue
		}
		report.ReturnedItems = len(response.Data)
		candidates, mapErr := mapGeckoMomentumCandidates(chain, network, source.source, response, now)
		report.UniqueCandidates = len(candidates)
		result.Candidates = append(result.Candidates, candidates...)
		if mapErr != nil {
			report.Error = mapErr.Error()
			result.Errors = append(result.Errors, fmt.Sprintf("%s %s: %v", chain, source.source, mapErr))
		}
		result.Reports = append(result.Reports, report)
	}
	return result
}

func mapGeckoMomentumCandidates(chain domain.Chain, network string, source domain.MomentumSource, response geckoPoolsResponse, collectedAt time.Time) ([]domain.Candidate, error) {
	included := make(map[string]struct{ Address, Name, Symbol string }, len(response.Included))
	for _, token := range response.Included {
		included[token.ID] = struct{ Address, Name, Symbol string }{
			token.Attributes.Address, token.Attributes.Name, token.Attributes.Symbol,
		}
	}
	seen := make(map[string]struct{}, len(response.Data))
	candidates := make([]domain.Candidate, 0, len(response.Data))
	var firstErr error
	for _, pool := range response.Data {
		createdAt, err := time.Parse(time.RFC3339, pool.Attributes.PoolCreatedAt)
		if err != nil {
			if firstErr == nil {
				firstErr = badResponse("geckoterminal", http.StatusOK, fmt.Errorf("parse pool_created_at: %w", err))
			}
			continue
		}
		createdAt = createdAt.UTC()
		token := included[pool.Relationships.BaseToken.Data.ID]
		address := token.Address
		if address == "" {
			address = strings.TrimPrefix(pool.Relationships.BaseToken.Data.ID, network+"_")
		}
		address = normalizeAddress(chain, address)
		id := chainID(chain, address)
		if _, exists := seen[id]; exists {
			continue
		}
		poolAddress := normalizeAddress(chain, pool.Attributes.Address)
		market, err := mapGeckoDiscoveryMarket(id, poolAddress, pool.Attributes, collectedAt)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		seen[id] = struct{}{}
		created := createdAt
		market.CandidateOrigin = domain.CandidateOriginMomentum
		market.MomentumSources = []domain.MomentumSource{source}
		market.MarketDataSource = domain.MarketDataSourceGecko
		market.MarketDataQuality = domain.MarketDataQualityPartial
		market.TriggerPoolAddress = poolAddress
		market.TriggerPoolCreatedAt = &created
		market.PairCreatedAt = &created
		candidates = append(candidates, domain.Candidate{
			ID: id, Chain: chain, Address: address, Name: token.Name, Symbol: token.Symbol,
			PairAddress: poolAddress, Origin: domain.CandidateOriginMomentum,
			MomentumSources: []domain.MomentumSource{source}, TriggerPoolAddress: poolAddress,
			TriggerPoolCreatedAt: &created, DetectedAt: collectedAt, DiscoveryMarket: market,
		})
	}
	return candidates, firstErr
}
