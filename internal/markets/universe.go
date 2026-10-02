package markets

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/providers"
)

type UniverseConfig struct {
	AssetClass            domain.AssetClass
	MinimumAge            time.Duration
	MinimumTurnover       float64
	MinimumOI             float64
	MaximumSize           int
	AllowedSymbols        map[string]struct{}
	ExcludedInstrumentIDs map[string]struct{}
}

type UniverseAsset struct {
	domain.MarketInstrument
	Snapshot       domain.DerivativesSnapshot
	LiquidityScore float64
}

func DefaultUniverseConfig() UniverseConfig {
	return UniverseConfig{
		AssetClass: domain.AssetClassCrypto,
		MinimumAge: 365 * 24 * time.Hour, MinimumTurnover: 10_000_000,
		MinimumOI: 5_000_000, MaximumSize: 60,
	}
}

// BuildUniverse 先执行硬门槛，再按相对流动性排序；不为凑数放宽门槛。
func BuildUniverse(now time.Time, result providers.OKXMarketResult, cfg UniverseConfig) []UniverseAsset {
	if !cfg.AssetClass.Valid() || cfg.MinimumAge <= 0 || cfg.MinimumTurnover < 0 || cfg.MinimumOI < 0 || cfg.MaximumSize <= 0 {
		return nil
	}
	assets := make([]UniverseAsset, 0, min(len(result.Snapshots), cfg.MaximumSize))
	seen := make(map[string]struct{}, len(result.Snapshots))
	for _, snapshot := range result.Snapshots {
		instrument, ok := result.Instruments[snapshot.InstrumentID]
		if !ok || !strings.HasSuffix(snapshot.InstrumentID, "-USDT-SWAP") || snapshot.AssetClass != cfg.AssetClass || !validPositive(snapshot.PriceUSD) || !validPositive(instrument.TickSize) || instrument.ListedAt.IsZero() {
			continue
		}
		if _, excluded := cfg.ExcludedInstrumentIDs[snapshot.InstrumentID]; excluded {
			continue
		}
		if len(cfg.AllowedSymbols) > 0 {
			if _, allowed := cfg.AllowedSymbols[snapshot.Symbol]; !allowed {
				continue
			}
		}
		if now.Sub(instrument.ListedAt) < cfg.MinimumAge || !validPositive(snapshot.Turnover24hUSD) || snapshot.Turnover24hUSD < cfg.MinimumTurnover || !snapshot.OpenInterestAvailable || !validPositive(snapshot.OpenInterestUSD) || snapshot.OpenInterestUSD < cfg.MinimumOI {
			continue
		}
		if _, exists := seen[snapshot.InstrumentID]; exists {
			continue
		}
		seen[snapshot.InstrumentID] = struct{}{}
		assets = append(assets, UniverseAsset{MarketInstrument: instrument, Snapshot: snapshot})
	}
	if len(assets) == 0 {
		return nil
	}
	turnovers := make([]float64, len(assets))
	ois := make([]float64, len(assets))
	ages := make([]float64, len(assets))
	for index, asset := range assets {
		turnovers[index] = asset.Snapshot.Turnover24hUSD
		ois[index] = asset.Snapshot.OpenInterestUSD
		ages[index] = now.Sub(asset.ListedAt).Hours()
	}
	for index := range assets {
		integrity := 0.0
		if assets[index].Snapshot.FundingRateAvailable {
			integrity = 100
		}
		assets[index].LiquidityScore = percentile(turnovers, turnovers[index])*0.50 + percentile(ois, ois[index])*0.35 + percentile(ages, ages[index])*0.10 + integrity*0.05
	}
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].LiquidityScore != assets[j].LiquidityScore {
			return assets[i].LiquidityScore > assets[j].LiquidityScore
		}
		if assets[i].Snapshot.Turnover24hUSD != assets[j].Snapshot.Turnover24hUSD {
			return assets[i].Snapshot.Turnover24hUSD > assets[j].Snapshot.Turnover24hUSD
		}
		return assets[i].InstrumentID < assets[j].InstrumentID
	})
	return assets[:min(len(assets), cfg.MaximumSize)]
}

func percentile(values []float64, target float64) float64 {
	if len(values) <= 1 {
		return 100
	}
	lower := 0
	for _, value := range values {
		if value < target {
			lower++
		}
	}
	return float64(lower) / float64(len(values)-1) * 100
}

func validPositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
