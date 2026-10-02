// Package score implements the deterministic, versioned FOMO score.
package score

import (
	"encoding/json"
	"math"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

// Version is the immutable identifier for this scoring configuration.
const Version = "FOMO_SCORE_V1.0"

const (
	buyerVelocityFactor     = "buyer_velocity"
	volumeVelocityFactor    = "volume_velocity"
	buyPressureFactor       = "buy_pressure"
	participantGrowthFactor = "participant_growth"
	socialMomentumFactor    = "social_momentum"
	liquidityQualityFactor  = "liquidity_quality"
)

var coreEvidenceFactors = [...]string{
	buyerVelocityFactor,
	volumeVelocityFactor,
	buyPressureFactor,
	participantGrowthFactor,
	liquidityQualityFactor,
}

// Input contains market observations and score-specific evidence not carried by
// a market snapshot. Ratio inputs are marked missing through DataValue.Quality.
type Input struct {
	Current              domain.MarketSnapshot
	Previous             []domain.MarketSnapshot
	BuyerVelocity        domain.DataValue[float64]
	VolumeVelocity       domain.DataValue[float64]
	BuyVolumeM5USD       domain.DataValue[float64]
	SellVolumeM5USD      domain.DataValue[float64]
	ParticipantGrowth    domain.DataValue[float64]
	SocialMomentum       domain.DataValue[float64]
	TokenAge             domain.DataValue[time.Duration]
	Top10HolderPercent   domain.DataValue[float64]
	ConcentratedActivity domain.DataValue[bool]
	SuspectedBotActivity domain.DataValue[bool]
	StrongDevSelling     domain.DataValue[bool]
}

// Engine evaluates one immutable FOMO Score configuration without external state.
type Engine struct{ cfg config }

// NewEngine returns the immutable V1 engine.
func NewEngine() Engine { return Engine{cfg: defaultConfig()} }

// Version returns the immutable identifier paired with ConfigJSON.
func (e Engine) Version() string { return e.effectiveConfig().Version }

type velocityThresholds struct {
	Full    float64 `json:"full"`
	High    float64 `json:"high"`
	Medium  float64 `json:"medium"`
	Low     float64 `json:"low"`
	Minimum float64 `json:"minimum"`
}

type pointMultipliers struct {
	Full    float64 `json:"full"`
	High    float64 `json:"high"`
	Medium  float64 `json:"medium"`
	Low     float64 `json:"low"`
	Minimum float64 `json:"minimum"`
}

type liquidityThresholds struct {
	Full    float64 `json:"full"`
	High    float64 `json:"high"`
	Medium  float64 `json:"medium"`
	Low     float64 `json:"low"`
	Minimum float64 `json:"minimum"`
}

type factorConfig struct {
	Maximum            float64              `json:"maximum"`
	VelocityThresholds *velocityThresholds  `json:"velocity_thresholds,omitempty"`
	RatioThresholds    *liquidityThresholds `json:"ratio_thresholds,omitempty"`
	PointMultipliers   *pointMultipliers    `json:"point_multipliers,omitempty"`
}

type ageConfig struct {
	Under30m  float64 `json:"under_30m"`
	Under60m  float64 `json:"under_60m"`
	Under120m float64 `json:"under_120m"`
	Later     float64 `json:"later"`
}

type ageCutoffs struct {
	Minutes30  float64 `json:"minutes_30"`
	Minutes60  float64 `json:"minutes_60"`
	Minutes120 float64 `json:"minutes_120"`
}

type riskConfig struct {
	Top10Over80          float64 `json:"top10_over_80"`
	Top10Over60          float64 `json:"top10_over_60"`
	LiquidityBelow2000   float64 `json:"liquidity_below_2000"`
	Liquidity2000To5000  float64 `json:"liquidity_2000_to_5000"`
	ConcentratedActivity float64 `json:"concentrated_activity"`
	SuspectedBotActivity float64 `json:"suspected_bot_activity"`
	StrongDevSelling     float64 `json:"strong_dev_selling"`
}

type riskThresholds struct {
	Top10HighPercent    float64 `json:"top10_high_percent"`
	Top10MediumPercent  float64 `json:"top10_medium_percent"`
	LiquidityVeryLowUSD float64 `json:"liquidity_very_low_usd"`
	LiquidityLowUSD     float64 `json:"liquidity_low_usd"`
}

type tierConfig struct {
	Breakout   float64 `json:"breakout"`
	FastRising float64 `json:"fast_rising"`
	Watch      float64 `json:"watch"`
}

type clampConfig struct {
	Minimum float64 `json:"minimum"`
	Maximum float64 `json:"maximum"`
}

type config struct {
	Version           string                  `json:"version"`
	Factors           map[string]factorConfig `json:"factors"`
	AgeBonus          ageConfig               `json:"age_bonus"`
	AgeCutoffsMinutes ageCutoffs              `json:"age_cutoffs_minutes"`
	RiskPenalties     riskConfig              `json:"risk_penalties"`
	RiskThresholds    riskThresholds          `json:"risk_thresholds"`
	Tiers             tierConfig              `json:"tiers"`
	Clamp             clampConfig             `json:"clamp"`
}

func defaultConfig() config {
	velocity := velocityThresholds{Full: 4, High: 3, Medium: 2, Low: 1.5, Minimum: 1.1}
	multipliers := pointMultipliers{Full: 1, High: .85, Medium: .65, Low: .45, Minimum: .25}
	velocityFactor := func(maximum float64) factorConfig {
		thresholdsCopy := velocity
		multipliersCopy := multipliers
		return factorConfig{Maximum: maximum, VelocityThresholds: &thresholdsCopy, PointMultipliers: &multipliersCopy}
	}
	liquidity := liquidityThresholds{Full: .20, High: .10, Medium: .05, Low: .02, Minimum: .01}
	liquidityMultipliers := multipliers
	return config{
		Version: Version,
		Factors: map[string]factorConfig{
			buyerVelocityFactor:     velocityFactor(25),
			volumeVelocityFactor:    velocityFactor(20),
			buyPressureFactor:       velocityFactor(15),
			participantGrowthFactor: velocityFactor(15),
			socialMomentumFactor:    velocityFactor(15),
			liquidityQualityFactor:  {Maximum: 10, RatioThresholds: &liquidity, PointMultipliers: &liquidityMultipliers},
		},
		AgeBonus:          ageConfig{Under30m: 10, Under60m: 6, Under120m: 2, Later: 0},
		AgeCutoffsMinutes: ageCutoffs{Minutes30: 30, Minutes60: 60, Minutes120: 120},
		RiskPenalties: riskConfig{
			Top10Over80: 30, Top10Over60: 15,
			LiquidityBelow2000: 20, Liquidity2000To5000: 10,
			ConcentratedActivity: 15, SuspectedBotActivity: 15, StrongDevSelling: 20,
		},
		RiskThresholds: riskThresholds{
			Top10HighPercent: 80, Top10MediumPercent: 60,
			LiquidityVeryLowUSD: 2000, LiquidityLowUSD: 5000,
		},
		Tiers: tierConfig{Breakout: 85, FastRising: 70, Watch: 55},
		Clamp: clampConfig{Minimum: 0, Maximum: 100},
	}
}

// Evaluate returns a complete explanation of every score contribution.
func (e Engine) Evaluate(input Input) domain.ScoreBreakdown {
	cfg := e.effectiveConfig()
	factors := evaluateFactors(input, cfg)

	raw, availableMaximum := 0.0, 0.0
	for _, factor := range factors {
		raw += factor.Points
		if factor.Available {
			availableMaximum += factor.Maximum
		}
	}
	normalized := 0.0
	if availableMaximum > 0 {
		normalized = raw / availableMaximum * cfg.Clamp.Maximum
	}
	age := ageBonus(input.TokenAge, cfg)
	penalty, flags := riskPenalty(input, cfg)
	final := clamp(normalized+age-penalty, cfg.Clamp.Minimum, cfg.Clamp.Maximum)

	return applyEvidenceGate(domain.ScoreBreakdown{
		Version: cfg.Version, Factors: factors,
		RawMomentum: raw, AvailableMaximum: availableMaximum, NormalizedMomentum: normalized,
		AgeBonus: age, RiskPenalty: penalty, RiskFlags: flags, EvidenceQuality: evidenceQuality(input),
		Final: final, Tier: tier(final, cfg),
	})
}

func applyEvidenceGate(breakdown domain.ScoreBreakdown) domain.ScoreBreakdown {
	available, confidence := evidenceConfidence(breakdown.Factors)
	breakdown.RawScore = breakdown.Final
	breakdown.RawTier = breakdown.Tier
	breakdown.EvidenceAvailable = available
	breakdown.EvidenceTotal = len(coreEvidenceFactors)
	breakdown.EvidenceConfidence = confidence
	breakdown.EffectiveTier = capTier(breakdown.RawTier, confidence)
	breakdown.Tier = breakdown.EffectiveTier
	return breakdown
}

func evidenceConfidence(factors map[string]domain.FactorScore) (int, domain.EvidenceConfidence) {
	available := 0
	for _, name := range coreEvidenceFactors {
		if factors[name].Available {
			available++
		}
	}
	switch available {
	case 5:
		return available, domain.EvidenceConfidenceHigh
	case 4:
		return available, domain.EvidenceConfidenceGood
	case 3:
		return available, domain.EvidenceConfidenceMedium
	default:
		return available, domain.EvidenceConfidenceLow
	}
}

func capTier(raw domain.Tier, confidence domain.EvidenceConfidence) domain.Tier {
	maximum := raw
	switch confidence {
	case domain.EvidenceConfidenceMedium:
		maximum = domain.TierFastRising
	case domain.EvidenceConfidenceLow:
		maximum = domain.TierWatch
	}
	if tierRank(raw) > tierRank(maximum) {
		return maximum
	}
	return raw
}

func tierRank(value domain.Tier) int {
	switch value {
	case domain.TierBreakout:
		return 3
	case domain.TierFastRising:
		return 2
	case domain.TierWatch:
		return 1
	default:
		return 0
	}
}

func evaluateFactors(input Input, cfg config) map[string]domain.FactorScore {
	previous, hasPrevious := previousSnapshot(input.Previous)

	buyerRatio, buyerAvailable := input.BuyerVelocity.Value, available(input.BuyerVelocity.Quality)
	if !buyerAvailable {
		buyerRatio, buyerAvailable = ratioInt(input.Current.BuyersM5, previous.BuyersM5, hasPrevious)
	}
	volumeRatio, volumeAvailable := input.VolumeVelocity.Value, available(input.VolumeVelocity.Quality)
	if !volumeAvailable {
		volumeRatio, volumeAvailable = ratioFloat(input.Current.VolumeM5USD, previous.VolumeM5USD, hasPrevious)
	}

	txRatio, txAvailable := ratioInt(input.Current.BuysM5, input.Current.SellsM5, true)
	volumePressureRatio, volumePressureAvailable := ratioFloat(input.BuyVolumeM5USD, input.SellVolumeM5USD, true)
	pressurePoints, pressureAvailable := combinedRatioPoints(txRatio, txAvailable, volumePressureRatio, volumePressureAvailable, cfg.Factors[buyPressureFactor])

	liquidityRatio, liquidityAvailable := ratioFloat(input.Current.LiquidityUSD, input.Current.MarketCapUSD, true)
	return map[string]domain.FactorScore{
		buyerVelocityFactor:     factorFromRatio(buyerRatio, buyerAvailable, cfg.Factors[buyerVelocityFactor]),
		volumeVelocityFactor:    factorFromRatio(volumeRatio, volumeAvailable, cfg.Factors[volumeVelocityFactor]),
		buyPressureFactor:       {Points: pressurePoints, Maximum: cfg.Factors[buyPressureFactor].Maximum, Available: pressureAvailable},
		participantGrowthFactor: factorFromRatio(input.ParticipantGrowth.Value, available(input.ParticipantGrowth.Quality), cfg.Factors[participantGrowthFactor]),
		socialMomentumFactor:    factorFromRatio(input.SocialMomentum.Value, available(input.SocialMomentum.Quality), cfg.Factors[socialMomentumFactor]),
		liquidityQualityFactor:  liquidityFactor(liquidityRatio, liquidityAvailable, cfg.Factors[liquidityQualityFactor]),
	}
}

func factorFromRatio(ratio float64, isAvailable bool, cfg factorConfig) domain.FactorScore {
	points := 0.0
	if isAvailable {
		points = velocityPoints(ratio, cfg)
	}
	return domain.FactorScore{Points: points, Maximum: cfg.Maximum, Available: isAvailable}
}

func velocityPoints(ratio float64, cfg factorConfig) float64 {
	t := cfg.VelocityThresholds
	m := cfg.PointMultipliers
	switch {
	case ratio >= t.Full:
		return cfg.Maximum * m.Full
	case ratio >= t.High:
		return cfg.Maximum * m.High
	case ratio >= t.Medium:
		return cfg.Maximum * m.Medium
	case ratio >= t.Low:
		return cfg.Maximum * m.Low
	case ratio >= t.Minimum:
		return cfg.Maximum * m.Minimum
	default:
		return 0
	}
}

func combinedRatioPoints(first float64, firstAvailable bool, second float64, secondAvailable bool, cfg factorConfig) (float64, bool) {
	total, count := 0.0, 0.0
	if firstAvailable {
		total += velocityPoints(first, cfg)
		count++
	}
	if secondAvailable {
		total += velocityPoints(second, cfg)
		count++
	}
	if count == 0 {
		return 0, false
	}
	return math.Min(total/count, cfg.Maximum), true
}

func liquidityFactor(ratio float64, isAvailable bool, cfg factorConfig) domain.FactorScore {
	result := domain.FactorScore{Maximum: cfg.Maximum, Available: isAvailable}
	if !isAvailable {
		return result
	}
	t := cfg.RatioThresholds
	m := cfg.PointMultipliers
	switch {
	case ratio >= t.Full:
		result.Points = cfg.Maximum * m.Full
	case ratio >= t.High:
		result.Points = cfg.Maximum * m.High
	case ratio >= t.Medium:
		result.Points = cfg.Maximum * m.Medium
	case ratio >= t.Low:
		result.Points = cfg.Maximum * m.Low
	case ratio >= t.Minimum:
		result.Points = cfg.Maximum * m.Minimum
	}
	return result
}

func ageBonus(age domain.DataValue[time.Duration], cfg config) float64 {
	if !available(age.Quality) {
		return 0
	}
	minutes := age.Value.Minutes()
	switch {
	case minutes <= cfg.AgeCutoffsMinutes.Minutes30:
		return cfg.AgeBonus.Under30m
	case minutes <= cfg.AgeCutoffsMinutes.Minutes60:
		return cfg.AgeBonus.Under60m
	case minutes <= cfg.AgeCutoffsMinutes.Minutes120:
		return cfg.AgeBonus.Under120m
	default:
		return cfg.AgeBonus.Later
	}
}

func riskPenalty(input Input, cfg config) (float64, []string) {
	penalty := 0.0
	flags := make([]string, 0, 4)
	if available(input.Top10HolderPercent.Quality) {
		switch {
		case input.Top10HolderPercent.Value > cfg.RiskThresholds.Top10HighPercent:
			penalty += cfg.RiskPenalties.Top10Over80
			flags = append(flags, "HIGH_CONCENTRATION")
		case input.Top10HolderPercent.Value > cfg.RiskThresholds.Top10MediumPercent:
			penalty += cfg.RiskPenalties.Top10Over60
			flags = append(flags, "HIGH_CONCENTRATION")
		}
	}
	if available(input.Current.LiquidityUSD.Quality) {
		switch {
		case input.Current.LiquidityUSD.Value < cfg.RiskThresholds.LiquidityVeryLowUSD:
			penalty += cfg.RiskPenalties.LiquidityBelow2000
			flags = append(flags, "LOW_LIQUIDITY")
		case input.Current.LiquidityUSD.Value <= cfg.RiskThresholds.LiquidityLowUSD:
			penalty += cfg.RiskPenalties.Liquidity2000To5000
			flags = append(flags, "LOW_LIQUIDITY")
		}
	}
	if available(input.ConcentratedActivity.Quality) && input.ConcentratedActivity.Value {
		penalty += cfg.RiskPenalties.ConcentratedActivity
		flags = append(flags, "CONCENTRATED_ACTIVITY")
	}
	if available(input.SuspectedBotActivity.Quality) && input.SuspectedBotActivity.Value {
		penalty += cfg.RiskPenalties.SuspectedBotActivity
		flags = append(flags, "SUSPECTED_BOT_ACTIVITY")
	}
	if available(input.StrongDevSelling.Quality) && input.StrongDevSelling.Value {
		penalty += cfg.RiskPenalties.StrongDevSelling
		flags = append(flags, "STRONG_DEV_SELLING")
	}
	return penalty, flags
}

func evidenceQuality(input Input) map[string]domain.Quality {
	previous, hasPrevious := previousSnapshot(input.Previous)
	return map[string]domain.Quality{
		"buyer_velocity":         velocityEvidenceQuality(input.BuyerVelocity.Quality, input.Current.BuyersM5.Quality, previous.BuyersM5.Quality, hasPrevious),
		"volume_velocity":        velocityEvidenceQuality(input.VolumeVelocity.Quality, input.Current.VolumeM5USD.Quality, previous.VolumeM5USD.Quality, hasPrevious),
		"token_age":              canonicalQuality(input.TokenAge.Quality),
		"buy_volume_m5_usd":      canonicalQuality(input.BuyVolumeM5USD.Quality),
		"sell_volume_m5_usd":     canonicalQuality(input.SellVolumeM5USD.Quality),
		"participant_growth":     canonicalQuality(input.ParticipantGrowth.Quality),
		"social_momentum":        canonicalQuality(input.SocialMomentum.Quality),
		"top10_holder_percent":   canonicalQuality(input.Top10HolderPercent.Quality),
		"concentrated_activity":  canonicalQuality(input.ConcentratedActivity.Quality),
		"suspected_bot_activity": canonicalQuality(input.SuspectedBotActivity.Quality),
		"strong_dev_selling":     canonicalQuality(input.StrongDevSelling.Quality),
	}
}

func velocityEvidenceQuality(explicit, current, previous domain.Quality, hasPrevious bool) domain.Quality {
	if available(explicit) {
		return explicit
	}
	if !hasPrevious || !available(current) || !available(previous) {
		return domain.QualityMissing
	}
	if current == domain.QualityDegraded || previous == domain.QualityDegraded {
		return domain.QualityDegraded
	}
	if current == domain.QualityCached || previous == domain.QualityCached {
		return domain.QualityCached
	}
	return domain.QualityFresh
}

func canonicalQuality(quality domain.Quality) domain.Quality {
	if available(quality) || quality == domain.QualityMissing {
		return quality
	}
	return domain.QualityMissing
}

func previousSnapshot(previous []domain.MarketSnapshot) (domain.MarketSnapshot, bool) {
	if len(previous) == 0 {
		return domain.MarketSnapshot{}, false
	}
	latest := previous[0]
	for _, candidate := range previous[1:] {
		if candidate.CollectedAt.After(latest.CollectedAt) {
			latest = candidate
		}
	}
	return latest, true
}

func ratioInt(numerator, denominator domain.DataValue[int], enabled bool) (float64, bool) {
	if !enabled || !available(numerator.Quality) || !available(denominator.Quality) || denominator.Value < 0 {
		return 0, false
	}
	if denominator.Value == 0 {
		if numerator.Value > 0 {
			return math.Inf(1), true
		}
		return 0, true
	}
	return float64(numerator.Value) / float64(denominator.Value), true
}

func ratioFloat(numerator, denominator domain.DataValue[float64], enabled bool) (float64, bool) {
	if !enabled || !available(numerator.Quality) || !available(denominator.Quality) || denominator.Value < 0 {
		return 0, false
	}
	if denominator.Value == 0 {
		if numerator.Value > 0 {
			return math.Inf(1), true
		}
		return 0, true
	}
	return numerator.Value / denominator.Value, true
}

func available(quality domain.Quality) bool {
	return quality == domain.QualityFresh || quality == domain.QualityCached || quality == domain.QualityDegraded
}

func clamp(value, minimum, maximum float64) float64 {
	return math.Min(math.Max(value, minimum), maximum)
}

// Tier returns the dashboard tier at the V1 boundaries.
func Tier(final float64) domain.Tier { return tier(final, defaultConfig()) }

func tier(final float64, cfg config) domain.Tier {
	switch {
	case final >= cfg.Tiers.Breakout:
		return domain.TierBreakout
	case final >= cfg.Tiers.FastRising:
		return domain.TierFastRising
	case final >= cfg.Tiers.Watch:
		return domain.TierWatch
	default:
		return domain.TierHidden
	}
}

func (e Engine) effectiveConfig() config {
	if e.cfg.Version == "" {
		return defaultConfig()
	}
	return e.cfg
}

// ConfigJSON returns the complete numeric configuration for audit storage.
func (e Engine) ConfigJSON() ([]byte, error) { return json.Marshal(e.effectiveConfig()) }
