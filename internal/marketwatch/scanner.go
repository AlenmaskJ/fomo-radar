package marketwatch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/markets"
	"github.com/alen1/fomo-radar/internal/providers"
)

type BatchReader interface {
	Read(context.Context) (providers.OKXMarketResult, error)
}

type CandleReader interface {
	Read(context.Context, string, domain.Timeframe, int) ([]domain.Candle, error)
}

type OpportunityStore interface {
	StartOpportunityRun(context.Context, domain.AssetClass, string, time.Time) (int64, error)
	FinishOpportunityRun(context.Context, domain.OpportunityRun, []domain.Opportunity) error
	InsertDerivativesSnapshots(context.Context, []domain.DerivativesSnapshot) error
	DerivativesSnapshotAtOrBefore(context.Context, domain.AssetClass, string, time.Time) (domain.DerivativesSnapshot, bool, error)
}

type ScanRequest struct {
	Trigger         string
	RefreshUniverse bool
	Frames          []domain.Timeframe
	Force           bool
}

type ProgressFunc func(current, total int)

type Scanner struct {
	batch                 BatchReader
	candles               CandleReader
	store                 OpportunityStore
	universe              markets.UniverseConfig
	benchmarkInstrumentID string
	now                   func() time.Time

	mu          sync.Mutex
	assets      []markets.UniverseAsset
	candleCache map[string]map[domain.Timeframe][]domain.Candle
}

func NewScanner(batch BatchReader, candles CandleReader, store OpportunityStore, cfg markets.UniverseConfig, benchmarkInstrumentID string, now func() time.Time) *Scanner {
	if now == nil {
		now = time.Now
	}
	return &Scanner{batch: batch, candles: candles, store: store, universe: cfg, benchmarkInstrumentID: benchmarkInstrumentID, now: now, candleCache: make(map[string]map[domain.Timeframe][]domain.Candle)}
}

func fullScanRequest(trigger string) ScanRequest {
	return ScanRequest{Trigger: trigger, RefreshUniverse: true, Frames: allFrames(), Force: true}
}

func allFrames() []domain.Timeframe {
	return []domain.Timeframe{domain.Timeframe15m, domain.Timeframe1H, domain.Timeframe4H, domain.Timeframe1D}
}

func (s *Scanner) Scan(ctx context.Context, request ScanRequest, progress ProgressFunc) (domain.OpportunityRun, error) {
	startedAt := s.now().UTC()
	if request.Trigger == "" {
		request.Trigger = "scheduled"
	}
	run := domain.OpportunityRun{AssetClass: s.universe.AssetClass, StartedAt: startedAt, Status: domain.OpportunityRunRunning, Trigger: request.Trigger}
	id, err := s.store.StartOpportunityRun(ctx, s.universe.AssetClass, request.Trigger, startedAt)
	if err != nil {
		return run, err
	}
	run.ID = id
	fail := func(scanErr error) (domain.OpportunityRun, error) {
		run.Status = domain.OpportunityRunFailed
		run.FinishedAt = s.now().UTC()
		run.ErrorSummary = scanErr.Error()
		finishCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.store.FinishOpportunityRun(finishCtx, run, nil)
		return run, scanErr
	}

	batch, err := s.batch.Read(ctx)
	if err != nil {
		return fail(fmt.Errorf("read market batch: %w", err))
	}
	batch = marketResultForAssetClass(batch, s.universe.AssetClass)
	if err := s.store.InsertDerivativesSnapshots(ctx, batch.Snapshots); err != nil {
		return fail(fmt.Errorf("persist market snapshots: %w", err))
	}
	assets := s.selectAssets(startedAt, batch, request.RefreshUniverse)
	run.PoolSize = len(assets)
	if len(assets) == 0 {
		run.Status = domain.OpportunityRunCompleted
		run.FinishedAt = s.now().UTC()
		if err := s.store.FinishOpportunityRun(ctx, run, nil); err != nil {
			return run, err
		}
		return run, nil
	}

	framesToRefresh := normalizeFrames(request)
	assetFrames := make(map[string]map[domain.Timeframe][]domain.Candle, len(assets))
	warnings := append([]string(nil), batch.Warnings...)
	for index, asset := range assets {
		frames, readErr := s.framesForAsset(ctx, asset.InstrumentID, framesToRefresh, request.Force)
		if readErr != nil {
			warnings = appendBounded(warnings, fmt.Sprintf("%s: %v", asset.InstrumentID, readErr))
		} else {
			assetFrames[asset.InstrumentID] = frames
		}
		if progress != nil {
			progress(index+1, len(assets))
		}
	}

	benchmarkFrames := assetFrames[s.benchmarkInstrumentID]
	if benchmarkFrames == nil {
		benchmarkFrames, err = s.framesForAsset(ctx, s.benchmarkInstrumentID, framesToRefresh, request.Force)
		if err != nil {
			return fail(fmt.Errorf("read %s context: %w", s.benchmarkInstrumentID, err))
		}
	}
	poolReturns := make([]float64, 0, len(assetFrames))
	for _, frames := range assetFrames {
		poolReturns = append(poolReturns, scannerReturn(frames[domain.Timeframe1H], 4))
	}
	poolMedian := medianValue(poolReturns)
	opportunities := make([]domain.Opportunity, 0, 10)
	analyzed := 0
	for _, asset := range assets {
		frames := assetFrames[asset.InstrumentID]
		if frames == nil {
			continue
		}
		oi15 := s.oiChange(ctx, asset.Snapshot, startedAt.Add(-15*time.Minute))
		oi1H := s.oiChange(ctx, asset.Snapshot, startedAt.Add(-time.Hour))
		var funding *float64
		if asset.Snapshot.FundingRateAvailable {
			value := asset.Snapshot.FundingRate
			funding = &value
		}
		opportunity, visible, analyzeErr := markets.AnalyzeOpportunity(markets.AnalysisInput{
			Asset: asset, Candles: frames, BenchmarkCandles: benchmarkFrames, OIChange15m: oi15, OIChange1H: oi1H,
			FundingRate: funding, PoolMedianReturn1H: poolMedian, Now: startedAt,
		})
		if analyzeErr != nil {
			warnings = appendBounded(warnings, fmt.Sprintf("%s analysis: %v", asset.InstrumentID, analyzeErr))
			continue
		}
		analyzed++
		if !visible {
			continue
		}
		indicatorMap, indicatorErr := calculateFrameIndicators(frames)
		if indicatorErr == nil {
			if plan, planErr := markets.BuildTradePlan(opportunity, frames, indicatorMap); planErr == nil {
				opportunity.TradePlan = &plan
			} else if !errors.Is(planErr, markets.ErrNoTradePlan) {
				warnings = appendBounded(warnings, fmt.Sprintf("%s plan: %v", asset.InstrumentID, planErr))
			}
		}
		last15 := frames[domain.Timeframe15m][len(frames[domain.Timeframe15m])-1]
		if !last15.Confirmed && !hasConfirmedEvidence(opportunity.Evidence, domain.Timeframe15m) {
			opportunity.Action = "等待已完成K线确认"
			if opportunity.HighVolatility {
				opportunity.Action += "（高波动）"
			}
		}
		opportunities = append(opportunities, opportunity)
	}
	sort.Slice(opportunities, func(i, j int) bool {
		if opportunities[i].OpportunityScore != opportunities[j].OpportunityScore {
			return opportunities[i].OpportunityScore > opportunities[j].OpportunityScore
		}
		if opportunities[i].FOMOScore != opportunities[j].FOMOScore {
			return opportunities[i].FOMOScore > opportunities[j].FOMOScore
		}
		return opportunities[i].Instrument.InstrumentID < opportunities[j].Instrument.InstrumentID
	})
	opportunities = opportunities[:min(len(opportunities), 10)]
	run.CandidateCount = len(opportunities)
	run.FinishedAt = s.now().UTC()
	switch {
	case len(warnings) == 0:
		run.Status = domain.OpportunityRunCompleted
	case analyzed > 0:
		run.Status = domain.OpportunityRunDegraded
		run.ErrorSummary = strings.Join(warnings, "; ")
	default:
		return fail(fmt.Errorf("no asset could be analyzed: %s", strings.Join(warnings, "; ")))
	}
	if err := s.store.FinishOpportunityRun(ctx, run, opportunities); err != nil {
		return run, fmt.Errorf("publish opportunity run: %w", err)
	}
	return run, nil
}

func hasConfirmedEvidence(evidence []domain.TimeframeEvidence, timeframe domain.Timeframe) bool {
	for _, item := range evidence {
		if item.Timeframe == timeframe {
			return item.Confirmed
		}
	}
	return false
}

func (s *Scanner) RefreshSnapshots(ctx context.Context) error {
	result, err := s.batch.Read(ctx)
	if err != nil {
		return err
	}
	result = marketResultForAssetClass(result, s.universe.AssetClass)
	return s.store.InsertDerivativesSnapshots(ctx, result.Snapshots)
}

func marketResultForAssetClass(result providers.OKXMarketResult, assetClass domain.AssetClass) providers.OKXMarketResult {
	filtered := providers.OKXMarketResult{
		Snapshots:   make([]domain.DerivativesSnapshot, 0, len(result.Snapshots)),
		Instruments: make(map[string]domain.MarketInstrument),
		Warnings:    append([]string(nil), result.Warnings...),
	}
	for _, snapshot := range result.Snapshots {
		if snapshot.AssetClass != assetClass {
			continue
		}
		filtered.Snapshots = append(filtered.Snapshots, snapshot)
		if instrument, ok := result.Instruments[snapshot.InstrumentID]; ok {
			filtered.Instruments[snapshot.InstrumentID] = instrument
		}
	}
	return filtered
}

func (s *Scanner) selectAssets(now time.Time, result providers.OKXMarketResult, rebuild bool) []markets.UniverseAsset {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rebuild || len(s.assets) == 0 {
		s.assets = markets.BuildUniverse(now, result, s.universe)
		return append([]markets.UniverseAsset(nil), s.assets...)
	}
	snapshots := make(map[string]domain.DerivativesSnapshot, len(result.Snapshots))
	for _, snapshot := range result.Snapshots {
		snapshots[snapshot.InstrumentID] = snapshot
	}
	updated := make([]markets.UniverseAsset, 0, len(s.assets))
	for _, asset := range s.assets {
		if snapshot, ok := snapshots[asset.InstrumentID]; ok {
			asset.Snapshot = snapshot
			updated = append(updated, asset)
		}
	}
	s.assets = updated
	return append([]markets.UniverseAsset(nil), updated...)
}

func (s *Scanner) framesForAsset(ctx context.Context, instrumentID string, refresh []domain.Timeframe, force bool) (map[domain.Timeframe][]domain.Candle, error) {
	s.mu.Lock()
	cached := s.candleCache[instrumentID]
	if cached == nil {
		cached = make(map[domain.Timeframe][]domain.Candle, 4)
		s.candleCache[instrumentID] = cached
	}
	s.mu.Unlock()
	refreshSet := make(map[domain.Timeframe]bool, len(refresh))
	for _, timeframe := range refresh {
		refreshSet[timeframe] = true
	}
	result := make(map[domain.Timeframe][]domain.Candle, 4)
	for _, timeframe := range allFrames() {
		s.mu.Lock()
		candles := cached[timeframe]
		s.mu.Unlock()
		if force || refreshSet[timeframe] || len(candles) == 0 {
			read, err := s.candles.Read(ctx, instrumentID, timeframe, 300)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", timeframe, err)
			}
			candles = read
			s.mu.Lock()
			cached[timeframe] = read
			s.mu.Unlock()
		}
		result[timeframe] = append([]domain.Candle(nil), candles...)
	}
	return result, nil
}

func normalizeFrames(request ScanRequest) []domain.Timeframe {
	if request.Force {
		return allFrames()
	}
	if len(request.Frames) == 0 {
		return []domain.Timeframe{domain.Timeframe15m}
	}
	return request.Frames
}

func (s *Scanner) oiChange(ctx context.Context, current domain.DerivativesSnapshot, at time.Time) *float64 {
	if !current.OpenInterestAvailable || current.OpenInterestUSD <= 0 {
		return nil
	}
	baseline, ok, err := s.store.DerivativesSnapshotAtOrBefore(ctx, current.AssetClass, current.InstrumentID, at)
	if err != nil || !ok || !baseline.OpenInterestAvailable || baseline.OpenInterestUSD <= 0 {
		return nil
	}
	change := (current.OpenInterestUSD/baseline.OpenInterestUSD - 1) * 100
	return &change
}

func calculateFrameIndicators(frames map[domain.Timeframe][]domain.Candle) (map[domain.Timeframe]markets.IndicatorSeries, error) {
	result := make(map[domain.Timeframe]markets.IndicatorSeries, len(frames))
	for timeframe, candles := range frames {
		indicators, err := markets.CalculateIndicators(candles)
		if err != nil {
			return nil, err
		}
		result[timeframe] = indicators
	}
	return result, nil
}

func scannerReturn(candles []domain.Candle, periods int) float64 {
	completed := make([]domain.Candle, 0, len(candles))
	for _, candle := range candles {
		if candle.Confirmed {
			completed = append(completed, candle)
		}
	}
	if len(completed) <= periods {
		return 0
	}
	before := completed[len(completed)-1-periods].Close
	if before <= 0 {
		return 0
	}
	return (completed[len(completed)-1].Close/before - 1) * 100
}

func medianValue(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 == 0 {
		return (ordered[middle-1] + ordered[middle]) / 2
	}
	return ordered[middle]
}

func appendBounded(values []string, value string) []string {
	if len(values) >= 8 {
		return values
	}
	return append(values, value)
}
