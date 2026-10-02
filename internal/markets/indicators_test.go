package markets

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestCalculateIndicatorsRejectsShortSeries(t *testing.T) {
	_, err := CalculateIndicators(indicatorCandles(49, func(int) float64 { return 100 }))
	if !errors.Is(err, ErrInsufficientCandles) {
		t.Fatalf("error = %v", err)
	}
}

func TestCalculateIndicatorsAcceptsFiftyCompletedCandles(t *testing.T) {
	got, err := CalculateIndicators(indicatorCandles(50, func(index int) float64 { return 100 + float64(index) }))
	if err != nil {
		t.Fatal(err)
	}
	if got.EMA50[49] == 0 || len(got.RSI14) != 50 {
		t.Fatalf("indicators were not calculated at the 50-candle boundary: %+v", got)
	}
}

func TestCalculateIndicatorsIgnoresIncompleteTail(t *testing.T) {
	candles := indicatorCandles(201, func(index int) float64 { return 100 + float64(index) })
	candles[200].Close = 10000
	candles[200].High = 10001
	candles[200].Confirmed = false
	got, err := CalculateIndicators(candles)
	if err != nil {
		t.Fatal(err)
	}
	if got.EMA20[len(got.EMA20)-1] >= 1000 {
		t.Fatalf("incomplete candle affected EMA: %v", got.EMA20[len(got.EMA20)-1])
	}
}

func TestEMAUsesSMASeed(t *testing.T) {
	got := ema([]float64{1, 2, 3, 4, 5}, 3)
	want := []float64{0, 0, 2, 3, 4}
	for index := range want {
		if math.Abs(got[index]-want[index]) > 1e-9 {
			t.Fatalf("EMA[%d] = %v, want %v", index, got[index], want[index])
		}
	}
}

func TestRSIHandlesFlatAndNoLossesWithoutNaN(t *testing.T) {
	flat := rsi([]float64{10, 10, 10, 10}, 3)
	rising := rsi([]float64{1, 2, 3, 4}, 3)
	if flat[3] != 50 || rising[3] != 100 || math.IsNaN(flat[3]) || math.IsNaN(rising[3]) {
		t.Fatalf("flat/rising RSI = %v/%v", flat[3], rising[3])
	}
}

func TestMACDLineSignalHistogramStayConsistent(t *testing.T) {
	values := make([]float64, 50)
	for index := range values {
		values[index] = float64(index + 1)
	}
	line, signal, histogram := macd(values)
	last := len(values) - 1
	if math.Abs((line[last]-signal[last])-histogram[last]) > 1e-9 || line[last] <= 0 {
		t.Fatalf("MACD = line %v signal %v histogram %v", line[last], signal[last], histogram[last])
	}
}

func TestATRUsesTrueRangeAndWilderSeed(t *testing.T) {
	candles := []domain.Candle{
		{High: 11, Low: 9, Close: 10},
		{High: 12, Low: 10, Close: 11},
		{High: 13, Low: 11, Close: 12},
		{High: 15, Low: 12, Close: 14},
	}
	got := atr(candles, 3)
	if got[2] != 2 || math.Abs(got[3]-2.3333333333333335) > 1e-9 {
		t.Fatalf("ATR = %v", got)
	}
}

func TestMedianHandlesEvenLengthAndZeroVolume(t *testing.T) {
	if got := median([]float64{1, 4, 2, 3}); got != 2.5 {
		t.Fatalf("median = %v", got)
	}
	if got := median([]float64{0, 0}); got != 0 {
		t.Fatalf("zero median = %v", got)
	}
}

func TestFindPivotsUsesFiveConfirmedCandles(t *testing.T) {
	candles := indicatorCandles(6, func(int) float64 { return 5 })
	lows := []float64{4, 3, 1, 3, 4, 0}
	highs := []float64{6, 7, 9, 7, 6, 10}
	for index := range candles {
		candles[index].Low = lows[index]
		candles[index].High = highs[index]
	}
	candles[5].Confirmed = false
	pivots := FindPivots(candles)
	if len(pivots) != 2 || pivots[0].Index != 2 || pivots[1].Index != 2 || pivots[0].High || !pivots[1].High {
		t.Fatalf("pivots = %+v", pivots)
	}
}

func TestCalculateIndicatorsRejectsNonIncreasingTimesAndInvalidPrices(t *testing.T) {
	candles := indicatorCandles(220, func(int) float64 { return 100 })
	candles[10].OpenTime = candles[9].OpenTime
	if _, err := CalculateIndicators(candles); err == nil {
		t.Fatal("duplicate time should fail")
	}
	candles = indicatorCandles(220, func(int) float64 { return 100 })
	candles[10].High = math.Inf(1)
	if _, err := CalculateIndicators(candles); err == nil {
		t.Fatal("non-finite price should fail")
	}
}

func indicatorCandles(count int, closeAt func(int) float64) []domain.Candle {
	startedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := make([]domain.Candle, count)
	for index := range candles {
		closePrice := closeAt(index)
		candles[index] = domain.Candle{
			OpenTime: startedAt.Add(time.Duration(index) * time.Hour), CloseTime: startedAt.Add(time.Duration(index+1) * time.Hour),
			Open: closePrice, High: closePrice + 1, Low: closePrice - 1, Close: closePrice,
			VolumeBase: float64(index + 1), VolumeQuote: float64(index+1) * closePrice, Confirmed: true,
		}
	}
	return candles
}
