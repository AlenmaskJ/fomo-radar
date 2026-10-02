package scanner

import (
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

// MergeMarket overlays Dex observations onto Gecko discovery evidence.
func MergeMarket(candidate domain.Candidate, dex domain.MarketSnapshot, dexOK bool, at time.Time) domain.MarketSnapshot {
	market := candidate.DiscoveryMarket
	market.TokenID = candidate.ID
	market.DiscoveryPoolAddress = candidate.DiscoveryPoolAddress
	market.DiscoveryPoolCreatedAt = candidate.DiscoveryPoolCreatedAt
	market.CandidateOrigin = candidate.Origin
	market.MomentumSources = append([]domain.MomentumSource(nil), candidate.MomentumSources...)
	market.TriggerPoolAddress = candidate.TriggerPoolAddress
	market.TriggerPoolCreatedAt = candidate.TriggerPoolCreatedAt
	if market.CollectedAt.IsZero() {
		market.CollectedAt = at
	}
	if market.PairAddress == "" {
		market.PairAddress = candidate.DiscoveryPoolAddress
	}
	ensureParticipantSemantics(&market, at)

	if !dexOK {
		if candidate.DiscoveryPoolAddress != "" {
			market.PairAddress = candidate.DiscoveryPoolAddress
		} else if market.PairAddress == "" {
			market.PairAddress = candidate.TriggerPoolAddress
		}
		market.MarketDataSource = domain.MarketDataSourceGecko
		market.MarketDataQuality = domain.MarketDataQualityPartial
		refreshMarketQuality(&market)
		return market
	}

	hadGecko := hasMarketEvidence(market)
	market.PriceUSD = prefer(dex.PriceUSD, market.PriceUSD)
	market.MarketCapUSD = prefer(dex.MarketCapUSD, market.MarketCapUSD)
	market.FDVUSD = prefer(dex.FDVUSD, market.FDVUSD)
	market.LiquidityUSD = prefer(dex.LiquidityUSD, market.LiquidityUSD)
	market.VolumeM5USD = prefer(dex.VolumeM5USD, market.VolumeM5USD)
	market.VolumeH1USD = prefer(dex.VolumeH1USD, market.VolumeH1USD)
	market.VolumeH6USD = prefer(dex.VolumeH6USD, market.VolumeH6USD)
	market.Volume24hUSD = prefer(dex.Volume24hUSD, market.Volume24hUSD)
	market.BuysM5 = prefer(dex.BuysM5, market.BuysM5)
	market.SellsM5 = prefer(dex.SellsM5, market.SellsM5)
	market.BuysH1 = prefer(dex.BuysH1, market.BuysH1)
	market.SellsH1 = prefer(dex.SellsH1, market.SellsH1)
	market.BuysH6 = prefer(dex.BuysH6, market.BuysH6)
	market.SellsH6 = prefer(dex.SellsH6, market.SellsH6)
	market.BuysH24 = prefer(dex.BuysH24, market.BuysH24)
	market.SellsH24 = prefer(dex.SellsH24, market.SellsH24)
	market.PriceChangeM5 = prefer(dex.PriceChangeM5, market.PriceChangeM5)
	market.PriceChangeH1 = prefer(dex.PriceChangeH1, market.PriceChangeH1)
	market.PriceChangeH6 = prefer(dex.PriceChangeH6, market.PriceChangeH6)
	market.PriceChangeH24 = prefer(dex.PriceChangeH24, market.PriceChangeH24)
	market.Holders = prefer(dex.Holders, market.Holders)
	market.BoostsActive = prefer(dex.BoostsActive, market.BoostsActive)
	if len(dex.Links) > 0 {
		market.Links = append([]domain.Link(nil), dex.Links...)
	}
	if dex.PairAddress != "" {
		market.PairAddress = dex.PairAddress
	}
	market.SelectedPairCreatedAt = dex.PairCreatedAt
	market.PairCreatedAt = dex.PairCreatedAt
	if dex.CollectedAt.After(market.CollectedAt) {
		market.CollectedAt = dex.CollectedAt
	}
	market.MarketDataSource = domain.MarketDataSourceDex
	if hadGecko {
		market.MarketDataSource = domain.MarketDataSourceMerged
	}
	market.MarketDataQuality = domain.MarketDataQualityFull
	ensureParticipantSemantics(&market, at)
	refreshMarketQuality(&market)
	return market
}

func prefer[T any](primary, fallback domain.DataValue[T]) domain.DataValue[T] {
	if dataAvailable(primary.Quality) {
		return primary
	}
	return fallback
}

func hasMarketEvidence(market domain.MarketSnapshot) bool {
	for _, quality := range []domain.Quality{
		market.PriceUSD.Quality, market.MarketCapUSD.Quality, market.FDVUSD.Quality,
		market.LiquidityUSD.Quality, market.VolumeM5USD.Quality, market.VolumeH1USD.Quality,
		market.VolumeH6USD.Quality, market.Volume24hUSD.Quality,
		market.BuysM5.Quality, market.SellsM5.Quality, market.PriceChangeH1.Quality,
	} {
		if dataAvailable(quality) {
			return true
		}
	}
	return false
}

func ensureParticipantSemantics(market *domain.MarketSnapshot, at time.Time) {
	if market.BuyersM5.Quality == "" {
		market.BuyersM5 = domain.DataValue[int]{Quality: domain.QualityMissing, CollectedAt: at}
	}
	if market.SellersM5.Quality == "" {
		market.SellersM5 = domain.DataValue[int]{Quality: domain.QualityMissing, CollectedAt: at}
	}
}

func refreshMarketQuality(market *domain.MarketSnapshot) {
	if market.DataQuality == nil {
		market.DataQuality = map[string]domain.Quality{}
	}
	market.DataQuality["price_usd"] = canonicalQuality(market.PriceUSD.Quality)
	market.DataQuality["market_cap_usd"] = canonicalQuality(market.MarketCapUSD.Quality)
	market.DataQuality["fdv_usd"] = canonicalQuality(market.FDVUSD.Quality)
	market.DataQuality["liquidity_usd"] = canonicalQuality(market.LiquidityUSD.Quality)
	market.DataQuality["volume_m5_usd"] = canonicalQuality(market.VolumeM5USD.Quality)
	market.DataQuality["volume_h1_usd"] = canonicalQuality(market.VolumeH1USD.Quality)
	market.DataQuality["volume_h6_usd"] = canonicalQuality(market.VolumeH6USD.Quality)
	market.DataQuality["volume_24h_usd"] = canonicalQuality(market.Volume24hUSD.Quality)
	market.DataQuality["buys_m5"] = canonicalQuality(market.BuysM5.Quality)
	market.DataQuality["sells_m5"] = canonicalQuality(market.SellsM5.Quality)
	market.DataQuality["buys_h1"] = canonicalQuality(market.BuysH1.Quality)
	market.DataQuality["sells_h1"] = canonicalQuality(market.SellsH1.Quality)
	market.DataQuality["buys_h6"] = canonicalQuality(market.BuysH6.Quality)
	market.DataQuality["sells_h6"] = canonicalQuality(market.SellsH6.Quality)
	market.DataQuality["buys_h24"] = canonicalQuality(market.BuysH24.Quality)
	market.DataQuality["sells_h24"] = canonicalQuality(market.SellsH24.Quality)
	market.DataQuality["aggregate_buyers_m5"] = canonicalQuality(market.AggregateBuyersM5.Quality)
	market.DataQuality["aggregate_sellers_m5"] = canonicalQuality(market.AggregateSellersM5.Quality)
	market.DataQuality["aggregate_buyers_h1"] = canonicalQuality(market.AggregateBuyersH1.Quality)
	market.DataQuality["aggregate_sellers_h1"] = canonicalQuality(market.AggregateSellersH1.Quality)
	market.DataQuality["aggregate_buyers_h6"] = canonicalQuality(market.AggregateBuyersH6.Quality)
	market.DataQuality["aggregate_sellers_h6"] = canonicalQuality(market.AggregateSellersH6.Quality)
	market.DataQuality["aggregate_buyers_h24"] = canonicalQuality(market.AggregateBuyersH24.Quality)
	market.DataQuality["aggregate_sellers_h24"] = canonicalQuality(market.AggregateSellersH24.Quality)
	market.DataQuality["buyers_m5"] = canonicalQuality(market.BuyersM5.Quality)
	market.DataQuality["sellers_m5"] = canonicalQuality(market.SellersM5.Quality)
	market.DataQuality["price_change_h1"] = canonicalQuality(market.PriceChangeH1.Quality)
	market.DataQuality["price_change_h6"] = canonicalQuality(market.PriceChangeH6.Quality)
	market.DataQuality["price_change_h24"] = canonicalQuality(market.PriceChangeH24.Quality)
}
