package score

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestEvaluateCapsEachRawFactor(t *testing.T) {
	input := maxInput()
	got := NewEngine().Evaluate(input)

	wants := map[string]float64{
		"buyer_velocity":     25,
		"volume_velocity":    20,
		"buy_pressure":       15,
		"participant_growth": 15,
		"social_momentum":    15,
		"liquidity_quality":  10,
	}
	for name, want := range wants {
		factor, ok := got.Factors[name]
		if !ok {
			t.Fatalf("factor %q missing", name)
		}
		if factor.Points != want || factor.Maximum != want || !factor.Available {
			t.Errorf("factor %q = %+v, want points=max=%v and available", name, factor, want)
		}
	}
	if got.RawMomentum != 100 || got.AvailableMaximum != 100 {
		t.Fatalf("raw/max = %v/%v, want 100/100", got.RawMomentum, got.AvailableMaximum)
	}
}

func TestEvaluateAgeBonus(t *testing.T) {
	tests := []struct {
		name string
		age  time.Duration
		want float64
	}{
		{"twenty minutes", 20 * time.Minute, 10},
		{"forty five minutes", 45 * time.Minute, 6},
		{"ninety minutes", 90 * time.Minute, 2},
		{"three hours", 3 * time.Hour, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewEngine().Evaluate(Input{TokenAge: durationValue(tt.age)})
			if got.AgeBonus != tt.want {
				t.Fatalf("age bonus = %v, want %v", got.AgeBonus, tt.want)
			}
		})
	}
}

func TestEvaluateMissingAgeGetsNoBonus(t *testing.T) {
	got := NewEngine().Evaluate(Input{})
	if got.AgeBonus != 0 {
		t.Fatalf("missing age bonus = %v, want 0", got.AgeBonus)
	}
	if got.EvidenceQuality["token_age"] != domain.QualityMissing {
		t.Fatalf("missing age quality = %q, want %q", got.EvidenceQuality["token_age"], domain.QualityMissing)
	}
}

func TestEvaluateConcentrationPenalty(t *testing.T) {
	tests := []struct {
		name    string
		percent float64
		want    float64
	}{
		{"sixty five percent", 65, 15},
		{"eighty five percent", 85, 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := maxInput()
			input.Top10HolderPercent = floatValue(tt.percent)
			got := NewEngine().Evaluate(input)
			if got.RiskPenalty != tt.want {
				t.Fatalf("risk penalty = %v, want %v", got.RiskPenalty, tt.want)
			}
			if !contains(got.RiskFlags, "HIGH_CONCENTRATION") {
				t.Fatalf("risk flags = %v, want HIGH_CONCENTRATION", got.RiskFlags)
			}
		})
	}
}

func TestEvaluateRiskPenaltiesStaySeparateFromMomentum(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*Input)
		want  float64
		flag  string
	}{
		{"liquidity below two thousand", func(i *Input) {
			i.Current.LiquidityUSD = floatValue(1_999)
			i.Current.MarketCapUSD = floatValue(9_995)
		}, 20, "LOW_LIQUIDITY"},
		{"liquidity up to five thousand", func(i *Input) {
			i.Current.LiquidityUSD = floatValue(5_000)
			i.Current.MarketCapUSD = floatValue(25_000)
		}, 10, "LOW_LIQUIDITY"},
		{"concentrated activity", func(i *Input) { i.ConcentratedActivity = boolValue(true) }, 15, "CONCENTRATED_ACTIVITY"},
		{"suspected bots", func(i *Input) { i.SuspectedBotActivity = boolValue(true) }, 15, "SUSPECTED_BOT_ACTIVITY"},
		{"strong developer selling", func(i *Input) { i.StrongDevSelling = boolValue(true) }, 20, "STRONG_DEV_SELLING"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := maxInput()
			tt.apply(&input)
			got := NewEngine().Evaluate(input)
			if got.NormalizedMomentum != 100 {
				t.Fatalf("normalized momentum = %v, want 100; risk must not alter factor points", got.NormalizedMomentum)
			}
			if got.RiskPenalty != tt.want || !contains(got.RiskFlags, tt.flag) {
				t.Fatalf("penalty/flags = %v/%v, want %v containing %q", got.RiskPenalty, got.RiskFlags, tt.want, tt.flag)
			}
		})
	}
}

func TestEvaluateClampsFinalScore(t *testing.T) {
	upper := NewEngine().Evaluate(maxInput())
	if upper.Final != 100 {
		t.Fatalf("upper final = %v, want 100", upper.Final)
	}

	lower := NewEngine().Evaluate(Input{
		TokenAge:             durationValue(3 * time.Hour),
		Top10HolderPercent:   floatValue(85),
		ConcentratedActivity: boolValue(true),
		SuspectedBotActivity: boolValue(true),
		StrongDevSelling:     boolValue(true),
	})
	if lower.Final != 0 {
		t.Fatalf("lower final = %v, want 0", lower.Final)
	}
}

func TestTierBoundaries(t *testing.T) {
	tests := []struct {
		score float64
		want  domain.Tier
	}{
		{85, domain.TierBreakout},
		{70, domain.TierFastRising},
		{55, domain.TierWatch},
		{54.99, domain.TierHidden},
	}

	for _, tt := range tests {
		if got := Tier(tt.score); got != tt.want {
			t.Errorf("Tier(%v) = %q, want %q", tt.score, got, tt.want)
		}
	}
}

func TestEvidenceConfidenceCapsEffectiveTierWithoutChangingRawScore(t *testing.T) {
	tests := []struct {
		name           string
		rawScore       float64
		available      int
		wantConfidence domain.EvidenceConfidence
		wantTier       domain.Tier
	}{
		{name: "raw 100 with two of five", rawScore: 100, available: 2, wantConfidence: domain.EvidenceConfidenceLow, wantTier: domain.TierWatch},
		{name: "raw 90 with three of five", rawScore: 90, available: 3, wantConfidence: domain.EvidenceConfidenceMedium, wantTier: domain.TierFastRising},
		{name: "raw 90 with four of five", rawScore: 90, available: 4, wantConfidence: domain.EvidenceConfidenceGood, wantTier: domain.TierBreakout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawTier := Tier(tt.rawScore)
			got := applyEvidenceGate(domain.ScoreBreakdown{
				Final:   tt.rawScore,
				Tier:    rawTier,
				Factors: factorsWithCoreAvailability(tt.available),
			})

			if got.Final != tt.rawScore || got.RawScore != tt.rawScore {
				t.Fatalf("raw score changed: Final=%v RawScore=%v want=%v", got.Final, got.RawScore, tt.rawScore)
			}
			if got.RawTier != rawTier {
				t.Fatalf("raw tier = %q, want %q", got.RawTier, rawTier)
			}
			if got.EvidenceAvailable != tt.available || got.EvidenceTotal != 5 {
				t.Fatalf("evidence = %d/%d, want %d/5", got.EvidenceAvailable, got.EvidenceTotal, tt.available)
			}
			if got.EvidenceConfidence != tt.wantConfidence {
				t.Fatalf("confidence = %q, want %q", got.EvidenceConfidence, tt.wantConfidence)
			}
			if got.Tier != tt.wantTier || got.EffectiveTier != tt.wantTier {
				t.Fatalf("effective tier = %q/%q, want %q", got.Tier, got.EffectiveTier, tt.wantTier)
			}
		})
	}
}

func TestEvidenceConfidenceExcludesSocialAndNeverPromotesRawTier(t *testing.T) {
	factors := factorsWithCoreAvailability(2)
	factors[socialMomentumFactor] = domain.FactorScore{Available: true}

	got := applyEvidenceGate(domain.ScoreBreakdown{
		Final:   40,
		Tier:    domain.TierHidden,
		Factors: factors,
	})

	if got.EvidenceAvailable != 2 || got.EvidenceConfidence != domain.EvidenceConfidenceLow {
		t.Fatalf("social affected required evidence: %d/%s", got.EvidenceAvailable, got.EvidenceConfidence)
	}
	if got.EffectiveTier != domain.TierHidden || got.Tier != domain.TierHidden {
		t.Fatalf("tier cap promoted hidden score: effective=%q tier=%q", got.EffectiveTier, got.Tier)
	}
}

func TestEvaluateStoresEvidenceConfidenceMetadata(t *testing.T) {
	got := NewEngine().Evaluate(maxInput())

	if got.RawScore != 100 || got.RawTier != domain.TierBreakout {
		t.Fatalf("raw score/tier = %v/%q, want 100/BREAKOUT", got.RawScore, got.RawTier)
	}
	if got.EvidenceAvailable != 5 || got.EvidenceTotal != 5 || got.EvidenceConfidence != domain.EvidenceConfidenceHigh {
		t.Fatalf("evidence metadata = %d/%d %q, want 5/5 HIGH", got.EvidenceAvailable, got.EvidenceTotal, got.EvidenceConfidence)
	}
	if got.EffectiveTier != domain.TierBreakout || got.Tier != domain.TierBreakout {
		t.Fatalf("effective tier = %q/%q, want BREAKOUT", got.EffectiveTier, got.Tier)
	}
}

func TestEvaluateNormalizesUnavailableSocialOutOfDenominator(t *testing.T) {
	input := maxInput()
	input.SocialMomentum = domain.DataValue[float64]{Quality: domain.QualityMissing}

	got := NewEngine().Evaluate(input)
	factor := got.Factors["social_momentum"]
	if factor.Available || factor.Points != 0 || factor.Maximum != 15 {
		t.Fatalf("social factor = %+v, want unavailable with zero points and max 15", factor)
	}
	if got.RawMomentum != 85 || got.AvailableMaximum != 85 || got.NormalizedMomentum != 100 {
		t.Fatalf("raw/max/normalized = %v/%v/%v, want 85/85/100", got.RawMomentum, got.AvailableMaximum, got.NormalizedMomentum)
	}

	input.SocialMomentum = floatValue(1)
	got = NewEngine().Evaluate(input)
	if got.RawMomentum != 85 || got.AvailableMaximum != 100 || got.NormalizedMomentum != 85 {
		t.Fatalf("available zero social raw/max/normalized = %v/%v/%v, want 85/100/85", got.RawMomentum, got.AvailableMaximum, got.NormalizedMomentum)
	}
}

func TestEvaluatePreservesRiskEvidenceQuality(t *testing.T) {
	input := Input{
		TokenAge:             domain.DataValue[time.Duration]{Value: 20 * time.Minute, Quality: domain.QualityMissing},
		Top10HolderPercent:   domain.DataValue[float64]{Value: 85, Quality: domain.QualityMissing},
		ConcentratedActivity: domain.DataValue[bool]{Value: true, Quality: domain.QualityMissing},
		SuspectedBotActivity: boolValue(false),
		StrongDevSelling:     domain.DataValue[bool]{Value: true, Quality: domain.QualityMissing},
	}

	got := NewEngine().Evaluate(input)
	if got.AgeBonus != 0 || got.RiskPenalty != 0 || len(got.RiskFlags) != 0 {
		t.Fatalf("missing evidence affected score: age=%v penalty=%v flags=%v", got.AgeBonus, got.RiskPenalty, got.RiskFlags)
	}
	wants := map[string]domain.Quality{
		"token_age":              domain.QualityMissing,
		"top10_holder_percent":   domain.QualityMissing,
		"concentrated_activity":  domain.QualityMissing,
		"suspected_bot_activity": domain.QualityFresh,
		"strong_dev_selling":     domain.QualityMissing,
	}
	for name, want := range wants {
		if quality := got.EvidenceQuality[name]; quality != want {
			t.Errorf("evidence quality %q = %q, want %q", name, quality, want)
		}
	}
}

func TestEvaluateDistinguishesAvailableZeroFromMissing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Input)
		factor string
		points float64
	}{
		{
			name: "buyers growing from zero scores maximum",
			mutate: func(input *Input) {
				input.Previous[0].BuyersM5 = intValue(0)
			},
			factor: "buyer_velocity",
			points: 25,
		},
		{
			name: "zero buyers after zero buyers remains available",
			mutate: func(input *Input) {
				input.Previous[0].BuyersM5 = intValue(0)
				input.Current.BuyersM5 = intValue(0)
			},
			factor: "buyer_velocity",
			points: 0,
		},
		{
			name: "buy pressure with no sells scores maximum",
			mutate: func(input *Input) {
				input.Current.SellsM5 = intValue(0)
				input.SellVolumeM5USD = floatValue(0)
			},
			factor: "buy_pressure",
			points: 15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := maxInput()
			tt.mutate(&input)
			got := NewEngine().Evaluate(input).Factors[tt.factor]
			if !got.Available || got.Points != tt.points {
				t.Fatalf("factor = %+v, want available with %v points", got, tt.points)
			}
		})
	}
}

func TestEvaluatePrefersExplicitDeepVelocityEvidence(t *testing.T) {
	input := maxInput()
	input.BuyerVelocity = floatValue(1.5)
	input.VolumeVelocity = domain.DataValue[float64]{Value: 3, Quality: domain.QualityDegraded, Source: "geckoterminal"}

	got := NewEngine().Evaluate(input)
	if factor := got.Factors["buyer_velocity"]; !factor.Available || factor.Points != 11.25 {
		t.Fatalf("buyer velocity = %+v, want explicit 1.5 ratio worth 11.25", factor)
	}
	if factor := got.Factors["volume_velocity"]; !factor.Available || factor.Points != 17 {
		t.Fatalf("volume velocity = %+v, want explicit 3 ratio worth 17", factor)
	}
	if got.EvidenceQuality["buyer_velocity"] != domain.QualityFresh || got.EvidenceQuality["volume_velocity"] != domain.QualityDegraded {
		t.Fatalf("velocity evidence quality = %+v", got.EvidenceQuality)
	}
}

func TestEvaluateMissingDeepVelocityFallsBackToSnapshotHistory(t *testing.T) {
	input := maxInput()
	input.BuyerVelocity = domain.DataValue[float64]{Quality: domain.QualityMissing}
	input.VolumeVelocity = domain.DataValue[float64]{Quality: domain.QualityMissing}

	got := NewEngine().Evaluate(input)
	if factor := got.Factors["buyer_velocity"]; !factor.Available || factor.Points != 25 {
		t.Fatalf("buyer velocity fallback = %+v, want current/history ratio 4", factor)
	}
	if factor := got.Factors["volume_velocity"]; !factor.Available || factor.Points != 20 {
		t.Fatalf("volume velocity fallback = %+v, want current/history ratio 4", factor)
	}
	if got.EvidenceQuality["buyer_velocity"] != domain.QualityFresh || got.EvidenceQuality["volume_velocity"] != domain.QualityFresh {
		t.Fatalf("fallback velocity quality = %+v, want fresh current/history evidence", got.EvidenceQuality)
	}
}

func TestConfigJSONAuditsEveryNumericDecision(t *testing.T) {
	raw, err := NewEngine().ConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("invalid config JSON: %v", err)
	}

	assertJSONNumber(t, got, 25, "factors", "buyer_velocity", "maximum")
	assertJSONNumber(t, got, 20, "factors", "volume_velocity", "maximum")
	assertJSONNumber(t, got, 15, "factors", "buy_pressure", "maximum")
	assertJSONNumber(t, got, 15, "factors", "participant_growth", "maximum")
	assertJSONNumber(t, got, 15, "factors", "social_momentum", "maximum")
	assertJSONNumber(t, got, 10, "factors", "liquidity_quality", "maximum")
	assertJSONNumber(t, got, 10, "age_bonus", "under_30m")
	assertJSONNumber(t, got, 6, "age_bonus", "under_60m")
	assertJSONNumber(t, got, 2, "age_bonus", "under_120m")
	assertJSONNumber(t, got, 0, "age_bonus", "later")
	assertJSONNumber(t, got, 30, "age_cutoffs_minutes", "minutes_30")
	assertJSONNumber(t, got, 60, "age_cutoffs_minutes", "minutes_60")
	assertJSONNumber(t, got, 120, "age_cutoffs_minutes", "minutes_120")
	assertJSONNumber(t, got, 30, "risk_penalties", "top10_over_80")
	assertJSONNumber(t, got, 15, "risk_penalties", "top10_over_60")
	assertJSONNumber(t, got, 20, "risk_penalties", "liquidity_below_2000")
	assertJSONNumber(t, got, 10, "risk_penalties", "liquidity_2000_to_5000")
	assertJSONNumber(t, got, 15, "risk_penalties", "concentrated_activity")
	assertJSONNumber(t, got, 15, "risk_penalties", "suspected_bot_activity")
	assertJSONNumber(t, got, 20, "risk_penalties", "strong_dev_selling")
	assertJSONNumber(t, got, 80, "risk_thresholds", "top10_high_percent")
	assertJSONNumber(t, got, 60, "risk_thresholds", "top10_medium_percent")
	assertJSONNumber(t, got, 2000, "risk_thresholds", "liquidity_very_low_usd")
	assertJSONNumber(t, got, 5000, "risk_thresholds", "liquidity_low_usd")
	assertJSONNumber(t, got, 85, "tiers", "breakout")
	assertJSONNumber(t, got, 70, "tiers", "fast_rising")
	assertJSONNumber(t, got, 55, "tiers", "watch")
	assertJSONNumber(t, got, 0, "clamp", "minimum")
	assertJSONNumber(t, got, 100, "clamp", "maximum")

	if version, _ := got["version"].(string); version != Version {
		t.Fatalf("version = %q, want %q", version, Version)
	}
	for _, factor := range []string{"buyer_velocity", "volume_velocity", "buy_pressure", "participant_growth", "social_momentum"} {
		assertJSONNumber(t, got, 4, "factors", factor, "velocity_thresholds", "full")
		assertJSONNumber(t, got, 3, "factors", factor, "velocity_thresholds", "high")
		assertJSONNumber(t, got, 2, "factors", factor, "velocity_thresholds", "medium")
		assertJSONNumber(t, got, 1.5, "factors", factor, "velocity_thresholds", "low")
		assertJSONNumber(t, got, 1.1, "factors", factor, "velocity_thresholds", "minimum")
		assertJSONNumber(t, got, 1, "factors", factor, "point_multipliers", "full")
		assertJSONNumber(t, got, .85, "factors", factor, "point_multipliers", "high")
		assertJSONNumber(t, got, .65, "factors", factor, "point_multipliers", "medium")
		assertJSONNumber(t, got, .45, "factors", factor, "point_multipliers", "low")
		assertJSONNumber(t, got, .25, "factors", factor, "point_multipliers", "minimum")
	}
	assertJSONNumber(t, got, 0.2, "factors", "liquidity_quality", "ratio_thresholds", "full")
	assertJSONNumber(t, got, 0.1, "factors", "liquidity_quality", "ratio_thresholds", "high")
	assertJSONNumber(t, got, 0.05, "factors", "liquidity_quality", "ratio_thresholds", "medium")
	assertJSONNumber(t, got, 0.02, "factors", "liquidity_quality", "ratio_thresholds", "low")
	assertJSONNumber(t, got, 0.01, "factors", "liquidity_quality", "ratio_thresholds", "minimum")
	assertJSONNumber(t, got, 1, "factors", "liquidity_quality", "point_multipliers", "full")
	assertJSONNumber(t, got, .85, "factors", "liquidity_quality", "point_multipliers", "high")
	assertJSONNumber(t, got, .65, "factors", "liquidity_quality", "point_multipliers", "medium")
	assertJSONNumber(t, got, .45, "factors", "liquidity_quality", "point_multipliers", "low")
	assertJSONNumber(t, got, .25, "factors", "liquidity_quality", "point_multipliers", "minimum")
}

func maxInput() Input {
	previous := domain.MarketSnapshot{
		BuyersM5:     intValue(10),
		VolumeM5USD:  floatValue(1_000),
		LiquidityUSD: floatValue(20_000),
		MarketCapUSD: floatValue(100_000),
	}
	current := domain.MarketSnapshot{
		BuyersM5:     intValue(40),
		BuysM5:       intValue(40),
		SellsM5:      intValue(10),
		VolumeM5USD:  floatValue(4_000),
		LiquidityUSD: floatValue(20_000),
		MarketCapUSD: floatValue(100_000),
	}
	return Input{
		Current:           current,
		Previous:          []domain.MarketSnapshot{previous},
		TokenAge:          durationValue(20 * time.Minute),
		BuyVolumeM5USD:    floatValue(4_000),
		SellVolumeM5USD:   floatValue(1_000),
		ParticipantGrowth: floatValue(4),
		SocialMomentum:    floatValue(4),
	}
}

func factorsWithCoreAvailability(available int) map[string]domain.FactorScore {
	names := []string{
		buyerVelocityFactor,
		volumeVelocityFactor,
		buyPressureFactor,
		participantGrowthFactor,
		liquidityQualityFactor,
	}
	factors := make(map[string]domain.FactorScore, len(names)+1)
	for index, name := range names {
		factors[name] = domain.FactorScore{Available: index < available}
	}
	factors[socialMomentumFactor] = domain.FactorScore{}
	return factors
}

func floatValue(value float64) domain.DataValue[float64] {
	return domain.DataValue[float64]{Value: value, Quality: domain.QualityFresh}
}

func intValue(value int) domain.DataValue[int] {
	return domain.DataValue[int]{Value: value, Quality: domain.QualityFresh}
}

func durationValue(value time.Duration) domain.DataValue[time.Duration] {
	return domain.DataValue[time.Duration]{Value: value, Quality: domain.QualityFresh}
}

func boolValue(value bool) domain.DataValue[bool] {
	return domain.DataValue[bool]{Value: value, Quality: domain.QualityFresh}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertJSONNumber(t *testing.T, root map[string]any, want float64, path ...string) {
	t.Helper()
	var current any = root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("config path %v: %T is not an object", path, current)
		}
		current, ok = object[key]
		if !ok {
			t.Fatalf("config path %v: key %q missing", path, key)
		}
	}
	got, ok := current.(float64)
	if !ok || math.Abs(got-want) > 1e-9 {
		t.Fatalf("config path %v = %#v, want %v", path, current, want)
	}
}
