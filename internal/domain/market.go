package domain

import "time"

// AssetClass 用于隔离加密市场和美股市场，避免信号互相比较。
type AssetClass string

const (
	AssetClassCrypto AssetClass = "crypto"
	AssetClassStock  AssetClass = "stock"
)

func (a AssetClass) Valid() bool {
	return a == AssetClassCrypto || a == AssetClassStock
}

// DerivativesSnapshot 是交易所公共合约行情的标准化快照。
type DerivativesSnapshot struct {
	AssetClass            AssetClass `json:"asset_class"`
	InstrumentID          string     `json:"instrument_id"`
	Symbol                string     `json:"symbol"`
	CollectedAt           time.Time  `json:"collected_at"`
	SourceTime            time.Time  `json:"source_time"`
	PriceUSD              float64    `json:"price_usd"`
	Change24hPct          float64    `json:"change_24h_pct"`
	Turnover24hUSD        float64    `json:"turnover_24h_usd"`
	OpenInterestUSD       float64    `json:"open_interest_usd"`
	OpenInterestAvailable bool       `json:"open_interest_available"`
	FundingRate           float64    `json:"funding_rate"`
	FundingRateAvailable  bool       `json:"funding_rate_available"`
}
