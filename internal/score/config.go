package score

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
)

var scoreVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ParseConfig 严格校验结构和数值后返回不可变评分引擎。
func ParseConfig(raw []byte) (Engine, error) {
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return Engine{}, err
	}
	if err := validateConfigShape(raw); err != nil {
		return Engine{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cfg config
	if err := decoder.Decode(&cfg); err != nil {
		return Engine{}, fmt.Errorf("decode score config: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Engine{}, err
	}
	if err := validateConfig(cfg); err != nil {
		return Engine{}, err
	}
	return Engine{cfg: cfg}, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing score config: %w", err)
	}
	return fmt.Errorf("score config contains trailing JSON")
}

func rejectDuplicateJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJSONValue(decoder, "$"); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode score config: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode score config object: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("decode score config object key")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON field %s.%s", path, key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("decode score config object end: %w", err)
		}
	case '[':
		index := 0
		for decoder.More() {
			if err := scanJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			index++
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("decode score config array end: %w", err)
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}

func validateConfigShape(raw []byte) error {
	root, err := decodeObject(raw, "score config")
	if err != nil {
		return err
	}
	if err := requireKeys(root, "score config", "version", "factors", "age_bonus", "age_cutoffs_minutes", "risk_penalties", "risk_thresholds", "tiers", "clamp"); err != nil {
		return err
	}
	factors, err := decodeObject(root["factors"], "factors")
	if err != nil {
		return err
	}
	if err := requireKeys(factors, "factors", buyerVelocityFactor, volumeVelocityFactor, buyPressureFactor, participantGrowthFactor, socialMomentumFactor, liquidityQualityFactor); err != nil {
		return err
	}
	for name, encoded := range factors {
		factor, err := decodeObject(encoded, "factor "+name)
		if err != nil {
			return err
		}
		thresholdKey := "velocity_thresholds"
		if name == liquidityQualityFactor {
			thresholdKey = "ratio_thresholds"
		}
		if err := requireKeys(factor, "factor "+name, "maximum", thresholdKey, "point_multipliers"); err != nil {
			return err
		}
		thresholds, err := decodeObject(factor[thresholdKey], name+" "+thresholdKey)
		if err != nil {
			return err
		}
		if err := requireKeys(thresholds, name+" "+thresholdKey, "full", "high", "medium", "low", "minimum"); err != nil {
			return err
		}
		multipliers, err := decodeObject(factor["point_multipliers"], name+" point_multipliers")
		if err != nil {
			return err
		}
		if err := requireKeys(multipliers, name+" point_multipliers", "full", "high", "medium", "low", "minimum"); err != nil {
			return err
		}
	}
	objects := []struct {
		key  string
		want []string
	}{
		{"age_bonus", []string{"under_30m", "under_60m", "under_120m", "later"}},
		{"age_cutoffs_minutes", []string{"minutes_30", "minutes_60", "minutes_120"}},
		{"risk_penalties", []string{"top10_over_80", "top10_over_60", "liquidity_below_2000", "liquidity_2000_to_5000", "concentrated_activity", "suspected_bot_activity", "strong_dev_selling"}},
		{"risk_thresholds", []string{"top10_high_percent", "top10_medium_percent", "liquidity_very_low_usd", "liquidity_low_usd"}},
		{"tiers", []string{"breakout", "fast_rising", "watch"}},
		{"clamp", []string{"minimum", "maximum"}},
	}
	for _, object := range objects {
		decoded, err := decodeObject(root[object.key], object.key)
		if err != nil {
			return err
		}
		if err := requireKeys(decoded, object.key, object.want...); err != nil {
			return err
		}
	}
	return nil
}

func decodeObject(raw []byte, name string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("%s must be an object: %w", name, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	return object, nil
}

func requireKeys(object map[string]json.RawMessage, name string, want ...string) error {
	wanted := make(map[string]struct{}, len(want))
	for _, key := range want {
		wanted[key] = struct{}{}
	}
	missing := make([]string, 0)
	extra := make([]string, 0)
	for _, key := range want {
		if _, ok := object[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range object {
		if _, ok := wanted[key]; !ok {
			extra = append(extra, key)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return fmt.Errorf("%s fields invalid: missing=%s extra=%s", name, strings.Join(missing, ","), strings.Join(extra, ","))
}

func validateConfig(cfg config) error {
	if !scoreVersionPattern.MatchString(cfg.Version) {
		return fmt.Errorf("score version %q is invalid", cfg.Version)
	}
	if err := validateFactorSet(cfg.Factors); err != nil {
		return err
	}
	if err := validateNonNegative("age bonus", cfg.AgeBonus.Under30m, cfg.AgeBonus.Under60m, cfg.AgeBonus.Under120m, cfg.AgeBonus.Later); err != nil {
		return err
	}
	if !descending(cfg.AgeBonus.Under30m, cfg.AgeBonus.Under60m, cfg.AgeBonus.Under120m, cfg.AgeBonus.Later) {
		return fmt.Errorf("age bonus must be descending")
	}
	if !strictAscending(cfg.AgeCutoffsMinutes.Minutes30, cfg.AgeCutoffsMinutes.Minutes60, cfg.AgeCutoffsMinutes.Minutes120) || cfg.AgeCutoffsMinutes.Minutes30 <= 0 {
		return fmt.Errorf("age cutoffs must be positive and ascending")
	}
	if err := validateNonNegative("risk penalties", cfg.RiskPenalties.Top10Over80, cfg.RiskPenalties.Top10Over60, cfg.RiskPenalties.LiquidityBelow2000, cfg.RiskPenalties.Liquidity2000To5000, cfg.RiskPenalties.ConcentratedActivity, cfg.RiskPenalties.SuspectedBotActivity, cfg.RiskPenalties.StrongDevSelling); err != nil {
		return err
	}
	if !allFinite(cfg.RiskThresholds.Top10HighPercent, cfg.RiskThresholds.Top10MediumPercent, cfg.RiskThresholds.LiquidityVeryLowUSD, cfg.RiskThresholds.LiquidityLowUSD) || cfg.RiskThresholds.Top10MediumPercent < 0 || cfg.RiskThresholds.Top10HighPercent > 100 || cfg.RiskThresholds.Top10HighPercent <= cfg.RiskThresholds.Top10MediumPercent {
		return fmt.Errorf("top10 risk thresholds must satisfy 0 <= medium < high <= 100")
	}
	if cfg.RiskThresholds.LiquidityVeryLowUSD < 0 || cfg.RiskThresholds.LiquidityLowUSD <= cfg.RiskThresholds.LiquidityVeryLowUSD {
		return fmt.Errorf("liquidity risk thresholds must be non-negative and ascending")
	}
	if !allFinite(cfg.Clamp.Minimum, cfg.Clamp.Maximum) || cfg.Clamp.Minimum >= cfg.Clamp.Maximum {
		return fmt.Errorf("clamp minimum must be below maximum")
	}
	if !strictDescending(cfg.Tiers.Breakout, cfg.Tiers.FastRising, cfg.Tiers.Watch) {
		return fmt.Errorf("tiers must satisfy breakout > fast_rising > watch")
	}
	if cfg.Tiers.Watch < cfg.Clamp.Minimum || cfg.Tiers.Breakout > cfg.Clamp.Maximum {
		return fmt.Errorf("clamp must contain all tier thresholds")
	}
	return nil
}

func validateFactorSet(factors map[string]factorConfig) error {
	want := []string{buyerVelocityFactor, volumeVelocityFactor, buyPressureFactor, participantGrowthFactor, socialMomentumFactor, liquidityQualityFactor}
	if len(factors) != len(want) {
		return fmt.Errorf("factors must contain exactly %d entries", len(want))
	}
	for _, name := range want {
		factor, ok := factors[name]
		if !ok {
			return fmt.Errorf("factors missing %q", name)
		}
		if !isFinite(factor.Maximum) || factor.Maximum < 0 {
			return fmt.Errorf("factor %s maximum must be finite and non-negative", name)
		}
		if factor.PointMultipliers == nil {
			return fmt.Errorf("factor %s point multipliers are required", name)
		}
		m := factor.PointMultipliers
		if !allFinite(m.Full, m.High, m.Medium, m.Low, m.Minimum) || !betweenZeroAndOne(m.Full, m.High, m.Medium, m.Low, m.Minimum) || !descending(m.Full, m.High, m.Medium, m.Low, m.Minimum) {
			return fmt.Errorf("factor %s multipliers must be descending within [0,1]", name)
		}
		if name == liquidityQualityFactor {
			if factor.VelocityThresholds != nil || factor.RatioThresholds == nil {
				return fmt.Errorf("factor %s must use ratio thresholds", name)
			}
			t := factor.RatioThresholds
			if !strictDescending(t.Full, t.High, t.Medium, t.Low, t.Minimum) || t.Minimum < 0 {
				return fmt.Errorf("factor %s ratio thresholds must be non-negative and descending", name)
			}
			continue
		}
		if factor.RatioThresholds != nil || factor.VelocityThresholds == nil {
			return fmt.Errorf("factor %s must use velocity thresholds", name)
		}
		t := factor.VelocityThresholds
		if !strictDescending(t.Full, t.High, t.Medium, t.Low, t.Minimum) || t.Minimum < 0 {
			return fmt.Errorf("factor %s velocity thresholds must be non-negative and descending", name)
		}
	}
	return nil
}

func validateNonNegative(name string, values ...float64) error {
	if !allFinite(values...) {
		return fmt.Errorf("%s must be finite", name)
	}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("%s must be non-negative", name)
		}
	}
	return nil
}

func allFinite(values ...float64) bool {
	for _, value := range values {
		if !isFinite(value) {
			return false
		}
	}
	return true
}

func isFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func betweenZeroAndOne(values ...float64) bool {
	for _, value := range values {
		if value < 0 || value > 1 {
			return false
		}
	}
	return true
}

func strictDescending(values ...float64) bool {
	if !allFinite(values...) {
		return false
	}
	for index := 1; index < len(values); index++ {
		if values[index-1] <= values[index] {
			return false
		}
	}
	return true
}

func descending(values ...float64) bool {
	if !allFinite(values...) {
		return false
	}
	for index := 1; index < len(values); index++ {
		if values[index-1] < values[index] {
			return false
		}
	}
	return true
}

func strictAscending(values ...float64) bool {
	if !allFinite(values...) {
		return false
	}
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}
