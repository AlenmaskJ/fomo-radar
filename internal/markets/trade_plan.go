package markets

import (
	"errors"
	"math"
	"sort"

	"github.com/alen1/fomo-radar/internal/domain"
)

var ErrNoTradePlan = errors.New("no valid trade plan")

type PriceZone struct {
	Low      float64
	High     float64
	Strength int
}

func ClusterLevels(pivots []Pivot, atrValue float64, high bool) []PriceZone {
	if atrValue <= 0 {
		return nil
	}
	prices := make([]float64, 0, len(pivots))
	for _, pivot := range pivots {
		if pivot.High == high && validPositive(pivot.Price) {
			prices = append(prices, pivot.Price)
		}
	}
	sort.Float64s(prices)
	zones := make([]PriceZone, 0)
	for _, price := range prices {
		if len(zones) == 0 || price-zones[len(zones)-1].High > 0.5*atrValue {
			zones = append(zones, PriceZone{Low: price, High: price, Strength: 1})
			continue
		}
		zones[len(zones)-1].High = price
		zones[len(zones)-1].Strength++
	}
	return zones
}

func BuildTradePlan(opportunity domain.Opportunity, candles map[domain.Timeframe][]domain.Candle, indicators map[domain.Timeframe]IndicatorSeries) (domain.TradePlan, error) {
	if !dailyLongAllowed(opportunity.Evidence) {
		return domain.TradePlan{}, ErrNoTradePlan
	}
	fourHour, ok := candles[domain.Timeframe4H]
	series, seriesOK := indicators[domain.Timeframe4H]
	fifteen, fifteenOK := candles[domain.Timeframe15m]
	if !ok || !seriesOK || !fifteenOK || len(fourHour) < 5 || len(fifteen) < 5 {
		return domain.TradePlan{}, ErrNoTradePlan
	}
	atrValue := lastValue(series.ATR14)
	if atrValue <= 0 {
		return domain.TradePlan{}, ErrNoTradePlan
	}
	start := max(0, len(fourHour)-60)
	pivots := FindPivots(fourHour[start:])
	pivots = supportPivots(pivots, series, opportunity.CurrentPrice)
	supports := ClusterLevels(pivots, atrValue, false)
	resistances := ClusterLevels(pivots, atrValue, true)
	support, ok := nearestSupport(supports, opportunity.CurrentPrice)
	if !ok {
		return domain.TradePlan{}, ErrNoTradePlan
	}
	resistances = upperZones(resistances, opportunity.CurrentPrice)
	confirmation := confirmationLevel(fifteen)
	if confirmation <= support.High {
		confirmation = opportunity.CurrentPrice
	}
	return buildTradePlanFromStructure(opportunity, support, resistances, support.Low, atrValue, confirmation)
}

func buildTradePlanFromStructure(opportunity domain.Opportunity, support PriceZone, resistances []PriceZone, structuralLow, atrValue, confirmationPrice float64) (domain.TradePlan, error) {
	minimumScore := 65
	if opportunity.HighVolatility {
		minimumScore = 70
	}
	// 单个枢轴只有一个价格，用半个 ATR 向上扩成可实际分批的支撑区。
	support = expandSupportZone(support, atrValue)
	tick := opportunity.Instrument.TickSize
	if opportunity.FOMOScore < minimumScore || !validPositive(tick) || !validPositive(atrValue) || support.Low <= 0 || support.High < support.Low || support.High >= opportunity.CurrentPrice || len(resistances) == 0 {
		return domain.TradePlan{}, ErrNoTradePlan
	}
	entries := []domain.EntryLevel{
		{Price: roundDownTick(support.High, tick), Weight: 0.30, Condition: "4H支撑区上沿出现15m止跌"},
		{Price: roundDownTick((support.Low+support.High)/2, tick), Weight: 0.25, Condition: "回踩支撑区中部且未破前低"},
		{Price: roundDownTick(support.Low, tick), Weight: 0.25, Condition: "支撑区下沿出现底背离或更高低点"},
	}
	if !(entries[0].Price > entries[1].Price && entries[1].Price > entries[2].Price) {
		return domain.TradePlan{}, ErrNoTradePlan
	}
	confirmation := domain.EntryLevel{Price: roundUpTick(confirmationPrice, tick), Weight: 0.20, Condition: "1H确认后，15m收盘突破结构高点"}
	for _, entry := range append(append([]domain.EntryLevel(nil), entries...), confirmation) {
		if entry.Price <= 0 {
			return domain.TradePlan{}, ErrNoTradePlan
		}
	}
	leftAverage := (entries[0].Price*0.30 + entries[1].Price*0.25 + entries[2].Price*0.25) / 0.80
	fullAverage := entries[0].Price*0.30 + entries[1].Price*0.25 + entries[2].Price*0.25 + confirmation.Price*0.20
	stop := roundDownTick(structuralLow-0.5*atrValue, tick)
	if stop <= 0 || stop >= fullAverage {
		return domain.TradePlan{}, ErrNoTradePlan
	}
	riskDistance := fullAverage - stop
	riskPct := riskDistance / fullAverage * 100
	if riskPct > 15+1e-9 {
		return domain.TradePlan{}, ErrNoTradePlan
	}
	targetOne := roundUpTick(resistances[0].Low, tick)
	if targetOne <= fullAverage || targetOne <= opportunity.CurrentPrice {
		return domain.TradePlan{}, ErrNoTradePlan
	}
	rewardRiskOne := (targetOne - fullAverage) / riskDistance
	if rewardRiskOne < 1.3-1e-9 {
		return domain.TradePlan{}, ErrNoTradePlan
	}
	targetTwo := roundUpTick(fullAverage+2*riskDistance, tick)
	if len(resistances) > 1 && resistances[1].Low > targetOne {
		targetTwo = roundUpTick(resistances[1].Low, tick)
	}
	if targetTwo <= targetOne {
		targetTwo = roundUpTick(targetOne+riskDistance, tick)
	}
	return domain.TradePlan{
		Entries: entries, ConfirmationEntry: confirmation, LeftAverageCost: leftAverage, FullAverageCost: fullAverage,
		StopPrice: stop, RiskPct: riskPct, HighRisk: riskPct > 10,
		Targets: []domain.TargetLevel{
			makeTarget(targetOne, 0.30, fullAverage, riskDistance),
			makeTarget(targetTwo, 0.40, fullAverage, riskDistance),
		},
		TrailingRule: "剩余30%跌破1H最近更高低点或EMA20时退出",
	}, nil
}

func expandSupportZone(zone PriceZone, atrValue float64) PriceZone {
	minimumWidth := 0.5 * atrValue
	if minimumWidth > 0 && zone.High-zone.Low < minimumWidth {
		zone.High = zone.Low + minimumWidth
	}
	return zone
}

func supportPivots(pivots []Pivot, series IndicatorSeries, currentPrice float64) []Pivot {
	result := append([]Pivot(nil), pivots...)
	for _, price := range []float64{lastValue(series.EMA20), lastValue(series.EMA50)} {
		if validPositive(price) && price < currentPrice {
			result = append(result, Pivot{Price: price})
		}
	}
	return result
}

func confirmationLevel(candles []domain.Candle) float64 {
	if len(candles) == 0 {
		return 0
	}
	before := len(candles)
	if candles[len(candles)-1].Confirmed {
		before--
	}
	return priorHigh(candles, before, 20)
}

func makeTarget(price, weight, average, riskDistance float64) domain.TargetLevel {
	return domain.TargetLevel{Price: price, ExitWeight: weight, ReturnPct: (price/average - 1) * 100, RewardRisk: (price - average) / riskDistance}
}

func nearestSupport(zones []PriceZone, current float64) (PriceZone, bool) {
	var result PriceZone
	found := false
	for _, zone := range zones {
		if zone.High < current && (!found || zone.High > result.High) {
			result, found = zone, true
		}
	}
	return result, found
}

func upperZones(zones []PriceZone, current float64) []PriceZone {
	result := make([]PriceZone, 0, len(zones))
	for _, zone := range zones {
		if zone.Low > current {
			result = append(result, zone)
		}
	}
	return result
}

func dailyLongAllowed(evidence []domain.TimeframeEvidence) bool {
	for _, item := range evidence {
		if item.Timeframe == domain.Timeframe1D {
			return item.Confirmed
		}
	}
	return false
}

func roundDownTick(price, tick float64) float64 {
	return cleanTick(math.Floor(price/tick+1e-10)*tick, tick)
}

func roundUpTick(price, tick float64) float64 {
	return cleanTick(math.Ceil(price/tick-1e-10)*tick, tick)
}

func cleanTick(price, tick float64) float64 {
	if tick <= 0 {
		return 0
	}
	digits := max(0, int(math.Ceil(-math.Log10(tick)))+2)
	scale := math.Pow10(min(digits, 12))
	return math.Round(price*scale) / scale
}
