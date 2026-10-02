package markets

import (
	"fmt"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/providers"
)

func TestBuildUniverseFiltersAndCapsByRelativeLiquidity(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	result := universeFixture(now, 65)
	result.Snapshots = append(result.Snapshots,
		testSnapshot("NEW-USDT-SWAP", domain.AssetClassCrypto, 20_000_000, 10_000_000),
		testSnapshot("THIN-USDT-SWAP", domain.AssetClassCrypto, 9_999_999, 10_000_000),
		testSnapshot("NOOI-USDT-SWAP", domain.AssetClassCrypto, 20_000_000, 0),
		testSnapshot("AAPL-USDT-SWAP", domain.AssetClassStock, 20_000_000, 10_000_000),
	)
	result.Instruments["NEW-USDT-SWAP"] = testInstrument("NEW-USDT-SWAP", now.Add(-364*24*time.Hour))
	result.Instruments["THIN-USDT-SWAP"] = testInstrument("THIN-USDT-SWAP", now.Add(-500*24*time.Hour))
	result.Instruments["NOOI-USDT-SWAP"] = testInstrument("NOOI-USDT-SWAP", now.Add(-500*24*time.Hour))
	result.Instruments["AAPL-USDT-SWAP"] = testInstrument("AAPL-USDT-SWAP", now.Add(-500*24*time.Hour))

	got := BuildUniverse(now, result, DefaultUniverseConfig())
	if len(got) != 60 {
		t.Fatalf("len = %d, want 60", len(got))
	}
	assertUniverseContains(t, got, "BTC-USDT-SWAP")
	assertUniverseMissing(t, got, "NEW-USDT-SWAP")
	assertUniverseMissing(t, got, "THIN-USDT-SWAP")
	assertUniverseMissing(t, got, "NOOI-USDT-SWAP")
	assertUniverseMissing(t, got, "AAPL-USDT-SWAP")
	for index := 1; index < len(got); index++ {
		if got[index-1].LiquidityScore < got[index].LiquidityScore {
			t.Fatalf("scores not descending at %d: %v < %v", index, got[index-1].LiquidityScore, got[index].LiquidityScore)
		}
	}
}

func TestBuildUniverseDoesNotFillQuotaWithIneligibleAssets(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	got := BuildUniverse(now, universeFixture(now, 35), DefaultUniverseConfig())
	if len(got) != 35 {
		t.Fatalf("len = %d, want 35", len(got))
	}
}

func TestBuildUniverseIncludesExactBoundariesAndRequiresMetadata(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	result := providers.OKXMarketResult{
		Snapshots:   []domain.DerivativesSnapshot{testSnapshot("EDGE-USDT-SWAP", domain.AssetClassCrypto, 10_000_000, 5_000_000), testSnapshot("MISSING-USDT-SWAP", domain.AssetClassCrypto, 20_000_000, 8_000_000)},
		Instruments: map[string]domain.MarketInstrument{"EDGE-USDT-SWAP": testInstrument("EDGE-USDT-SWAP", now.Add(-365*24*time.Hour))},
	}
	got := BuildUniverse(now, result, DefaultUniverseConfig())
	if len(got) != 1 || got[0].InstrumentID != "EDGE-USDT-SWAP" {
		t.Fatalf("universe = %+v", got)
	}
}

func TestBuildUniverseOnlyIncludesUSDTPerpetuals(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	result := providers.OKXMarketResult{Instruments: map[string]domain.MarketInstrument{}}
	for _, id := range []string{"DOGE-USDT-SWAP", "DOGE-USD-SWAP", "DOGE-USDC-SWAP"} {
		result.Snapshots = append(result.Snapshots, testSnapshot(id, domain.AssetClassCrypto, 20_000_000, 8_000_000))
		result.Instruments[id] = testInstrument(id, now.Add(-500*24*time.Hour))
	}

	got := BuildUniverse(now, result, DefaultUniverseConfig())
	if len(got) != 1 || got[0].InstrumentID != "DOGE-USDT-SWAP" {
		t.Fatalf("universe = %+v, want only DOGE-USDT-SWAP", got)
	}
}

func TestBuildUniverseDeduplicatesInstrumentIDs(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	id := "DOGE-USDT-SWAP"
	result := providers.OKXMarketResult{
		Snapshots:   []domain.DerivativesSnapshot{testSnapshot(id, domain.AssetClassCrypto, 20_000_000, 8_000_000), testSnapshot(id, domain.AssetClassCrypto, 20_000_000, 8_000_000)},
		Instruments: map[string]domain.MarketInstrument{id: testInstrument(id, now.Add(-500*24*time.Hour))},
	}
	if got := BuildUniverse(now, result, DefaultUniverseConfig()); len(got) != 1 {
		t.Fatalf("duplicate instrument produced %d universe rows", len(got))
	}
}

func TestBuildUniverseTieBreaksByTurnoverThenInstrument(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	result := providers.OKXMarketResult{Instruments: map[string]domain.MarketInstrument{}}
	for _, id := range []string{"CCC-USDT-SWAP", "AAA-USDT-SWAP", "BBB-USDT-SWAP"} {
		result.Snapshots = append(result.Snapshots, testSnapshot(id, domain.AssetClassCrypto, 20_000_000, 8_000_000))
		result.Instruments[id] = testInstrument(id, now.Add(-500*24*time.Hour))
	}
	got := BuildUniverse(now, result, DefaultUniverseConfig())
	if got[0].InstrumentID != "AAA-USDT-SWAP" || got[1].InstrumentID != "BBB-USDT-SWAP" {
		t.Fatalf("tie order = %s, %s, %s", got[0].InstrumentID, got[1].InstrumentID, got[2].InstrumentID)
	}
}

func universeFixture(now time.Time, count int) providers.OKXMarketResult {
	result := providers.OKXMarketResult{Instruments: make(map[string]domain.MarketInstrument, count)}
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("COIN%02d-USDT-SWAP", index)
		turnover := 10_000_000 + float64(index)*1_000_000
		oi := 5_000_000 + float64(index)*500_000
		if index == 0 {
			id = "BTC-USDT-SWAP"
			turnover = 100_000_000
			oi = 50_000_000
		}
		result.Snapshots = append(result.Snapshots, testSnapshot(id, domain.AssetClassCrypto, turnover, oi))
		result.Instruments[id] = testInstrument(id, now.Add(-time.Duration(500+index)*24*time.Hour))
	}
	return result
}

func testSnapshot(id string, class domain.AssetClass, turnover, oi float64) domain.DerivativesSnapshot {
	return domain.DerivativesSnapshot{AssetClass: class, InstrumentID: id, Symbol: id, PriceUSD: 10, Turnover24hUSD: turnover, OpenInterestUSD: oi, OpenInterestAvailable: oi > 0}
}

func testInstrument(id string, listedAt time.Time) domain.MarketInstrument {
	return domain.MarketInstrument{InstrumentID: id, Symbol: id, ListedAt: listedAt, TickSize: 0.01}
}

func assertUniverseContains(t *testing.T, assets []UniverseAsset, id string) {
	t.Helper()
	for _, asset := range assets {
		if asset.InstrumentID == id {
			return
		}
	}
	t.Fatalf("%s missing", id)
}

func assertUniverseMissing(t *testing.T, assets []UniverseAsset, id string) {
	t.Helper()
	for _, asset := range assets {
		if asset.InstrumentID == id {
			t.Fatalf("%s should be excluded", id)
		}
	}
}
