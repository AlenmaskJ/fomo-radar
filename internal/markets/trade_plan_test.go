package markets

import (
	"errors"
	"math"
	"testing"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestClusterLevelsGroupsNearbyPivots(t *testing.T) {
	pivots := []Pivot{{Index: 1, Price: 94}, {Index: 2, Price: 94.6}, {Index: 3, Price: 101, High: true}, {Index: 4, Price: 102, High: true}}
	lows := ClusterLevels(pivots, 2, false)
	highs := ClusterLevels(pivots, 2, true)
	if len(lows) != 1 || lows[0].Low != 94 || lows[0].High != 94.6 || lows[0].Strength != 2 {
		t.Fatalf("low zones = %+v", lows)
	}
	if len(highs) != 1 || highs[0].Low != 101 || highs[0].High != 102 {
		t.Fatalf("high zones = %+v", highs)
	}
}

func TestBuildTradePlanFromStructureUsesFourStagedEntries(t *testing.T) {
	opportunity := planOpportunity(70, false, 0.01)
	plan, err := buildTradePlanFromStructure(opportunity, PriceZone{Low: 94, High: 96}, []PriceZone{{Low: 105, High: 106}, {Low: 112, High: 113}}, 94, 2, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 3 || plan.Entries[0].Weight != 0.30 || plan.Entries[1].Weight != 0.25 || plan.Entries[2].Weight != 0.25 || plan.ConfirmationEntry.Weight != 0.20 {
		t.Fatalf("entry weights = %+v / %+v", plan.Entries, plan.ConfirmationEntry)
	}
	if math.Abs(plan.LeftAverageCost-95.0625) > 1e-9 || math.Abs(plan.FullAverageCost-96.25) > 1e-9 || plan.StopPrice != 93 {
		t.Fatalf("averages/stop = %v/%v/%v", plan.LeftAverageCost, plan.FullAverageCost, plan.StopPrice)
	}
	if len(plan.Targets) != 2 || plan.Targets[0].Price != 105 || plan.Targets[1].Price != 112 || plan.Targets[0].RewardRisk < 1.3 {
		t.Fatalf("targets = %+v", plan.Targets)
	}
	if plan.TrailingRule == "" {
		t.Fatal("trailing rule is empty")
	}
}

func TestBuildTradePlanExpandsSinglePivotIntoStagedSupportZone(t *testing.T) {
	opportunity := planOpportunity(70, false, 0.01)
	plan, err := buildTradePlanFromStructure(opportunity, PriceZone{Low: 94, High: 94}, []PriceZone{{Low: 105, High: 106}}, 94, 2, 101)
	if err != nil {
		t.Fatal(err)
	}
	if !(plan.Entries[0].Price > plan.Entries[1].Price && plan.Entries[1].Price > plan.Entries[2].Price) {
		t.Fatalf("single pivot did not form staged prices: %+v", plan.Entries)
	}
	if plan.StopPrice != 93 {
		t.Fatalf("stop = %v, want pivot 94 minus 0.5 ATR", plan.StopPrice)
	}
}

func TestConfirmationLevelExcludesCurrentBreakoutCandle(t *testing.T) {
	candles := make([]domain.Candle, 22)
	for index := range candles {
		candles[index] = domain.Candle{High: 100, Close: 99, Confirmed: true}
	}
	candles[len(candles)-1].High = 120
	candles[len(candles)-1].Close = 115
	if got := confirmationLevel(candles); got != 100 {
		t.Fatalf("confirmation level = %v, want prior structure high 100", got)
	}
}

func TestSupportPivotsIncludeFourHourMovingAverages(t *testing.T) {
	pivots := supportPivots([]Pivot{{Price: 92}}, IndicatorSeries{EMA20: []float64{95}, EMA50: []float64{90}}, 100)
	prices := map[float64]bool{}
	for _, pivot := range pivots {
		prices[pivot.Price] = true
	}
	if !prices[95] || !prices[90] {
		t.Fatalf("support pivots = %+v, want EMA20 and EMA50", pivots)
	}
}

func TestBuildTradePlanFromStructureRoundsSmallTicks(t *testing.T) {
	opportunity := planOpportunity(75, false, 0.000001)
	opportunity.CurrentPrice = 0.124
	plan, err := buildTradePlanFromStructure(opportunity, PriceZone{Low: 0.1234567, High: 0.1234599}, []PriceZone{{Low: 0.13, High: 0.131}}, 0.1234567, 0.00001, 0.124)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range append(plan.Entries, plan.ConfirmationEntry) {
		if math.Abs(entry.Price*1_000_000-math.Round(entry.Price*1_000_000)) > 1e-7 {
			t.Fatalf("entry is off tick: %.10f", entry.Price)
		}
	}
}

func TestBuildTradePlanRejectsRiskAndRewardBoundaries(t *testing.T) {
	opportunity := planOpportunity(70, false, 0.01)
	_, err := buildTradePlanFromStructure(opportunity, PriceZone{Low: 80, High: 82}, []PriceZone{{Low: 105, High: 106}}, 80, 20, 101)
	if !errors.Is(err, ErrNoTradePlan) {
		t.Fatalf("risk above 15%% error = %v", err)
	}

	_, err = buildTradePlanFromStructure(opportunity, PriceZone{Low: 94, High: 96}, []PriceZone{{Low: 100.4, High: 101}}, 91, 2, 99)
	if !errors.Is(err, ErrNoTradePlan) {
		t.Fatalf("RR below 1.3 error = %v", err)
	}

	plan, err := buildTradePlanFromStructure(opportunity, PriceZone{Low: 94, High: 96}, []PriceZone{{Low: 100.6, High: 101}}, 94, 2, 99)
	if err != nil || plan.Targets[0].RewardRisk < 1.3 {
		t.Fatalf("RR boundary plan/error = %+v/%v", plan, err)
	}
}

func TestBuildTradePlanAcceptsRiskExactlyFifteenPercent(t *testing.T) {
	opportunity := planOpportunity(70, false, 0.0001)
	plan, err := buildTradePlanFromStructure(opportunity, PriceZone{Low: 94, High: 96}, []PriceZone{{Low: 120, High: 121}}, 82.8125, 2, 101)
	if err != nil || math.Abs(plan.RiskPct-15) > 0.0001 {
		t.Fatalf("exact 15%% plan/error = %+v/%v", plan, err)
	}
	_, err = buildTradePlanFromStructure(opportunity, PriceZone{Low: 94, High: 96}, []PriceZone{{Low: 120, High: 121}}, 82.8, 2, 101)
	if !errors.Is(err, ErrNoTradePlan) {
		t.Fatalf("risk above 15%% error = %v", err)
	}
}

func TestBuildTradePlanRejectsLowScoreAndHighVolatilityNeedsSeventy(t *testing.T) {
	for _, opportunity := range []domain.Opportunity{planOpportunity(64, false, 0.01), planOpportunity(69, true, 0.01)} {
		_, err := buildTradePlanFromStructure(opportunity, PriceZone{Low: 94, High: 96}, []PriceZone{{Low: 105, High: 106}}, 94, 2, 101)
		if !errors.Is(err, ErrNoTradePlan) {
			t.Fatalf("score %d/high-vol %v error = %v", opportunity.FOMOScore, opportunity.HighVolatility, err)
		}
	}
}

func planOpportunity(score int, highVolatility bool, tick float64) domain.Opportunity {
	return domain.Opportunity{
		Instrument:   domain.MarketInstrument{InstrumentID: "TEST-USDT-SWAP", Symbol: "TEST", TickSize: tick},
		CurrentPrice: 100, FOMOScore: score, HighVolatility: highVolatility,
		Evidence: []domain.TimeframeEvidence{{Timeframe: domain.Timeframe1D, Confirmed: true}},
	}
}
