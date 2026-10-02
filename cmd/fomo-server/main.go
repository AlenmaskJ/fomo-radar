package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/markets"
	"github.com/alen1/fomo-radar/internal/marketwatch"
	"github.com/alen1/fomo-radar/internal/providers"
	"github.com/alen1/fomo-radar/internal/store"
	"github.com/alen1/fomo-radar/internal/web"
)

const (
	defaultDatabasePath = "./fomo.db"
	defaultListen       = "127.0.0.1:8080"
)

type options struct {
	serve         bool
	marketWatch   bool
	databasePath  string
	listenAddress string
	proxy         string
}

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, getenv func(string) string) error {
	options, err := parseOptions(args, getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var writer *store.Store
	if options.marketWatch {
		writer, err = store.Open(options.databasePath)
		if err != nil {
			return err
		}
	}
	repository, err := web.OpenSQLiteRepository(options.databasePath)
	if err != nil {
		if writer != nil {
			return errors.Join(err, writer.Close())
		}
		return err
	}
	logger := log.New(os.Stderr, "fomo-server: ", log.LstdFlags|log.LUTC)
	var controls map[domain.AssetClass]web.MarketControl
	if options.marketWatch {
		client, clientErr := providers.NewHTTPClientWithProxy(15*time.Second, options.proxy)
		if clientErr != nil {
			return errors.Join(clientErr, repository.Close(), writer.Close())
		}
		batch := providers.NewOKXMarketReader(client, "", time.Now)
		candles := providers.NewOKXCandleReader(client, "", providers.NewRequestPacer(120*time.Millisecond))
		cryptoScheduler, stockScheduler := newRadarSchedulers(batch, candles, writer, time.Now)
		cryptoScheduler.Start(ctx)
		stockScheduler.Start(ctx)
		controls = map[domain.AssetClass]web.MarketControl{
			domain.AssetClassCrypto: cryptoScheduler,
			domain.AssetClassStock:  stockScheduler,
		}
	}
	handler, err := web.NewServerWithMarketControls(repository, controls, logger)
	if err != nil {
		if writer != nil {
			return errors.Join(err, repository.Close(), writer.Close())
		}
		return errors.Join(err, repository.Close())
	}
	server := &http.Server{
		Addr:              options.listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	logger.Printf("serving dashboard on http://%s (market watch=%t)", options.listenAddress, options.marketWatch)
	closeResources := func() error {
		if writer != nil {
			return errors.Join(repository.Close(), writer.Close())
		}
		return repository.Close()
	}

	select {
	case err := <-serveErr:
		return errors.Join(normalizeServeError(err), closeResources())
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		listenErr := <-serveErr
		return errors.Join(shutdownErr, normalizeServeError(listenErr), closeResources())
	}
}

// newRadarSchedulers 复用行情输入和存储，但为两类资产保留独立候选池、缓存与调度状态。
func newRadarSchedulers(batch marketwatch.BatchReader, candles marketwatch.CandleReader, writer marketwatch.OpportunityStore, now func() time.Time) (crypto, stock *marketwatch.Scheduler) {
	cryptoScanner := marketwatch.NewScanner(batch, candles, writer, markets.DefaultUniverseConfig(), "BTC-USDT-SWAP", now)
	stockScanner := marketwatch.NewScanner(batch, candles, writer, markets.StockUniverseConfig(), "SPY-USDT-SWAP", now)
	return marketwatch.NewScheduler(cryptoScanner, "crypto", now), marketwatch.NewScheduler(stockScanner, "stock", now)
}

func normalizeServeError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func parseOptions(args []string, getenv func(string) string) (options, error) {
	defaults := options{
		databasePath:  environmentOrDefault(getenv, "FOMO_DB", defaultDatabasePath),
		listenAddress: environmentOrDefault(getenv, "FOMO_HTTP_ADDR", defaultListen),
		proxy:         environmentOrDefault(getenv, "FOMO_PROXY", ""),
	}
	flags := flag.NewFlagSet("fomo-server", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&defaults.serve, "serve", false, "serve the read-only Dashboard")
	flags.BoolVar(&defaults.marketWatch, "market-watch", false, "run the dynamic opportunity scanner")
	flags.StringVar(&defaults.databasePath, "db", defaults.databasePath, "SQLite database path")
	flags.StringVar(&defaults.listenAddress, "listen", defaults.listenAddress, "HTTP listen address")
	flags.StringVar(&defaults.proxy, "proxy", defaults.proxy, "HTTP/HTTPS proxy")
	if err := flags.Parse(args); err != nil {
		return options{}, fmt.Errorf("parse fomo-server options: %w", err)
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if !defaults.serve {
		return options{}, fmt.Errorf("-serve is required")
	}
	if defaults.databasePath == "" {
		return options{}, fmt.Errorf("database path must not be empty")
	}
	if defaults.listenAddress == "" {
		return options{}, fmt.Errorf("listen address must not be empty")
	}
	if _, err := providers.NewHTTPClientWithProxy(time.Second, defaults.proxy); err != nil {
		return options{}, fmt.Errorf("proxy: %w", err)
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
