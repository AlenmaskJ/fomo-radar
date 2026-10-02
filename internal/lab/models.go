package lab

import "time"

const AlgorithmVersion = "SIGNAL_LAB_V1.0"

func Thresholds() []int {
	return []int{55, 60, 65, 70, 75, 80, 85, 90}
}

type SignalSource string

const (
	SignalBackfill SignalSource = "BACKFILL"
	SignalLive     SignalSource = "LIVE"
)

type EntryStatus string

const (
	EntryEvaluable        EntryStatus = "EVALUABLE"
	EntryUnavailablePrice EntryStatus = "UNAVAILABLE_PRICE"
)

type OutcomeStatus string

const (
	OutcomePending          OutcomeStatus = "PENDING"
	OutcomeMatured          OutcomeStatus = "MATURED"
	OutcomeInsufficientData OutcomeStatus = "INSUFFICIENT_DATA"
	OutcomeNotEvaluable     OutcomeStatus = "NOT_EVALUABLE"
)

type Horizon struct {
	Key       string
	Offset    time.Duration
	Tolerance time.Duration
}

var frozenHorizons = [...]Horizon{
	{Key: "5m", Offset: 5 * time.Minute, Tolerance: 3 * time.Minute},
	{Key: "15m", Offset: 15 * time.Minute, Tolerance: 3 * time.Minute},
	{Key: "30m", Offset: 30 * time.Minute, Tolerance: 3 * time.Minute},
	{Key: "1h", Offset: time.Hour, Tolerance: 10 * time.Minute},
	{Key: "3h", Offset: 3 * time.Hour, Tolerance: 10 * time.Minute},
	{Key: "6h", Offset: 6 * time.Hour, Tolerance: 10 * time.Minute},
	{Key: "24h", Offset: 24 * time.Hour, Tolerance: time.Hour},
}

func Horizons() []Horizon {
	result := make([]Horizon, len(frozenHorizons))
	copy(result, frozenHorizons[:])
	return result
}

type SourceRegistration struct {
	CanonicalPath        string
	Identity             string
	ActivationSnapshotID int64
	CurrentSourceMaximum int64
	InitializedAt        time.Time
}

type LabState struct {
	AlgorithmVersion             string
	CanonicalSourcePath          string
	SourceDBIdentity             string
	ActivationSnapshotID         int64
	LastProcessedSnapshotID      int64
	LastCycleSourceHighWatermark int64
	SourceObservationFrontierAt  *time.Time
}

type SourceFrontier struct {
	HighWatermark int64
	ObservationAt *time.Time
}

type SourceSnapshot struct {
	ID                     int64
	TokenID                string
	Chain                  string
	Address                string
	Name                   string
	Symbol                 string
	CollectedAt            time.Time
	SourceTime             *time.Time
	PriceUSD               *float64
	MarketCapUSD           *float64
	LiquidityUSD           *float64
	TokenAgeSeconds        *int64
	RawScore               *float64
	RawTier                string
	EffectiveTier          string
	EvidenceConfidence     string
	EvidenceAvailable      int
	EvidenceTotal          int
	ScoreVersion           string
	ScoreConfigJSON        string
	ScoreBreakdownJSON     string
	RiskFlagsJSON          string
	DataQualityJSON        string
	DiscoveryPoolCreatedAt *time.Time
	SelectedPairCreatedAt  *time.Time
}

type ApplyResult struct {
	SourceSnapshotsProcessed int
	SignalsCreated           int
	EntriesCreated           int
	LastSnapshotID           int64
}

type CycleResult struct {
	ActivationSnapshotID      int64
	SourceHighWatermark       int64
	SourceObservationFrontier *time.Time
	SourceSnapshotsProcessed  int
	SignalsCreated            int
	EntriesCreated            int
	OutcomesMatured           int
	OutcomesInsufficient      int
}

type PendingOutcome struct {
	ID               int64
	SignalID         int64
	TokenID          string
	Horizon          string
	SignalSnapshotID int64
	SignalAt         time.Time
	TargetAt         time.Time
	WindowEndAt      time.Time
	EntryPriceUSD    float64
}

type PriceObservation struct {
	SnapshotID  int64
	CollectedAt time.Time
	PriceUSD    float64
}

type OutcomeResult struct {
	Examined     int
	Matured      int
	Insufficient int
}

type AnalysisRun struct {
	ID                         int64      `json:"id"`
	AlgorithmVersion           string     `json:"algorithm_version"`
	GeneratedAt                time.Time  `json:"generated_at"`
	PersistedThroughSnapshotID int64      `json:"persisted_through_snapshot_id"`
	SourceObservationFrontier  *time.Time `json:"source_observation_frontier_at"`
}

type ThresholdStatistic struct {
	SignalSourceScope  string   `json:"signal_source_scope"`
	ChainScope         string   `json:"chain_scope"`
	EvidenceScope      string   `json:"evidence_scope"`
	ScoreVersion       string   `json:"score_version"`
	Threshold          int      `json:"threshold"`
	Horizon            string   `json:"horizon"`
	Signals            int      `json:"signals"`
	EvaluableEntries   int      `json:"evaluable_entries"`
	NotEvaluable       int      `json:"not_evaluable"`
	EntryPriceCoverage *float64 `json:"entry_price_coverage"`
	Pending            int      `json:"pending"`
	Matured            int      `json:"matured"`
	InsufficientData   int      `json:"insufficient_data"`
	MaturityCoverage   *float64 `json:"maturity_coverage"`
	ResultCoverage     *float64 `json:"result_coverage"`
	PositiveReturnRate *float64 `json:"positive_return_rate"`
	MedianReturn       *float64 `json:"median_return"`
	MedianMFE          *float64 `json:"median_mfe"`
	MedianMAE          *float64 `json:"median_mae"`
	InsufficientSample bool     `json:"insufficient_sample"`
}

type ThresholdReport struct {
	Title      string               `json:"title"`
	Run        AnalysisRun          `json:"run"`
	Statistics []ThresholdStatistic `json:"statistics"`
}

// ComparisonReport 只组合最近一次已持久化分析中的指定版本统计。
type ComparisonReport struct {
	Run        AnalysisRun          `json:"run"`
	Statistics []ThresholdStatistic `json:"statistics"`
}
