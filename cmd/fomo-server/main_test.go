package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/marketwatch"
	"github.com/alen1/fomo-radar/internal/providers"
)

func TestParseOptionsDefaultsAndPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		env       map[string]string
		wantDB    string
		wantURL   string
		wantWatch bool
		wantProxy string
	}{
		{name: "defaults", args: []string{"-serve"}, wantDB: "./fomo.db", wantURL: "127.0.0.1:8080"},
		{name: "environment", args: []string{"-serve", "-market-watch"}, env: map[string]string{"FOMO_DB": "live.db", "FOMO_HTTP_ADDR": "127.0.0.1:9090", "FOMO_PROXY": "http://127.0.0.1:10808"}, wantDB: "live.db", wantURL: "127.0.0.1:9090", wantWatch: true, wantProxy: "http://127.0.0.1:10808"},
		{name: "flags win", args: []string{"-serve", "-market-watch", "-db", "flag.db", "-listen", "0.0.0.0:8181", "-proxy", "http://10.0.0.1:9999"}, env: map[string]string{"FOMO_DB": "env.db", "FOMO_HTTP_ADDR": "127.0.0.1:9090", "FOMO_PROXY": "http://127.0.0.1:10808"}, wantDB: "flag.db", wantURL: "0.0.0.0:8181", wantWatch: true, wantProxy: "http://10.0.0.1:9999"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseOptions(test.args, mapEnvironment(test.env))
			if err != nil {
				t.Fatalf("parseOptions() error = %v", err)
			}
			if !got.serve || got.databasePath != test.wantDB || got.listenAddress != test.wantURL || got.marketWatch != test.wantWatch || got.proxy != test.wantProxy {
				t.Fatalf("parseOptions() = %+v, want serve db %q listen %q", got, test.wantDB, test.wantURL)
			}
		})
	}
}

func TestParseOptionsRejectsMissingModeAndPositionals(t *testing.T) {
	for _, args := range [][]string{{}, {"-db", "fomo.db"}, {"-serve", "unexpected"}} {
		if _, err := parseOptions(args, mapEnvironment(nil)); err == nil {
			t.Fatalf("parseOptions(%q) accepted invalid invocation", args)
		}
	}
	if _, err := parseOptions([]string{"-serve", "-listen", ""}, mapEnvironment(nil)); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("empty listen error = %v", err)
	}
	if _, err := parseOptions([]string{"-serve", "-market-watch", "-proxy", "127.0.0.1:10808"}, mapEnvironment(nil)); err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("invalid proxy error = %v", err)
	}
}

func TestStartDemoDryRunShowsCompleteLaunchSequence(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 启动脚本仅在 Windows 验证")
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "..", "start-demo.cmd")
	command := exec.Command("cmd", "/d", "/c", script, "--dry-run")
	command.Env = append(os.Environ(), "FOMO_PROXY=")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("start-demo.cmd --dry-run error = %v: %s", err, output)
	}
	visible := string(output)
	for _, want := range []string{
		"fomo-server.exe -serve -market-watch",
		"fomo-scanner.exe -watch",
		"FOMO_DB=\"" + filepath.Join(repositoryRoot, "fomo.db") + "\"",
		"HTTP_PROXY=http://127.0.0.1:10808",
		"HTTPS_PROXY=http://127.0.0.1:10808",
		"-proxy http://127.0.0.1:10808", "http://127.0.0.1:18080/market", "http://127.0.0.1:18080/health/market", "http://127.0.0.1:18080/api/market/status", "wait up to 240 seconds",
	} {
		if !strings.Contains(visible, want) {
			t.Fatalf("dry-run output missing %q: %s", want, visible)
		}
	}
	if strings.Contains(visible, "fomo-market.exe") {
		t.Fatalf("dry-run should not start the legacy market process: %s", visible)
	}
	if strings.Contains(visible, "fomo-stock.exe") {
		t.Fatalf("dry-run should not start a separate stock process: %s", visible)
	}
	scriptContents, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(scriptContents), "$status.state -in @('completed','degraded')") || !strings.Contains(string(scriptContents), "$status.state -eq 'failed'") {
		t.Fatal("start-demo must accept only successful scans and stop on startup failure")
	}
	if strings.Contains(string(scriptContents), "/health/stocks") {
		t.Fatal("start-demo must not block opening the dashboard on the first stock scan")
	}

	override := exec.Command("cmd", "/d", "/c", script, "--dry-run")
	override.Env = append(os.Environ(), "FOMO_PROXY=http://10.0.0.1:9999")
	overrideOutput, err := override.CombinedOutput()
	overrideText := string(overrideOutput)
	if err != nil || !strings.Contains(overrideText, "-proxy http://10.0.0.1:9999") ||
		!strings.Contains(overrideText, "HTTP_PROXY=http://10.0.0.1:9999") ||
		!strings.Contains(overrideText, "HTTPS_PROXY=http://10.0.0.1:9999") {
		t.Fatalf("proxy override error = %v: %s", err, overrideOutput)
	}
}

func TestNewRadarSchedulersCreatesCryptoAndStockInstances(t *testing.T) {
	now := time.Date(2026, 9, 30, 5, 0, 0, 0, time.UTC)
	batch := radarBatchReader{result: radarMarketResult(now)}
	candles := &recordingRadarCandleReader{}
	writer := &recordingRadarStore{}
	crypto, stock := newRadarSchedulers(batch, candles, writer, func() time.Time { return now })
	if crypto == nil || stock == nil || crypto == stock {
		t.Fatalf("schedulers = %p / %p", crypto, stock)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	crypto.Start(ctx)
	stock.Start(ctx)
	waitForRadarScheduler(t, crypto)
	waitForRadarScheduler(t, stock)
	if got := writer.startedClasses(); got[domain.AssetClassCrypto] == 0 || got[domain.AssetClassStock] == 0 {
		t.Fatalf("started asset classes = %+v", got)
	}
	if !candles.called("BTC-USDT-SWAP") || !candles.called("SPY-USDT-SWAP") {
		t.Fatalf("benchmark candle calls = %+v", candles.callsCopy())
	}
	if !strings.HasPrefix(crypto.Status().ID, "crypto-scan-") || !strings.HasPrefix(stock.Status().ID, "stock-scan-") {
		t.Fatalf("scheduler IDs = %q / %q", crypto.Status().ID, stock.Status().ID)
	}
}

type radarBatchReader struct{ result providers.OKXMarketResult }

func (r radarBatchReader) Read(context.Context) (providers.OKXMarketResult, error) {
	return r.result, nil
}

type recordingRadarCandleReader struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingRadarCandleReader) Read(_ context.Context, instrumentID string, timeframe domain.Timeframe, _ int) ([]domain.Candle, error) {
	r.mu.Lock()
	r.calls = append(r.calls, instrumentID+"|"+string(timeframe))
	r.mu.Unlock()
	duration := time.Hour
	switch timeframe {
	case domain.Timeframe15m:
		duration = 15 * time.Minute
	case domain.Timeframe4H:
		duration = 4 * time.Hour
	case domain.Timeframe1D:
		duration = 24 * time.Hour
	}
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := make([]domain.Candle, 220)
	for index := range candles {
		price := 100 + float64(index)*0.03 + float64(index%7)*0.02
		candles[index] = domain.Candle{
			OpenTime: base.Add(time.Duration(index) * duration), CloseTime: base.Add(time.Duration(index+1) * duration),
			Open: price - 0.1, High: price + 0.5, Low: price - 0.5, Close: price,
			VolumeBase: 10_000, VolumeQuote: 1_000_000, Confirmed: true,
		}
	}
	return candles, nil
}

func (r *recordingRadarCandleReader) called(instrumentID string) bool {
	for _, call := range r.callsCopy() {
		if strings.HasPrefix(call, instrumentID+"|") {
			return true
		}
	}
	return false
}

func (r *recordingRadarCandleReader) callsCopy() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

type recordingRadarStore struct {
	mu      sync.Mutex
	nextID  int64
	started map[domain.AssetClass]int
}

func (s *recordingRadarStore) StartOpportunityRun(_ context.Context, assetClass domain.AssetClass, _ string, _ time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started == nil {
		s.started = make(map[domain.AssetClass]int)
	}
	s.nextID++
	s.started[assetClass]++
	return s.nextID, nil
}

func (s *recordingRadarStore) FinishOpportunityRun(context.Context, domain.OpportunityRun, []domain.Opportunity) error {
	return nil
}

func (s *recordingRadarStore) InsertDerivativesSnapshots(context.Context, []domain.DerivativesSnapshot) error {
	return nil
}

func (s *recordingRadarStore) DerivativesSnapshotAtOrBefore(context.Context, domain.AssetClass, string, time.Time) (domain.DerivativesSnapshot, bool, error) {
	return domain.DerivativesSnapshot{}, false, nil
}

func (s *recordingRadarStore) startedClasses() map[domain.AssetClass]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[domain.AssetClass]int, len(s.started))
	for assetClass, count := range s.started {
		result[assetClass] = count
	}
	return result
}

func radarMarketResult(now time.Time) providers.OKXMarketResult {
	result := providers.OKXMarketResult{Instruments: make(map[string]domain.MarketInstrument)}
	fixtures := []struct {
		id         string
		symbol     string
		assetClass domain.AssetClass
		age        time.Duration
	}{
		{id: "BTC-USDT-SWAP", symbol: "BTC", assetClass: domain.AssetClassCrypto, age: 800 * 24 * time.Hour},
		{id: "AAPL-USDT-SWAP", symbol: "AAPL", assetClass: domain.AssetClassStock, age: 48 * time.Hour},
		{id: "SPY-USDT-SWAP", symbol: "SPY", assetClass: domain.AssetClassStock, age: 48 * time.Hour},
	}
	for index, fixture := range fixtures {
		result.Instruments[fixture.id] = domain.MarketInstrument{InstrumentID: fixture.id, Symbol: fixture.symbol, ListedAt: now.Add(-fixture.age), TickSize: 0.01}
		result.Snapshots = append(result.Snapshots, domain.DerivativesSnapshot{
			AssetClass: fixture.assetClass, InstrumentID: fixture.id, Symbol: fixture.symbol,
			CollectedAt: now, SourceTime: now, PriceUSD: 100 + float64(index),
			Turnover24hUSD: 50_000_000, OpenInterestUSD: 20_000_000,
			OpenInterestAvailable: true, FundingRateAvailable: true,
		})
	}
	return result
}

func waitForRadarScheduler(t *testing.T, scheduler *marketwatch.Scheduler) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		switch scheduler.Status().State {
		case "completed", "degraded":
			return
		case "failed":
			t.Fatalf("scheduler failed: %+v", scheduler.Status())
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("scheduler timed out: %+v", scheduler.Status())
}

func mapEnvironment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}
