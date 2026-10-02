package markets

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestClassifyOpportunityStageUsesEvidenceBoundaries(t *testing.T) {
	tests := []struct {
		name                       string
		score                      int
		oneHour, completed15m, hot bool
		want                       domain.OpportunityStage
		visible                    bool
	}{
		{name: "hidden", score: 49, want: domain.StageNone},
		{name: "prelaunch", score: 50, want: domain.StagePrelaunch, visible: true},
		{name: "starting", score: 65, want: domain.StageStarting, visible: true},
		{name: "incomplete cannot confirm", score: 80, oneHour: true, want: domain.StageStarting, visible: true},
		{name: "confirmed", score: 80, oneHour: true, completed15m: true, want: domain.StageConfirmed, visible: true},
		{name: "accelerating", score: 90, oneHour: true, completed15m: true, want: domain.StageAccelerating, visible: true},
		{name: "overheated", score: 90, oneHour: true, completed15m: true, hot: true, want: domain.StageOverheated, visible: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, visible := classifyOpportunityStage(tt.score, tt.oneHour, tt.completed15m, tt.hot)
			if got != tt.want || visible != tt.visible {
				t.Fatalf("stage/visible = %q/%v, want %q/%v", got, visible, tt.want, tt.visible)
			}
		})
	}
}

func TestAnalyzeOpportunityIncompleteBreakoutNeverConfirms(t *testing.T) {
	input := opportunityFixture()
	last := len(input.Candles[domain.Timeframe15m]) - 1
	input.Candles[domain.Timeframe15m][last].Confirmed = false
	input.Candles[domain.Timeframe15m][last].Close += 8
	input.Candles[domain.Timeframe15m][last].High += 8
	input.Candles[domain.Timeframe15m][last].VolumeQuote *= 4

	got, visible, err := AnalyzeOpportunity(input)
	if err != nil {
		t.Fatal(err)
	}
	if !visible {
		t.Fatalf("strong preview should be visible: %+v", got)
	}
	if got.Stage == domain.StageConfirmed || got.Stage == domain.StageAccelerating {
		t.Fatalf("incomplete 15m confirmed stage: %+v", got)
	}
	if got.FOMOScore < 50 {
		t.Fatalf("preview FOMO = %d", got.FOMOScore)
	}
}

func TestAnalyzeOpportunityKeepsLastCompletedBreakoutConfirmation(t *testing.T) {
	input := opportunityFixture()
	last := len(input.Candles[domain.Timeframe15m]) - 1
	input.Candles[domain.Timeframe15m][last].Confirmed = false
	input.Candles[domain.Timeframe15m][last-1].Close += 8
	input.Candles[domain.Timeframe15m][last-1].High += 8
	input.Candles[domain.Timeframe15m][last].Close = input.Candles[domain.Timeframe15m][last-1].Close
	input.Candles[domain.Timeframe15m][last].High = input.Candles[domain.Timeframe15m][last-1].High

	got, visible, err := AnalyzeOpportunity(input)
	if err != nil {
		t.Fatal(err)
	}
	if !visible || !got.Evidence[3].Confirmed {
		t.Fatalf("completed breakout confirmation was lost: %+v", got)
	}
}

func TestAnalyzeOpportunityRejectsMissingTimeframe(t *testing.T) {
	input := opportunityFixture()
	delete(input.Candles, domain.Timeframe4H)
	_, _, err := AnalyzeOpportunity(input)
	if !errors.Is(err, ErrIncompleteAnalysis) {
		t.Fatalf("error = %v", err)
	}
}

func TestAnalyzeOpportunityFallingVolumeAndOIDoNotConfirm(t *testing.T) {
	input := opportunityFixture()
	negative := -3.0
	input.OIChange15m = &negative
	input.OIChange1H = &negative
	last := len(input.Candles[domain.Timeframe15m]) - 1
	input.Candles[domain.Timeframe15m][last].VolumeQuote = 1
	got, _, err := AnalyzeOpportunity(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage == domain.StageConfirmed || got.Stage == domain.StageAccelerating {
		t.Fatalf("falling volume/OI confirmed: %+v", got)
	}
}

func TestDetectHighVolatilityUsesATRorDailyChange(t *testing.T) {
	if !detectHighVolatility(8, 100, 0) || !detectHighVolatility(1, 100, -15) || detectHighVolatility(7.99, 100, 14.99) {
		t.Fatal("high-volatility boundaries are wrong")
	}
}

func TestScorePriceAccelerationRequiresOneHourParticipation(t *testing.T) {
	if got := scorePriceAcceleration(2, 1, 0, 1); got >= 25 {
		t.Fatalf("15m-only acceleration scored %d, want below full score", got)
	}
	if got := scorePriceAcceleration(2, 1, 2, 1); got != 25 {
		t.Fatalf("multi-timeframe acceleration scored %d, want 25", got)
	}
}

func TestOneHourSignalsRequireRSIZoneAndMACDFlip(t *testing.T) {
	if rsiRecoveredFromZone([]float64{20, 30, 51}) {
		t.Fatal("deep oversold values without a 35-45 recovery zone must not confirm")
	}
	if !rsiRecoveredFromZone([]float64{34, 40, 51}) {
		t.Fatal("35-45 recovery followed by RSI above 50 should confirm")
	}
	if macdTurnedPositive([]float64{0.1, 0.2, 0.3}) {
		t.Fatal("already-positive MACD continuation must not count as a fresh reversal")
	}
	if !macdTurnedPositive([]float64{-0.3, -0.1, 0.1}) {
		t.Fatal("contracting negative MACD followed by a positive bar should confirm")
	}
}

func opportunityFixture() AnalysisInput {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	frames := map[domain.Timeframe][]domain.Candle{}
	btcFrames := map[domain.Timeframe][]domain.Candle{}
	for _, timeframe := range []domain.Timeframe{domain.Timeframe15m, domain.Timeframe1H, domain.Timeframe4H, domain.Timeframe1D} {
		frames[timeframe] = trendCandles(now, timeframe, 220, 100, 0.04)
		btcFrames[timeframe] = trendCandles(now, timeframe, 220, 100, 0.01)
	}
	positive := 3.0
	asset := UniverseAsset{
		MarketInstrument: domain.MarketInstrument{InstrumentID: "TEST-USDT-SWAP", Symbol: "TEST", ListedAt: now.Add(-800 * 24 * time.Hour), TickSize: 0.01},
		Snapshot:         domain.DerivativesSnapshot{AssetClass: domain.AssetClassCrypto, InstrumentID: "TEST-USDT-SWAP", Symbol: "TEST", PriceUSD: frames[domain.Timeframe15m][219].Close, Change24hPct: 4, Turnover24hUSD: 50_000_000, OpenInterestUSD: 20_000_000, OpenInterestAvailable: true, FundingRateAvailable: true},
	}
	return AnalysisInput{Asset: asset, Candles: frames, BenchmarkCandles: btcFrames, OIChange15m: &positive, OIChange1H: &positive, FundingRate: floatPointer(0.0001), Now: now}
}

func trendCandles(now time.Time, timeframe domain.Timeframe, count int, base, slope float64) []domain.Candle {
	duration := time.Hour
	switch timeframe {
	case domain.Timeframe15m:
		duration = 15 * time.Minute
	case domain.Timeframe4H:
		duration = 4 * time.Hour
	case domain.Timeframe1D:
		duration = 24 * time.Hour
	}
	startedAt := now.Add(-time.Duration(count) * duration)
	candles := make([]domain.Candle, count)
	for index := range candles {
		closePrice := base + slope*float64(index) + math.Sin(float64(index)/4)*1.5
		volume := 1_000_000.0
		if index >= count-3 {
			volume = 2_200_000
		}
		candles[index] = domain.Candle{
			OpenTime: startedAt.Add(time.Duration(index) * duration), CloseTime: startedAt.Add(time.Duration(index+1) * duration),
			Open: closePrice - 0.2, High: closePrice + 1, Low: closePrice - 1, Close: closePrice,
			VolumeBase: volume / closePrice, VolumeQuote: volume, Confirmed: true,
		}
	}
	return candles
}

func floatPointer(value float64) *float64 { return &value }
