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
	"time"

	"github.com/alen1/fomo-radar/internal/lab"
)

type mode string

const (
	modeRunOnce mode = "run-once"
	modeWatch   mode = "watch"
	modeReport  mode = "report"
)

type options struct {
	mode     mode
	sourceDB string
	labDB    string
	interval time.Duration
	format   string
}

type cycleFunc func(context.Context) (lab.CycleResult, error)

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
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	logger := log.New(stderr, "fomo-lab: ", log.LstdFlags|log.LUTC)
	if parsed.mode == modeReport {
		repo, err := lab.OpenRepository(parsed.labDB)
		if err != nil {
			return err
		}
		analyzer := lab.NewAnalyzer(repo, time.Now)
		_, analysisErr := analyzer.Materialize(ctx)
		if analysisErr == nil {
			var report lab.ThresholdReport
			report, analysisErr = repo.LatestAnalysis(ctx)
			if analysisErr == nil {
				if parsed.format == "json" {
					analysisErr = lab.WriteJSON(stdout, report)
				} else {
					analysisErr = lab.WriteTable(stdout, report)
				}
			}
		}
		return errors.Join(analysisErr, repo.Close())
	}

	source, err := lab.OpenSource(parsed.sourceDB)
	if err != nil {
		return err
	}
	repo, err := lab.OpenRepository(parsed.labDB)
	if err != nil {
		return errors.Join(err, source.Close())
	}
	processor := lab.NewProcessor(source, repo, time.Now)
	cycle := func(cycleCtx context.Context) (lab.CycleResult, error) {
		return processor.ProcessCycle(cycleCtx)
	}
	var ticks <-chan time.Time
	var ticker *time.Ticker
	if parsed.mode == modeWatch {
		ticker = time.NewTicker(parsed.interval)
		ticks = ticker.C
	}
	if ticker != nil {
		defer ticker.Stop()
	}
	cycleErr := executeCycles(ctx, parsed.mode, ticks, cycle, logger)
	return errors.Join(cycleErr, repo.Close(), source.Close())
}

func executeCycles(ctx context.Context, selected mode, ticks <-chan time.Time, cycle cycleFunc, logger *log.Logger) error {
	if cycle == nil {
		return fmt.Errorf("Signal Lab cycle function is required")
	}
	runCycle := func() error {
		result, err := cycle(ctx)
		if err != nil {
			return err
		}
		if logger != nil {
			logger.Printf("cycle source=%d processed=%d signals=%d entries=%d matured=%d insufficient=%d",
				result.SourceHighWatermark, result.SourceSnapshotsProcessed, result.SignalsCreated,
				result.EntriesCreated, result.OutcomesMatured, result.OutcomesInsufficient)
		}
		return nil
	}
	if selected == modeRunOnce {
		return runCycle()
	}
	if selected != modeWatch {
		return fmt.Errorf("unsupported cycle mode %q", selected)
	}
	if err := runCycle(); err != nil && logger != nil {
		logger.Printf("cycle failed: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ticks:
			if !ok {
				return nil
			}
			if err := runCycle(); err != nil && logger != nil {
				logger.Printf("cycle failed: %v", err)
			}
		}
	}
}

func parseOptions(args []string, getenv func(string) string) (options, error) {
	defaults := options{
		sourceDB: environmentOrDefault(getenv, "FOMO_DB", "./fomo.db"),
		labDB:    environmentOrDefault(getenv, "FOMO_LAB_DB", "./fomo-lab.db"),
		interval: 2 * time.Minute,
		format:   "table",
	}
	flags := flag.NewFlagSet("fomo-lab", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var runOnce, watch, report bool
	flags.BoolVar(&runOnce, "run-once", false, "process one Signal Lab cycle")
	flags.BoolVar(&watch, "watch", false, "continuously process Signal Lab cycles")
	flags.BoolVar(&report, "report", false, "materialize and print a threshold report")
	flags.StringVar(&defaults.sourceDB, "source-db", defaults.sourceDB, "read-only Core SQLite database path")
	flags.StringVar(&defaults.labDB, "lab-db", defaults.labDB, "Signal Lab SQLite database path")
	flags.DurationVar(&defaults.interval, "interval", defaults.interval, "watch cycle interval")
	flags.StringVar(&defaults.format, "format", defaults.format, "report format: table or json")
	if err := flags.Parse(args); err != nil {
		return options{}, fmt.Errorf("parse fomo-lab options: %w", err)
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	modeCount := 0
	if runOnce {
		defaults.mode = modeRunOnce
		modeCount++
	}
	if watch {
		defaults.mode = modeWatch
		modeCount++
	}
	if report {
		defaults.mode = modeReport
		modeCount++
	}
	if modeCount != 1 {
		return options{}, fmt.Errorf("exactly one of -run-once, -watch, or -report is required")
	}
	if defaults.sourceDB == "" || defaults.labDB == "" {
		return options{}, fmt.Errorf("database paths must not be empty")
	}
	if defaults.mode == modeWatch && defaults.interval <= 0 {
		return options{}, fmt.Errorf("watch interval must be positive")
	}
	if defaults.format != "table" && defaults.format != "json" {
		return options{}, fmt.Errorf("unsupported report format %q", defaults.format)
	}
	return defaults, nil
}

func environmentOrDefault(getenv func(string) string, name, fallback string) string {
	if getenv != nil {
		if value := getenv(name); value != "" {
			return value
		}
	}
	return fallback
}
