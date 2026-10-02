package markets

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

var ErrIncompleteAnalysis = errors.New("incomplete multi-timeframe analysis")

type AnalysisInput struct {
	Asset              UniverseAsset
	Candles            map[domain.Timeframe][]domain.Candle
	BenchmarkCandles   map[domain.Timeframe][]domain.Candle
	OIChange15m        *float64
	OIChange1H         *float64
	FundingRate        *float64
	PoolMedianReturn1H float64
	Now                time.Time
}

type analyzedFrame struct {
	candles    []domain.Candle
	indicators IndicatorSeries
	last       domain.Candle
}

// AnalyzeOpportunity 按 1D→4H→1H→15m 建立证据链，再判断启动度。
func AnalyzeOpportunity(input AnalysisInput) (domain.Opportunity, bool, error) {
	frames := make(map[domain.Timeframe]analyzedFrame, 4)
	for _, timeframe := range []domain.Timeframe{domain.Timeframe1D, domain.Timeframe4H, domain.Timeframe1H, domain.Timeframe15m} {
		candles := input.Candles[timeframe]
		if len(candles) == 0 {
			return domain.Opportunity{}, false, fmt.Errorf("%w: missing %s", ErrIncompleteAnalysis, timeframe)
		}
		indicators, err := CalculateIndicators(candles)
		if err != nil {
			return domain.Opportunity{}, false, fmt.Errorf("%w: %s: %v", ErrIncompleteAnalysis, timeframe, err)
		}
		index := lastConfirmedIndex(candles)
		if index < 0 {
			return domain.Opportunity{}, false, fmt.Errorf("%w: no completed %s candle", ErrIncompleteAnalysis, timeframe)
		}
		frames[timeframe] = analyzedFrame{candles: candles, indicators: indicators, last: candles[index]}
	}

	fomo, risks, volumeRatio, breakout, falseBreakout := scoreFOMO(input, frames)
	dailyScore, dailyReasons, dailyAllowsLong := scoreDaily(frames[domain.Timeframe1D])
	fourHourScore, fourHourReasons := scoreFourHour(frames[domain.Timeframe4H])
	oneHourScore, oneHourReasons, oneHourConfirmed := scoreOneHour(frames[domain.Timeframe1H], volumeRatio, input.OIChange1H)
	fifteenScore, fifteenReasons, completed15mBreakout := scoreFifteenMinute(input.Candles[domain.Timeframe15m], breakout)
	quality := dailyScore + fourHourScore + oneHourScore + fifteenScore + scoreDerivatives(volumeRatio, input.OIChange15m, input.FundingRate)

	daily := frames[domain.Timeframe1D]
	highVolatility := detectHighVolatility(lastValue(daily.indicators.ATR14), daily.last.Close, input.Asset.Snapshot.Change24hPct)
	overheated := lastValue(frames[domain.Timeframe15m].indicators.RSI14) > 80 && (containsString(risks, "RSI顶背离") || volumeRatio < 1 || fundingAtLeast(input.FundingRate, 0.0005))
	stage, visible := classifyOpportunityStage(fomo, oneHourConfirmed && !falseBreakout, completed15mBreakout, overheated)
	if !visible {
		return domain.Opportunity{}, false, nil
	}
	if !dailyAllowsLong && stage != domain.StagePrelaunch {
		stage = domain.StagePrelaunch
		risks = append(risks, "日线环境禁止追涨")
	}
	sort.Strings(risks)
	latest15m := input.Candles[domain.Timeframe15m][len(input.Candles[domain.Timeframe15m])-1]
	return domain.Opportunity{
		Instrument: input.Asset.MarketInstrument, GeneratedAt: input.Now.UTC(), CurrentPrice: latest15m.Close,
		FOMOScore: fomo, OpportunityScore: quality, Stage: stage, HighVolatility: highVolatility,
		Evidence: []domain.TimeframeEvidence{
			makeEvidence(domain.Timeframe1D, dailyScore, dailyAllowsLong, frames[domain.Timeframe1D], dailyReasons),
			makeEvidence(domain.Timeframe4H, fourHourScore, fourHourScore >= 15, frames[domain.Timeframe4H], fourHourReasons),
			makeEvidence(domain.Timeframe1H, oneHourScore, oneHourConfirmed, frames[domain.Timeframe1H], oneHourReasons),
			makeEvidence(domain.Timeframe15m, fifteenScore, completed15mBreakout, frames[domain.Timeframe15m], fifteenReasons),
		},
		RiskFlags: risks, Action: actionForStage(stage, highVolatility),
	}, true, nil
}

func classifyOpportunityStage(score int, oneHourConfirmed, completed15m, overheated bool) (domain.OpportunityStage, bool) {
	if score < 50 {
		return domain.StageNone, false
	}
	if overheated {
		return domain.StageOverheated, true
	}
	if score < 65 {
		return domain.StagePrelaunch, true
	}
	if score >= 80 && oneHourConfirmed && completed15m {
		if score >= 90 {
			return domain.StageAccelerating, true
		}
		return domain.StageConfirmed, true
	}
	return domain.StageStarting, true
}

func scoreFOMO(input AnalysisInput, frames map[domain.Timeframe]analyzedFrame) (int, []string, float64, bool, bool) {
	frame15 := frames[domain.Timeframe15m]
	candles15 := input.Candles[domain.Timeframe15m]
	latest := candles15[len(candles15)-1]
	atr15 := lastValue(frame15.indicators.ATR14)
	priceMove := latest.Close - candles15[max(0, len(candles15)-5)].Close
	frame1H := frames[domain.Timeframe1H]
	priceScore := scorePriceAcceleration(priceMove, atr15, confirmedPriceMove(frame1H.candles, 4), lastValue(frame1H.indicators.ATR14))
	volumeRatio := normalizedVolumeRatio(latest, frame15.indicators.VolumeMedian20, input.Now)
	volumeScore := 0
	switch {
	case volumeRatio >= 2:
		volumeScore = 25
	case volumeRatio >= 1.5:
		volumeScore = 18
	case volumeRatio >= 1.2:
		volumeScore = 10
	}
	level := priorHigh(candles15, len(candles15)-1, 20)
	breakout := level > 0 && latest.Close > level || completedFifteenBreakout(candles15)
	breakoutScore := 0
	if breakout {
		breakoutScore = 20
	} else if latest.Close > lastValue(frame15.indicators.EMA20) {
		breakoutScore = 10
	}
	oiScore := 0
	if input.OIChange15m != nil && *input.OIChange15m > 0 && priceMove > 0 {
		oiScore = 15
	} else if input.OIChange1H != nil && *input.OIChange1H > 0 && priceMove > 0 {
		oiScore = 10
	}
	assetReturn := recentReturn(frames[domain.Timeframe1H].candles, 4)
	benchmarkReturn := recentReturn(input.BenchmarkCandles[domain.Timeframe1H], 4)
	relativeScore := 0
	if assetReturn > benchmarkReturn && assetReturn > input.PoolMedianReturn1H {
		relativeScore = 15
	} else if assetReturn > 0 {
		relativeScore = 7
	}
	score := priceScore + volumeScore + breakoutScore + oiScore + relativeScore
	risks := make([]string, 0, 4)
	if lastValue(frame15.indicators.RSI14) > 80 && hasBearishDivergence(frame15) {
		score -= 10
		risks = append(risks, "RSI顶背离")
	}
	if atr15 > 0 && latest.Close-lastValue(frame15.indicators.EMA20) > 2*atr15 {
		score -= 10
		risks = append(risks, "偏离EMA20超过2ATR")
	}
	if fundingAtLeast(input.FundingRate, 0.0005) {
		score -= 5
		risks = append(risks, "资金费率拥挤")
	}
	falseBreakout := isFalseBreakout(candles15)
	if falseBreakout {
		score -= 10
		risks = append(risks, "假突破")
	}
	return min(100, max(0, score)), risks, volumeRatio, breakout, falseBreakout
}

func scoreDaily(frame analyzedFrame) (int, []string, bool) {
	closePrice := frame.last.Close
	ema20, ema50 := lastValue(frame.indicators.EMA20), lastValue(frame.indicators.EMA50)
	hist := frame.indicators.MACDHistogram
	strengthening := len(hist) >= 2 && hist[len(hist)-1] >= hist[len(hist)-2]
	switch {
	case closePrice > ema20 && closePrice > ema50 && strengthening:
		return 15, []string{"站上EMA20/50，MACD动能增强"}, true
	case closePrice > min(ema20, ema50):
		return 8, []string{"日线中性，只允许小仓试探"}, true
	default:
		return 0, []string{"跌破EMA50且动能不足，禁止追涨"}, false
	}
}

func scoreFourHour(frame analyzedFrame) (int, []string) {
	score := 5
	reasons := []string{"4H ATR有效，可计算结构风险"}
	if frame.last.Close > lastValue(frame.indicators.EMA20) {
		score += 10
		reasons = append(reasons, "价格位于EMA20上方")
	}
	if lastValue(frame.indicators.EMA20) > lastValue(frame.indicators.EMA50) {
		score += 10
		reasons = append(reasons, "EMA20高于EMA50")
	}
	return score, reasons
}

func scoreOneHour(frame analyzedFrame, volumeRatio float64, oiChange *float64) (int, []string, bool) {
	checks := 0
	reasons := make([]string, 0, 3)
	rsiValues := frame.indicators.RSI14
	if len(rsiValues) >= 20 && rsiRecoveredFromZone(rsiValues[len(rsiValues)-20:]) {
		checks++
		reasons = append(reasons, "RSI从35-45区域回升并站上50")
	}
	hist := frame.indicators.MACDHistogram
	if macdTurnedPositive(hist) {
		checks++
		reasons = append(reasons, "MACD柱连续收缩后翻正")
	}
	pivots := lowPivots(FindPivots(frame.candles))
	volumeOrOI := volumeRatio >= 1.2 || oiChange != nil && *oiChange > 0
	if len(pivots) >= 2 && pivots[len(pivots)-1].Price > pivots[len(pivots)-2].Price && volumeOrOI {
		checks++
		reasons = append(reasons, "形成更高低点且量能或OI配合")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "1H反转条件不足")
	}
	scores := []int{0, 12, 25, 25}
	return scores[checks], reasons, checks >= 2
}

func scorePriceAcceleration(move15m, atr15m, move1H, atr1H float64) int {
	if atr15m <= 0 || atr1H <= 0 {
		return 0
	}
	ratio15m, ratio1H := move15m/atr15m, move1H/atr1H
	switch {
	case ratio15m >= 1 && ratio1H >= 1:
		return 25
	case ratio15m >= 1 && ratio1H >= 0.25 || ratio1H >= 1 && ratio15m > 0:
		return 18
	case ratio15m > 0 && ratio1H > 0:
		return 12
	default:
		return 0
	}
}

func confirmedPriceMove(candles []domain.Candle, periods int) float64 {
	last := lastConfirmedIndex(candles)
	if last < 0 {
		return 0
	}
	return candles[last].Close - candles[max(0, last-periods)].Close
}

func rsiRecoveredFromZone(values []float64) bool {
	if len(values) < 2 || lastValue(values) <= 50 {
		return false
	}
	for _, value := range values[:len(values)-1] {
		if value >= 35 && value <= 45 {
			return true
		}
	}
	return false
}

func macdTurnedPositive(histogram []float64) bool {
	if len(histogram) < 3 {
		return false
	}
	last := len(histogram) - 1
	return histogram[last-2] < histogram[last-1] && histogram[last-1] <= 0 && histogram[last] > 0
}

func scoreFifteenMinute(candles []domain.Candle, previewBreakout bool) (int, []string, bool) {
	// 当前未收盘 K 线只用于预警；确认状态取最近一根已完成 K 线。
	completedBreakout := completedFifteenBreakout(candles)
	if completedBreakout {
		return 20, []string{"已完成15m K线突破结构高点"}, true
	}
	if previewBreakout {
		return 10, []string{"盘中突破预警，等待收盘确认"}, false
	}
	return 5, []string{"接近执行区，尚未突破"}, false
}

func completedFifteenBreakout(candles []domain.Candle) bool {
	confirmedIndex := lastConfirmedIndex(candles)
	if confirmedIndex < 0 {
		return false
	}
	level := priorHigh(candles, confirmedIndex, 20)
	return level > 0 && candles[confirmedIndex].Close > level
}

func scoreDerivatives(volumeRatio float64, oiChange, funding *float64) int {
	score := 0
	if volumeRatio >= 1.2 {
		score += 5
	}
	if oiChange != nil && *oiChange > 0 {
		score += 5
	}
	if !fundingAtLeast(funding, 0.0005) {
		score += 5
	}
	return score
}

func makeEvidence(timeframe domain.Timeframe, score int, confirmed bool, frame analyzedFrame, reasons []string) domain.TimeframeEvidence {
	return domain.TimeframeEvidence{Timeframe: timeframe, Score: score, Confirmed: confirmed, RSI: lastValue(frame.indicators.RSI14), MACDHistogram: lastValue(frame.indicators.MACDHistogram), Reasons: reasons}
}

func detectHighVolatility(atrValue, closePrice, change24h float64) bool {
	return closePrice > 0 && atrValue/closePrice >= 0.08 || math.Abs(change24h) >= 15
}

func normalizedVolumeRatio(candle domain.Candle, medianVolume float64, now time.Time) float64 {
	if medianVolume <= 0 {
		return 0
	}
	volume := candle.VolumeQuote
	if !candle.Confirmed && candle.CloseTime.After(candle.OpenTime) {
		progress := now.Sub(candle.OpenTime).Seconds() / candle.CloseTime.Sub(candle.OpenTime).Seconds()
		progress = min(1, max(0.1, progress))
		volume = min(volume/progress, medianVolume*3)
	}
	return volume / medianVolume
}

func priorHigh(candles []domain.Candle, before, lookback int) float64 {
	high := 0.0
	for index := max(0, before-lookback); index < before; index++ {
		if candles[index].Confirmed && candles[index].High > high {
			high = candles[index].High
		}
	}
	return high
}

func recentReturn(candles []domain.Candle, periods int) float64 {
	completed := make([]domain.Candle, 0, len(candles))
	for _, candle := range candles {
		if candle.Confirmed {
			completed = append(completed, candle)
		}
	}
	if len(completed) <= periods || completed[len(completed)-1-periods].Close <= 0 {
		return 0
	}
	return (completed[len(completed)-1].Close/completed[len(completed)-1-periods].Close - 1) * 100
}

func hasBearishDivergence(frame analyzedFrame) bool {
	last := len(frame.indicators.RSI14) - 1
	if last < 10 || len(frame.candles) <= last {
		return false
	}
	return frame.last.Close > frame.candles[last-10].Close && frame.indicators.RSI14[last] < frame.indicators.RSI14[last-10]
}

func isFalseBreakout(candles []domain.Candle) bool {
	if len(candles) < 22 {
		return false
	}
	last := len(candles) - 1
	level := priorHigh(candles, last-1, 20)
	return candles[last-1].High > level && candles[last].Close < level
}

func fundingAtLeast(value *float64, threshold float64) bool {
	return value != nil && *value >= threshold
}

func lastConfirmedIndex(candles []domain.Candle) int {
	for index := len(candles) - 1; index >= 0; index-- {
		if candles[index].Confirmed {
			return index
		}
	}
	return -1
}

func lastValue(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return values[len(values)-1]
}

func minSlice(values []float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		result = min(result, value)
	}
	return result
}

func lowPivots(pivots []Pivot) []Pivot {
	result := make([]Pivot, 0, len(pivots))
	for _, pivot := range pivots {
		if !pivot.High {
			result = append(result, pivot)
		}
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func actionForStage(stage domain.OpportunityStage, highVolatility bool) string {
	actions := map[domain.OpportunityStage]string{
		domain.StagePrelaunch: "左侧观察，等待15m止跌", domain.StageStarting: "等待回踩分批，不追现价",
		domain.StageConfirmed: "反转已确认，按计划分批执行", domain.StageAccelerating: "趋势加速，只等回踩，不追高",
		domain.StageOverheated: "过热等待，暂不追涨",
	}
	action := actions[stage]
	if highVolatility {
		action += "（高波动）"
	}
	return action
}
