package domain

import "time"

// Timeframe 是机会雷达固定使用的 OKX K 线周期。
type Timeframe string

const (
	Timeframe15m Timeframe = "15m"
	Timeframe1H  Timeframe = "1H"
	Timeframe4H  Timeframe = "4H"
	Timeframe1D  Timeframe = "1Dutc"
)

// MarketInstrument 保存筛选与价格舍入所需的产品元数据。
type MarketInstrument struct {
	InstrumentID string    `json:"instrument_id"`
	Symbol       string    `json:"symbol"`
	ListedAt     time.Time `json:"listed_at"`
	TickSize     float64   `json:"tick_size"`
}

// Candle 是按开盘时间升序排列的标准 K 线。
type Candle struct {
	OpenTime    time.Time `json:"open_time"`
	CloseTime   time.Time `json:"close_time"`
	Open        float64   `json:"open"`
	High        float64   `json:"high"`
	Low         float64   `json:"low"`
	Close       float64   `json:"close"`
	VolumeBase  float64   `json:"volume_base"`
	VolumeQuote float64   `json:"volume_quote"`
	Confirmed   bool      `json:"confirmed"`
}

type OpportunityStage string

const (
	StageNone         OpportunityStage = "NONE"
	StagePrelaunch    OpportunityStage = "PRELAUNCH"
	StageStarting     OpportunityStage = "STARTING"
	StageConfirmed    OpportunityStage = "CONFIRMED"
	StageAccelerating OpportunityStage = "ACCELERATING"
	StageOverheated   OpportunityStage = "OVERHEATED"
)

type TimeframeEvidence struct {
	Timeframe     Timeframe `json:"timeframe"`
	Score         int       `json:"score"`
	Confirmed     bool      `json:"confirmed"`
	RSI           float64   `json:"rsi"`
	MACDHistogram float64   `json:"macd_histogram"`
	Reasons       []string  `json:"reasons"`
}

type Opportunity struct {
	Instrument       MarketInstrument    `json:"instrument"`
	GeneratedAt      time.Time           `json:"generated_at"`
	CurrentPrice     float64             `json:"current_price"`
	FOMOScore        int                 `json:"fomo_score"`
	OpportunityScore int                 `json:"opportunity_score"`
	Stage            OpportunityStage    `json:"stage"`
	HighVolatility   bool                `json:"high_volatility"`
	Evidence         []TimeframeEvidence `json:"evidence"`
	RiskFlags        []string            `json:"risk_flags"`
	Action           string              `json:"action"`
	TradePlan        *TradePlan          `json:"trade_plan,omitempty"`
}

type EntryLevel struct {
	Price     float64 `json:"price"`
	Weight    float64 `json:"weight"`
	Condition string  `json:"condition"`
}

type TargetLevel struct {
	Price      float64 `json:"price"`
	ExitWeight float64 `json:"exit_weight"`
	ReturnPct  float64 `json:"return_pct"`
	RewardRisk float64 `json:"reward_risk"`
}

type TradePlan struct {
	Entries           []EntryLevel  `json:"entries"`
	ConfirmationEntry EntryLevel    `json:"confirmation_entry"`
	LeftAverageCost   float64       `json:"left_average_cost"`
	FullAverageCost   float64       `json:"full_average_cost"`
	StopPrice         float64       `json:"stop_price"`
	RiskPct           float64       `json:"risk_pct"`
	HighRisk          bool          `json:"high_risk"`
	Targets           []TargetLevel `json:"targets"`
	TrailingRule      string        `json:"trailing_rule"`
}

type OpportunityRunStatus string

const (
	OpportunityRunRunning   OpportunityRunStatus = "running"
	OpportunityRunCompleted OpportunityRunStatus = "completed"
	OpportunityRunDegraded  OpportunityRunStatus = "degraded"
	OpportunityRunFailed    OpportunityRunStatus = "failed"
)

type OpportunityRun struct {
	ID             int64                `json:"id"`
	AssetClass     AssetClass           `json:"asset_class"`
	StartedAt      time.Time            `json:"started_at"`
	FinishedAt     time.Time            `json:"finished_at"`
	Status         OpportunityRunStatus `json:"status"`
	Trigger        string               `json:"trigger"`
	PoolSize       int                  `json:"pool_size"`
	CandidateCount int                  `json:"candidate_count"`
	ErrorSummary   string               `json:"error_summary,omitempty"`
}

type OpportunityReport struct {
	Run           OpportunityRun `json:"run"`
	Opportunities []Opportunity  `json:"opportunities"`
}
