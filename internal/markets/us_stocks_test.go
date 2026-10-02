package markets

import (
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/providers"
)

func TestStockUniverseAllowlistIsStrict(t *testing.T) {
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	allowed := []string{"AAPL", "ARM", "ASML", "SHOP", "SONY", "TSM"}
	rejected := []string{"SPY", "QQQ", "US100", "SAMSUNG", "XIAOMI", "UNKNOWN"}
	result := stockUniverseFixture(now, append(append([]string(nil), allowed...), rejected...))

	got := BuildUniverse(now, result, StockUniverseConfig())
	if len(got) != len(allowed) {
		t.Fatalf("stock universe size = %d, want %d: %+v", len(got), len(allowed), got)
	}
	for _, symbol := range allowed {
		assertUniverseContains(t, got, symbol+"-USDT-SWAP")
	}
	for _, symbol := range rejected {
		assertUniverseMissing(t, got, symbol+"-USDT-SWAP")
	}
}

func TestStockUniverseUsesOneDayPositiveLiquidityAndTopSixty(t *testing.T) {
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	rankingSymbols := []string{
		"AAOI", "AAPL", "ADBE", "AEHR", "ALAB", "AMAT", "AMC", "AMD", "AMZN", "APLD",
		"APP", "ARM", "ASML", "ASTS", "AVGO", "AXTI", "BB", "BE", "BMNR", "BRKB",
		"BX", "CGNX", "CIEN", "COHR", "COIN", "COST", "CRCL", "CRDO", "CRM", "CRWD",
		"CRWV", "CSCO", "DDOG", "DELL", "DKNG", "FLNC", "GEV", "GLW", "GME", "GOOGL",
		"GPRO", "GTLB", "HIMS", "HOOD", "HPE", "HUT", "IBM", "INTC", "IONQ", "IREN",
		"ISRG", "JNJ", "KLAC", "KO", "LITE", "LLY", "LRCX", "LUNR", "MARA", "META",
		"MRK", "MRNA", "MRVL", "MSFT", "MSTR",
	}
	result := stockUniverseFixture(now, rankingSymbols)
	for index := range result.Snapshots {
		result.Snapshots[index].Turnover24hUSD = float64(index + 1)
		result.Snapshots[index].OpenInterestUSD = float64(index + 1)
	}

	got := BuildUniverse(now, result, StockUniverseConfig())
	if len(got) != 60 {
		t.Fatalf("ranked stock universe size = %d, want 60", len(got))
	}
	if got[0].InstrumentID != "MSTR-USDT-SWAP" {
		t.Fatalf("top stock = %s, want MSTR-USDT-SWAP", got[0].InstrumentID)
	}
	assertUniverseMissing(t, got, "AAOI-USDT-SWAP")
	assertUniverseContains(t, got, "AMAT-USDT-SWAP")

	tests := []struct {
		name   string
		mutate func(*providers.OKXMarketResult)
		want   int
	}{
		{name: "exactly 24 hours", want: 1},
		{name: "one millisecond too young", mutate: func(result *providers.OKXMarketResult) {
			result.Instruments["AAPL-USDT-SWAP"] = testInstrument("AAPL-USDT-SWAP", now.Add(-24*time.Hour).Add(time.Millisecond))
		}},
		{name: "zero turnover", mutate: func(result *providers.OKXMarketResult) { result.Snapshots[0].Turnover24hUSD = 0 }},
		{name: "zero open interest", mutate: func(result *providers.OKXMarketResult) {
			result.Snapshots[0].OpenInterestUSD = 0
			result.Snapshots[0].OpenInterestAvailable = true
		}},
		{name: "missing open interest", mutate: func(result *providers.OKXMarketResult) { result.Snapshots[0].OpenInterestAvailable = false }},
		{name: "wrong asset class", mutate: func(result *providers.OKXMarketResult) { result.Snapshots[0].AssetClass = domain.AssetClassCrypto }},
		{name: "not USDT perpetual", mutate: func(result *providers.OKXMarketResult) {
			result.Snapshots[0].InstrumentID = "AAPL-USD-SWAP"
			result.Instruments = map[string]domain.MarketInstrument{"AAPL-USD-SWAP": testInstrument("AAPL-USD-SWAP", now.Add(-24*time.Hour))}
		}},
		{name: "missing metadata", mutate: func(result *providers.OKXMarketResult) { result.Instruments = map[string]domain.MarketInstrument{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := stockUniverseFixture(now, []string{"AAPL"})
			fixture.Instruments["AAPL-USDT-SWAP"] = testInstrument("AAPL-USDT-SWAP", now.Add(-24*time.Hour))
			if test.mutate != nil {
				test.mutate(&fixture)
			}
			if got := BuildUniverse(now, fixture, StockUniverseConfig()); len(got) != test.want {
				t.Fatalf("universe size = %d, want %d: %+v", len(got), test.want, got)
			}
		})
	}

	invalid := StockUniverseConfig()
	invalid.AssetClass = domain.AssetClass("forex")
	if got := BuildUniverse(now, stockUniverseFixture(now, []string{"AAPL"}), invalid); len(got) != 0 {
		t.Fatalf("invalid asset class produced %+v", got)
	}
}

func stockUniverseFixture(now time.Time, symbols []string) providers.OKXMarketResult {
	result := providers.OKXMarketResult{Instruments: make(map[string]domain.MarketInstrument, len(symbols))}
	for index, symbol := range symbols {
		id := symbol + "-USDT-SWAP"
		snapshot := testSnapshot(id, domain.AssetClassStock, 1_000_000+float64(index), 500_000+float64(index))
		snapshot.Symbol = symbol
		snapshot.FundingRateAvailable = true
		result.Snapshots = append(result.Snapshots, snapshot)
		result.Instruments[id] = domain.MarketInstrument{InstrumentID: id, Symbol: symbol, ListedAt: now.Add(-48 * time.Hour), TickSize: 0.01}
	}
	return result
}
