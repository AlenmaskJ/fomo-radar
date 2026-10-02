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
	"sort"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/config"
	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/providers"
	"github.com/alen1/fomo-radar/internal/scanner"
	"github.com/alen1/fomo-radar/internal/score"
	"github.com/alen1/fomo-radar/internal/store"
)

func main() {
	scan := flag.Bool("scan", false, "run one manual scan and exit")
	watch := flag.Bool("watch", false, "run Fast discovery every two minutes with background Deep enrichment")
	flag.Parse()
	if err := validateMode(*scan, *watch); err != nil {
		log.Fatal(err)
	}
	if !*scan && !*watch {
		flag.Usage()
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	database, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	client := providers.NewHTTPClient(cfg.HTTPTimeout)
	geckoPacer := providers.NewRequestPacer(6 * time.Second)
	launchDiscovery := providers.NewGeckoTerminalDiscoverer(client, "", nil, geckoPacer)
	geckoMomentum := providers.NewGeckoMomentumDiscoverer(client, "", nil, geckoPacer)
	dexMomentum := providers.NewDexMomentumDiscoverer(client, "", nil)
	coordinator := scanner.NewDiscoveryCoordinator(launchDiscovery, geckoMomentum, dexMomentum, database, nil)
	service := scanner.NewService(
		cfg,
		launchDiscovery,
		providers.NewDexScreenerEnricher(client, "", nil),
		providers.NewGeckoTradeReader(client, "", nil, geckoPacer),
		providers.NewMetadataSocialProvider(nil),
		score.NewEngine(),
		database,
		nil,
		coordinator,
	)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *scan {
		if err := runScan(ctx, database, service, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	queue := scanner.NewDeepQueue(service, database, 4, 64)
	if err := queue.Start(ctx); err != nil {
		_ = database.Close()
		log.Fatal(err)
	}
	runErr := runWatch(ctx, service, queue, os.Stdout, 2*time.Minute)
	closeErr := errors.Join(queue.Close(), database.Close())
	if runErr != nil {
		log.Fatal(runErr)
	}
	if closeErr != nil {
		log.Fatal(closeErr)
	}
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func validateMode(scan, watch bool) error {
	if scan && watch {
		return fmt.Errorf("-scan and -watch are mutually exclusive")
	}
	return nil
}

type scanService interface {
	Scan(context.Context, scanner.Mode) (scanner.Result, error)
}

type watchService interface {
	FastScan(context.Context, scanner.Mode) (scanner.Result, []domain.DeepTask, error)
}

type deepQueue interface {
	TryEnqueue(domain.DeepTask) (scanner.DeepAdmission, error)
	Stats() scanner.DeepQueueStats
}

func runScan(ctx context.Context, database io.Closer, service scanService, output io.Writer) (err error) {
	defer func() {
		if closeErr := database.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close database: %w", closeErr)
		}
	}()

	result, err := service.Scan(ctx, scanner.ModeManual)
	if err != nil {
		return err
	}
	return writeScanResult(output, "full", result, scanner.DeepQueueStats{Completed: result.DeepCompleted})
}

func runWatch(ctx context.Context, service watchService, queue deepQueue, output io.Writer, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("watch interval must be positive")
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		roundStarted := time.Now()
		result, tasks, err := service.FastScan(ctx, scanner.ModeWatch)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		for _, task := range tasks {
			if _, err := queue.TryEnqueue(task); err != nil {
				return fmt.Errorf("admit Deep task %s: %w", task.TokenID, err)
			}
		}
		stats := queue.Stats()
		result.DeepBacklog = stats.Queued + stats.InFlight + stats.Deferred
		if err := writeScanResult(output, "fast", result, stats); err != nil {
			return err
		}
		wait := interval - time.Since(roundStarted)
		if wait <= 0 {
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func writeScanResult(output io.Writer, stage string, result scanner.Result, stats scanner.DeepQueueStats) error {
	if _, err := fmt.Fprintf(output,
		"scan stage=%s status=%s candidates_seen=%d candidates_scored=%d fast_duration=%s deep_duration=%s gecko_only=%d merged=%d deep_backlog=%d\n",
		stage, result.Status, result.CandidatesSeen, result.CandidatesScored, result.FastDuration, result.DeepDuration,
		result.GeckoOnlyCandidates, result.MergedCandidates, result.DeepBacklog,
	); err != nil {
		return fmt.Errorf("write scan metrics: %w", err)
	}
	for _, chain := range []domain.Chain{domain.ChainBSC, domain.ChainSolana} {
		coverage, ok := result.DiscoveryCoverage[chain]
		if !ok {
			continue
		}
		span := time.Duration(0)
		if !coverage.NewestPoolAt.IsZero() && !coverage.OldestPoolAt.IsZero() {
			span = coverage.NewestPoolAt.Sub(coverage.OldestPoolAt)
		}
		if _, err := fmt.Fprintf(output,
			"coverage chain=%s coverage=%s newest_pool_at=%s oldest_pool_at=%s pages=%d unique_candidates=%d\n",
			chain, span, formatTime(coverage.NewestPoolAt), formatTime(coverage.OldestPoolAt), coverage.PagesFetched, coverage.UniqueCandidates,
		); err != nil {
			return fmt.Errorf("write discovery coverage: %w", err)
		}
	}
	if _, err := fmt.Fprintf(output,
		"distribution evidence_confidence=%s raw_tier=%s effective_tier=%s launch_type=%s\n",
		formatCounts(result.EvidenceConfidenceDistribution), formatCounts(result.RawTierDistribution),
		formatCounts(result.EffectiveTierDistribution), formatCounts(result.LaunchTypeDistribution),
	); err != nil {
		return fmt.Errorf("write distributions: %w", err)
	}
	if _, err := fmt.Fprintf(output,
		"deep deep_queued=%d deep_in_flight=%d deep_deferred=%d deep_completed=%d deep_stale=%d deep_failed=%d\n",
		stats.Queued, stats.InFlight, stats.Deferred, stats.Completed, stats.Stale, stats.Failed,
	); err != nil {
		return fmt.Errorf("write Deep metrics: %w", err)
	}
	if result.ErrorSummary != "" {
		if _, err := fmt.Fprintf(output, "degraded error_summary=%s\n", result.ErrorSummary); err != nil {
			return fmt.Errorf("write degraded summary: %w", err)
		}
	}
	limit := min(len(result.Candidates), 20)
	for _, candidate := range result.Candidates[:limit] {
		if _, err := fmt.Fprintf(output, "candidate chain=%s symbol=%s address=%s age=%s mc=%.2f liquidity=%.2f raw_score=%.2f raw_tier=%s effective_tier=%s confidence=%s launch_type=%s source=%s quality=%s\n",
			candidate.Candidate.Chain, candidate.Candidate.Symbol, candidate.Candidate.Address, candidateAge(candidate),
			candidate.Snapshot.MarketCapUSD.Value, candidate.Snapshot.LiquidityUSD.Value,
			candidate.Snapshot.ScoreBreakdown.RawScore, candidate.Snapshot.ScoreBreakdown.RawTier,
			candidate.Snapshot.ScoreBreakdown.EffectiveTier, candidate.Snapshot.ScoreBreakdown.EvidenceConfidence,
			candidate.LaunchType, candidate.Snapshot.MarketDataSource, candidate.Snapshot.MarketDataQuality,
		); err != nil {
			return fmt.Errorf("write scan candidate: %w", err)
		}
	}
	return nil
}

func candidateAge(candidate scanner.CandidateResult) string {
	created := candidate.Snapshot.DiscoveryPoolCreatedAt
	if created == nil {
		created = candidate.Snapshot.SelectedPairCreatedAt
	}
	if created == nil || candidate.Snapshot.CollectedAt.IsZero() {
		return "N/A"
	}
	return candidate.Snapshot.CollectedAt.Sub(*created).Round(time.Second).String()
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "N/A"
	}
	return value.UTC().Format(time.RFC3339)
}

func formatCounts[K ~string](counts map[K]int) string {
	parts := make([]string, 0, len(counts))
	for key, count := range counts {
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", key, count))
		}
	}
	if len(parts) == 0 {
		return "N/A"
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
