package markets

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

var cryptoWatchlist = symbolSet("BTC", "ETH", "SOL", "BNB", "XRP", "DOGE", "ADA", "AVAX", "LINK", "TRX", "LTC", "BCH")
var stockWatchlist = symbolSet("SPY", "QQQ", "IWM", "NVDA", "AAPL", "MSFT", "GOOGL", "AMZN", "META", "TSLA", "AMD", "AVGO")

const maxShortComparisonInterval = 5 * time.Minute

type Row struct {
	domain.DerivativesSnapshot
	PriceChangePct        *float64
	OpenInterestChangePct *float64
	Signal                string
}

type Report struct {
	Crypto        []Row
	Stocks        []Row
	CryptoSummary string
	StockSummary  string
	Warnings      []string
}

// Analyze 只比较同板块标的，并用透明规则生成第一版提示，不冒充训练模型分数。
func Analyze(current []domain.DerivativesSnapshot, previous map[string]domain.DerivativesSnapshot, warnings []string) Report {
	report := Report{Warnings: append([]string(nil), warnings...)}
	for _, snapshot := range current {
		watchlist := cryptoWatchlist
		switch snapshot.AssetClass {
		case domain.AssetClassCrypto:
		case domain.AssetClassStock:
			watchlist = stockWatchlist
		default:
			continue
		}
		if !watchlist[snapshot.Symbol] {
			continue
		}
		// 同一币种可能同时存在币本位和 USDT 合约，展示层固定选主 USDT 合约避免重复。
		if snapshot.InstrumentID != snapshot.Symbol+"-USDT-SWAP" {
			continue
		}
		row := Row{DerivativesSnapshot: snapshot}
		if before, ok := previous[snapshot.InstrumentID]; ok && snapshot.CollectedAt.After(before.CollectedAt) && snapshot.CollectedAt.Sub(before.CollectedAt) <= maxShortComparisonInterval {
			if snapshot.PriceUSD > 0 && before.PriceUSD > 0 {
				change := (snapshot.PriceUSD/before.PriceUSD - 1) * 100
				row.PriceChangePct = &change
			}
			if snapshot.OpenInterestAvailable && before.OpenInterestAvailable && before.OpenInterestUSD > 0 {
				change := (snapshot.OpenInterestUSD/before.OpenInterestUSD - 1) * 100
				row.OpenInterestChangePct = &change
			}
		}
		row.Signal = signal(row)
		if snapshot.AssetClass == domain.AssetClassCrypto {
			report.Crypto = append(report.Crypto, row)
		} else {
			report.Stocks = append(report.Stocks, row)
		}
	}
	sortRows(report.Crypto)
	sortRows(report.Stocks)
	report.CryptoSummary = summarize(report.Crypto, "暂无主流加密行情")
	report.StockSummary = summarize(report.Stocks, "暂无美股行情")
	return report
}

func signal(row Row) string {
	parts := make([]string, 0, 3)
	switch {
	case row.Change24hPct >= 3:
		parts = append(parts, "强势")
	case row.Change24hPct >= 1:
		parts = append(parts, "偏强")
	case row.Change24hPct <= -3:
		parts = append(parts, "弱势")
	case row.Change24hPct <= -1:
		parts = append(parts, "偏弱")
	default:
		parts = append(parts, "震荡")
	}
	if row.OpenInterestChangePct != nil {
		if *row.OpenInterestChangePct >= 5 {
			parts = append(parts, "增仓")
		} else if *row.OpenInterestChangePct <= -5 {
			parts = append(parts, "减仓")
		}
	}
	if row.FundingRateAvailable {
		if row.FundingRate >= 0.0005 {
			parts = append(parts, "多头拥挤")
		} else if row.FundingRate <= -0.0005 {
			parts = append(parts, "空头拥挤")
		}
	}
	return strings.Join(parts, " / ")
}

func summarize(rows []Row, empty string) string {
	if len(rows) == 0 {
		return empty
	}
	up := 0
	for _, row := range rows {
		if row.Change24hPct > 0 {
			up++
		}
	}
	return fmt.Sprintf("上涨 %d/%d；最强 %s %+.2f%%；最弱 %s %+.2f%%",
		up, len(rows), rows[0].Symbol, rows[0].Change24hPct, rows[len(rows)-1].Symbol, rows[len(rows)-1].Change24hPct)
}

func sortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Change24hPct == rows[j].Change24hPct {
			return rows[i].Symbol < rows[j].Symbol
		}
		return rows[i].Change24hPct > rows[j].Change24hPct
	})
}

func symbolSet(symbols ...string) map[string]bool {
	set := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		set[symbol] = true
	}
	return set
}
