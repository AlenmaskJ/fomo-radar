package marketwatch

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/markets"
	"github.com/alen1/fomo-radar/internal/providers"
)

func TestScannerPublishesDynamicCandidatesAndProgress(t *testing.T) {
	now := time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC)
	store := &fakeOpportunityStore{}
	scanner := NewScanner(fakeBatchReader{result: scannerMarketResult(now)}, fakeCandleReader{}, store, markets.DefaultUniverseConfig(), "BTC-USDT-SWAP", func() time.Time { return now })
	progressCalls := 0
	run, err := scanner.Scan(context.Background(), fullScanRequest("manual"), func(current, total int) {
		progressCalls++
		if current > total || total != 2 {
			t.Fatalf("progress = %d/%d", current, total)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.OpportunityRunCompleted || run.PoolSize != 2 || progressCalls != 2 {
		t.Fatalf("run/progress = %+v/%d", run, progressCalls)
	}
	if len(store.published) == 0 || store.published[0].Instrument.InstrumentID == "" {
		t.Fatalf("published = %+v", store.published)
	}
}

func TestScannerDegradesWhenOneAssetFrameFails(t *testing.T) {
	now := time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC)
	store := &fakeOpportunityStore{}
	reader := fakeCandleReader{failInstrument: "AAA-USDT-SWAP", failFrame: domain.Timeframe4H}
	scanner := NewScanner(fakeBatchReader{result: scannerMarketResult(now)}, reader, store, markets.DefaultUniverseConfig(), "BTC-USDT-SWAP", func() time.Time { return now })
	run, err := scanner.Scan(context.Background(), fullScanRequest("scheduled"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.OpportunityRunDegraded || run.ErrorSummary == "" {
		t.Fatalf("run = %+v", run)
	}
	for _, opportunity := range store.published {
		if opportunity.Instrument.InstrumentID == "AAA-USDT-SWAP" {
			t.Fatalf("incomplete asset published: %+v", opportunity)
		}
	}
}

func TestScannerBatchFailureFinishesFailedWithoutPublishing(t *testing.T) {
	now := time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC)
	store := &fakeOpportunityStore{}
	scanner := NewScanner(fakeBatchReader{err: errors.New("batch unavailable")}, fakeCandleReader{}, store, markets.DefaultUniverseConfig(), "BTC-USDT-SWAP", func() time.Time { return now })
	run, err := scanner.Scan(context.Background(), fullScanRequest("startup"), nil)
	if err == nil || run.Status != domain.OpportunityRunFailed || store.finished.Status != domain.OpportunityRunFailed || len(store.published) != 0 {
		t.Fatalf("run/error/finished/published = %+v/%v/%+v/%d", run, err, store.finished, len(store.published))
	}
}

func TestScannerIncompleteNewestCandleNeverConfirms(t *testing.T) {
	now := time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC)
	store := &fakeOpportunityStore{}
	scanner := NewScanner(fakeBatchReader{result: scannerMarketResult(now)}, fakeCandleReader{incomplete15m: true}, store, markets.DefaultUniverseConfig(), "BTC-USDT-SWAP", func() time.Time { return now })
	_, err := scanner.Scan(context.Background(), fullScanRequest("manual"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, opportunity := range store.published {
		if opportunity.Stage == domain.StageConfirmed || opportunity.Stage == domain.StageAccelerating {
			t.Fatalf("incomplete candle confirmed: %+v", opportunity)
		}
	}
}

func TestScannerPreservesCompletedBreakoutActionWithOpenCandle(t *testing.T) {
	now := time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC)
	store := &fakeOpportunityStore{}
	scanner := NewScanner(fakeBatchReader{result: scannerMarketResult(now)}, fakeCandleReader{completedBreakoutBeforeOpen: true}, store, markets.DefaultUniverseConfig(), "BTC-USDT-SWAP", func() time.Time { return now })
	if _, err := scanner.Scan(context.Background(), fullScanRequest("manual"), nil); err != nil {
		t.Fatal(err)
	}
	for _, opportunity := range store.published {
		if opportunity.Evidence[3].Confirmed && opportunity.Action == "等待已完成K线确认" {
			t.Fatalf("confirmed breakout action was overwritten: %+v", opportunity)
		}
	}
}

func TestScannerKeepsHighVolatilityWarningWhileWaitingForClose(t *testing.T) {
	now := time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC)
	result := scannerMarketResult(now)
	for index := range result.Snapshots {
		result.Snapshots[index].Change24hPct = 20
	}
	store := &fakeOpportunityStore{}
	scanner := NewScanner(fakeBatchReader{result: result}, fakeCandleReader{incomplete15m: true}, store, markets.DefaultUniverseConfig(), "BTC-USDT-SWAP", func() time.Time { return now })
	if _, err := scanner.Scan(context.Background(), fullScanRequest("manual"), nil); err != nil {
		t.Fatal(err)
	}
	for _, opportunity := range store.published {
		if opportunity.HighVolatility && !strings.Contains(opportunity.Action, "高波动") {
			t.Fatalf("high-volatility warning was hidden: %+v", opportunity)
		}
	}
}

func TestScannerUsesConfiguredAssetClassAndBenchmark(t *testing.T) {
	now := time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)
	batch := fakeBatchReader{result: mixedScannerMarketResult(now)}

	cryptoStore := &fakeOpportunityStore{}
	cryptoCandles := &recordingCandleReader{}
	cryptoScanner := NewScanner(batch, cryptoCandles, cryptoStore, markets.DefaultUniverseConfig(), "BTC-USDT-SWAP", func() time.Time { return now })
	cryptoRun, err := cryptoScanner.Scan(context.Background(), fullScanRequest("manual"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cryptoRun.AssetClass != domain.AssetClassCrypto || cryptoRun.PoolSize != 2 {
		t.Fatalf("crypto run = %+v", cryptoRun)
	}
	assertOnlySnapshotClass(t, cryptoStore.snapshots, domain.AssetClassCrypto)
	if !cryptoCandles.called("BTC-USDT-SWAP") || cryptoCandles.called("SPY-USDT-SWAP") {
		t.Fatalf("crypto candle calls = %+v", cryptoCandles.calls)
	}

	stockStore := &fakeOpportunityStore{}
	stockCandles := &recordingCandleReader{}
	stockScanner := NewScanner(batch, stockCandles, stockStore, markets.StockUniverseConfig(), "SPY-USDT-SWAP", func() time.Time { return now })
	stockRun, err := stockScanner.Scan(context.Background(), fullScanRequest("manual"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if stockRun.AssetClass != domain.AssetClassStock || stockRun.PoolSize != 2 {
		t.Fatalf("stock run = %+v", stockRun)
	}
	assertOnlySnapshotClass(t, stockStore.snapshots, domain.AssetClassStock)
	if !stockCandles.called("SPY-USDT-SWAP") || stockCandles.called("BTC-USDT-SWAP") {
		t.Fatalf("stock candle calls = %+v", stockCandles.calls)
	}
	for _, opportunity := range stockStore.published {
		if opportunity.Instrument.InstrumentID == "SPY-USDT-SWAP" {
			t.Fatalf("SPY benchmark was published: %+v", opportunity)
		}
	}
}

func TestScannersDoNotShareUniverseOrCandleCache(t *testing.T) {
	now := time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)
	batch := fakeBatchReader{result: mixedScannerMarketResult(now)}
	reader := &recordingCandleReader{}
	crypto := NewScanner(batch, reader, &fakeOpportunityStore{}, markets.DefaultUniverseConfig(), "BTC-USDT-SWAP", func() time.Time { return now })
	stock := NewScanner(batch, reader, &fakeOpportunityStore{}, markets.StockUniverseConfig(), "SPY-USDT-SWAP", func() time.Time { return now })
	if _, err := crypto.Scan(context.Background(), fullScanRequest("startup"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := stock.Scan(context.Background(), fullScanRequest("startup"), nil); err != nil {
		t.Fatal(err)
	}
	request := ScanRequest{Trigger: "scheduled", Frames: []domain.Timeframe{domain.Timeframe15m}}
	if _, err := crypto.Scan(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := stock.Scan(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	if _, exists := crypto.candleCache["AAPL-USDT-SWAP"]; exists {
		t.Fatal("crypto scanner cached a stock candidate")
	}
	if _, exists := stock.candleCache["BTC-USDT-SWAP"]; exists {
		t.Fatal("stock scanner cached a crypto candidate")
	}
	if len(crypto.assets) != 2 || len(stock.assets) != 2 {
		t.Fatalf("pool sizes = crypto %d stock %d", len(crypto.assets), len(stock.assets))
	}
}

func TestStockScannerFailsWhenSPYContextFails(t *testing.T) {
	now := time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)
	store := &fakeOpportunityStore{}
	reader := &recordingCandleReader{failInstrument: "SPY-USDT-SWAP", failFrame: domain.Timeframe1H}
	scanner := NewScanner(fakeBatchReader{result: mixedScannerMarketResult(now)}, reader, store, markets.StockUniverseConfig(), "SPY-USDT-SWAP", func() time.Time { return now })
	run, err := scanner.Scan(context.Background(), fullScanRequest("scheduled"), nil)
	if err == nil || run.Status != domain.OpportunityRunFailed || store.finished.Status != domain.OpportunityRunFailed || len(store.published) != 0 {
		t.Fatalf("run/error/finished/published = %+v/%v/%+v/%d", run, err, store.finished, len(store.published))
	}
}

func TestStockScannerDegradesWhenOneStockFrameFails(t *testing.T) {
	now := time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)
	store := &fakeOpportunityStore{}
	reader := &recordingCandleReader{failInstrument: "AAPL-USDT-SWAP", failFrame: domain.Timeframe4H}
	scanner := NewScanner(fakeBatchReader{result: mixedScannerMarketResult(now)}, reader, store, markets.StockUniverseConfig(), "SPY-USDT-SWAP", func() time.Time { return now })
	run, err := scanner.Scan(context.Background(), fullScanRequest("scheduled"), nil)
	if err != nil || run.Status != domain.OpportunityRunDegraded {
		t.Fatalf("run/error = %+v/%v", run, err)
	}
	for _, opportunity := range store.published {
		if opportunity.Instrument.InstrumentID == "AAPL-USDT-SWAP" {
			t.Fatalf("incomplete stock published: %+v", opportunity)
		}
	}
}

type fakeBatchReader struct {
	result providers.OKXMarketResult
	err    error
}

func (f fakeBatchReader) Read(context.Context) (providers.OKXMarketResult, error) {
	return f.result, f.err
}

type fakeCandleReader struct {
	failInstrument              string
	failFrame                   domain.Timeframe
	incomplete15m               bool
	completedBreakoutBeforeOpen bool
}

type recordingCandleReader struct {
	calls          []string
	failInstrument string
	failFrame      domain.Timeframe
}

func (r *recordingCandleReader) Read(_ context.Context, instrumentID string, timeframe domain.Timeframe, _ int) ([]domain.Candle, error) {
	r.calls = append(r.calls, instrumentID+"|"+string(timeframe))
	if instrumentID == r.failInstrument && timeframe == r.failFrame {
		return nil, errors.New("fixture frame failure")
	}
	benchmark := instrumentID == "BTC-USDT-SWAP" || instrumentID == "SPY-USDT-SWAP"
	return scannerCandles(timeframe, benchmark), nil
}

func (r *recordingCandleReader) called(instrumentID string) bool {
	for _, call := range r.calls {
		if strings.HasPrefix(call, instrumentID+"|") {
			return true
		}
	}
	return false
}

func (f fakeCandleReader) Read(_ context.Context, instrumentID string, timeframe domain.Timeframe, _ int) ([]domain.Candle, error) {
	if instrumentID == f.failInstrument && timeframe == f.failFrame {
		return nil, errors.New("fixture frame failure")
	}
	candles := scannerCandles(timeframe, instrumentID == "BTC-USDT-SWAP")
	if f.incomplete15m && timeframe == domain.Timeframe15m {
		candles[len(candles)-1].Confirmed = false
		candles[len(candles)-1].Close += 8
		candles[len(candles)-1].High += 8
	}
	if f.completedBreakoutBeforeOpen && timeframe == domain.Timeframe15m {
		last := len(candles) - 1
		candles[last-1].Close += 8
		candles[last-1].High += 8
		candles[last].Confirmed = false
		candles[last].Close = candles[last-1].Close
		candles[last].High = candles[last-1].High
	}
	return candles, nil
}

type fakeOpportunityStore struct {
	nextID    int64
	started   []domain.AssetClass
	finished  domain.OpportunityRun
	published []domain.Opportunity
	snapshots []domain.DerivativesSnapshot
}

func (f *fakeOpportunityStore) StartOpportunityRun(_ context.Context, assetClass domain.AssetClass, _ string, _ time.Time) (int64, error) {
	f.nextID++
	f.started = append(f.started, assetClass)
	return f.nextID, nil
}
func (f *fakeOpportunityStore) FinishOpportunityRun(_ context.Context, run domain.OpportunityRun, opportunities []domain.Opportunity) error {
	f.finished = run
	f.published = append([]domain.Opportunity(nil), opportunities...)
	return nil
}
func (f *fakeOpportunityStore) InsertDerivativesSnapshots(_ context.Context, snapshots []domain.DerivativesSnapshot) error {
	f.snapshots = append(f.snapshots, snapshots...)
	return nil
}
func (f *fakeOpportunityStore) DerivativesSnapshotAtOrBefore(context.Context, domain.AssetClass, string, time.Time) (domain.DerivativesSnapshot, bool, error) {
	return domain.DerivativesSnapshot{}, false, nil
}

func scannerMarketResult(now time.Time) providers.OKXMarketResult {
	result := providers.OKXMarketResult{Instruments: map[string]domain.MarketInstrument{}}
	for index, id := range []string{"BTC-USDT-SWAP", "AAA-USDT-SWAP"} {
		symbol := id[:3]
		result.Instruments[id] = domain.MarketInstrument{InstrumentID: id, Symbol: symbol, ListedAt: now.Add(-800 * 24 * time.Hour), TickSize: 0.01}
		result.Snapshots = append(result.Snapshots, domain.DerivativesSnapshot{AssetClass: domain.AssetClassCrypto, InstrumentID: id, Symbol: symbol, CollectedAt: now, SourceTime: now, PriceUSD: 110, Change24hPct: 4, Turnover24hUSD: 50_000_000 + float64(index), OpenInterestUSD: 20_000_000, OpenInterestAvailable: true, FundingRate: 0.0001, FundingRateAvailable: true})
	}
	return result
}

func mixedScannerMarketResult(now time.Time) providers.OKXMarketResult {
	result := providers.OKXMarketResult{Instruments: map[string]domain.MarketInstrument{}}
	fixtures := []struct {
		id     string
		symbol string
		class  domain.AssetClass
		age    time.Duration
	}{
		{id: "BTC-USDT-SWAP", symbol: "BTC", class: domain.AssetClassCrypto, age: 800 * 24 * time.Hour},
		{id: "AAA-USDT-SWAP", symbol: "AAA", class: domain.AssetClassCrypto, age: 800 * 24 * time.Hour},
		{id: "AAPL-USDT-SWAP", symbol: "AAPL", class: domain.AssetClassStock, age: 48 * time.Hour},
		{id: "MSFT-USDT-SWAP", symbol: "MSFT", class: domain.AssetClassStock, age: 48 * time.Hour},
		{id: "SPY-USDT-SWAP", symbol: "SPY", class: domain.AssetClassStock, age: 48 * time.Hour},
	}
	for index, fixture := range fixtures {
		result.Instruments[fixture.id] = domain.MarketInstrument{InstrumentID: fixture.id, Symbol: fixture.symbol, ListedAt: now.Add(-fixture.age), TickSize: 0.01}
		result.Snapshots = append(result.Snapshots, domain.DerivativesSnapshot{
			AssetClass: fixture.class, InstrumentID: fixture.id, Symbol: fixture.symbol,
			CollectedAt: now, SourceTime: now, PriceUSD: 110, Change24hPct: 4,
			Turnover24hUSD: 50_000_000 + float64(index), OpenInterestUSD: 20_000_000,
			OpenInterestAvailable: true, FundingRate: 0.0001, FundingRateAvailable: true,
		})
	}
	return result
}

func assertOnlySnapshotClass(t *testing.T, snapshots []domain.DerivativesSnapshot, want domain.AssetClass) {
	t.Helper()
	if len(snapshots) == 0 {
		t.Fatal("no snapshots persisted")
	}
	for _, snapshot := range snapshots {
		if snapshot.AssetClass != want {
			t.Fatalf("snapshot %s class = %s, want %s", snapshot.InstrumentID, snapshot.AssetClass, want)
		}
	}
}

func scannerCandles(timeframe domain.Timeframe, btc bool) []domain.Candle {
	duration := time.Hour
	switch timeframe {
	case domain.Timeframe15m:
		duration = 15 * time.Minute
	case domain.Timeframe4H:
		duration = 4 * time.Hour
	case domain.Timeframe1D:
		duration = 24 * time.Hour
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	slope := 0.04
	if btc {
		slope = 0.01
	}
	candles := make([]domain.Candle, 220)
	for index := range candles {
		price := 100 + slope*float64(index) + math.Sin(float64(index)/4)*1.5
		volume := 1_000_000.0
		if index >= 217 {
			volume = 2_200_000
		}
		candles[index] = domain.Candle{OpenTime: base.Add(time.Duration(index) * duration), CloseTime: base.Add(time.Duration(index+1) * duration), Open: price - 0.2, High: price + 1, Low: price - 1, Close: price, VolumeBase: volume / price, VolumeQuote: volume, Confirmed: true}
	}
	candles[len(candles)-1].Close += 4
	candles[len(candles)-1].High += 4
	return candles
}
