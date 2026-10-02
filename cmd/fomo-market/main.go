package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/markets"
	"github.com/alen1/fomo-radar/internal/marketwatch"
	"github.com/alen1/fomo-radar/internal/providers"
	"github.com/alen1/fomo-radar/internal/store"
)

type mode string

const (
	modeScan  mode = "scan"
	modeWatch mode = "watch"
)

type options struct {
	mode         mode
	databasePath string
	proxy        string
	interval     time.Duration
}

type marketReader interface {
	Read(context.Context) (providers.OKXMarketResult, error)
}

type snapshotStore interface {
	LatestDerivativesSnapshotBefore(context.Context, domain.AssetClass, string, time.Time) (domain.DerivativesSnapshot, bool, error)
	InsertDerivativesSnapshots(context.Context, []domain.DerivativesSnapshot) error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	parsed, err := parseOptions(args, getenv)
	if err != nil {
		return err
	}
	client, err := providers.NewHTTPClientWithProxy(15*time.Second, parsed.proxy)
	if err != nil {
		return err
	}
	database, err := store.Open(parsed.databasePath)
	if err != nil {
		return err
	}
	reader := providers.NewOKXMarketReader(client, "", time.Now)
	candleReader := providers.NewOKXCandleReader(client, "", providers.NewRequestPacer(120*time.Millisecond))
	scanner := marketwatch.NewScanner(reader, candleReader, database, markets.DefaultUniverseConfig(), "BTC-USDT-SWAP", time.Now)
	cycle := func() error {
		_, err := scanner.Scan(ctx, marketwatch.ScanRequest{
			Trigger: "cli", RefreshUniverse: true, Force: true,
			Frames: []domain.Timeframe{domain.Timeframe15m, domain.Timeframe1H, domain.Timeframe4H, domain.Timeframe1D},
		}, nil)
		if err != nil {
			return err
		}
		report, err := database.LatestOpportunityReport(ctx, domain.AssetClassCrypto)
		if err != nil {
			return err
		}
		return writeOpportunityReport(stdout, report)
	}

	var runErr error
	if parsed.mode == modeScan {
		runErr = cycle()
	} else {
		runErr = watch(ctx, parsed.interval, cycle, stderr)
	}
	return errors.Join(runErr, database.Close())
}

func writeOpportunityReport(output io.Writer, report domain.OpportunityReport) error {
	if output == nil {
		output = io.Discard
	}
	if _, err := fmt.Fprintf(output, "动态池 %d · 候选 %d · 状态 %s\n", report.Run.PoolSize, len(report.Opportunities), report.Run.Status); err != nil {
		return err
	}
	for _, opportunity := range report.Opportunities {
		entry, stop, targets := "等待确认", "--", "--"
		if plan := opportunity.TradePlan; plan != nil {
			if len(plan.Entries) > 0 {
				entry = formatPrice(plan.Entries[0].Price)
			}
			stop = formatPrice(plan.StopPrice)
			parts := make([]string, 0, len(plan.Targets))
			for _, target := range plan.Targets {
				parts = append(parts, formatPrice(target.Price))
			}
			targets = strings.Join(parts, "/")
		}
		if _, err := fmt.Fprintf(output, "%s\tFOMO %d\t机会 %d\t%s\t入场 %s\t止损 %s\t目标 %s\n",
			opportunity.Instrument.Symbol, opportunity.FOMOScore, opportunity.OpportunityScore,
			opportunity.Stage, entry, stop, targets); err != nil {
			return err
		}
	}
	return nil
}

// scanOnce 先读取历史基线再落库，避免本次数据被当成自己的上一轮。
func scanOnce(ctx context.Context, reader marketReader, database snapshotStore) (markets.Report, error) {
	result, err := reader.Read(ctx)
	if err != nil {
		return markets.Report{}, fmt.Errorf("读取 OKX 公共行情: %w", err)
	}
	previous := make(map[string]domain.DerivativesSnapshot)
	// 先让分析层筛选固定观察列表，只为需要展示的标的查询历史记录。
	selected := markets.Analyze(result.Snapshots, nil, result.Warnings)
	tracked := make([]domain.DerivativesSnapshot, 0, len(selected.Crypto)+len(selected.Stocks))
	for _, rows := range [][]markets.Row{selected.Crypto, selected.Stocks} {
		for _, row := range rows {
			tracked = append(tracked, row.DerivativesSnapshot)
			before, ok, err := database.LatestDerivativesSnapshotBefore(ctx, row.AssetClass, row.InstrumentID, row.CollectedAt)
			if err != nil {
				return markets.Report{}, err
			}
			if ok {
				previous[row.InstrumentID] = before
			}
		}
	}
	report := markets.Analyze(result.Snapshots, previous, result.Warnings)
	if err := database.InsertDerivativesSnapshots(ctx, tracked); err != nil {
		return markets.Report{}, err
	}
	return report, nil
}

func watch(ctx context.Context, interval time.Duration, cycle func() error, stderr io.Writer) error {
	if interval <= 0 {
		return fmt.Errorf("监控间隔必须大于 0")
	}
	if stderr == nil {
		stderr = io.Discard
	}
	for {
		if err := cycle(); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			_, _ = fmt.Fprintf(stderr, "本轮扫描失败：%v\n", err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func writeReport(output io.Writer, report markets.Report) error {
	if output == nil {
		output = io.Discard
	}
	if err := writeSection(output, "主流加密", report.Crypto, report.CryptoSummary); err != nil {
		return err
	}
	if err := writeSection(output, "美股", report.Stocks, report.StockSummary); err != nil {
		return err
	}
	if len(report.Warnings) > 0 {
		if _, err := fmt.Fprintf(output, "降级提示：%s\n", strings.Join(report.Warnings, "；")); err != nil {
			return fmt.Errorf("写入降级提示: %w", err)
		}
	}
	return nil
}

func writeSection(output io.Writer, title string, rows []markets.Row, summary string) error {
	if _, err := fmt.Fprintf(output, "\n=== %s ===\n", title); err != nil {
		return fmt.Errorf("写入%s标题: %w", title, err)
	}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "标的\t价格\t24h涨跌\t24h成交额\t持仓量\t持仓变化\t资金费率\t提示"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%+.2f%%\t%s\t%s\t%s\t%s\t%s\n",
			row.Symbol, formatPrice(row.PriceUSD), row.Change24hPct, formatAmount(row.Turnover24hUSD),
			formatOptionalAmount(row.OpenInterestUSD, row.OpenInterestAvailable), formatChange(row.OpenInterestChangePct),
			formatFunding(row.FundingRate, row.FundingRateAvailable), row.Signal,
		); err != nil {
			return fmt.Errorf("写入%s行情: %w", title, err)
		}
	}
	if err := table.Flush(); err != nil {
		return fmt.Errorf("刷新%s行情: %w", title, err)
	}
	if _, err := fmt.Fprintf(output, "概览：%s\n", summary); err != nil {
		return fmt.Errorf("写入%s概览: %w", title, err)
	}
	return nil
}

func formatPrice(value float64) string {
	switch {
	case value >= 100:
		return fmt.Sprintf("$%.2f", value)
	case value >= 1:
		return fmt.Sprintf("$%.4f", value)
	default:
		return fmt.Sprintf("$%.6f", value)
	}
}

func formatAmount(value float64) string {
	switch {
	case value >= 1_000_000_000:
		return fmt.Sprintf("$%.2fB", value/1_000_000_000)
	case value >= 1_000_000:
		return fmt.Sprintf("$%.2fM", value/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("$%.2fK", value/1_000)
	default:
		return fmt.Sprintf("$%.2f", value)
	}
}

func formatOptionalAmount(value float64, available bool) string {
	if !available {
		return "N/A"
	}
	return formatAmount(value)
}

func formatChange(value *float64) string {
	if value == nil {
		return "N/A"
	}
	return fmt.Sprintf("%+.2f%%", *value)
}

func formatFunding(value float64, available bool) string {
	if !available {
		return "N/A"
	}
	return fmt.Sprintf("%+.4f%%", value*100)
}

func parseOptions(args []string, getenv func(string) string) (options, error) {
	parsed := options{
		databasePath: environmentOrDefault(getenv, "FOMO_DB", "./fomo.db"),
		proxy:        environmentOrDefault(getenv, "FOMO_PROXY", ""),
		interval:     2 * time.Minute,
	}
	flags := flag.NewFlagSet("fomo-market", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var scan, watch bool
	flags.BoolVar(&scan, "scan", false, "执行一次 OKX 行情分析")
	flags.BoolVar(&watch, "watch", false, "持续执行 OKX 行情分析")
	flags.StringVar(&parsed.databasePath, "db", parsed.databasePath, "SQLite 数据库路径")
	flags.StringVar(&parsed.proxy, "proxy", parsed.proxy, "HTTP/HTTPS 代理，例如 http://127.0.0.1:10808")
	flags.DurationVar(&parsed.interval, "interval", parsed.interval, "持续监控间隔")
	if err := flags.Parse(args); err != nil {
		return options{}, fmt.Errorf("解析命令行参数: %w", err)
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("多余参数：%s", strings.Join(flags.Args(), " "))
	}
	if scan == watch {
		return options{}, fmt.Errorf("必须且只能指定 -scan 或 -watch")
	}
	if scan {
		parsed.mode = modeScan
	} else {
		parsed.mode = modeWatch
	}
	if strings.TrimSpace(parsed.databasePath) == "" {
		return options{}, fmt.Errorf("数据库路径不能为空")
	}
	if parsed.mode == modeWatch && parsed.interval <= 0 {
		return options{}, fmt.Errorf("监控间隔必须大于 0")
	}
	return parsed, nil
}

func environmentOrDefault(getenv func(string) string, name, fallback string) string {
	if getenv != nil {
		if value := getenv(name); value != "" {
			return value
		}
	}
	return fallback
}
