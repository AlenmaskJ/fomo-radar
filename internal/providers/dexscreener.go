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

const dexScreenerBaseURL = "https://api.dexscreener.com"

type DexScreenerEnricher struct {
	client  *http.Client
	baseURL string
	now     func() time.Time
}

func NewDexScreenerEnricher(client *http.Client, baseURL string, now func() time.Time) *DexScreenerEnricher {
	if baseURL == "" {
		baseURL = dexScreenerBaseURL
	}
	return &DexScreenerEnricher{client: client, baseURL: strings.TrimRight(baseURL, "/"), now: now}
}

type dexPair struct {
	ChainID     string `json:"chainId"`
	PairAddress string `json:"pairAddress"`
	BaseToken   struct {
		Address string `json:"address"`
		Name    string `json:"name"`
		Symbol  string `json:"symbol"`
	} `json:"baseToken"`
	PriceUSD *string `json:"priceUsd"`
	Txns     struct {
		M5  *dexTransactionWindow `json:"m5"`
		H1  *dexTransactionWindow `json:"h1"`
		H6  *dexTransactionWindow `json:"h6"`
		H24 *dexTransactionWindow `json:"h24"`
	} `json:"txns"`
	Volume struct {
		M5  *float64 `json:"m5"`
		H1  *float64 `json:"h1"`
		H6  *float64 `json:"h6"`
		H24 *float64 `json:"h24"`
	} `json:"volume"`
	PriceChange struct {
		M5  *float64 `json:"m5"`
		H1  *float64 `json:"h1"`
		H6  *float64 `json:"h6"`
		H24 *float64 `json:"h24"`
	} `json:"priceChange"`
	Liquidity struct {
		USD *float64 `json:"usd"`
	} `json:"liquidity"`
	FDV           *float64 `json:"fdv"`
	MarketCap     *float64 `json:"marketCap"`
	PairCreatedAt *int64   `json:"pairCreatedAt"`
	Info          struct {
		Websites []struct {
			Label string `json:"label"`
			URL   string `json:"url"`
		} `json:"websites"`
		Socials []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"socials"`
	} `json:"info"`
	Boosts struct {
		Active *int `json:"active"`
	} `json:"boosts"`
}

type dexTransactionWindow struct {
	Buys  *int `json:"buys"`
	Sells *int `json:"sells"`
}

func (e *DexScreenerEnricher) Enrich(ctx context.Context, c domain.Candidate) (domain.MarketSnapshot, error) {
	chainID, err := dexChain(c.Chain)
	if err != nil {
		return domain.MarketSnapshot{}, err
	}
	endpoint := fmt.Sprintf("%s/token-pairs/v1/%s/%s", e.baseURL, url.PathEscape(chainID), url.PathEscape(normalizeAddress(c.Chain, c.Address)))
	var pairs []dexPair
	if err := requestJSON(ctx, e.client, "dexscreener", endpoint, &pairs); err != nil {
		return domain.MarketSnapshot{}, err
	}
	var selected *dexPair
	for i := range pairs {
		pair := &pairs[i]
		if pair.ChainID != chainID || !sameAddress(c.Chain, pair.BaseToken.Address, c.Address) || pair.Liquidity.USD == nil {
			continue
		}
		if selected == nil || *pair.Liquidity.USD > *selected.Liquidity.USD {
			selected = pair
		}
	}
	if selected == nil {
		return domain.MarketSnapshot{}, badResponse("dexscreener", http.StatusOK, fmt.Errorf("no matching pair with USD liquidity"))
	}
	return mapDexPair(c, *selected, providerNow(e.now))
}

func mapDexPair(c domain.Candidate, pair dexPair, collectedAt time.Time) (domain.MarketSnapshot, error) {
	tokenID := c.ID
	if tokenID == "" || c.Chain == domain.ChainBSC {
		tokenID = chainID(c.Chain, c.Address)
	}
	snapshot := domain.MarketSnapshot{
		TokenID: tokenID, PairAddress: normalizeAddress(c.Chain, pair.PairAddress), CollectedAt: collectedAt,
		PriceUSD: missingFloat("dexscreener", collectedAt), MarketCapUSD: missingFloat("dexscreener", collectedAt),
		FDVUSD: missingFloat("dexscreener", collectedAt), LiquidityUSD: missingFloat("dexscreener", collectedAt),
		VolumeM5USD: missingFloat("dexscreener", collectedAt), VolumeH1USD: missingFloat("dexscreener", collectedAt),
		VolumeH6USD: missingFloat("dexscreener", collectedAt), Volume24hUSD: missingFloat("dexscreener", collectedAt),
		BuysM5: missingInt("dexscreener", collectedAt), SellsM5: missingInt("dexscreener", collectedAt),
		BuysH1: missingInt("dexscreener", collectedAt), SellsH1: missingInt("dexscreener", collectedAt),
		BuysH6: missingInt("dexscreener", collectedAt), SellsH6: missingInt("dexscreener", collectedAt),
		BuysH24: missingInt("dexscreener", collectedAt), SellsH24: missingInt("dexscreener", collectedAt),
		BuyersM5: missingInt("dexscreener", collectedAt), SellersM5: missingInt("dexscreener", collectedAt),
		PriceChangeM5: missingFloat("dexscreener", collectedAt), PriceChangeH1: missingFloat("dexscreener", collectedAt),
		PriceChangeH6: missingFloat("dexscreener", collectedAt), PriceChangeH24: missingFloat("dexscreener", collectedAt),
		Holders: missingInt("dexscreener", collectedAt), BoostsActive: missingInt("dexscreener", collectedAt),
		Score: missingFloat("", collectedAt), DataQuality: map[string]domain.Quality{},
	}
	if pair.PriceUSD != nil {
		price, err := strconv.ParseFloat(*pair.PriceUSD, 64)
		if err != nil {
			return domain.MarketSnapshot{}, badResponse("dexscreener", http.StatusOK, fmt.Errorf("parse priceUsd: %w", err))
		}
		snapshot.PriceUSD = freshFloat(price, "dexscreener", collectedAt)
	}
	snapshot.MarketCapUSD = optionalFloat(pair.MarketCap, "dexscreener", collectedAt)
	snapshot.FDVUSD = optionalFloat(pair.FDV, "dexscreener", collectedAt)
	snapshot.LiquidityUSD = optionalFloat(pair.Liquidity.USD, "dexscreener", collectedAt)
	snapshot.VolumeM5USD = optionalFloat(pair.Volume.M5, "dexscreener", collectedAt)
	snapshot.VolumeH1USD = optionalFloat(pair.Volume.H1, "dexscreener", collectedAt)
	snapshot.VolumeH6USD = optionalFloat(pair.Volume.H6, "dexscreener", collectedAt)
	snapshot.Volume24hUSD = optionalFloat(pair.Volume.H24, "dexscreener", collectedAt)
	snapshot.PriceChangeM5 = optionalFloat(pair.PriceChange.M5, "dexscreener", collectedAt)
	snapshot.PriceChangeH1 = optionalFloat(pair.PriceChange.H1, "dexscreener", collectedAt)
	snapshot.PriceChangeH6 = optionalFloat(pair.PriceChange.H6, "dexscreener", collectedAt)
	snapshot.PriceChangeH24 = optionalFloat(pair.PriceChange.H24, "dexscreener", collectedAt)
	mapDexTransactions(pair.Txns.M5, collectedAt, &snapshot.BuysM5, &snapshot.SellsM5)
	mapDexTransactions(pair.Txns.H1, collectedAt, &snapshot.BuysH1, &snapshot.SellsH1)
	mapDexTransactions(pair.Txns.H6, collectedAt, &snapshot.BuysH6, &snapshot.SellsH6)
	mapDexTransactions(pair.Txns.H24, collectedAt, &snapshot.BuysH24, &snapshot.SellsH24)
	if pair.PairCreatedAt != nil {
		created := time.UnixMilli(*pair.PairCreatedAt).UTC()
		snapshot.PairCreatedAt = &created
	}
	if pair.Boosts.Active != nil {
		snapshot.BoostsActive = freshInt(*pair.Boosts.Active, "dexscreener", collectedAt)
	}
	for _, website := range pair.Info.Websites {
		if website.URL != "" {
			snapshot.Links = append(snapshot.Links, domain.Link{Kind: domain.LinkWebsite, URL: website.URL})
		}
	}
	for _, social := range pair.Info.Socials {
		if social.URL == "" {
			continue
		}
		kind := domain.LinkOther
		switch strings.ToLower(social.Type) {
		case "twitter", "x":
			kind = domain.LinkX
		case "telegram":
			kind = domain.LinkTelegram
		}
		snapshot.Links = append(snapshot.Links, domain.Link{Kind: kind, URL: social.URL})
	}
	snapshot.DataQuality = map[string]domain.Quality{
		"price_usd": snapshot.PriceUSD.Quality, "market_cap_usd": snapshot.MarketCapUSD.Quality,
		"fdv_usd": snapshot.FDVUSD.Quality, "liquidity_usd": snapshot.LiquidityUSD.Quality,
		"volume_m5_usd": snapshot.VolumeM5USD.Quality, "volume_h1_usd": snapshot.VolumeH1USD.Quality,
		"volume_h6_usd": snapshot.VolumeH6USD.Quality, "volume_24h_usd": snapshot.Volume24hUSD.Quality,
		"buys_m5": snapshot.BuysM5.Quality, "sells_m5": snapshot.SellsM5.Quality,
		"buyers_m5": snapshot.BuyersM5.Quality, "sellers_m5": snapshot.SellersM5.Quality,
		"buys_h1": snapshot.BuysH1.Quality, "sells_h1": snapshot.SellsH1.Quality,
		"buys_h6": snapshot.BuysH6.Quality, "sells_h6": snapshot.SellsH6.Quality,
		"buys_h24": snapshot.BuysH24.Quality, "sells_h24": snapshot.SellsH24.Quality,
		"price_change_m5": snapshot.PriceChangeM5.Quality, "price_change_h1": snapshot.PriceChangeH1.Quality,
		"price_change_h6": snapshot.PriceChangeH6.Quality, "price_change_h24": snapshot.PriceChangeH24.Quality,
	}
	return snapshot, nil
}

func mapDexTransactions(window *dexTransactionWindow, collectedAt time.Time, buys, sells *domain.DataValue[int]) {
	if window == nil {
		return
	}
	*buys = optionalInt(window.Buys, "dexscreener", collectedAt)
	*sells = optionalInt(window.Sells, "dexscreener", collectedAt)
}

func dexChain(chain domain.Chain) (string, error) {
	switch chain {
	case domain.ChainBSC:
		return "bsc", nil
	case domain.ChainSolana:
		return "solana", nil
	default:
		return "", badResponse("dexscreener", 0, fmt.Errorf("unsupported chain %q", chain))
	}
}

func sameAddress(chain domain.Chain, left, right string) bool {
	return normalizeAddress(chain, left) == normalizeAddress(chain, right)
}

func freshFloat(value float64, source string, collectedAt time.Time) domain.DataValue[float64] {
	return domain.DataValue[float64]{Value: value, Quality: domain.QualityFresh, Source: source, CollectedAt: collectedAt}
}

func optionalFloat(value *float64, source string, collectedAt time.Time) domain.DataValue[float64] {
	if value == nil {
		return missingFloat(source, collectedAt)
	}
	return freshFloat(*value, source, collectedAt)
}

func missingFloat(source string, collectedAt time.Time) domain.DataValue[float64] {
	return domain.DataValue[float64]{Quality: domain.QualityMissing, Source: source, CollectedAt: collectedAt}
}

func freshInt(value int, source string, collectedAt time.Time) domain.DataValue[int] {
	return domain.DataValue[int]{Value: value, Quality: domain.QualityFresh, Source: source, CollectedAt: collectedAt}
}

func missingInt(source string, collectedAt time.Time) domain.DataValue[int] {
	return domain.DataValue[int]{Quality: domain.QualityMissing, Source: source, CollectedAt: collectedAt}
}
