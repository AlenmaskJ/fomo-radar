package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

const geckoTerminalBaseURL = "https://api.geckoterminal.com/api/v2"

type GeckoTerminalDiscoverer struct {
	client  *http.Client
	baseURL string
	now     func() time.Time
	pacer   Pacer
}

func NewGeckoTerminalDiscoverer(client *http.Client, baseURL string, now func() time.Time, pacers ...Pacer) *GeckoTerminalDiscoverer {
	if baseURL == "" {
		baseURL = geckoTerminalBaseURL
	}
	var pacer Pacer
	if len(pacers) > 0 {
		pacer = pacers[0]
	}
	return &GeckoTerminalDiscoverer{client: client, baseURL: strings.TrimRight(baseURL, "/"), now: now, pacer: pacer}
}

type geckoPoolsResponse struct {
	Data []struct {
		Attributes    geckoPoolAttributes `json:"attributes"`
		Relationships struct {
			BaseToken struct {
				Data struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"base_token"`
		} `json:"relationships"`
	} `json:"data"`
	Included []struct {
		ID         string `json:"id"`
		Attributes struct {
			Address string `json:"address"`
			Name    string `json:"name"`
			Symbol  string `json:"symbol"`
		} `json:"attributes"`
	} `json:"included"`
}

type geckoPoolAttributes struct {
	Address           string  `json:"address"`
	PoolCreatedAt     string  `json:"pool_created_at"`
	BaseTokenPriceUSD *string `json:"base_token_price_usd"`
	FDVUSD            *string `json:"fdv_usd"`
	MarketCapUSD      *string `json:"market_cap_usd"`
	ReserveInUSD      *string `json:"reserve_in_usd"`
	PriceChange       struct {
		M5  *string `json:"m5"`
		H1  *string `json:"h1"`
		H6  *string `json:"h6"`
		H24 *string `json:"h24"`
	} `json:"price_change_percentage"`
	Transactions struct {
		M5  *geckoTransactionWindow `json:"m5"`
		H1  *geckoTransactionWindow `json:"h1"`
		H6  *geckoTransactionWindow `json:"h6"`
		H24 *geckoTransactionWindow `json:"h24"`
	} `json:"transactions"`
	VolumeUSD struct {
		M5  *string `json:"m5"`
		H1  *string `json:"h1"`
		H6  *string `json:"h6"`
		H24 *string `json:"h24"`
	} `json:"volume_usd"`
}

type geckoTransactionWindow struct {
	Buys    *int `json:"buys"`
	Sells   *int `json:"sells"`
	Buyers  *int `json:"buyers"`
	Sellers *int `json:"sellers"`
}

func (d *GeckoTerminalDiscoverer) Discover(ctx context.Context, chain domain.Chain) ([]domain.Candidate, error) {
	batch, err := d.DiscoverWithCoverage(ctx, chain)
	return batch.Candidates, err
}

func (d *GeckoTerminalDiscoverer) DiscoverWithCoverage(ctx context.Context, chain domain.Chain) (domain.DiscoveryBatch, error) {
	network, err := geckoNetwork(chain)
	if err != nil {
		return domain.DiscoveryBatch{}, err
	}
	collectedAt := providerNow(d.now)
	cutoff := collectedAt.Add(-6 * time.Hour)
	candidates := make([]domain.Candidate, 0, 20)
	seen := make(map[string]struct{})
	coverage := domain.DiscoveryCoverage{Chain: chain, ScanStartedAt: collectedAt}
	var hasCoverageTime bool

	for page := 1; page <= 3; page++ {
		endpoint := fmt.Sprintf("%s/networks/%s/new_pools?include=base_token&page=%d", d.baseURL, url.PathEscape(network), page)
		var response geckoPoolsResponse
		if err := requestJSONPacedPriority(ctx, d.client, "geckoterminal", endpoint, &response, d.pacer, RequestPriorityDiscovery); err != nil {
			return domain.DiscoveryBatch{}, err
		}
		coverage.PagesFetched++
		included := make(map[string]struct{ Address, Name, Symbol string }, len(response.Included))
		for _, token := range response.Included {
			included[token.ID] = struct{ Address, Name, Symbol string }{token.Attributes.Address, token.Attributes.Name, token.Attributes.Symbol}
		}
		oldest := collectedAt
		for _, pool := range response.Data {
			createdAt, err := time.Parse(time.RFC3339, pool.Attributes.PoolCreatedAt)
			if err != nil {
				return domain.DiscoveryBatch{}, badResponse("geckoterminal", http.StatusOK, fmt.Errorf("parse pool_created_at: %w", err))
			}
			createdAt = createdAt.UTC()
			if !hasCoverageTime || createdAt.After(coverage.NewestPoolAt) {
				coverage.NewestPoolAt = createdAt
			}
			if !hasCoverageTime || createdAt.Before(coverage.OldestPoolAt) {
				coverage.OldestPoolAt = createdAt
			}
			hasCoverageTime = true
			if createdAt.Before(oldest) {
				oldest = createdAt
			}
			if createdAt.Before(cutoff) {
				continue
			}
			token := included[pool.Relationships.BaseToken.Data.ID]
			address := token.Address
			if address == "" {
				address = strings.TrimPrefix(pool.Relationships.BaseToken.Data.ID, network+"_")
			}
			address = normalizeAddress(chain, address)
			id := chainID(chain, address)
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			created := createdAt
			poolAddress := normalizeAddress(chain, pool.Attributes.Address)
			market, err := mapGeckoDiscoveryMarket(id, poolAddress, pool.Attributes, collectedAt)
			if err != nil {
				return domain.DiscoveryBatch{}, err
			}
			candidates = append(candidates, domain.Candidate{
				ID: id, Chain: chain, Address: address, Name: token.Name, Symbol: token.Symbol,
				PairAddress: poolAddress, DiscoveryPoolAddress: poolAddress, DetectedAt: collectedAt,
				ChainCreatedAt: &created, DiscoveryPoolCreatedAt: &created, DiscoveryMarket: market,
			})
		}
		if len(response.Data) == 0 || oldest.Before(cutoff) {
			break
		}
	}
	coverage.UniqueCandidates = len(candidates)
	return domain.DiscoveryBatch{Candidates: candidates, Coverage: coverage}, nil
}

func mapGeckoDiscoveryMarket(tokenID, poolAddress string, attributes geckoPoolAttributes, collectedAt time.Time) (domain.MarketSnapshot, error) {
	market := domain.MarketSnapshot{
		TokenID: tokenID, PairAddress: poolAddress, CollectedAt: collectedAt,
		PriceUSD: missingFloat("geckoterminal", collectedAt), MarketCapUSD: missingFloat("geckoterminal", collectedAt),
		FDVUSD: missingFloat("geckoterminal", collectedAt), LiquidityUSD: missingFloat("geckoterminal", collectedAt),
		VolumeM5USD: missingFloat("geckoterminal", collectedAt), VolumeH1USD: missingFloat("geckoterminal", collectedAt),
		VolumeH6USD: missingFloat("geckoterminal", collectedAt), Volume24hUSD: missingFloat("geckoterminal", collectedAt),
		BuysM5: missingInt("geckoterminal", collectedAt), SellsM5: missingInt("geckoterminal", collectedAt),
		BuysH1: missingInt("geckoterminal", collectedAt), SellsH1: missingInt("geckoterminal", collectedAt),
		BuysH6: missingInt("geckoterminal", collectedAt), SellsH6: missingInt("geckoterminal", collectedAt),
		BuysH24: missingInt("geckoterminal", collectedAt), SellsH24: missingInt("geckoterminal", collectedAt),
		BuyersM5:  missingInt("geckoterminal", collectedAt),
		SellersM5: missingInt("geckoterminal", collectedAt), AggregateBuyersM5: missingInt("geckoterminal", collectedAt),
		AggregateSellersM5: missingInt("geckoterminal", collectedAt), AggregateBuyersH1: missingInt("geckoterminal", collectedAt),
		AggregateSellersH1: missingInt("geckoterminal", collectedAt), AggregateBuyersH6: missingInt("geckoterminal", collectedAt),
		AggregateSellersH6: missingInt("geckoterminal", collectedAt), AggregateBuyersH24: missingInt("geckoterminal", collectedAt),
		AggregateSellersH24: missingInt("geckoterminal", collectedAt), PriceChangeM5: missingFloat("geckoterminal", collectedAt),
		PriceChangeH1: missingFloat("geckoterminal", collectedAt), PriceChangeH6: missingFloat("geckoterminal", collectedAt),
		PriceChangeH24: missingFloat("geckoterminal", collectedAt),
		Holders:        missingInt("geckoterminal", collectedAt), BoostsActive: missingInt("geckoterminal", collectedAt),
		Score: missingFloat("", collectedAt), DataQuality: map[string]domain.Quality{},
	}
	var err error
	for _, field := range []struct {
		name string
		raw  *string
		dst  *domain.DataValue[float64]
	}{
		{"base_token_price_usd", attributes.BaseTokenPriceUSD, &market.PriceUSD},
		{"market_cap_usd", attributes.MarketCapUSD, &market.MarketCapUSD},
		{"fdv_usd", attributes.FDVUSD, &market.FDVUSD},
		{"reserve_in_usd", attributes.ReserveInUSD, &market.LiquidityUSD},
		{"volume_usd.m5", attributes.VolumeUSD.M5, &market.VolumeM5USD},
		{"volume_usd.h1", attributes.VolumeUSD.H1, &market.VolumeH1USD},
		{"volume_usd.h6", attributes.VolumeUSD.H6, &market.VolumeH6USD},
		{"volume_usd.h24", attributes.VolumeUSD.H24, &market.Volume24hUSD},
		{"price_change_percentage.m5", attributes.PriceChange.M5, &market.PriceChangeM5},
		{"price_change_percentage.h1", attributes.PriceChange.H1, &market.PriceChangeH1},
		{"price_change_percentage.h6", attributes.PriceChange.H6, &market.PriceChangeH6},
		{"price_change_percentage.h24", attributes.PriceChange.H24, &market.PriceChangeH24},
	} {
		*field.dst, err = geckoFloat(field.raw, field.name, collectedAt)
		if err != nil {
			return domain.MarketSnapshot{}, err
		}
	}
	mapGeckoTransactions(attributes.Transactions.M5, collectedAt, &market.BuysM5, &market.SellsM5, &market.AggregateBuyersM5, &market.AggregateSellersM5)
	mapGeckoTransactions(attributes.Transactions.H1, collectedAt, &market.BuysH1, &market.SellsH1, &market.AggregateBuyersH1, &market.AggregateSellersH1)
	mapGeckoTransactions(attributes.Transactions.H6, collectedAt, &market.BuysH6, &market.SellsH6, &market.AggregateBuyersH6, &market.AggregateSellersH6)
	mapGeckoTransactions(attributes.Transactions.H24, collectedAt, &market.BuysH24, &market.SellsH24, &market.AggregateBuyersH24, &market.AggregateSellersH24)
	market.DataQuality = map[string]domain.Quality{
		"price_usd": market.PriceUSD.Quality, "market_cap_usd": market.MarketCapUSD.Quality,
		"fdv_usd": market.FDVUSD.Quality, "liquidity_usd": market.LiquidityUSD.Quality,
		"volume_m5_usd": market.VolumeM5USD.Quality, "volume_h1_usd": market.VolumeH1USD.Quality,
		"volume_h6_usd": market.VolumeH6USD.Quality, "volume_24h_usd": market.Volume24hUSD.Quality,
		"buys_m5":  market.BuysM5.Quality,
		"sells_m5": market.SellsM5.Quality, "aggregate_buyers_m5": market.AggregateBuyersM5.Quality,
		"aggregate_sellers_m5": market.AggregateSellersM5.Quality, "buyers_m5": market.BuyersM5.Quality,
		"sellers_m5": market.SellersM5.Quality, "price_change_m5": market.PriceChangeM5.Quality,
		"price_change_h1": market.PriceChangeH1.Quality, "price_change_h6": market.PriceChangeH6.Quality,
		"price_change_h24": market.PriceChangeH24.Quality, "buys_h1": market.BuysH1.Quality,
		"sells_h1": market.SellsH1.Quality, "buys_h6": market.BuysH6.Quality,
		"sells_h6": market.SellsH6.Quality, "buys_h24": market.BuysH24.Quality,
		"sells_h24": market.SellsH24.Quality, "aggregate_buyers_h1": market.AggregateBuyersH1.Quality,
		"aggregate_sellers_h1": market.AggregateSellersH1.Quality, "aggregate_buyers_h6": market.AggregateBuyersH6.Quality,
		"aggregate_sellers_h6": market.AggregateSellersH6.Quality, "aggregate_buyers_h24": market.AggregateBuyersH24.Quality,
		"aggregate_sellers_h24": market.AggregateSellersH24.Quality,
	}
	return market, nil
}

func mapGeckoTransactions(window *geckoTransactionWindow, collectedAt time.Time, buys, sells, buyers, sellers *domain.DataValue[int]) {
	if window == nil {
		return
	}
	*buys = optionalInt(window.Buys, "geckoterminal", collectedAt)
	*sells = optionalInt(window.Sells, "geckoterminal", collectedAt)
	*buyers = optionalInt(window.Buyers, "geckoterminal", collectedAt)
	*sellers = optionalInt(window.Sellers, "geckoterminal", collectedAt)
}

func geckoFloat(raw *string, field string, collectedAt time.Time) (domain.DataValue[float64], error) {
	if raw == nil {
		return missingFloat("geckoterminal", collectedAt), nil
	}
	value, err := strconv.ParseFloat(*raw, 64)
	if err != nil {
		return domain.DataValue[float64]{}, badResponse("geckoterminal", http.StatusOK, fmt.Errorf("parse %s: %w", field, err))
	}
	return freshFloat(value, "geckoterminal", collectedAt), nil
}

func optionalInt(value *int, source string, collectedAt time.Time) domain.DataValue[int] {
	if value == nil {
		return missingInt(source, collectedAt)
	}
	return freshInt(*value, source, collectedAt)
}

func geckoNetwork(chain domain.Chain) (string, error) {
	switch chain {
	case domain.ChainBSC:
		return "bsc", nil
	case domain.ChainSolana:
		return "solana", nil
	default:
		return "", badResponse("geckoterminal", 0, fmt.Errorf("unsupported chain %q", chain))
	}
}

func chainID(chain domain.Chain, address string) string {
	return string(chain) + ":" + normalizeAddress(chain, address)
}
