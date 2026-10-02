package web

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/markets"
	"github.com/alen1/fomo-radar/internal/marketwatch"
)

const requestTimeout = 3 * time.Second
const marketStaleAfter = 10 * time.Minute

//go:embed templates/*.html assets/*
var dashboardFiles embed.FS

type server struct {
	repository Repository
	logger     *log.Logger
	templates  *template.Template
	assets     http.Handler
	markets    map[domain.AssetClass]MarketControl
}

type radarPage struct {
	Title  string
	Tokens []TokenView
	Status ScannerStatus
}

type marketPage struct {
	Config  marketPageConfig
	Report  domain.OpportunityReport
	Status  marketwatch.ScanStatus
	Stale   bool
	Waiting bool
}

type marketHistoryPage struct {
	OpportunityHistoryPage
	Config marketPageConfig
}

type marketPageConfig struct {
	AssetClass   domain.AssetClass
	PagePath     string
	HistoryPath  string
	StatusPath   string
	ScanPath     string
	Title        string
	HistoryTitle string
	Kicker       string
	Subtitle     string
	EmptyTitle   string
	AssetLabel   string
	Notice       string
}

var cryptoMarketPage = marketPageConfig{
	AssetClass: domain.AssetClassCrypto, PagePath: "/market", HistoryPath: "/market/history",
	StatusPath: "/api/market/status", ScanPath: "/api/market/scan", Title: "主流币雷达",
	HistoryTitle: "历史榜单", Kicker: "MAINSTREAM COIN RADAR",
	Subtitle:   "从主流 USDT 永续合约中筛出正在启动或出现启动预兆的标的。",
	EmptyTitle: "当前没有高质量机会", AssetLabel: "代币",
}

var stockMarketPage = marketPageConfig{
	AssetClass: domain.AssetClassStock, PagePath: "/stocks", HistoryPath: "/stocks/history",
	StatusPath: "/api/stocks/status", ScanPath: "/api/stocks/scan", Title: "美股机会雷达",
	HistoryTitle: "美股历史榜单", Kicker: "US STOCK OPPORTUNITY RADAR",
	Subtitle:   "从 OKX 美国股票永续合约中筛选正在启动或突破的标的。",
	EmptyTitle: "当前没有高质量美股机会", AssetLabel: "股票",
	Notice: "OKX 股票永续 24 小时交易；美股现货休市期间流动性和价格偏差可能扩大。",
}

// NewServer builds the read-only Dashboard handler.
func NewServer(repository Repository, logger *log.Logger) (http.Handler, error) {
	return NewServerWithMarketControls(repository, nil, logger)
}

func NewServerWithMarketControl(repository Repository, control MarketControl, logger *log.Logger) (http.Handler, error) {
	controls := map[domain.AssetClass]MarketControl{}
	if control != nil {
		controls[domain.AssetClassCrypto] = control
	}
	return NewServerWithMarketControls(repository, controls, logger)
}

func NewServerWithMarketControls(repository Repository, controls map[domain.AssetClass]MarketControl, logger *log.Logger) (http.Handler, error) {
	if repository == nil {
		return nil, fmt.Errorf("dashboard repository is required")
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	templates, err := template.New("dashboard").Funcs(template.FuncMap{
		"age":              formatAge,
		"boolValue":        formatBool,
		"chainLabel":       chainLabel,
		"confidenceLabel":  confidenceLabel,
		"coverage":         formatCoverage,
		"dataLabel":        dataQualityLabel,
		"deepLabel":        deepStatusLabel,
		"delta":            formatDelta,
		"duration":         formatDuration,
		"factorScore":      formatFactorScore,
		"intPair":          formatIntPair,
		"intValue":         formatInt,
		"launchLabel":      launchTypeLabel,
		"marketClass":      marketClass,
		"marketFunding":    formatMarketFunding,
		"marketInsight":    marketInsight,
		"marketMoney":      formatMarketMoney,
		"marketOIChange":   formatMarketOIChange,
		"marketPercent":    formatMarketPercent,
		"marketPrice":      formatMarketPrice,
		"marketTickPrice":  formatMarketTickPrice,
		"scanState":        marketScanStateLabel,
		"scanTrigger":      marketScanTriggerLabel,
		"opportunityStage": opportunityStageLabel,
		"opportunityClass": opportunityStageClass,
		"weightPercent":    formatWeightPercent,
		"addOne":           func(value int) int { return value + 1 },
		"money":            formatMoney,
		"percent":          formatPercent,
		"providerLabel":    providerStatusLabel,
		"ratio":            formatRatio,
		"relative":         formatRelative,
		"riskLabel":        riskFlagLabel,
		"score":            formatScore,
		"scorePair":        formatScorePair,
		"sourceLabel":      marketSourceLabel,
		"stageLabel":       stageLabel,
		"tierLabel":        tierLabel,
		"time":             formatTimestamp,
		"tokenURL":         tokenURL,
		"historyURL":       historyURL,
		"historyPrice":     formatHistoryPrice,
		"historyReturn":    formatHistoryReturn,
		"historyReturnClass": func(value *float64) string {
			if value == nil {
				return "neutral"
			}
			return marketClass(*value)
		},
		"historyNextDir": func(currentSort, currentDir, column string) string {
			if currentSort == column && currentDir == "desc" {
				return "asc"
			}
			return "desc"
		},
		"historySortArrow": func(currentSort, currentDir, column string) string {
			if currentSort != column {
				return "↕"
			}
			if currentDir == "asc" {
				return "↑"
			}
			return "↓"
		},
	}).ParseFS(dashboardFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse dashboard templates: %w", err)
	}
	assetFiles, err := fs.Sub(dashboardFiles, "assets")
	if err != nil {
		return nil, fmt.Errorf("open dashboard assets: %w", err)
	}
	assets := http.FileServer(http.FS(assetFiles))
	s := &server{repository: repository, logger: logger, templates: templates, assets: assets, markets: controls}
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", getOnly(s.handleRadar))
	for _, config := range []marketPageConfig{cryptoMarketPage, stockMarketPage} {
		config := config
		mux.HandleFunc(config.PagePath, getOnly(func(response http.ResponseWriter, request *http.Request) { s.handleMarket(response, request, config) }))
		mux.HandleFunc(config.HistoryPath, getOnly(func(response http.ResponseWriter, request *http.Request) {
			s.handleMarketHistory(response, request, config)
		}))
		mux.HandleFunc("/health"+config.PagePath, getOnly(func(response http.ResponseWriter, request *http.Request) {
			s.handleMarketHealth(response, request, config)
		}))
		mux.HandleFunc(config.StatusPath, getOnly(func(response http.ResponseWriter, request *http.Request) {
			s.handleMarketStatus(response, request, config)
		}))
		mux.HandleFunc(config.ScanPath, postOnly(func(response http.ResponseWriter, request *http.Request) {
			s.handleMarketScan(response, request, config)
		}))
	}
	mux.HandleFunc("/token/{address}", getOnly(s.handleToken))
	mux.HandleFunc("/api/tokens", getOnly(s.handleAPITokens))
	mux.Handle("/assets/", getOnlyHandler(http.StripPrefix("/assets/", s.assets)))
	return mux, nil
}

func (s *server) handleMarket(response http.ResponseWriter, request *http.Request, config marketPageConfig) {
	ctx, cancel := context.WithTimeout(request.Context(), requestTimeout)
	defer cancel()
	report, err := s.repository.MarketOpportunities(ctx, config.AssetClass)
	waiting := errors.Is(err, sql.ErrNoRows)
	if err != nil && !waiting {
		s.serviceUnavailable(response, err)
		return
	}
	page := marketPage{Config: config, Report: report, Waiting: waiting}
	if control := s.markets[config.AssetClass]; control != nil {
		page.Status = control.Status()
	}
	page.Stale = report.Run.ID > 0 && (report.Run.FinishedAt.IsZero() || time.Since(report.Run.FinishedAt) > marketStaleAfter)
	s.render(response, "market", page)
}

func (s *server) handleMarketHistory(response http.ResponseWriter, request *http.Request, config marketPageConfig) {
	ctx, cancel := context.WithTimeout(request.Context(), requestTimeout)
	defer cancel()
	filter := opportunityHistoryFilterFromRequest(request, time.Now().UTC())
	page, err := s.repository.MarketOpportunityHistory(ctx, config.AssetClass, filter)
	if err != nil {
		s.serviceUnavailable(response, err)
		return
	}
	page.Title = config.HistoryTitle
	page.Filter = filter
	s.render(response, "market_history", marketHistoryPage{OpportunityHistoryPage: page, Config: config})
}

func opportunityHistoryFilterFromRequest(request *http.Request, asOf time.Time) OpportunityHistoryFilter {
	query := request.URL.Query()
	filter := OpportunityHistoryFilter{
		Window: historyWindow24H, Status: historyStatusAll, Sort: historySortRate, Direction: "desc", AsOf: asOf.UTC(),
	}
	switch query.Get("window") {
	case historyWindow24H, historyWindow7D:
		filter.Window = query.Get("window")
	}
	switch query.Get("status") {
	case historyStatusAll, historyStatusActive, historyStatusEnded:
		filter.Status = query.Get("status")
	}
	switch query.Get("sort") {
	case historySortRate, historySortCount, historySortReturn:
		filter.Sort = query.Get("sort")
	}
	switch query.Get("dir") {
	case "asc", "desc":
		filter.Direction = query.Get("dir")
	}
	return filter
}

func historyURL(basePath, window, status, sortKey, direction string) string {
	query := url.Values{}
	query.Set("window", window)
	query.Set("status", status)
	query.Set("sort", sortKey)
	query.Set("dir", direction)
	return basePath + "?" + query.Encode()
}

func (s *server) handleMarketHealth(response http.ResponseWriter, request *http.Request, config marketPageConfig) {
	ctx, cancel := context.WithTimeout(request.Context(), requestTimeout)
	defer cancel()
	report, err := s.repository.MarketOpportunities(ctx, config.AssetClass)
	ready := err == nil && report.Run.ID > 0 && (report.Run.Status == domain.OpportunityRunCompleted || report.Run.Status == domain.OpportunityRunDegraded) && !report.Run.FinishedAt.IsZero() && time.Since(report.Run.FinishedAt) <= marketStaleAfter
	if !ready {
		if err != nil {
			s.logger.Printf("%s health unavailable: %v", config.AssetClass, err)
		}
		http.Error(response, "market data not ready", http.StatusServiceUnavailable)
		return
	}
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(response, "ok\n")
}

func (s *server) handleMarketStatus(response http.ResponseWriter, _ *http.Request, config marketPageConfig) {
	control := s.markets[config.AssetClass]
	if control == nil {
		http.Error(response, "market scanner unavailable", http.StatusServiceUnavailable)
		return
	}
	s.writeJSON(response, http.StatusOK, control.Status())
}

func (s *server) handleMarketScan(response http.ResponseWriter, request *http.Request, config marketPageConfig) {
	control := s.markets[config.AssetClass]
	if control == nil {
		http.Error(response, "market scanner unavailable", http.StatusServiceUnavailable)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(response, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if !sameOriginOrLoopback(request) {
		http.Error(response, "forbidden origin", http.StatusForbidden)
		return
	}
	var payload map[string]any
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1024))
	if err := decoder.Decode(&payload); err != nil {
		http.Error(response, "invalid JSON", http.StatusBadRequest)
		return
	}
	status, err := control.Trigger(request.Context())
	if errors.Is(err, marketwatch.ErrScanCooldown) {
		s.writeJSON(response, http.StatusTooManyRequests, status)
		return
	}
	if err != nil {
		s.logger.Printf("trigger %s scan: %v", config.AssetClass, err)
		http.Error(response, "market scan unavailable", http.StatusServiceUnavailable)
		return
	}
	s.writeJSON(response, http.StatusAccepted, status)
}

func (s *server) writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		s.logger.Printf("encode market API: %v", err)
	}
}

func sameOriginOrLoopback(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin != "" {
		parsed, err := url.Parse(origin)
		return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && strings.EqualFold(parsed.Host, request.Host)
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	return net.ParseIP(host).IsLoopback()
}

func oldestMarketTime(rows []markets.Row) time.Time {
	var oldest time.Time
	for _, row := range rows {
		if oldest.IsZero() || row.CollectedAt.Before(oldest) {
			oldest = row.CollectedAt
		}
	}
	return oldest
}

func getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(response, request)
	}
}

func getOnlyHandler(next http.Handler) http.Handler {
	return getOnly(next.ServeHTTP)
}

func postOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(response, request)
	}
}

func (s *server) handleRadar(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), requestTimeout)
	defer cancel()
	tokens, err := s.repository.ListRadar(ctx, 30)
	if err != nil {
		s.serviceUnavailable(response, err)
		return
	}
	status, err := s.repository.DashboardStatus(ctx)
	if err != nil {
		s.logger.Printf("read dashboard status: %v", err)
	}
	s.render(response, "radar", radarPage{Title: "FOMO 异动雷达", Tokens: tokens, Status: status})
}

func (s *server) handleToken(response http.ResponseWriter, request *http.Request) {
	address := request.PathValue("address")
	if address == "" {
		http.NotFound(response, request)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), requestTimeout)
	defer cancel()
	detail, err := s.repository.TokenByAddress(ctx, address, request.URL.Query().Get("chain"), 100)
	if err != nil {
		s.handleRepositoryError(response, request, err)
		return
	}
	s.render(response, "token", detail)
}

func (s *server) handleAPITokens(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), requestTimeout)
	defer cancel()
	filter := tokenFilterFromRequest(request, 500)
	filter.IncludeDetails = true
	tokens, err := s.repository.ListTokens(ctx, filter)
	if err != nil {
		s.serviceUnavailable(response, err)
		return
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	encoder := json.NewEncoder(response)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(TokenListResponse{GeneratedAt: time.Now().UTC(), Tokens: tokens}); err != nil {
		s.logger.Printf("encode dashboard token API: %v", err)
	}
}

func tokenFilterFromRequest(request *http.Request, limit int) TokenFilter {
	query := request.URL.Query()
	return TokenFilter{
		Limit: limit, Query: strings.TrimSpace(query.Get("q")), Chain: query.Get("chain"),
		EffectiveTier: query.Get("tier"), LaunchType: query.Get("launch"),
		EvidenceConfidence: query.Get("evidence"), DataQuality: query.Get("data"),
	}
}

func (s *server) render(response http.ResponseWriter, name string, value any) {
	var body bytes.Buffer
	if err := s.templates.ExecuteTemplate(&body, name, value); err != nil {
		s.logger.Printf("render dashboard template %s: %v", name, err)
		http.Error(response, "internal server error", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(body.Bytes())
}

func (s *server) handleRepositoryError(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrTokenNotFound):
		http.NotFound(response, request)
	case errors.Is(err, ErrAmbiguousToken):
		http.Error(response, "token address is ambiguous; specify chain", http.StatusConflict)
	default:
		s.serviceUnavailable(response, err)
	}
}

func (s *server) serviceUnavailable(response http.ResponseWriter, err error) {
	s.logger.Printf("dashboard repository unavailable: %v", err)
	http.Error(response, "dashboard data unavailable", http.StatusServiceUnavailable)
}

func formatAge(seconds *int64) string {
	if seconds == nil || *seconds < 0 {
		return "N/A"
	}
	return formatDuration(seconds)
}

func formatDuration(seconds *int64) string {
	if seconds == nil || *seconds < 0 {
		return "N/A"
	}
	duration := time.Duration(*seconds) * time.Second
	if duration < time.Minute {
		return fmt.Sprintf("%d秒", *seconds)
	}
	if duration < time.Hour {
		return fmt.Sprintf("%d分钟", int64(duration/time.Minute))
	}
	hours := int64(duration / time.Hour)
	minutes := int64((duration % time.Hour) / time.Minute)
	return fmt.Sprintf("%d小时%d分钟", hours, minutes)
}

func formatCoverage(coverage CoverageView) string {
	return formatDuration(coverage.DurationSeconds)
}

func formatMoney(value *float64) string {
	if value == nil {
		return "N/A"
	}
	switch absolute := *value; {
	case absolute >= 1_000_000_000:
		return fmt.Sprintf("$%.2fB", *value/1_000_000_000)
	case absolute >= 1_000_000:
		return fmt.Sprintf("$%.2fM", *value/1_000_000)
	case absolute >= 1_000:
		return fmt.Sprintf("$%.1fK", *value/1_000)
	default:
		return fmt.Sprintf("$%.2f", *value)
	}
}

func formatMarketMoney(value float64) string {
	return formatMoney(&value)
}

func formatMarketPrice(value float64) string {
	decimals := 2
	if value < 1 {
		decimals = 6
	} else if value < 100 {
		decimals = 4
	}
	return formatMarketPriceDecimals(value, decimals)
}

func formatMarketTickPrice(value, tick float64) string {
	decimals := 2
	if value < 1 {
		decimals = 6
	} else if value < 100 {
		decimals = 4
	}
	formattedTick := strconv.FormatFloat(tick, 'f', -1, 64)
	if dot := strings.IndexByte(formattedTick, '.'); dot >= 0 {
		decimals = max(decimals, len(strings.TrimRight(formattedTick[dot+1:], "0")))
	}
	return formatMarketPriceDecimals(value, min(decimals, 12))
}

func formatMarketPriceDecimals(value float64, decimals int) string {
	parts := strings.SplitN(fmt.Sprintf("%.*f", decimals, value), ".", 2)
	for index := len(parts[0]) - 3; index > 0; index -= 3 {
		parts[0] = parts[0][:index] + "," + parts[0][index:]
	}
	return "$" + strings.Join(parts, ".")
}

func formatMarketPercent(value float64) string {
	return fmt.Sprintf("%+.2f%%", value)
}

func formatHistoryPrice(value *float64) string {
	if value == nil {
		return "—"
	}
	return formatMarketPrice(*value)
}

func formatHistoryReturn(value *float64) string {
	if value == nil {
		return "—"
	}
	return formatMarketPercent(*value)
}

func formatMarketOIChange(value *float64) string {
	if value == nil {
		return "等待下一轮"
	}
	return formatMarketPercent(*value)
}

func formatMarketFunding(value float64, available bool) string {
	if !available {
		return "N/A"
	}
	return fmt.Sprintf("%+.4f%%", value*100)
}

func marketClass(value float64) string {
	if value > 0 {
		return "positive"
	}
	if value < 0 {
		return "negative"
	}
	return "neutral"
}

func opportunityStageLabel(stage domain.OpportunityStage) string {
	labels := map[domain.OpportunityStage]string{
		domain.StagePrelaunch: "启动预兆", domain.StageStarting: "正在启动",
		domain.StageConfirmed: "反转确认", domain.StageAccelerating: "趋势加速",
		domain.StageOverheated: "过热等待",
	}
	if label := labels[stage]; label != "" {
		return label
	}
	return "无信号"
}

func marketScanStateLabel(state string) string {
	labels := map[string]string{
		"queued": "等待中", "running": "扫描中", "completed": "已完成",
		"degraded": "部分完成", "failed": "失败",
	}
	if label := labels[state]; label != "" {
		return label
	}
	return "未开始"
}

func marketScanTriggerLabel(trigger string) string {
	labels := map[string]string{
		"startup": "启动扫描", "manual": "手动扫描", "scheduled": "定时扫描", "snapshot": "行情更新",
	}
	if label := labels[trigger]; label != "" {
		return label
	}
	return "市场扫描"
}

func opportunityStageClass(stage domain.OpportunityStage) string {
	switch stage {
	case domain.StageConfirmed, domain.StageAccelerating:
		return "stage-positive"
	case domain.StageOverheated:
		return "stage-risk"
	default:
		return "stage-wait"
	}
}

func formatWeightPercent(value float64) string {
	return fmt.Sprintf("%.0f%%", value*100)
}

func marketInsight(row markets.Row) string {
	if row.PriceChangePct == nil || row.OpenInterestChangePct == nil {
		return "等待下一轮短线数据"
	}
	// 两个方向都使用相邻采集周期，避免把 24 小时涨跌与 2 分钟持仓变化混用。
	if math.Abs(*row.PriceChangePct) < 0.05 || math.Abs(*row.OpenInterestChangePct) < 0.05 {
		return "短线方向暂不明确"
	}
	priceUp := *row.PriceChangePct > 0
	oiUp := *row.OpenInterestChangePct > 0
	switch {
	case priceUp && oiUp:
		return "多头主动进场"
	case !priceUp && oiUp:
		return "空头主动进场"
	case priceUp && !oiUp:
		return "空头回补"
	default:
		return "多头离场"
	}
}

func formatScore(value *float64) string {
	if value == nil {
		return "N/A"
	}
	return fmt.Sprintf("%.2f", *value)
}

func formatDelta(value *float64) string {
	if value == nil {
		return "N/A"
	}
	return fmt.Sprintf("%+.2f", *value)
}

func formatPercent(value *float64) string {
	if value == nil {
		return "N/A"
	}
	if *value >= 0 {
		return fmt.Sprintf("↑%.1f%%", *value)
	}
	return fmt.Sprintf("↓%.1f%%", -*value)
}

func formatRatio(value *float64) string {
	if value == nil {
		return "N/A"
	}
	return fmt.Sprintf("%.2fx", *value)
}

func formatInt(value *int) string {
	if value == nil {
		return "N/A"
	}
	return fmt.Sprintf("%d", *value)
}

func formatIntPair(previous, current *int) string {
	if previous == nil || current == nil {
		return "N/A"
	}
	return fmt.Sprintf("%d → %d", *previous, *current)
}

func formatScorePair(previous, current *float64) string {
	if previous == nil || current == nil {
		return "N/A"
	}
	return fmt.Sprintf("%.2f → %.2f", *previous, *current)
}

func formatBool(value *bool) string {
	if value == nil {
		return "N/A"
	}
	if *value {
		return "是"
	}
	return "否"
}

func formatFactorScore(factors map[string]FactorView, name string) string {
	factor, ok := factors[name]
	if !ok || !factor.Available || factor.Points == nil {
		return "N/A"
	}
	return fmt.Sprintf("%.2f / %.2f", *factor.Points, factor.Maximum)
}

func formatTimestamp(value time.Time) string {
	if value.IsZero() {
		return "N/A"
	}
	return value.Local().Format("2006-01-02 15:04:05")
}

func formatRelative(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "N/A"
	}
	delta := time.Until(*value)
	if delta >= 0 {
		seconds := int64(delta / time.Second)
		return formatDuration(&seconds) + "后"
	}
	seconds := int64((-delta) / time.Second)
	return formatDuration(&seconds) + "前"
}

func tierLabel(value string) string {
	return mapLabel(value, map[string]string{
		"BREAKOUT": "突破", "FAST_RISING": "快速升温", "WATCH": "观察", "HIDDEN": "已降温",
	})
}

func confidenceLabel(value string) string {
	return mapLabel(value, map[string]string{"HIGH": "高", "GOOD": "良好", "MEDIUM": "中等", "LOW": "低"})
}

func launchTypeLabel(value string) string {
	return mapLabel(value, map[string]string{"NEW_LAUNCH": "新启动", "REACTIVATION": "二次启动"})
}

func dataQualityLabel(value string) string {
	return mapLabel(value, map[string]string{"full": "完整数据", "partial": "部分数据"})
}

func marketSourceLabel(value string) string {
	return mapLabel(value, map[string]string{"merged": "Merged", "gecko": "Gecko-only", "dex": "Dex"})
}

func deepStatusLabel(value string) string {
	return mapLabel(value, map[string]string{
		"pending": "Deep 等待中…", "queued": "Deep 等待中…", "completed": "Deep ✓",
		"failed": "Deep ⚠", "deferred": "Deep 延后", "stale": "Deep 已过期", "not_required": "无需 Deep",
	})
}

func providerStatusLabel(value string) string {
	return mapLabel(value, map[string]string{"healthy": "正常", "degraded": "降级", "unknown": "未知"})
}

func stageLabel(value string) string {
	return mapLabel(value, map[string]string{"FAST": "Fast", "DEEP": "Deep"})
}

func chainLabel(value string) string {
	return mapLabel(value, map[string]string{"bsc": "BSC", "solana": "SOL"})
}

func riskFlagLabel(value string) string {
	return mapLabel(value, map[string]string{
		"HIGH_CONCENTRATION": "持仓高度集中", "LOW_LIQUIDITY": "流动性偏低",
		"CONCENTRATED_ACTIVITY": "交易活动集中", "SUSPECTED_BOT_ACTIVITY": "疑似机器人活动",
		"STRONG_DEV_SELLING": "开发者强卖出", "EARLY_LIQUIDITY": "早期流动性风险",
	})
}

func mapLabel(value string, labels map[string]string) string {
	if label, ok := labels[value]; ok {
		return label
	}
	if value == "" {
		return "N/A"
	}
	return value
}

func tokenURL(token TokenView) string {
	path := "/token/" + url.PathEscape(token.Address)
	if token.Chain == "" {
		return path
	}
	return path + "?chain=" + url.QueryEscape(strings.ToLower(token.Chain))
}
