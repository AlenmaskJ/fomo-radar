package web

import (
	"context"
	"errors"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/markets"
	"github.com/alen1/fomo-radar/internal/marketwatch"
)

var (
	ErrTokenNotFound  = errors.New("token not found")
	ErrAmbiguousToken = errors.New("token address is ambiguous")
)

// Repository is the read-only data boundary used by the Dashboard.
type Repository interface {
	ListRadar(context.Context, int) ([]TokenView, error)
	ListTokens(context.Context, TokenFilter) ([]TokenView, error)
	TokenByAddress(context.Context, string, string, int) (TokenDetail, error)
	DashboardStatus(context.Context) (ScannerStatus, error)
	MarketRadar(context.Context) (markets.Report, error)
	MarketOpportunities(context.Context, domain.AssetClass) (domain.OpportunityReport, error)
	MarketOpportunityHistory(context.Context, domain.AssetClass, OpportunityHistoryFilter) (OpportunityHistoryPage, error)
	Close() error
}

type MarketControl interface {
	Trigger(context.Context) (marketwatch.ScanStatus, error)
	Status() marketwatch.ScanStatus
}

// TokenFilter controls the read-only archive query used by HTML and JSON routes.
type TokenFilter struct {
	Limit              int
	IncludeDetails     bool
	Query              string
	Chain              string
	EffectiveTier      string
	LaunchType         string
	EvidenceConfidence string
	DataQuality        string
}

// FactorView is one persisted FOMO score factor. Missing points stay nil.
type FactorView struct {
	Points    *float64 `json:"points"`
	Maximum   float64  `json:"maximum"`
	Available bool     `json:"available"`
}

// TokenView is the stable current-state DTO shared by HTML and JSON routes.
type TokenView struct {
	ID                     string                `json:"id"`
	SnapshotID             int64                 `json:"snapshot_id"`
	Chain                  string                `json:"chain"`
	Address                string                `json:"address"`
	Name                   string                `json:"name"`
	Symbol                 string                `json:"symbol"`
	PairAddress            string                `json:"pair_address"`
	FirstSeenAt            time.Time             `json:"first_seen_at"`
	CollectedAt            time.Time             `json:"collected_at"`
	AgeSeconds             *int64                `json:"age_seconds"`
	MarketCapUSD           *float64              `json:"market_cap_usd"`
	FDVUSD                 *float64              `json:"fdv_usd"`
	LiquidityUSD           *float64              `json:"liquidity_usd"`
	VolumeM5USD            *float64              `json:"volume_m5_usd"`
	BuysM5                 *int                  `json:"buys_m5"`
	SellsM5                *int                  `json:"sells_m5"`
	UniqueBuyersM5         *int                  `json:"unique_buyers_m5"`
	UniqueSellersM5        *int                  `json:"unique_sellers_m5"`
	AggregateBuyersM5      *int                  `json:"aggregate_buyers_m5"`
	AggregateSellersM5     *int                  `json:"aggregate_sellers_m5"`
	PriceChangeM5          *float64              `json:"price_change_m5"`
	RawScore               *float64              `json:"raw_score"`
	RawTier                string                `json:"raw_tier"`
	EffectiveTier          string                `json:"effective_tier"`
	EvidenceAvailable      int                   `json:"evidence_available"`
	EvidenceTotal          int                   `json:"evidence_total"`
	EvidenceConfidence     string                `json:"evidence_confidence"`
	LaunchType             string                `json:"launch_type"`
	MarketDataSource       string                `json:"market_data_source"`
	DataQuality            string                `json:"data_quality"`
	Stage                  string                `json:"stage"`
	DeepStatus             string                `json:"deep_status"`
	ScoreVersion           string                `json:"score_version"`
	Factors                map[string]FactorView `json:"factors"`
	AgeBonus               float64               `json:"age_bonus"`
	RiskPenalty            float64               `json:"risk_penalty"`
	RiskFlags              []string              `json:"risk_flags"`
	DiscoveryPoolCreatedAt *time.Time            `json:"discovery_pool_created_at"`
	SelectedPairCreatedAt  *time.Time            `json:"selected_pair_created_at"`
	DiscoveryPoolAge       *int64                `json:"discovery_pool_age_seconds"`
	SelectedPairAge        *int64                `json:"selected_pair_age_seconds"`
	PreviousRawScore       *float64              `json:"previous_raw_score"`
	PreviousVolumeM5USD    *float64              `json:"previous_volume_m5_usd"`
	PreviousBuyersM5       *int                  `json:"previous_buyers_m5"`
	MomentumBuyersM5       *int                  `json:"momentum_buyers_m5"`
	MomentumSellersM5      *int                  `json:"momentum_sellers_m5"`
	ParticipantEvidence    string                `json:"participant_evidence"`
	ScoreDelta             *float64              `json:"score_delta"`
	VolumeChangePercent    *float64              `json:"volume_change_percent"`
	BuySellRatio           *float64              `json:"buy_sell_ratio"`
	TotalTradesM5          *int                  `json:"total_trades_m5"`
	DeepTruncated          *bool                 `json:"deep_truncated"`
}

// TimelinePoint is one immutable stored snapshot, not a recalculated sample.
type TimelinePoint struct {
	SnapshotID         int64     `json:"snapshot_id"`
	CollectedAt        time.Time `json:"collected_at"`
	Stage              string    `json:"stage"`
	MarketCapUSD       *float64  `json:"market_cap_usd"`
	LiquidityUSD       *float64  `json:"liquidity_usd"`
	VolumeM5USD        *float64  `json:"volume_m5_usd"`
	BuyersM5           *int      `json:"buyers_m5"`
	SellersM5          *int      `json:"sellers_m5"`
	RawScore           *float64  `json:"raw_score"`
	EffectiveTier      string    `json:"effective_tier"`
	EvidenceConfidence string    `json:"evidence_confidence"`
	DeepStatus         string    `json:"deep_status"`
}

// TokenDetail combines current state with bounded immutable history.
type TokenDetail struct {
	Token               TokenView       `json:"token"`
	Timeline            []TimelinePoint `json:"timeline"`
	ScoreChange         *float64        `json:"score_change"`
	ScoreChangeDuration *int64          `json:"score_change_duration_seconds"`
	ScoreFallingFast    bool            `json:"score_falling_fast"`
}

// CoverageView describes the observed API window persisted by one discovery run.
type CoverageView struct {
	Chain            string     `json:"chain"`
	ScanStartedAt    *time.Time `json:"scan_started_at"`
	NewestPoolAt     *time.Time `json:"newest_pool_at"`
	OldestPoolAt     *time.Time `json:"oldest_pool_at"`
	DurationSeconds  *int64     `json:"duration_seconds"`
	PagesFetched     int        `json:"pages_fetched"`
	UniqueCandidates int        `json:"unique_candidates"`
}

// ScannerStatus is persisted scanner state suitable for the read-only status strip.
type ScannerStatus struct {
	Available        bool         `json:"available"`
	Monitoring       bool         `json:"monitoring"`
	Mode             string       `json:"mode"`
	RunStatus        string       `json:"run_status"`
	LastScanAt       *time.Time   `json:"last_scan_at"`
	NextScanAt       *time.Time   `json:"next_scan_at"`
	CandidatesSeen   int          `json:"candidates_seen"`
	CandidatesScored int          `json:"candidates_scored"`
	ErrorSummary     string       `json:"error_summary"`
	BSC              CoverageView `json:"bsc_coverage"`
	Solana           CoverageView `json:"solana_coverage"`
	DeepQueued       int          `json:"deep_queued"`
	DeepRunning      *int         `json:"deep_running"`
	DeepCompleted    int          `json:"deep_completed"`
	DeepFailed       int          `json:"deep_failed"`
	GeckoStatus      string       `json:"gecko_status"`
	DexStatus        string       `json:"dex_status"`
}

// TokenListResponse is the stable JSON envelope for GET /api/tokens.
type TokenListResponse struct {
	GeneratedAt time.Time   `json:"generated_at"`
	Tokens      []TokenView `json:"tokens"`
}
