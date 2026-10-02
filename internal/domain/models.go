package domain

import "time"

// ScoreRole 表示一个不可变评分版本当前的运行角色。
type ScoreRole string

const (
	ScoreRoleChampion   ScoreRole = "CHAMPION"
	ScoreRoleChallenger ScoreRole = "CHALLENGER"
	ScoreRoleRetired    ScoreRole = "RETIRED"
)

// ScoreAssignment 绑定不可变评分配置及其运行角色。
type ScoreAssignment struct {
	Version    string    `json:"version"`
	Role       ScoreRole `json:"role"`
	ConfigJSON []byte    `json:"config_json"`
	CreatedAt  time.Time `json:"created_at"`
	ChangedAt  time.Time `json:"changed_at"`
}

// VersionedScore 表示同一市场观察下某个版本的不可变评分结果。
type VersionedScore struct {
	Version   string         `json:"version"`
	Role      ScoreRole      `json:"role"`
	Breakdown ScoreBreakdown `json:"breakdown"`
}

// ScoreReference 只记录冻结的模型版本和角色，不把配置复制进队列任务。
type ScoreReference struct {
	Version string    `json:"version"`
	Role    ScoreRole `json:"role"`
}

// Chain identifies a canonical blockchain supported by the scanner.
type Chain string

const (
	ChainBSC    Chain = "bsc"
	ChainSolana Chain = "solana"
)

// UniverseAdmissionSource records why a token entered the bounded active universe.
type UniverseAdmissionSource string

const (
	UniverseAdmissionHistorical UniverseAdmissionSource = "HISTORICAL"
	UniverseAdmissionTrending   UniverseAdmissionSource = "TRENDING"
	UniverseAdmissionAnomaly    UniverseAdmissionSource = "ANOMALY"
)

// CandidateOrigin records which discovery path emitted one candidate observation.
type CandidateOrigin string

const (
	CandidateOriginLaunch   CandidateOrigin = "LAUNCH"
	CandidateOriginMomentum CandidateOrigin = "MOMENTUM"
	CandidateOriginBoth     CandidateOrigin = "BOTH"
)

// MomentumSource identifies one bounded source that contributed to discovery.
type MomentumSource string

const (
	MomentumSourceGeckoTrending  MomentumSource = "GECKO_TRENDING"
	MomentumSourceGeckoTopVolume MomentumSource = "GECKO_TOP_VOLUME"
	MomentumSourceDexBoostTop    MomentumSource = "DEX_BOOST_TOP"
	MomentumSourceLocalUniverse  MomentumSource = "LOCAL_UNIVERSE"
)

// TokenAgeSource identifies the evidence used to derive token age.
type TokenAgeSource string

const (
	TokenAgeSourceOnChain        TokenAgeSource = "ON_CHAIN_CREATION"
	TokenAgeSourceEarliestPair   TokenAgeSource = "EARLIEST_PAIR"
	TokenAgeSourceLocalFirstSeen TokenAgeSource = "LOCAL_FIRST_SEEN_LOWER_BOUND"
)

// MomentumTrigger records why a seed passed the versioned discovery gate.
type MomentumTrigger struct {
	GateVersion string         `json:"gate_version"`
	Source      MomentumSource `json:"source"`
	Reasons     []string       `json:"reasons"`
}

// MomentumFactorEvidence stores one raw, provenance-bearing factor input. It has
// no score points because final V2 weights are not yet frozen.
type MomentumFactorEvidence struct {
	Available  bool       `json:"available"`
	Value      float64    `json:"value"`
	Unit       string     `json:"unit"`
	Source     string     `json:"source"`
	Window     string     `json:"window"`
	ObservedAt time.Time  `json:"observed_at"`
	BaselineAt *time.Time `json:"baseline_at,omitempty"`
}

// MomentumEvidence is the versioned raw evidence envelope used for later Lab study.
type MomentumEvidence struct {
	Version string                            `json:"version"`
	Factors map[string]MomentumFactorEvidence `json:"factors"`
}

// MomentumState is compact operational state used only as a strictly earlier
// baseline. Historical observations remain in snapshots.
type MomentumState struct {
	TokenID           string             `json:"token_id"`
	ObservedAt        time.Time          `json:"observed_at"`
	PairAddress       string             `json:"pair_address"`
	PriceUSD          DataValue[float64] `json:"price_usd"`
	LiquidityUSD      DataValue[float64] `json:"liquidity_usd"`
	VolumeM5USD       DataValue[float64] `json:"volume_m5_usd"`
	VolumeH1USD       DataValue[float64] `json:"volume_h1_usd"`
	VolumeH6USD       DataValue[float64] `json:"volume_h6_usd"`
	VolumeH24USD      DataValue[float64] `json:"volume_h24_usd"`
	AggregateBuyersM5 DataValue[int]     `json:"aggregate_buyers_m5"`
	AggregateBuyersH1 DataValue[int]     `json:"aggregate_buyers_h1"`
	Sources           []MomentumSource   `json:"sources"`
}

// Candidate is a discovered token that may be evaluated by the scanner.
type Candidate struct {
	ID                     string             `json:"id"`
	Chain                  Chain              `json:"chain"`
	Address                string             `json:"address"`
	Name                   string             `json:"name"`
	Symbol                 string             `json:"symbol"`
	PairAddress            string             `json:"pair_address"`
	DiscoveryPoolAddress   string             `json:"discovery_pool_address"`
	Origin                 CandidateOrigin    `json:"candidate_origin"`
	MomentumSources        []MomentumSource   `json:"momentum_sources,omitempty"`
	TriggerPoolAddress     string             `json:"trigger_pool_address"`
	DetectedAt             time.Time          `json:"detected_at"`
	ChainCreatedAt         *time.Time         `json:"chain_created_at,omitempty"`
	DiscoveryPoolCreatedAt *time.Time         `json:"discovery_pool_created_at,omitempty"`
	TriggerPoolCreatedAt   *time.Time         `json:"trigger_pool_created_at,omitempty"`
	DiscoveryMarket        MarketSnapshot     `json:"discovery_market"`
	FirstSeenPriceUSD      DataValue[float64] `json:"first_seen_price_usd"`
	FirstSeenMarketCapUSD  DataValue[float64] `json:"first_seen_market_cap_usd"`
	FirstSeenScore         DataValue[float64] `json:"first_seen_score"`
}

// MarketSnapshot contains chain-agnostic market observations for a candidate.
type MarketSnapshot struct {
	ID                     int64              `json:"id"`
	TokenID                string             `json:"token_id"`
	PairAddress            string             `json:"pair_address"`
	DiscoveryPoolAddress   string             `json:"discovery_pool_address"`
	CandidateOrigin        CandidateOrigin    `json:"candidate_origin"`
	MomentumSources        []MomentumSource   `json:"momentum_sources,omitempty"`
	MomentumTrigger        *MomentumTrigger   `json:"momentum_trigger,omitempty"`
	TriggerPoolAddress     string             `json:"trigger_pool_address"`
	PairCreatedAt          *time.Time         `json:"pair_created_at,omitempty"`
	DiscoveryPoolCreatedAt *time.Time         `json:"discovery_pool_created_at,omitempty"`
	SelectedPairCreatedAt  *time.Time         `json:"selected_pair_created_at,omitempty"`
	TriggerPoolCreatedAt   *time.Time         `json:"trigger_pool_created_at,omitempty"`
	TokenAgeSeconds        DataValue[int64]   `json:"token_age_seconds"`
	TokenAgeSource         TokenAgeSource     `json:"token_age_source"`
	SignalFirstSeenAt      time.Time          `json:"signal_first_seen_at"`
	Stage                  SnapshotStage      `json:"stage"`
	BaseFastSnapshotID     *int64             `json:"base_fast_snapshot_id,omitempty"`
	DeepStatus             DeepStatus         `json:"deep_status"`
	LaunchType             LaunchType         `json:"launch_type"`
	MarketDataSource       MarketDataSource   `json:"market_data_source"`
	MarketDataQuality      MarketDataQuality  `json:"market_data_quality"`
	ScanRunID              *int64             `json:"scan_run_id,omitempty"`
	CollectedAt            time.Time          `json:"collected_at"`
	SourceTime             *time.Time         `json:"source_time,omitempty"`
	PriceUSD               DataValue[float64] `json:"price_usd"`
	MarketCapUSD           DataValue[float64] `json:"market_cap_usd"`
	FDVUSD                 DataValue[float64] `json:"fdv_usd"`
	LiquidityUSD           DataValue[float64] `json:"liquidity_usd"`
	VolumeM5USD            DataValue[float64] `json:"volume_m5_usd"`
	VolumeH1USD            DataValue[float64] `json:"volume_h1_usd"`
	VolumeH6USD            DataValue[float64] `json:"volume_h6_usd"`
	Volume24hUSD           DataValue[float64] `json:"volume_24h_usd"`
	BuysM5                 DataValue[int]     `json:"buys_m5"`
	SellsM5                DataValue[int]     `json:"sells_m5"`
	BuysH1                 DataValue[int]     `json:"buys_h1"`
	SellsH1                DataValue[int]     `json:"sells_h1"`
	BuysH6                 DataValue[int]     `json:"buys_h6"`
	SellsH6                DataValue[int]     `json:"sells_h6"`
	BuysH24                DataValue[int]     `json:"buys_h24"`
	SellsH24               DataValue[int]     `json:"sells_h24"`
	BuyersM5               DataValue[int]     `json:"buyers_m5"`
	SellersM5              DataValue[int]     `json:"sellers_m5"`
	AggregateBuyersM5      DataValue[int]     `json:"aggregate_buyers_m5"`
	AggregateSellersM5     DataValue[int]     `json:"aggregate_sellers_m5"`
	AggregateBuyersH1      DataValue[int]     `json:"aggregate_buyers_h1"`
	AggregateSellersH1     DataValue[int]     `json:"aggregate_sellers_h1"`
	AggregateBuyersH6      DataValue[int]     `json:"aggregate_buyers_h6"`
	AggregateSellersH6     DataValue[int]     `json:"aggregate_sellers_h6"`
	AggregateBuyersH24     DataValue[int]     `json:"aggregate_buyers_h24"`
	AggregateSellersH24    DataValue[int]     `json:"aggregate_sellers_h24"`
	PriceChangeM5          DataValue[float64] `json:"price_change_m5"`
	PriceChangeH1          DataValue[float64] `json:"price_change_h1"`
	PriceChangeH6          DataValue[float64] `json:"price_change_h6"`
	PriceChangeH24         DataValue[float64] `json:"price_change_h24"`
	Holders                DataValue[int]     `json:"holders"`
	BoostsActive           DataValue[int]     `json:"boosts_active"`
	Links                  []Link             `json:"links,omitempty"`
	Score                  DataValue[float64] `json:"score"`
	Tier                   string             `json:"tier"`
	ScoreVersion           string             `json:"score_version"`
	ScoreBreakdown         ScoreBreakdown     `json:"score_breakdown"`
	RiskFlags              []string           `json:"risk_flags"`
	DataQuality            map[string]Quality `json:"data_quality"`
	LaunchBonus            DataValue[float64] `json:"launch_bonus"`
	MomentumBaselineAt     *time.Time         `json:"momentum_baseline_at,omitempty"`
	MomentumEvidence       *MomentumEvidence  `json:"momentum_evidence,omitempty"`
}

// SnapshotStage identifies whether an observation was produced by Fast or Deep processing.
type SnapshotStage string

const (
	SnapshotStageFast SnapshotStage = "FAST"
	SnapshotStageDeep SnapshotStage = "DEEP"
)

// DeepStatus records the lifecycle of Deep work associated with a Fast snapshot.
type DeepStatus string

const (
	DeepStatusPending     DeepStatus = "pending"
	DeepStatusQueued      DeepStatus = "queued"
	DeepStatusCompleted   DeepStatus = "completed"
	DeepStatusFailed      DeepStatus = "failed"
	DeepStatusDeferred    DeepStatus = "deferred"
	DeepStatusStale       DeepStatus = "stale"
	DeepStatusNotRequired DeepStatus = "not_required"
)

// LaunchType distinguishes a new launch from new momentum around an older token or pair.
type LaunchType string

const (
	LaunchTypeNewLaunch    LaunchType = "NEW_LAUNCH"
	LaunchTypeReactivation LaunchType = "REACTIVATION"
)

// MarketDataSource identifies which cheap market providers contributed evidence.
type MarketDataSource string

const (
	MarketDataSourceGecko  MarketDataSource = "gecko"
	MarketDataSourceDex    MarketDataSource = "dex"
	MarketDataSourceMerged MarketDataSource = "merged"
)

// MarketDataQuality summarizes completeness without replacing per-field quality.
type MarketDataQuality string

const (
	MarketDataQualityPartial MarketDataQuality = "partial"
	MarketDataQualityFull    MarketDataQuality = "full"
)

// DeepTask identifies one immutable Fast generation awaiting Deep evidence.
type DeepTask struct {
	TokenID            string           `json:"token_id"`
	BaseFastSnapshotID int64            `json:"base_fast_snapshot_id"`
	Candidate          Candidate        `json:"candidate"`
	Snapshot           MarketSnapshot   `json:"snapshot"`
	ScoreReferences    []ScoreReference `json:"score_references,omitempty"`
}

// DiscoveryCoverage records the actual API window observed for one chain.
type DiscoveryCoverage struct {
	Chain            Chain     `json:"chain"`
	ScanStartedAt    time.Time `json:"scan_started_at"`
	NewestPoolAt     time.Time `json:"newest_pool_at"`
	OldestPoolAt     time.Time `json:"oldest_pool_at"`
	PagesFetched     int       `json:"pages_fetched"`
	UniqueCandidates int       `json:"unique_candidates"`
}

// DiscoverySourceReport records one source attempt without claiming time coverage
// for ranked or cursor-based Momentum sources.
type DiscoverySourceReport struct {
	Chain            Chain      `json:"chain"`
	Provider         string     `json:"provider"`
	Source           string     `json:"source"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       time.Time  `json:"finished_at"`
	PagesFetched     int        `json:"pages_fetched"`
	ReturnedItems    int        `json:"returned_items"`
	UniqueCandidates int        `json:"unique_candidates"`
	NewestPoolAt     *time.Time `json:"newest_pool_at,omitempty"`
	OldestPoolAt     *time.Time `json:"oldest_pool_at,omitempty"`
	CursorBefore     string     `json:"cursor_before"`
	CursorAfter      string     `json:"cursor_after"`
	Error            string     `json:"error"`
}

// DiscoveryBatch pairs candidates with the API coverage that produced them.
type DiscoveryBatch struct {
	Candidates []Candidate       `json:"candidates"`
	Coverage   DiscoveryCoverage `json:"coverage"`
}

// DiscoveryResult 汇总多来源候选、审计报告和可降级错误。
type DiscoveryResult struct {
	Candidates []Candidate                 `json:"candidates"`
	Reports    []DiscoverySourceReport     `json:"reports"`
	Errors     []string                    `json:"errors"`
	Coverages  map[Chain]DiscoveryCoverage `json:"coverages"`
}

// TradeKind identifies which side of a pool trade the observed account took.
type TradeKind string

const (
	TradeBuy  TradeKind = "buy"
	TradeSell TradeKind = "sell"
)

// Trade is a provider-neutral observed pool trade.
type Trade struct {
	FromAddress    string    `json:"from_address"`
	Kind           TradeKind `json:"kind"`
	VolumeUSD      float64   `json:"volume_usd"`
	BlockTimestamp time.Time `json:"block_timestamp"`
}

// TradeStats aggregates one rolling trade interval.
type TradeStats struct {
	Start                 time.Time `json:"start"`
	End                   time.Time `json:"end"`
	Buys                  int       `json:"buys"`
	Sells                 int       `json:"sells"`
	UniqueBuyers          int       `json:"unique_buyers"`
	UniqueSellers         int       `json:"unique_sellers"`
	BuyVolumeUSD          float64   `json:"buy_volume_usd"`
	SellVolumeUSD         float64   `json:"sell_volume_usd"`
	TopAddressVolumeShare float64   `json:"top_address_volume_share"`
}

// TradeWindow contains current and previous five-minute observations.
type TradeWindow struct {
	CollectedAt time.Time  `json:"collected_at"`
	Trades      []Trade    `json:"trades"`
	Current     TradeStats `json:"current"`
	Previous    TradeStats `json:"previous"`
	Truncated   bool       `json:"truncated"`
}

// LinkKind identifies public project metadata without assigning momentum to it.
type LinkKind string

const (
	LinkWebsite  LinkKind = "website"
	LinkX        LinkKind = "x"
	LinkTelegram LinkKind = "telegram"
	LinkOther    LinkKind = "other"
)

// Link is one public project URL returned by token metadata.
type Link struct {
	Kind LinkKind `json:"kind"`
	URL  string   `json:"url"`
}

// SocialEvidence separates public links from measured indexed mentions.
type SocialEvidence struct {
	CollectedAt  time.Time      `json:"collected_at"`
	Links        []Link         `json:"links,omitempty"`
	MentionCount DataValue[int] `json:"mention_count"`
}

// Tier identifies the dashboard visibility band for a score.
type Tier string

const (
	TierBreakout   Tier = "BREAKOUT"
	TierFastRising Tier = "FAST_RISING"
	TierWatch      Tier = "WATCH"
	TierHidden     Tier = "HIDDEN"
)

// EvidenceConfidence describes how many required core factors support a score.
type EvidenceConfidence string

const (
	EvidenceConfidenceHigh   EvidenceConfidence = "HIGH"
	EvidenceConfidenceGood   EvidenceConfidence = "GOOD"
	EvidenceConfidenceMedium EvidenceConfidence = "MEDIUM"
	EvidenceConfidenceLow    EvidenceConfidence = "LOW"
)

// FactorScore records one independently auditable scoring factor.
type FactorScore struct {
	Points    float64 `json:"points"`
	Maximum   float64 `json:"maximum"`
	Available bool    `json:"available"`
}

// ScoreBreakdown explains the score used to prioritize candidates.
type ScoreBreakdown struct {
	Version            string                 `json:"version"`
	Factors            map[string]FactorScore `json:"factors"`
	RawMomentum        float64                `json:"raw_momentum"`
	AvailableMaximum   float64                `json:"available_maximum"`
	NormalizedMomentum float64                `json:"normalized_momentum"`
	AgeBonus           float64                `json:"age_bonus"`
	RiskPenalty        float64                `json:"risk_penalty"`
	RiskFlags          []string               `json:"risk_flags"`
	EvidenceQuality    map[string]Quality     `json:"evidence_quality"`
	Final              float64                `json:"final"`
	Tier               Tier                   `json:"tier"`
	RawScore           float64                `json:"raw_score"`
	RawTier            Tier                   `json:"raw_tier"`
	EvidenceAvailable  int                    `json:"evidence_available"`
	EvidenceTotal      int                    `json:"evidence_total"`
	EvidenceConfidence EvidenceConfidence     `json:"evidence_confidence"`
	EffectiveTier      Tier                   `json:"effective_tier"`
}
