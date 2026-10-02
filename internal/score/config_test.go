package score

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseConfigRoundTripsBuiltInV1(t *testing.T) {
	builtIn := NewEngine()
	raw, err := builtIn.ConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version() != Version {
		t.Fatalf("version = %q, want %q", parsed.Version(), Version)
	}
	if got, want := parsed.Evaluate(maxInput()), builtIn.Evaluate(maxInput()); !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed V1 = %+v, built-in = %+v", got, want)
	}
	canonical, err := parsed.ConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(raw) {
		t.Fatalf("canonical config changed:\n got %s\nwant %s", canonical, raw)
	}
}

func TestParseConfigRejectsInvalidConfigurations(t *testing.T) {
	valid, err := NewEngine().ConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(path []string, value any) []byte {
		t.Helper()
		var root map[string]any
		if err := json.Unmarshal(valid, &root); err != nil {
			t.Fatal(err)
		}
		current := root
		for _, key := range path[:len(path)-1] {
			current = current[key].(map[string]any)
		}
		current[path[len(path)-1]] = value
		encoded, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}

	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "unknown field", raw: append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"extra":1}`)...), want: "extra"},
		{name: "duplicate field", raw: []byte(strings.Replace(string(valid), `"version":"FOMO_SCORE_V1.0"`, `"version":"FOMO_SCORE_V1.0","version":"FOMO_SCORE_V1.1"`, 1)), want: "duplicate JSON field"},
		{name: "unsafe version", raw: mutate([]string{"version"}, "../../bad"), want: "version"},
		{name: "negative maximum", raw: mutate([]string{"factors", buyerVelocityFactor, "maximum"}, -1), want: "maximum"},
		{name: "multiplier above one", raw: mutate([]string{"factors", buyerVelocityFactor, "point_multipliers", "full"}, 1.1), want: "multiplier"},
		{name: "velocity thresholds reversed", raw: mutate([]string{"factors", buyerVelocityFactor, "velocity_thresholds", "high"}, 5), want: "thresholds"},
		{name: "age cutoffs reversed", raw: mutate([]string{"age_cutoffs_minutes", "minutes_60"}, 20), want: "age cutoffs"},
		{name: "risk thresholds reversed", raw: mutate([]string{"risk_thresholds", "top10_high_percent"}, 50), want: "top10"},
		{name: "tiers reversed", raw: mutate([]string{"tiers", "fast_rising"}, 90), want: "tiers"},
		{name: "invalid clamp", raw: mutate([]string{"clamp", "maximum"}, 80), want: "clamp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseConfig() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseConfigRejectsMissingOrExtraFactors(t *testing.T) {
	valid, err := NewEngine().ConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(valid, &root); err != nil {
		t.Fatal(err)
	}
	factors := root["factors"].(map[string]any)
	delete(factors, socialMomentumFactor)
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseConfig(raw); err == nil || !strings.Contains(err.Error(), "factors") {
		t.Fatalf("missing factor error = %v", err)
	}

	factors[socialMomentumFactor] = factors[buyerVelocityFactor]
	factors["future_factor"] = factors[buyerVelocityFactor]
	raw, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseConfig(raw); err == nil || !strings.Contains(err.Error(), "factors") {
		t.Fatalf("extra factor error = %v", err)
	}
}
