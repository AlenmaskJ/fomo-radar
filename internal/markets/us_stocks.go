package markets

import (
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

const usListedStockSymbols = `
AAOI AAPL ADBE AEHR ALAB AMAT AMC AMD AMZN APLD APP ARM ASML ASTS AVGO AXTI BB BE BMNR BRKB BX
CGNX CIEN COHR COIN COST CRCL CRDO CRM CRWD CRWV CSCO DDOG DELL DKNG FLNC GEV GLW GME GOOGL GPRO
GTLB HIMS HOOD HPE HUT IBM INTC IONQ IREN ISRG JNJ KLAC KO LITE LLY LRCX LUNR MARA META MRK MRNA
MRVL MSFT MSTR MU NBIS NET NFLX NOK NOW NVDA OKLO OKTA ON ONDS ORCL OSCR OUST PLTR POET PYPL QCOM
RDDT RDW RIOT RIVN RKLB ROK SHOP SIMO SMCI SNDK SNOW SONY TEAM TEM TER TSEM TSLA TSM TTMI TTWO TWLO
UNH USAR VRT WDC WEN WMT XOM ZM`

// StockUniverseConfig 使用人工审阅白名单；OKX 新增的未知代码默认拒绝。
func StockUniverseConfig() UniverseConfig {
	allowed := make(map[string]struct{}, 112)
	for _, symbol := range strings.Fields(usListedStockSymbols) {
		allowed[symbol] = struct{}{}
	}
	return UniverseConfig{
		AssetClass:            domain.AssetClassStock,
		MinimumAge:            24 * time.Hour,
		MaximumSize:           60,
		AllowedSymbols:        allowed,
		ExcludedInstrumentIDs: map[string]struct{}{"SPY-USDT-SWAP": {}},
	}
}
