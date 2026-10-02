package scanner

import (
	"math"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/config"
	"github.com/alen1/fomo-radar/internal/domain"
)

func TokenID(chain domain.Chain, address string) string {
	normalized := address
	if chain == domain.ChainBSC {
		normalized = strings.ToLower(address)
	}
	return strings.ToLower(string(chain)) + ":" + normalized
}

const (
	RejectTooOld       = "too_old"
	RejectLowLiquidity = "low_liquidity"
)

type AgeBand string

const (
	AgeUnknown   AgeBand = "UNKNOWN"
	AgeEarlyPool AgeBand = "EARLY_POOL"
	AgeEarlyLate AgeBand = "EARLY_LATE"
)

type FilterDecision struct {
	Accepted         bool
	RejectReason     string
	AgeBand          AgeBand
	Deprioritized    bool
	MarketCapQuality domain.Quality
}

// TokenState describes prior local knowledge used only for launch classification.
type TokenState struct {
	Exists      bool
	FirstSeenAt time.Time
}

// ClassifyLaunch explains whether a recent pool belongs to a new or older token/pair.
func ClassifyLaunch(candidate domain.Candidate, market domain.MarketSnapshot, token TokenState, now time.Time, cfg config.Config) (domain.LaunchType, string) {
	selectedCreatedAt := market.SelectedPairCreatedAt
	if selectedCreatedAt == nil {
		selectedCreatedAt = market.PairCreatedAt
	}
	if selectedCreatedAt != nil && cfg.EarlyMaxAge > 0 && now.Sub(*selectedCreatedAt) > cfg.EarlyMaxAge {
		return domain.LaunchTypeReactivation, "recent discovery pool with older selected pair"
	}
	if token.Exists && cfg.EarlyMaxAge > 0 && now.Sub(token.FirstSeenAt) > cfg.EarlyMaxAge {
		return domain.LaunchTypeReactivation, "recent discovery pool for previously observed token"
	}
	return domain.LaunchTypeNewLaunch, "recent discovery pool with no older token or selected pair evidence"
}

// PreliminaryScore is intentionally separate from the persisted final score.
type PreliminaryScore struct {
	Total      float64
	Components map[string]float64
}

func FastFilter(candidate domain.Candidate, market domain.MarketSnapshot, now time.Time, cfg config.Config) FilterDecision {
	isMomentum := candidate.Origin == domain.CandidateOriginMomentum || candidate.Origin == domain.CandidateOriginBoth
	decision := FilterDecision{
		Accepted:         true,
		AgeBand:          AgeUnknown,
		MarketCapQuality: canonicalQuality(market.MarketCapUSD.Quality),
	}
	if age, ok := tokenAge(candidate, market, now); ok {
		switch {
		case !isMomentum && cfg.CandidateMaxAge > 0 && age > cfg.CandidateMaxAge:
			decision.Accepted = false
			decision.RejectReason = RejectTooOld
			return decision
		case cfg.EarlyMaxAge <= 0 || age <= cfg.EarlyMaxAge:
			decision.AgeBand = AgeEarlyPool
		default:
			decision.AgeBand = AgeEarlyLate
		}
	}
	if dataAvailable(market.LiquidityUSD.Quality) && market.LiquidityUSD.Value < 2_000 {
		decision.Accepted = false
		decision.RejectReason = RejectLowLiquidity
		return decision
	}
	decision.Deprioritized = !isMomentum && cfg.PreferredMaxMCUSD > 0 && dataAvailable(market.MarketCapUSD.Quality) && market.MarketCapUSD.Value > cfg.PreferredMaxMCUSD
	return decision
}

func CalculatePreliminary(candidate domain.Candidate, market domain.MarketSnapshot, now time.Time) PreliminaryScore {
	components := map[string]float64{
		"volume_acceleration": 0,
		"buy_pressure":        0,
		"liquidity_quality":   0,
		"age_bonus":           0,
		"activity_breadth":    0,
	}
	if dataAvailable(market.VolumeM5USD.Quality) && dataAvailable(market.VolumeH1USD.Quality) {
		components["volume_acceleration"] = thresholdPoints(ratio(market.VolumeM5USD.Value, market.VolumeH1USD.Value/12), 25)
	}
	if dataAvailable(market.BuysM5.Quality) && dataAvailable(market.SellsM5.Quality) {
		components["buy_pressure"] = thresholdPoints(ratio(float64(market.BuysM5.Value), float64(market.SellsM5.Value)), 20)
		total := market.BuysM5.Value + market.SellsM5.Value
		switch {
		case total >= 20:
			components["activity_breadth"] = 20
		case total >= 10:
			components["activity_breadth"] = 15
		case total >= 5:
			components["activity_breadth"] = 10
		case total > 0:
			components["activity_breadth"] = 5
		}
	}
	if dataAvailable(market.LiquidityUSD.Quality) && dataAvailable(market.MarketCapUSD.Quality) {
		r := ratio(market.LiquidityUSD.Value, market.MarketCapUSD.Value)
		switch {
		case r >= .20:
			components["liquidity_quality"] = 15
		case r >= .10:
			components["liquidity_quality"] = 12
		case r >= .05:
			components["liquidity_quality"] = 9
		case r >= .02:
			components["liquidity_quality"] = 6
		case r >= .01:
			components["liquidity_quality"] = 3
		}
	}
	if age, ok := tokenAge(candidate, market, now); ok {
		switch {
		case age <= 30*time.Minute:
			components["age_bonus"] = 20
		case age <= time.Hour:
			components["age_bonus"] = 15
		case age <= 2*time.Hour:
			components["age_bonus"] = 10
		case age <= 6*time.Hour:
			components["age_bonus"] = 5
		}
	}
	total := 0.0
	for _, points := range components {
		total += points
	}
	return PreliminaryScore{Total: math.Min(total, 100), Components: components}
}

func tokenAge(candidate domain.Candidate, market domain.MarketSnapshot, now time.Time) (time.Duration, bool) {
	createdAt := candidate.DiscoveryPoolCreatedAt
	if createdAt == nil {
		createdAt = market.DiscoveryPoolCreatedAt
	}
	if createdAt == nil {
		createdAt = candidate.ChainCreatedAt
	}
	if createdAt == nil {
		createdAt = candidate.TriggerPoolCreatedAt
	}
	if createdAt == nil {
		createdAt = market.TriggerPoolCreatedAt
	}
	if createdAt == nil {
		createdAt = market.SelectedPairCreatedAt
	}
	if createdAt == nil {
		createdAt = market.PairCreatedAt
	}
	if createdAt == nil {
		return 0, false
	}
	age := now.Sub(*createdAt)
	if age < 0 {
		age = 0
	}
	return age, true
}

func thresholdPoints(value, maximum float64) float64 {
	switch {
	case value >= 4:
		return maximum
	case value >= 3:
		return maximum * .85
	case value >= 2:
		return maximum * .65
	case value >= 1.5:
		return maximum * .45
	case value >= 1.1:
		return maximum * .25
	default:
		return 0
	}
}

func ratio(numerator, denominator float64) float64 {
	if denominator == 0 {
		if numerator > 0 {
			return math.Inf(1)
		}
		return 0
	}
	if denominator < 0 {
		return 0
	}
	return numerator / denominator
}

func dataAvailable(quality domain.Quality) bool {
	return quality == domain.QualityFresh || quality == domain.QualityCached || quality == domain.QualityDegraded
}

func canonicalQuality(quality domain.Quality) domain.Quality {
	if dataAvailable(quality) || quality == domain.QualityMissing {
		return quality
	}
	return domain.QualityMissing
}
