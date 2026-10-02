package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/markets"
	"github.com/alen1/fomo-radar/internal/marketwatch"
)

func TestServerRoutesRenderRadarAPIAndDetail(t *testing.T) {
	marketCap := 120000.0
	liquidity := 30000.0
	rawScore := 88.0
	points := 21.0
	token := TokenView{
		ID: "bsc:0xbeef", SnapshotID: 41, Chain: "bsc", Address: "0xbeef",
		Name: "牛来 🚀", Symbol: `<script>alert(1)</script>`, PairAddress: "0xpair",
		FirstSeenAt: time.Date(2026, 8, 22, 8, 0, 0, 0, time.UTC),
		CollectedAt: time.Date(2026, 8, 22, 8, 8, 0, 0, time.UTC),
		AgeSeconds:  int64Pointer(480), MarketCapUSD: &marketCap, LiquidityUSD: &liquidity,
		RawScore: &rawScore, RawTier: "BREAKOUT", EffectiveTier: "BREAKOUT",
		EvidenceAvailable: 5, EvidenceTotal: 5, EvidenceConfidence: "HIGH",
		LaunchType: "NEW_LAUNCH", MarketDataSource: "merged", DataQuality: "full",
		Stage: "DEEP", DeepStatus: "completed", ScoreVersion: "FOMO_SCORE_V1.0",
		Factors: map[string]FactorView{
			"buyer_velocity":  {Points: &points, Maximum: 25, Available: true},
			"social_momentum": {Maximum: 15, Available: false},
		},
		AgeBonus: 10, RiskPenalty: 3, RiskFlags: []string{"EARLY_LIQUIDITY"},
	}
	repository := &fakeRepository{
		tokens: []TokenView{token, {ID: "solana:missing", Chain: "solana", Address: "mint", Name: "Missing", Symbol: "N/A", MarketCapUSD: nil}},
		detail: TokenDetail{Token: token, Timeline: []TimelinePoint{{
			SnapshotID: 40, CollectedAt: time.Date(2026, 8, 22, 8, 5, 0, 0, time.UTC),
			Stage: "FAST", MarketCapUSD: &marketCap, RawScore: &rawScore,
			EffectiveTier: "BREAKOUT", EvidenceConfidence: "HIGH", DeepStatus: "pending",
		}}},
	}
	handler := newTestServer(t, repository)

	home := serveRequest(t, handler, http.MethodGet, "/")
	if home.Code != http.StatusOK || !strings.Contains(home.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET / = %d %q", home.Code, home.Header().Get("Content-Type"))
	}
	if !strings.Contains(home.Body.String(), "牛来 🚀") {
		t.Fatalf("GET / lost UTF-8 name: %s", home.Body.String())
	}
	if strings.Contains(home.Body.String(), "<script>alert(1)</script>") || !strings.Contains(home.Body.String(), "&lt;script&gt;") {
		t.Fatalf("GET / did not escape symbol: %s", home.Body.String())
	}
	if repository.radarLimits[0] != 30 {
		t.Fatalf("GET / Radar limit = %d, want 30", repository.radarLimits[0])
	}
	for _, want := range []string{"主流币雷达", `href="/market"`} {
		if !strings.Contains(home.Body.String(), want) {
			t.Fatalf("GET / missing navigation %q: %s", want, home.Body.String())
		}
	}
	for _, unwanted := range []string{"全部代币", `href="/tokens"`} {
		if strings.Contains(home.Body.String(), unwanted) {
			t.Fatalf("GET / contains removed navigation %q: %s", unwanted, home.Body.String())
		}
	}
	if removed := serveRequest(t, handler, http.MethodGet, "/tokens"); removed.Code != http.StatusNotFound {
		t.Fatalf("GET /tokens = %d, want 404", removed.Code)
	}

	api := serveRequest(t, handler, http.MethodGet, "/api/tokens")
	if api.Code != http.StatusOK || !strings.Contains(api.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("GET /api/tokens = %d %q", api.Code, api.Header().Get("Content-Type"))
	}
	if len(repository.listFilters) != 1 || repository.listFilters[0].Limit != 500 {
		t.Fatalf("GET /api/tokens filters = %+v, want one filter with limit 500", repository.listFilters)
	}
	if !strings.Contains(api.Body.String(), `"market_cap_usd":null`) {
		t.Fatalf("GET /api/tokens missing JSON null: %s", api.Body.String())
	}
	var response TokenListResponse
	if err := json.Unmarshal(api.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode API response: %v", err)
	}
	if response.GeneratedAt.IsZero() || len(response.Tokens) != 2 {
		t.Fatalf("API response = %+v", response)
	}

	detail := serveRequest(t, handler, http.MethodGet, "/token/0xbeef")
	if detail.Code != http.StatusOK {
		t.Fatalf("GET detail = %d: %s", detail.Code, detail.Body.String())
	}
	for _, want := range []string{"FOMO_SCORE_V1.0", "Buyer Velocity", "21.00 / 25.00", "Social Momentum", "N/A", "EARLY_LIQUIDITY", "FAST"} {
		if !strings.Contains(detail.Body.String(), want) {
			t.Fatalf("GET detail missing %q: %s", want, detail.Body.String())
		}
	}
	if !strings.Contains(detail.Body.String(), "主流币雷达") || strings.Contains(detail.Body.String(), "全部代币") {
		t.Fatalf("GET detail navigation is inconsistent: %s", detail.Body.String())
	}
	if repository.detailAddress != "0xbeef" {
		t.Fatalf("detail address = %q, want 0xbeef", repository.detailAddress)
	}

	css := serveRequest(t, handler, http.MethodGet, "/assets/style.css")
	if css.Code != http.StatusOK || !strings.Contains(css.Header().Get("Content-Type"), "text/css") || css.Body.Len() == 0 {
		t.Fatalf("GET CSS = %d %q len=%d", css.Code, css.Header().Get("Content-Type"), css.Body.Len())
	}
	for _, want := range []string{"--bg:#090d13", "--panel:#111821", "--blue:#5b8cff", "body{margin:0;background:var(--bg)", ".brand-dot{display:inline-block;width:9px;height:9px;border-radius:50%;background:var(--blue)"} {
		if !strings.Contains(css.Body.String(), want) {
			t.Fatalf("GET CSS missing unified blue-black theme %q", want)
		}
	}
	if strings.Contains(css.Body.String(), "#12362e") {
		t.Fatalf("GET CSS still contains the old green page glow")
	}
}

func TestServerMapsMethodsAndRepositoryErrors(t *testing.T) {
	repository := &fakeRepository{detailErr: ErrTokenNotFound}
	handler := newTestServer(t, repository)

	for _, path := range []string{"/", "/market", "/market/history", "/health/market", "/api/tokens", "/token/0xbeef", "/assets/style.css"} {
		response := serveRequest(t, handler, http.MethodPost, path)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("POST %s = %d Allow %q, want 405 GET", path, response.Code, response.Header().Get("Allow"))
		}
	}
	if response := serveRequest(t, handler, http.MethodGet, "/tokens"); response.Code != http.StatusNotFound {
		t.Fatalf("removed tokens page status = %d, want 404", response.Code)
	}

	if response := serveRequest(t, handler, http.MethodGet, "/token/missing"); response.Code != http.StatusNotFound {
		t.Fatalf("missing token status = %d, want 404", response.Code)
	}
	repository.detailErr = ErrAmbiguousToken
	if response := serveRequest(t, handler, http.MethodGet, "/token/shared"); response.Code != http.StatusConflict {
		t.Fatalf("ambiguous token status = %d, want 409", response.Code)
	}
	repository.detailErr = errors.New("database busy")
	if response := serveRequest(t, handler, http.MethodGet, "/token/busy"); response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "database busy") {
		t.Fatalf("busy token response = %d %q, want opaque 503", response.Code, response.Body.String())
	}
	repository.detailErr = nil
	repository.listErr = errors.New("database path secret")
	if response := serveRequest(t, handler, http.MethodGet, "/api/tokens"); response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "database path secret") {
		t.Fatalf("busy list response = %d %q, want opaque 503", response.Code, response.Body.String())
	}
	if response := serveRequest(t, handler, http.MethodGet, "/unknown"); response.Code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want 404", response.Code)
	}
}

func TestServerMarketHistoryNormalizesQuery(t *testing.T) {
	repository := &fakeRepository{historyPage: OpportunityHistoryPage{EffectiveScans: 18}}
	handler := newTestServer(t, repository)

	response := serveRequest(t, handler, http.MethodGet, "/market/history?window=7d&status=ended&sort=return&dir=asc")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if len(repository.historyFilters) != 1 {
		t.Fatalf("history calls = %d", len(repository.historyFilters))
	}
	got := repository.historyFilters[0]
	if got.Window != "7d" || got.Status != "ended" || got.Sort != "return" || got.Direction != "asc" || got.AsOf.IsZero() {
		t.Fatalf("filter = %+v", got)
	}

	_ = serveRequest(t, handler, http.MethodGet, "/market/history?window=bad&status=bad&sort=drop-table&dir=sideways")
	got = repository.historyFilters[1]
	if got.Window != "24h" || got.Status != "all" || got.Sort != "rate" || got.Direction != "desc" {
		t.Fatalf("normalized invalid filter = %+v", got)
	}

	repository.historyErr = errors.New("secret database path")
	response = serveRequest(t, handler, http.MethodGet, "/market/history")
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "secret database path") {
		t.Fatalf("history error = %d %q", response.Code, response.Body.String())
	}
}

func TestHistoryURLUsesEncodedQuery(t *testing.T) {
	got := historyURL("/stocks/history", "7d", "active", "return", "asc")
	if got != "/stocks/history?dir=asc&sort=return&status=active&window=7d" {
		t.Fatalf("historyURL() = %q", got)
	}
}

func TestServerRendersMinimalMarketHistory(t *testing.T) {
	price, gain := 109.0, 9.0
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	endedAt := now.Add(-time.Hour)
	repository := &fakeRepository{historyPage: OpportunityHistoryPage{
		EffectiveScans: 18,
		GeneratedAt:    now,
		Rows: []OpportunityHistoryRow{
			{
				Rank: 1, InstrumentID: "AAA-USDT-SWAP", Symbol: "AAA", Status: historyStatusActive,
				Appearances: 16, ExperiencedScans: 18, ListingRate: 88.9,
				FirstListedAt: now.Add(-4 * time.Hour), LastListedAt: now.Add(-5 * time.Minute),
				FirstPrice: 100, EvaluationPrice: &price, ReturnPct: &gain,
				Episodes: []OpportunityEpisodeView{{
					StartedAt: now.Add(-4 * time.Hour), Status: historyStatusActive, Appearances: 16,
					ExperiencedScans: 18, ListingRate: 88.9, FirstPrice: 100,
					EvaluationPrice: &price, ReturnPct: &gain, LastListedAt: now.Add(-5 * time.Minute),
				}},
			},
			{
				Rank: 2, InstrumentID: "BBB-USDT-SWAP", Symbol: "BBB", Status: historyStatusEnded,
				Appearances: 5, ExperiencedScans: 18, ListingRate: 27.8,
				FirstListedAt: now.Add(-8 * time.Hour), LastListedAt: now.Add(-2 * time.Hour),
				FirstPrice: 50, EvaluationPrice: nil, ReturnPct: nil,
				Episodes: []OpportunityEpisodeView{{
					StartedAt: now.Add(-8 * time.Hour), EndedAt: &endedAt, Status: historyStatusEnded,
					Appearances: 5, ExperiencedScans: 18, ListingRate: 27.8, FirstPrice: 50,
					LastListedAt: now.Add(-2 * time.Hour),
				}},
			},
		},
	}}
	handler := newTestServer(t, repository)

	response := serveRequest(t, handler, http.MethodGet, "/market/history")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	body := html.UnescapeString(response.Body.String())
	for _, want := range []string{"主流币雷达", "历史榜单", "24 小时", "7 天", "AAA-USDT-SWAP", "88.9%", "16 / 18", "+9.00%", "已结束", "data-history-toggle", "—"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q: %s", want, body)
		}
	}
	for _, unwanted := range []string{"机会雷达", "全部代币", "OPPORTUNITY HISTORY", "界面预览", "market-history-kpi"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("unexpected %q: %s", unwanted, body)
		}
	}

	repository.historyPage.Rows = nil
	response = serveRequest(t, handler, http.MethodGet, "/market/history")
	if !strings.Contains(response.Body.String(), "当前窗口暂无上榜机会") {
		t.Fatalf("missing empty state: %s", response.Body.String())
	}

	repository.historyPage.EffectiveScans = 0
	response = serveRequest(t, handler, http.MethodGet, "/market/history")
	if !strings.Contains(response.Body.String(), "暂无完整扫描记录") {
		t.Fatalf("missing no-scan state: %s", response.Body.String())
	}

	repository.historyPage.EffectiveScans = 1
	repository.historyPage.Rows = []OpportunityHistoryRow{{Rank: 1, Symbol: `<script>alert(1)</script>`, InstrumentID: "SAFE", Status: historyStatusActive}}
	response = serveRequest(t, handler, http.MethodGet, "/market/history")
	if strings.Contains(response.Body.String(), "<script>alert(1)</script>") || !strings.Contains(response.Body.String(), "&lt;script&gt;") {
		t.Fatalf("history symbol was not escaped: %s", response.Body.String())
	}
}

func TestServerRendersMainstreamMarketRadar(t *testing.T) {
	finishedAt := time.Now().UTC()
	opportunity := domain.Opportunity{
		Instrument:  domain.MarketInstrument{InstrumentID: "AAA-USDT-SWAP", Symbol: "AAA", TickSize: 0.01},
		GeneratedAt: finishedAt, CurrentPrice: 101.2, FOMOScore: 82, OpportunityScore: 76, Stage: domain.StageConfirmed,
		Action: "反转已确认，按计划分批执行", RiskFlags: []string{"高波动"},
		Evidence: []domain.TimeframeEvidence{{Timeframe: domain.Timeframe1D, Score: 15, Confirmed: true, RSI: 61, MACDHistogram: 0.8, Reasons: []string{"站上EMA20/50"}}},
		TradePlan: &domain.TradePlan{
			Entries:           []domain.EntryLevel{{Price: 98, Weight: 0.3, Condition: "4H支撑区上沿止跌"}},
			ConfirmationEntry: domain.EntryLevel{Price: 102, Weight: 0.2, Condition: "15m收盘突破"}, FullAverageCost: 99,
			StopPrice: 93, RiskPct: 6.06, Targets: []domain.TargetLevel{{Price: 108, ExitWeight: 0.3, ReturnPct: 9.09, RewardRisk: 1.5}, {Price: 115, ExitWeight: 0.4, ReturnPct: 16.16, RewardRisk: 2.6}},
			TrailingRule: "1H更高低点或EMA20跟踪退出",
		},
	}
	repository := &fakeRepository{opportunityReport: domain.OpportunityReport{Run: domain.OpportunityRun{ID: 1, Status: domain.OpportunityRunCompleted, FinishedAt: finishedAt, PoolSize: 42, CandidateCount: 1}, Opportunities: []domain.Opportunity{opportunity}}}
	handler := newTestServer(t, repository)

	response := serveRequest(t, handler, http.MethodGet, "/market")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /market = %d: %s", response.Code, response.Body.String())
	}
	body := html.UnescapeString(response.Body.String())
	for _, want := range []string{"主流币雷达", "AAA", "FOMO 启动度", "82", "机会质量", "76", "分批建仓", "止损", "目标一", "下次雷达", "研究与演示用途", "market.js", "120"} {
		if !strings.Contains(body, want) {
			t.Fatalf("GET /market missing %q: %s", want, response.Body.String())
		}
	}
	for _, unwanted := range []string{"市场机会雷达", ">机会雷达<", "全部代币", `href="/tokens"`} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("GET /market contains stale copy %q: %s", unwanted, response.Body.String())
		}
	}

	repository.opportunityErr = errors.New("secret database path")
	response = serveRequest(t, handler, http.MethodGet, "/market")
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "secret database path") {
		t.Fatalf("market error response = %d %q, want opaque 503", response.Code, response.Body.String())
	}
}

func TestServerRendersStockRadarBeforeAndAfterFirstReport(t *testing.T) {
	now := time.Now().UTC()
	repository := &fakeRepository{stockOpportunityErr: sql.ErrNoRows}
	stockControl := &fakeMarketControl{status: marketwatch.ScanStatus{ID: "stock-scan-1", State: "running", Trigger: "startup", ProgressCurrent: 2, ProgressTotal: 12}}
	handler, err := NewServerWithMarketControls(repository, map[domain.AssetClass]MarketControl{domain.AssetClassStock: stockControl}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}

	response := serveRequest(t, handler, http.MethodGet, "/stocks")
	if response.Code != http.StatusOK {
		t.Fatalf("stock first scan = %d: %s", response.Code, response.Body.String())
	}
	firstBody := html.UnescapeString(response.Body.String())
	for _, want := range []string{"美股机会雷达", "扫描中", "2/12", "OKX 股票永续 24 小时交易"} {
		if !strings.Contains(firstBody, want) {
			t.Fatalf("first stock page missing %q: %s", want, firstBody)
		}
	}
	if strings.Contains(firstBody, "扫描结果已过期") {
		t.Fatalf("first scan was incorrectly marked stale: %s", firstBody)
	}

	repository.stockOpportunityErr = nil
	repository.stockOpportunityReport = domain.OpportunityReport{Run: domain.OpportunityRun{
		ID: 2, AssetClass: domain.AssetClassStock, Status: domain.OpportunityRunCompleted, FinishedAt: now, PoolSize: 24,
	}}
	stockControl.status = marketwatch.ScanStatus{ID: "stock-scan-2", State: "completed", Trigger: "startup"}
	response = serveRequest(t, handler, http.MethodGet, "/stocks")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "当前没有高质量美股机会") {
		t.Fatalf("empty stock page = %d: %s", response.Code, response.Body.String())
	}

	repository.stockOpportunityReport.Opportunities = []domain.Opportunity{{
		Instrument:  domain.MarketInstrument{InstrumentID: "AAPL-USDT-SWAP", Symbol: "AAPL", TickSize: 0.01},
		GeneratedAt: now, CurrentPrice: 230, FOMOScore: 80, OpportunityScore: 75, Stage: domain.StageStarting,
	}}
	response = serveRequest(t, handler, http.MethodGet, "/stocks")
	body := html.UnescapeString(response.Body.String())
	for _, want := range []string{"AAPL", "美股机会雷达", "美股现货休市期间流动性和价格偏差可能扩大"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stock opportunity page missing %q: %s", want, body)
		}
	}
}

func TestStockRoutesUseStockRepositoryAndControl(t *testing.T) {
	now := time.Now().UTC()
	repository := &fakeRepository{
		stockOpportunityReport: domain.OpportunityReport{Run: domain.OpportunityRun{ID: 3, AssetClass: domain.AssetClassStock, Status: domain.OpportunityRunCompleted, FinishedAt: now}},
		stockHistoryPage:       OpportunityHistoryPage{EffectiveScans: 1, GeneratedAt: now},
	}
	cryptoControl := &fakeMarketControl{status: marketwatch.ScanStatus{ID: "crypto-scan-1", State: "completed"}}
	stockControl := &fakeMarketControl{status: marketwatch.ScanStatus{ID: "stock-scan-1", State: "completed"}}
	handler, err := NewServerWithMarketControls(repository, map[domain.AssetClass]MarketControl{
		domain.AssetClassCrypto: cryptoControl,
		domain.AssetClassStock:  stockControl,
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/stocks", "/stocks/history", "/api/stocks/status", "/health/stocks"} {
		response := serveRequest(t, handler, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://example.com/api/stocks/scan", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || stockControl.triggered != 1 || cryptoControl.triggered != 0 {
		t.Fatalf("stock scan = %d, triggers stock/crypto = %d/%d", response.Code, stockControl.triggered, cryptoControl.triggered)
	}
	if response := serveRequest(t, handler, http.MethodGet, "/api/stocks/scan"); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET stock scan = %d", response.Code)
	}
	for _, assetClass := range repository.opportunityClasses {
		if assetClass != domain.AssetClassStock {
			t.Fatalf("stock route queried %s opportunities", assetClass)
		}
	}
	for _, assetClass := range repository.historyClasses {
		if assetClass != domain.AssetClassStock {
			t.Fatalf("stock route queried %s history", assetClass)
		}
	}

	stockControl.triggerErr = marketwatch.ErrScanCooldown
	request = httptest.NewRequest(http.MethodPost, "http://example.com/api/stocks/scan", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.com")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("stock cooldown = %d, want 429", response.Code)
	}
}

func TestMarketTemplatesUseConfiguredNavigationAndURLs(t *testing.T) {
	now := time.Now().UTC()
	repository := &fakeRepository{
		opportunityReport:      domain.OpportunityReport{Run: domain.OpportunityRun{ID: 1, AssetClass: domain.AssetClassCrypto, Status: domain.OpportunityRunCompleted, FinishedAt: now}},
		stockOpportunityReport: domain.OpportunityReport{Run: domain.OpportunityRun{ID: 2, AssetClass: domain.AssetClassStock, Status: domain.OpportunityRunCompleted, FinishedAt: now}},
		stockHistoryPage:       OpportunityHistoryPage{EffectiveScans: 1, GeneratedAt: now},
	}
	handler, err := NewServerWithMarketControls(repository, nil, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path  string
		wants []string
	}{
		{path: "/market", wants: []string{"主流币雷达", `data-page-url="/market"`, `data-status-url="/api/market/status"`, `data-scan-url="/api/market/scan"`, `href="/market/history"`, "market.js"}},
		{path: "/stocks", wants: []string{"美股机会雷达", `data-page-url="/stocks"`, `data-status-url="/api/stocks/status"`, `data-scan-url="/api/stocks/scan"`, `href="/market/history"`, "market.js"}},
		{path: "/stocks/history", wants: []string{"美股历史榜单", `href="/market/history"`, "美股雷达", `aria-label="榜单类别"`}},
		{path: "/", wants: []string{`href="/stocks"`, "美股雷达"}},
	}
	for _, test := range tests {
		response := serveRequest(t, handler, http.MethodGet, test.path)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", test.path, response.Code, response.Body.String())
		}
		for _, want := range test.wants {
			if !strings.Contains(response.Body.String(), want) {
				t.Fatalf("GET %s missing %q: %s", test.path, want, response.Body.String())
			}
		}
	}
}

func TestHistoryLeaderboardDefaultsToCryptoAndPreservesFiltersWhenSwitchingClass(t *testing.T) {
	now := time.Now().UTC()
	repository := &fakeRepository{
		historyPage:      OpportunityHistoryPage{EffectiveScans: 1, GeneratedAt: now},
		stockHistoryPage: OpportunityHistoryPage{EffectiveScans: 1, GeneratedAt: now},
	}
	handler, err := NewServerWithMarketControls(repository, nil, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}

	query := "?window=7d&status=active&sort=return&dir=asc"
	crypto := serveRequest(t, handler, http.MethodGet, "/market/history"+query)
	for _, want := range []string{
		`aria-label="榜单类别"`,
		`href="/market/history?dir=asc&amp;sort=return&amp;status=active&amp;window=7d" aria-current="page">主流币`,
		`href="/stocks/history?dir=asc&amp;sort=return&amp;status=active&amp;window=7d"`,
	} {
		if !strings.Contains(crypto.Body.String(), want) {
			t.Fatalf("crypto history missing %q: %s", want, crypto.Body.String())
		}
	}

	stock := serveRequest(t, handler, http.MethodGet, "/stocks/history"+query)
	for _, want := range []string{
		`href="/market/history?dir=asc&amp;sort=return&amp;status=active&amp;window=7d"`,
		`href="/stocks/history?dir=asc&amp;sort=return&amp;status=active&amp;window=7d" aria-current="page">美股`,
	} {
		if !strings.Contains(stock.Body.String(), want) {
			t.Fatalf("stock history missing %q: %s", want, stock.Body.String())
		}
	}
}

func TestMarketInsightExplainsPriceAndOpenInterestDirection(t *testing.T) {
	positivePrice := 2.0
	negativePrice := -2.0
	flatPrice := 0.01
	positiveOI := 3.0
	negativeOI := -3.0
	flatOI := 0.01
	tests := []struct {
		name   string
		price  *float64
		oi     *float64
		wanted string
	}{
		{name: "空头主动进场", price: &negativePrice, oi: &positiveOI, wanted: "空头主动进场"},
		{name: "空头回补", price: &positivePrice, oi: &negativeOI, wanted: "空头回补"},
		{name: "多头离场", price: &negativePrice, oi: &negativeOI, wanted: "多头离场"},
		{name: "等待基线", price: nil, oi: nil, wanted: "等待下一轮短线数据"},
		{name: "小幅波动", price: &flatPrice, oi: &flatOI, wanted: "短线方向暂不明确"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := markets.Row{PriceChangePct: test.price, OpenInterestChangePct: test.oi}
			if got := marketInsight(row); got != test.wanted {
				t.Fatalf("marketInsight() = %q, want %q", got, test.wanted)
			}
		})
	}
}

func TestServerMarksMarketPageStaleFromOldestInstrument(t *testing.T) {
	repository := &fakeRepository{opportunityReport: domain.OpportunityReport{Run: domain.OpportunityRun{ID: 1, Status: domain.OpportunityRunCompleted, FinishedAt: time.Now().UTC().Add(-11 * time.Minute)}}}

	response := serveRequest(t, newTestServer(t, repository), http.MethodGet, "/market")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "扫描结果已过期") {
		t.Fatalf("stale market response = %d %q", response.Code, response.Body.String())
	}
}

func TestMarketScanLabelsUsePlainChinese(t *testing.T) {
	states := map[string]string{"queued": "等待中", "running": "扫描中", "completed": "已完成", "degraded": "部分完成", "failed": "失败"}
	for input, want := range states {
		if got := marketScanStateLabel(input); got != want {
			t.Fatalf("marketScanStateLabel(%q) = %q, want %q", input, got, want)
		}
	}
	triggers := map[string]string{"startup": "启动扫描", "manual": "手动扫描", "scheduled": "定时扫描", "snapshot": "行情更新"}
	for input, want := range triggers {
		if got := marketScanTriggerLabel(input); got != want {
			t.Fatalf("marketScanTriggerLabel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMarketTickPricePreservesInstrumentPrecision(t *testing.T) {
	if got := formatMarketTickPrice(0.0056126, 0.0000001); got != "$0.0056126" {
		t.Fatalf("small tick price = %q", got)
	}
	if got := formatMarketTickPrice(12.25, 0.25); got != "$12.2500" {
		t.Fatalf("fractional tick price = %q", got)
	}
}

func TestMarketHealthAcceptsFreshEmptyCompletedRun(t *testing.T) {
	repository := &fakeRepository{opportunityReport: domain.OpportunityReport{Run: domain.OpportunityRun{ID: 1, Status: domain.OpportunityRunCompleted, FinishedAt: time.Now().UTC()}}}
	handler := newTestServer(t, repository)

	if response := serveRequest(t, handler, http.MethodGet, "/health/market"); response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "ok" {
		t.Fatalf("fresh health = %d %q, want 200 ok", response.Code, response.Body.String())
	}
	repository.opportunityReport.Run.FinishedAt = time.Now().UTC().Add(-11 * time.Minute)
	if response := serveRequest(t, handler, http.MethodGet, "/health/market"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale health = %d, want 503", response.Code)
	}
}

func TestMarketScanAPIEnforcesSameOriginAndDelegates(t *testing.T) {
	repository := &fakeRepository{}
	control := &fakeMarketControl{status: marketwatch.ScanStatus{ID: "scan-1", State: "queued", Trigger: "manual"}}
	handler, err := NewServerWithMarketControl(repository, control, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://example.com/api/market/scan", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || control.triggered != 1 || !strings.Contains(response.Body.String(), `"scan-1"`) {
		t.Fatalf("scan response/triggered = %d %q/%d", response.Code, response.Body.String(), control.triggered)
	}
	request = httptest.NewRequest(http.MethodPost, "http://example.com/api/market/scan", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || control.triggered != 1 {
		t.Fatalf("foreign origin = %d, triggers %d", response.Code, control.triggered)
	}
	if response := serveRequest(t, handler, http.MethodGet, "/api/market/scan"); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET scan = %d", response.Code)
	}
}

type fakeRepository struct {
	tokens                 []TokenView
	detail                 TokenDetail
	status                 ScannerStatus
	listErr                error
	detailErr              error
	statusErr              error
	marketReport           markets.Report
	marketErr              error
	opportunityReport      domain.OpportunityReport
	opportunityErr         error
	stockOpportunityReport domain.OpportunityReport
	stockOpportunityErr    error
	historyPage            OpportunityHistoryPage
	historyErr             error
	stockHistoryPage       OpportunityHistoryPage
	stockHistoryErr        error
	historyFilters         []OpportunityHistoryFilter
	opportunityClasses     []domain.AssetClass
	historyClasses         []domain.AssetClass
	radarLimits            []int
	listFilters            []TokenFilter
	detailAddress          string
}

func (r *fakeRepository) ListRadar(_ context.Context, limit int) ([]TokenView, error) {
	r.radarLimits = append(r.radarLimits, limit)
	return r.tokens, r.listErr
}

func (r *fakeRepository) ListTokens(_ context.Context, filter TokenFilter) ([]TokenView, error) {
	r.listFilters = append(r.listFilters, filter)
	return r.tokens, r.listErr
}

func (r *fakeRepository) TokenByAddress(_ context.Context, address, _ string, _ int) (TokenDetail, error) {
	r.detailAddress = address
	return r.detail, r.detailErr
}

func (r *fakeRepository) DashboardStatus(context.Context) (ScannerStatus, error) {
	return r.status, r.statusErr
}

func (r *fakeRepository) MarketRadar(context.Context) (markets.Report, error) {
	return r.marketReport, r.marketErr
}

func (r *fakeRepository) MarketOpportunities(_ context.Context, assetClass domain.AssetClass) (domain.OpportunityReport, error) {
	r.opportunityClasses = append(r.opportunityClasses, assetClass)
	if assetClass == domain.AssetClassStock {
		return r.stockOpportunityReport, r.stockOpportunityErr
	}
	return r.opportunityReport, r.opportunityErr
}

func (r *fakeRepository) MarketOpportunityHistory(_ context.Context, assetClass domain.AssetClass, filter OpportunityHistoryFilter) (OpportunityHistoryPage, error) {
	r.historyClasses = append(r.historyClasses, assetClass)
	r.historyFilters = append(r.historyFilters, filter)
	if assetClass == domain.AssetClassStock {
		return r.stockHistoryPage, r.stockHistoryErr
	}
	return r.historyPage, r.historyErr
}

type fakeMarketControl struct {
	status     marketwatch.ScanStatus
	triggered  int
	triggerErr error
}

func (c *fakeMarketControl) Trigger(context.Context) (marketwatch.ScanStatus, error) {
	c.triggered++
	return c.status, c.triggerErr
}

func (c *fakeMarketControl) Status() marketwatch.ScanStatus { return c.status }

func (r *fakeRepository) Close() error { return nil }

func newTestServer(t *testing.T, repository Repository) http.Handler {
	t.Helper()
	handler, err := NewServer(repository, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return handler
}

func serveRequest(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func int64Pointer(value int64) *int64 { return &value }
