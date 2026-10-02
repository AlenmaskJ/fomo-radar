package markets

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/alen1/fomo-radar/internal/domain"
)

var ErrInsufficientCandles = errors.New("insufficient candles")

type IndicatorSeries struct {
	EMA20          []float64
	EMA50          []float64
	EMA200         []float64
	RSI14          []float64
	MACD           []float64
	MACDSignal     []float64
	MACDHistogram  []float64
	ATR14          []float64
	VolumeMedian20 float64
}

type Pivot struct {
	Index int
	Price float64
	High  bool
}

func CalculateIndicators(candles []domain.Candle) (IndicatorSeries, error) {
	if err := validateCandles(candles); err != nil {
		return IndicatorSeries{}, err
	}
	completed := make([]domain.Candle, 0, len(candles))
	for _, candle := range candles {
		if candle.Confirmed {
			completed = append(completed, candle)
		}
	}
	if len(completed) < 50 {
		return IndicatorSeries{}, ErrInsufficientCandles
	}
	closes := make([]float64, len(completed))
	for index := range completed {
		closes[index] = completed[index].Close
	}
	line, signal, histogram := macd(closes)
	volumes := make([]float64, 20)
	for index := range volumes {
		volumes[index] = completed[len(completed)-20+index].VolumeQuote
	}
	return IndicatorSeries{
		EMA20: ema(closes, 20), EMA50: ema(closes, 50), EMA200: ema(closes, 200),
		RSI14: rsi(closes, 14), MACD: line, MACDSignal: signal, MACDHistogram: histogram,
		ATR14: atr(completed, 14), VolumeMedian20: median(volumes),
	}, nil
}

func validateCandles(candles []domain.Candle) error {
	for index, candle := range candles {
		values := []float64{candle.Open, candle.High, candle.Low, candle.Close, candle.VolumeBase, candle.VolumeQuote}
		for _, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("candle %d contains non-finite value", index)
			}
		}
		if candle.Open <= 0 || candle.High <= 0 || candle.Low <= 0 || candle.Close <= 0 || candle.High < candle.Low || candle.Open > candle.High || candle.Open < candle.Low || candle.Close > candle.High || candle.Close < candle.Low || candle.VolumeBase < 0 || candle.VolumeQuote < 0 {
			return fmt.Errorf("candle %d contains invalid market values", index)
		}
		if index > 0 && !candles[index-1].OpenTime.Before(candle.OpenTime) {
			return fmt.Errorf("candle timestamps are not strictly increasing")
		}
	}
	return nil
}

func ema(values []float64, period int) []float64 {
	result := make([]float64, len(values))
	if period <= 0 || len(values) < period {
		return result
	}
	seed := 0.0
	for _, value := range values[:period] {
		seed += value
	}
	seed /= float64(period)
	result[period-1] = seed
	alpha := 2 / float64(period+1)
	for index := period; index < len(values); index++ {
		result[index] = alpha*values[index] + (1-alpha)*result[index-1]
	}
	return result
}

func rsi(values []float64, period int) []float64 {
	result := make([]float64, len(values))
	if period <= 0 || len(values) <= period {
		return result
	}
	gain, loss := 0.0, 0.0
	for index := 1; index <= period; index++ {
		change := values[index] - values[index-1]
		if change > 0 {
			gain += change
		} else {
			loss -= change
		}
	}
	averageGain := gain / float64(period)
	averageLoss := loss / float64(period)
	result[period] = rsiValue(averageGain, averageLoss)
	for index := period + 1; index < len(values); index++ {
		change := values[index] - values[index-1]
		currentGain, currentLoss := 0.0, 0.0
		if change > 0 {
			currentGain = change
		} else {
			currentLoss = -change
		}
		averageGain = (averageGain*float64(period-1) + currentGain) / float64(period)
		averageLoss = (averageLoss*float64(period-1) + currentLoss) / float64(period)
		result[index] = rsiValue(averageGain, averageLoss)
	}
	return result
}

func rsiValue(gain, loss float64) float64 {
	if gain == 0 && loss == 0 {
		return 50
	}
	if loss == 0 {
		return 100
	}
	if gain == 0 {
		return 0
	}
	return 100 - 100/(1+gain/loss)
}

func macd(values []float64) ([]float64, []float64, []float64) {
	fast := ema(values, 12)
	slow := ema(values, 26)
	line := make([]float64, len(values))
	signal := make([]float64, len(values))
	histogram := make([]float64, len(values))
	if len(values) < 26 {
		return line, signal, histogram
	}
	for index := 25; index < len(values); index++ {
		line[index] = fast[index] - slow[index]
	}
	if len(values) >= 34 {
		seed := 0.0
		for index := 25; index < 34; index++ {
			seed += line[index]
		}
		signal[33] = seed / 9
		alpha := 2.0 / 10
		for index := 34; index < len(values); index++ {
			signal[index] = alpha*line[index] + (1-alpha)*signal[index-1]
		}
		for index := 33; index < len(values); index++ {
			histogram[index] = line[index] - signal[index]
		}
	}
	return line, signal, histogram
}

func atr(candles []domain.Candle, period int) []float64 {
	result := make([]float64, len(candles))
	if period <= 0 || len(candles) < period {
		return result
	}
	trueRanges := make([]float64, len(candles))
	for index, candle := range candles {
		trueRanges[index] = candle.High - candle.Low
		if index > 0 {
			trueRanges[index] = max(trueRanges[index], math.Abs(candle.High-candles[index-1].Close), math.Abs(candle.Low-candles[index-1].Close))
		}
	}
	seed := 0.0
	for _, value := range trueRanges[:period] {
		seed += value
	}
	result[period-1] = seed / float64(period)
	for index := period; index < len(candles); index++ {
		result[index] = (result[index-1]*float64(period-1) + trueRanges[index]) / float64(period)
	}
	return result
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle]
	}
	return (ordered[middle-1] + ordered[middle]) / 2
}

func FindPivots(candles []domain.Candle) []Pivot {
	pivots := make([]Pivot, 0)
	for index := 2; index+2 < len(candles); index++ {
		confirmed := true
		for offset := -2; offset <= 2; offset++ {
			confirmed = confirmed && candles[index+offset].Confirmed
		}
		if !confirmed {
			continue
		}
		center := candles[index]
		if center.Low < candles[index-2].Low && center.Low < candles[index-1].Low && center.Low < candles[index+1].Low && center.Low < candles[index+2].Low {
			pivots = append(pivots, Pivot{Index: index, Price: center.Low})
		}
		if center.High > candles[index-2].High && center.High > candles[index-1].High && center.High > candles[index+1].High && center.High > candles[index+2].High {
			pivots = append(pivots, Pivot{Index: index, Price: center.High, High: true})
		}
	}
	return pivots
}
