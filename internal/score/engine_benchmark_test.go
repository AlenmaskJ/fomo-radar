package score

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

var benchmarkEvaluateScoreResult domain.ScoreBreakdown
var benchmarkEvaluateScoreSetResult []domain.VersionedScore

func BenchmarkEvaluateScore(b *testing.B) {
	input := benchmarkInput()
	engine := NewEngine()
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		benchmarkEvaluateScoreResult = engine.Evaluate(input)
	}
}

func BenchmarkEvaluateScoreSetFourVersions(b *testing.B) {
	set := benchmarkFourVersionSet(b)
	input := benchmarkInput()
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		benchmarkEvaluateScoreSetResult = set.Evaluate(input)
	}
}

func benchmarkInput() Input {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	previousAt := now.Add(-5 * time.Minute)
	return Input{
		Current: domain.MarketSnapshot{
			CollectedAt:  now,
			BuyersM5:     domain.DataValue[int]{Value: 40, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
			BuysM5:       domain.DataValue[int]{Value: 40, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
			SellsM5:      domain.DataValue[int]{Value: 10, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
			VolumeM5USD:  domain.DataValue[float64]{Value: 4_000, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
			LiquidityUSD: domain.DataValue[float64]{Value: 20_000, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
			MarketCapUSD: domain.DataValue[float64]{Value: 100_000, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
		},
		Previous: []domain.MarketSnapshot{{
			CollectedAt: previousAt,
			BuyersM5:    domain.DataValue[int]{Value: 10, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: previousAt},
			VolumeM5USD: domain.DataValue[float64]{Value: 1_000, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: previousAt},
		}},
		BuyVolumeM5USD:       domain.DataValue[float64]{Value: 4_000, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
		SellVolumeM5USD:      domain.DataValue[float64]{Value: 1_000, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
		ParticipantGrowth:    domain.DataValue[float64]{Value: 4, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
		SocialMomentum:       domain.DataValue[float64]{Value: 3, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
		TokenAge:             domain.DataValue[time.Duration]{Value: 45 * time.Minute, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
		Top10HolderPercent:   domain.DataValue[float64]{Value: 55, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
		ConcentratedActivity: domain.DataValue[bool]{Value: false, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
		SuspectedBotActivity: domain.DataValue[bool]{Value: false, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
		StrongDevSelling:     domain.DataValue[bool]{Value: false, Quality: domain.QualityFresh, Source: "fixture", CollectedAt: now},
	}
}

func benchmarkFourVersionSet(b *testing.B) Set {
	b.Helper()
	raw, err := NewEngine().ConfigJSON()
	if err != nil {
		b.Fatal(err)
	}
	assignments := make([]Assignment, 0, 4)
	for index, version := range []string{"BENCH_V1", "BENCH_V2", "BENCH_V3", "BENCH_V4"} {
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			b.Fatal(err)
		}
		config["version"] = version
		config["factors"].(map[string]any)[buyerVelocityFactor].(map[string]any)["maximum"] = float64(25 - index)
		encoded, err := json.Marshal(config)
		if err != nil {
			b.Fatal(err)
		}
		engine, err := ParseConfig(encoded)
		if err != nil {
			b.Fatal(err)
		}
		role := domain.ScoreRoleChallenger
		if index == 0 {
			role = domain.ScoreRoleChampion
		}
		assignments = append(assignments, Assignment{Role: role, Engine: engine})
	}
	set, err := NewSet(assignments)
	if err != nil {
		b.Fatal(err)
	}
	return set
}
