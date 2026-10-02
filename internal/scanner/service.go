package scanner

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alen1/fomo-radar/internal/config"
	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/providers"
	"github.com/alen1/fomo-radar/internal/score"
)

const workerLimit = 4

type Mode string

const ModeManual Mode = "manual"

const ModeWatch Mode = "watch"

const (
	StatusCompleted = "completed"
	StatusDegraded  = "degraded"
	StatusFailed    = "failed"
)

type CandidateResult struct {
	Candidate        domain.Candidate
	Snapshot         domain.MarketSnapshot
	PreliminaryScore PreliminaryScore
	AgeBand          AgeBand
	Deprioritized    bool
	LaunchType       domain.LaunchType
	LaunchReason     string
}

type Result struct {
	ScanRunID                      int64
	Status                         string
	CandidatesSeen                 int
	CandidatesScored               int
	Candidates                     []CandidateResult
	DegradedSources                []string
	ErrorSummary                   string
	FastDuration                   time.Duration
	DeepDuration                   time.Duration
	DiscoveryCoverage              map[domain.Chain]domain.DiscoveryCoverage
	GeckoOnlyCandidates            int
	MergedCandidates               int
	EvidenceConfidenceDistribution map[domain.EvidenceConfidence]int
	RawTierDistribution            map[domain.Tier]int
	EffectiveTierDistribution      map[domain.Tier]int
	LaunchTypeDistribution         map[domain.LaunchType]int
	DeepBacklog                    int
	DeepCompleted                  int
}

type scoreEngine interface {
	Evaluate(score.Input) domain.ScoreBreakdown
	Version() string
	ConfigJSON() ([]byte, error)
}

type scanStore interface {
	StartScanRun(context.Context, string) (int64, error)
	FinishScanRun(context.Context, int64, string, int, int, string) error
	BootstrapChampion(context.Context, string, []byte, time.Time) error
	ActiveScoreAssignments(context.Context) ([]domain.ScoreAssignment, error)
	SnapshotScoreAssignments(context.Context, int64) ([]domain.ScoreAssignment, error)
	UpsertToken(context.Context, domain.Candidate) error
	GetRecentSnapshotsBefore(context.Context, string, time.Time, int) ([]domain.MarketSnapshot, error)
	InsertDiscoveryCoverage(context.Context, int64, domain.DiscoveryCoverage) error
	InsertFastSnapshot(context.Context, domain.Candidate, domain.MarketSnapshot) (int64, error)
	InsertFastScoredSnapshot(context.Context, domain.Candidate, domain.MarketSnapshot, []domain.VersionedScore) (int64, error)
	InsertDeepSnapshotIfCurrent(context.Context, int64, domain.MarketSnapshot) (bool, error)
	InsertDeepScoredSnapshotIfCurrent(context.Context, int64, domain.MarketSnapshot, []domain.VersionedScore) (bool, error)
	IsCurrentFastSnapshot(context.Context, string, int64) (bool, error)
	SetDeepStatus(context.Context, int64, domain.DeepStatus) error
	ListPendingDeep(context.Context, int) ([]domain.DeepTask, error)
	GetTokenFirstSeen(context.Context, string) (time.Time, bool, error)
	UpsertMomentumState(context.Context, domain.MomentumState) error
	UpdateMomentumUniverseEvidence(context.Context, string, *time.Time, domain.MarketSnapshot, time.Time) error
}

type candidateCoordinator interface {
	Discover(context.Context, int64) domain.DiscoveryResult
}

type Service struct {
	cfg         config.Config
	discoverer  providers.Discoverer
	enricher    providers.Enricher
	trades      providers.TradeReader
	social      providers.SocialProvider
	engine      scoreEngine
	store       scanStore
	now         func() time.Time
	coordinator candidateCoordinator
}

func NewService(cfg config.Config, discoverer providers.Discoverer, enricher providers.Enricher, trades providers.TradeReader, social providers.SocialProvider, engine scoreEngine, database scanStore, now func() time.Time, coordinators ...candidateCoordinator) *Service {
	if now == nil {
		now = time.Now
	}
	var coordinator candidateCoordinator
	if len(coordinators) > 0 {
		coordinator = coordinators[0]
	}
	return &Service{cfg: cfg, discoverer: discoverer, enricher: enricher, trades: trades, social: social, engine: engine, store: database, now: now, coordinator: coordinator}
}

// loadActiveScoreSet 在一轮 Fast 扫描开始时读取并冻结当前角色集合。
func (s *Service) loadActiveScoreSet(ctx context.Context, changedAt time.Time) (score.Set, error) {
	configJSON, err := s.engine.ConfigJSON()
	if err != nil {
		return score.Set{}, fmt.Errorf("serialize score config %s: %w", s.engine.Version(), err)
	}
	if err := s.store.BootstrapChampion(ctx, s.engine.Version(), configJSON, changedAt); err != nil {
		return score.Set{}, fmt.Errorf("bootstrap score Champion %s: %w", s.engine.Version(), err)
	}
	assignments, err := s.store.ActiveScoreAssignments(ctx)
	if err != nil {
		return score.Set{}, fmt.Errorf("load active score assignments: %w", err)
	}
	return s.scoreSetFromAssignments(assignments)
}

// loadDeepScoreSet 从基础 Fast 快照恢复该代冻结的版本和角色。
func (s *Service) loadDeepScoreSet(ctx context.Context, task domain.DeepTask) (score.Set, error) {
	if len(task.ScoreReferences) == 0 {
		// 兼容迁移前已进入内存的旧任务。
		return score.NewSet([]score.Assignment{{Role: domain.ScoreRoleChampion, Engine: s.engine}})
	}
	assignments, err := s.store.SnapshotScoreAssignments(ctx, task.BaseFastSnapshotID)
	if err != nil {
		return score.Set{}, fmt.Errorf("load Fast snapshot %d score assignments: %w", task.BaseFastSnapshotID, err)
	}
	if len(assignments) != len(task.ScoreReferences) {
		return score.Set{}, fmt.Errorf("Fast snapshot %d score assignment count changed: got %d want %d", task.BaseFastSnapshotID, len(assignments), len(task.ScoreReferences))
	}
	for index, assignment := range assignments {
		ref := task.ScoreReferences[index]
		if assignment.Version != ref.Version || assignment.Role != ref.Role {
			return score.Set{}, fmt.Errorf("Fast snapshot %d score assignment changed at index %d", task.BaseFastSnapshotID, index)
		}
	}
	return s.scoreSetFromAssignments(assignments)
}

func (s *Service) scoreSetFromAssignments(assignments []domain.ScoreAssignment) (score.Set, error) {
	scoreAssignments := make([]score.Assignment, 0, len(assignments))
	for _, assignment := range assignments {
		var evaluator score.Evaluator
		if assignment.Version == s.engine.Version() {
			evaluator = s.engine
		} else {
			engine, err := score.ParseConfig(assignment.ConfigJSON)
			if err != nil {
				return score.Set{}, fmt.Errorf("parse score config %s: %w", assignment.Version, err)
			}
			if engine.Version() != assignment.Version {
				return score.Set{}, fmt.Errorf("score config version %s does not match registry version %s", engine.Version(), assignment.Version)
			}
			evaluator = engine
		}
		scoreAssignments = append(scoreAssignments, score.Assignment{Role: assignment.Role, Engine: evaluator})
	}
	set, err := score.NewSet(scoreAssignments)
	if err != nil {
		return score.Set{}, fmt.Errorf("build score set: %w", err)
	}
	return set, nil
}

type enrichmentResult struct {
	candidate domain.Candidate
	market    domain.MarketSnapshot
	err       error
	preMerged bool
}

type readyCandidate struct {
	candidate domain.Candidate
	market    domain.MarketSnapshot
	filter    FilterDecision
	prelim    PreliminaryScore
}

type deepResult struct {
	trades    domain.TradeWindow
	tradeOK   bool
	social    domain.SocialEvidence
	socialOK  bool
	tradeErr  error
	socialErr error
}

// FastScan persists cheap evidence and returns Deep tasks without executing them.
func (s *Service) FastScan(ctx context.Context, mode Mode) (Result, []domain.DeepTask, error) {
	started := time.Now()
	if mode != ModeManual && mode != ModeWatch {
		return Result{}, nil, fmt.Errorf("unsupported scan mode %q", mode)
	}
	runID, err := s.store.StartScanRun(ctx, string(mode))
	if err != nil {
		return Result{}, nil, fmt.Errorf("start scan run: %w", err)
	}
	result := newResult(runID)
	fail := func(err error) (Result, []domain.DeepTask, error) {
		failed, failure := s.fail(ctx, result, err)
		return failed, nil, failure
	}
	now := s.now().UTC()
	scoreSet, err := s.loadActiveScoreSet(ctx, now)
	if err != nil {
		return fail(err)
	}
	scoreReferences := scoreSet.References()

	degraded := make([]string, 0)
	discovered := make([]domain.Candidate, 0)
	if s.coordinator != nil {
		discovery := s.coordinator.Discover(ctx, runID)
		discovered = append(discovered, discovery.Candidates...)
		degraded = append(degraded, discovery.Errors...)
		for chain, coverage := range discovery.Coverages {
			result.DiscoveryCoverage[chain] = coverage
			if err := s.store.InsertDiscoveryCoverage(ctx, runID, coverage); err != nil {
				return fail(fmt.Errorf("persist discovery coverage %s: %w", chain, err))
			}
		}
	} else {
		for _, chain := range []domain.Chain{domain.ChainBSC, domain.ChainSolana} {
			batch, discoverErr := s.discoverBatch(ctx, chain, now)
			if discoverErr != nil {
				degraded = append(degraded, fmt.Sprintf("discover %s: %v", chain, discoverErr))
			} else {
				discovered = append(discovered, batch.Candidates...)
				result.DiscoveryCoverage[chain] = batch.Coverage
				if err := s.store.InsertDiscoveryCoverage(ctx, runID, batch.Coverage); err != nil {
					return fail(fmt.Errorf("persist discovery coverage %s: %w", chain, err))
				}
			}
			if ctx.Err() != nil {
				return fail(fmt.Errorf("discover candidates: %w", ctx.Err()))
			}
		}
	}

	mergedCandidates := MergeDiscoveredCandidates(discovered)
	result.CandidatesSeen = len(mergedCandidates)
	tokenStates := make(map[string]TokenState, len(mergedCandidates))
	ageFiltered := make([]domain.Candidate, 0, len(mergedCandidates))
	for _, candidate := range mergedCandidates {
		firstSeen, exists, err := s.store.GetTokenFirstSeen(ctx, candidate.ID)
		if err != nil {
			return fail(fmt.Errorf("load token first seen %s: %w", candidate.ID, err))
		}
		tokenStates[candidate.ID] = TokenState{Exists: exists, FirstSeenAt: firstSeen}
		if FastFilter(candidate, domain.MarketSnapshot{}, now, s.cfg).Accepted {
			ageFiltered = append(ageFiltered, candidate)
		} else if err := s.store.UpsertToken(ctx, candidate); err != nil {
			return fail(fmt.Errorf("persist age-rejected token %s: %w", candidate.ID, err))
		}
	}

	enriched, err := parallelMap(ctx, ageFiltered, func(_ int, candidate domain.Candidate) enrichmentResult {
		if candidate.Origin == domain.CandidateOriginMomentum ||
			(candidate.Origin == domain.CandidateOriginBoth && candidate.DiscoveryMarket.MarketDataSource != "") {
			return enrichmentResult{candidate: candidate, market: candidate.DiscoveryMarket, preMerged: true}
		}
		market, enrichErr := s.enricher.Enrich(ctx, candidate)
		return enrichmentResult{candidate: candidate, market: market, err: enrichErr}
	})
	if err != nil {
		return fail(fmt.Errorf("enrich candidates: %w", err))
	}

	ready := make([]readyCandidate, 0, len(enriched))
	for _, item := range enriched {
		dexOK := item.err == nil
		if item.err != nil {
			degraded = append(degraded, fmt.Sprintf("enrich %s: %v", item.candidate.ID, item.err))
			if !hasMarketEvidence(item.candidate.DiscoveryMarket) {
				if err := s.store.UpsertToken(ctx, item.candidate); err != nil {
					return fail(fmt.Errorf("persist unenriched token %s: %w", item.candidate.ID, err))
				}
				continue
			}
		}
		if item.preMerged {
			item.market.TokenID = item.candidate.ID
			item.market.CandidateOrigin = item.candidate.Origin
			item.market.MomentumSources = append([]domain.MomentumSource(nil), item.candidate.MomentumSources...)
			item.market.TriggerPoolAddress = item.candidate.TriggerPoolAddress
			item.market.TriggerPoolCreatedAt = item.candidate.TriggerPoolCreatedAt
			ensureParticipantSemantics(&item.market, now)
			refreshMarketQuality(&item.market)
		} else {
			item.market = MergeMarket(item.candidate, item.market, dexOK, now)
		}
		if item.market.PairAddress != "" {
			item.candidate.PairAddress = item.market.PairAddress
		}
		decision := FastFilter(item.candidate, item.market, now, s.cfg)
		if !decision.Accepted {
			if err := s.store.UpsertToken(ctx, item.candidate); err != nil {
				return fail(fmt.Errorf("persist filtered token %s: %w", item.candidate.ID, err))
			}
			continue
		}
		prelim := CalculatePreliminary(item.candidate, item.market, now)
		if decision.Deprioritized {
			prelim.Total = math.Max(0, prelim.Total-10)
		}
		ready = append(ready, readyCandidate{candidate: item.candidate, market: item.market, filter: decision, prelim: prelim})
	}

	tasks := make([]domain.DeepTask, 0, len(ready))
	for index := range ready {
		item := &ready[index]
		if item.market.CollectedAt.IsZero() {
			item.market.CollectedAt = now
		}
		item.market.DiscoveryPoolAddress = item.candidate.DiscoveryPoolAddress
		item.market.DiscoveryPoolCreatedAt = item.candidate.DiscoveryPoolCreatedAt
		item.market.CandidateOrigin = item.candidate.Origin
		item.market.MomentumSources = append([]domain.MomentumSource(nil), item.candidate.MomentumSources...)
		item.market.TriggerPoolAddress = item.candidate.TriggerPoolAddress
		item.market.TriggerPoolCreatedAt = item.candidate.TriggerPoolCreatedAt
		setTokenAgeFeature(&item.market, item.candidate, now)
		launchType, launchReason := ClassifyLaunch(item.candidate, item.market, tokenStates[item.candidate.ID], now, s.cfg)
		if item.candidate.Origin == domain.CandidateOriginMomentum {
			launchType = domain.LaunchTypeReactivation
			launchReason = "Momentum discovery without same-cycle Launch evidence"
		}
		item.market.LaunchType = launchType
		item.market.SignalFirstSeenAt = item.market.CollectedAt
		if item.candidate.Origin == domain.CandidateOriginMomentum {
			setFirstSeen(&item.candidate, item.market)
			item.market.Score = domain.DataValue[float64]{Quality: domain.QualityMissing, CollectedAt: item.market.SignalFirstSeenAt}
			item.market.Tier = ""
			item.market.ScoreVersion = ""
			item.market.ScoreBreakdown = domain.ScoreBreakdown{}
			item.market.MomentumEvidence = buildMomentumEvidence(item.market)
			item.market.ScanRunID = &runID
			item.market.Stage = domain.SnapshotStageFast
			item.market.DeepStatus = domain.DeepStatusNotRequired
			if item.market.DataQuality == nil {
				item.market.DataQuality = map[string]domain.Quality{}
			}
			item.market.DataQuality["score"] = domain.QualityMissing
			fastID, err := s.store.InsertFastSnapshot(ctx, item.candidate, item.market)
			if err != nil {
				return fail(fmt.Errorf("insert Momentum evidence snapshot (Fast) %s: %w", item.candidate.ID, err))
			}
			item.market.ID = fastID
			if err := s.store.UpsertMomentumState(ctx, momentumStateFromSnapshot(item.market)); err != nil {
				return fail(fmt.Errorf("persist Momentum baseline %s: %w", item.candidate.ID, err))
			}
			result.Candidates = append(result.Candidates, CandidateResult{
				Candidate: item.candidate, Snapshot: item.market, PreliminaryScore: item.prelim,
				AgeBand: item.filter.AgeBand, Deprioritized: item.filter.Deprioritized,
				LaunchType: launchType, LaunchReason: launchReason,
			})
			result.MarketSourceCount(item.market.MarketDataSource)
			result.LaunchTypeDistribution[launchType]++
			continue
		}
		history, err := s.store.GetRecentSnapshotsBefore(ctx, item.candidate.ID, item.market.SignalFirstSeenAt, 12)
		if err != nil {
			return fail(fmt.Errorf("load recent snapshots %s: %w", item.candidate.ID, err))
		}
		input := scoreInput(*item, history, deepResult{}, item.market.SignalFirstSeenAt)
		item.market = input.Current
		versionedScores := scoreSet.Evaluate(input)
		breakdown := versionedScores[0].Breakdown
		setFirstSeen(&item.candidate, item.market)
		item.candidate.FirstSeenScore = domain.DataValue[float64]{Value: breakdown.RawScore, Quality: domain.QualityFresh, Source: breakdown.Version, CollectedAt: item.market.SignalFirstSeenAt}
		item.market.Score = domain.DataValue[float64]{Value: breakdown.RawScore, Quality: domain.QualityFresh, Source: breakdown.Version, CollectedAt: item.market.SignalFirstSeenAt}
		item.market.Tier = string(breakdown.EffectiveTier)
		item.market.ScoreVersion = breakdown.Version
		item.market.ScoreBreakdown = breakdown
		item.market.RiskFlags = append([]string(nil), breakdown.RiskFlags...)
		item.market.ScanRunID = &runID
		item.market.Stage = domain.SnapshotStageFast
		if item.market.DataQuality == nil {
			item.market.DataQuality = map[string]domain.Quality{}
		}
		item.market.DataQuality["score"] = domain.QualityFresh
		deepEligible := item.prelim.Total >= float64(s.cfg.DeepScanThreshold)
		item.market.DeepStatus = domain.DeepStatusNotRequired
		if deepEligible {
			item.market.DeepStatus = domain.DeepStatusPending
		}
		fastID, err := s.store.InsertFastScoredSnapshot(ctx, item.candidate, item.market, versionedScores)
		if err != nil {
			return fail(fmt.Errorf("insert snapshot (Fast) %s: %w", item.candidate.ID, err))
		}
		item.market.ID = fastID
		if item.candidate.Origin == domain.CandidateOriginBoth {
			if err := s.store.UpsertMomentumState(ctx, momentumStateFromSnapshot(item.market)); err != nil {
				return fail(fmt.Errorf("persist Momentum baseline %s: %w", item.candidate.ID, err))
			}
		}
		if deepEligible {
			tasks = append(tasks, domain.DeepTask{TokenID: item.candidate.ID, BaseFastSnapshotID: fastID, Candidate: item.candidate, Snapshot: item.market, ScoreReferences: append([]domain.ScoreReference(nil), scoreReferences...)})
		}
		result.Candidates = append(result.Candidates, CandidateResult{
			Candidate: item.candidate, Snapshot: item.market, PreliminaryScore: item.prelim,
			AgeBand: item.filter.AgeBand, Deprioritized: item.filter.Deprioritized,
			LaunchType: launchType, LaunchReason: launchReason,
		})
		result.MarketSourceCount(item.market.MarketDataSource)
		result.EvidenceConfidenceDistribution[breakdown.EvidenceConfidence]++
		result.RawTierDistribution[breakdown.RawTier]++
		result.EffectiveTierDistribution[breakdown.EffectiveTier]++
		result.LaunchTypeDistribution[launchType]++
	}

	sortCandidateResults(result.Candidates)
	result.CandidatesScored = countScoredCandidates(result.Candidates)
	result.DeepBacklog = len(tasks)
	sort.Strings(degraded)
	result.DegradedSources = degraded
	result.ErrorSummary = strings.Join(degraded, "; ")
	result.Status = StatusCompleted
	if len(degraded) > 0 {
		result.Status = StatusDegraded
	}
	result.FastDuration = time.Since(started)
	if err := s.store.FinishScanRun(ctx, runID, result.Status, result.CandidatesSeen, result.CandidatesScored, result.ErrorSummary); err != nil {
		return fail(fmt.Errorf("finish Fast scan run: %w", err))
	}
	return result, tasks, nil
}

func newResult(runID int64) Result {
	return Result{
		ScanRunID: runID, Candidates: []CandidateResult{}, DegradedSources: []string{},
		DiscoveryCoverage:              map[domain.Chain]domain.DiscoveryCoverage{},
		EvidenceConfidenceDistribution: map[domain.EvidenceConfidence]int{},
		RawTierDistribution:            map[domain.Tier]int{}, EffectiveTierDistribution: map[domain.Tier]int{},
		LaunchTypeDistribution: map[domain.LaunchType]int{},
	}
}

func (s *Service) discoverBatch(ctx context.Context, chain domain.Chain, now time.Time) (domain.DiscoveryBatch, error) {
	if discoverer, ok := s.discoverer.(providers.CoverageDiscoverer); ok {
		return discoverer.DiscoverWithCoverage(ctx, chain)
	}
	candidates, err := s.discoverer.Discover(ctx, chain)
	return domain.DiscoveryBatch{
		Candidates: candidates,
		Coverage:   domain.DiscoveryCoverage{Chain: chain, ScanStartedAt: now, UniqueCandidates: len(candidates)},
	}, err
}

func (r *Result) MarketSourceCount(source domain.MarketDataSource) {
	switch source {
	case domain.MarketDataSourceGecko:
		r.GeckoOnlyCandidates++
	case domain.MarketDataSourceMerged:
		r.MergedCandidates++
	}
}

func sortCandidateResults(candidates []CandidateResult) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.Snapshot.Score.Value != right.Snapshot.Score.Value {
			return left.Snapshot.Score.Value > right.Snapshot.Score.Value
		}
		if left.Deprioritized != right.Deprioritized {
			return !left.Deprioritized
		}
		return left.Candidate.ID < right.Candidate.ID
	})
}

// ProcessDeepTask adds address-deduplicated trade evidence to one immutable Fast generation.
func (s *Service) ProcessDeepTask(ctx context.Context, task domain.DeepTask) (DeepOutcome, error) {
	deepScoreSet, err := s.loadDeepScoreSet(ctx, task)
	if err != nil {
		return DeepOutcome{}, err
	}
	item := readyCandidate{candidate: task.Candidate, market: task.Snapshot}
	decision := FastFilter(item.candidate, item.market, task.Snapshot.CollectedAt, s.cfg)
	item.filter = decision
	item.prelim = CalculatePreliminary(item.candidate, item.market, task.Snapshot.CollectedAt)
	if decision.Deprioritized {
		item.prelim.Total = math.Max(0, item.prelim.Total-10)
	}

	targetPool := item.candidate.DiscoveryPoolAddress
	if targetPool == "" {
		targetPool = item.market.DiscoveryPoolAddress
	}
	if targetPool == "" {
		targetPool = item.market.PairAddress
	}
	deep := deepResult{}
	deep.trades, deep.tradeErr = s.trades.ReadRecentTrades(ctx, item.candidate.Chain, targetPool)
	deep.tradeOK = deep.tradeErr == nil
	if ctx.Err() != nil {
		return DeepOutcome{}, ctx.Err()
	}
	deep.social, deep.socialErr = s.social.Evidence(ctx, item.candidate, item.market)
	deep.socialOK = deep.socialErr == nil
	if ctx.Err() != nil {
		return DeepOutcome{}, ctx.Err()
	}
	if deep.tradeOK && (item.candidate.Origin == domain.CandidateOriginMomentum || item.candidate.Origin == domain.CandidateOriginBoth) {
		var lastTradeAt *time.Time
		for _, trade := range deep.trades.Trades {
			if trade.BlockTimestamp.IsZero() || (lastTradeAt != nil && !trade.BlockTimestamp.After(*lastTradeAt)) {
				continue
			}
			observed := trade.BlockTimestamp.UTC()
			lastTradeAt = &observed
		}
		if lastTradeAt != nil {
			if err := s.store.UpdateMomentumUniverseEvidence(ctx, task.TokenID, lastTradeAt, item.market, deep.trades.CollectedAt); err != nil {
				return DeepOutcome{}, fmt.Errorf("update Momentum trade evidence %s: %w", task.TokenID, err)
			}
		}
	}

	signalAt := item.market.SignalFirstSeenAt
	if signalAt.IsZero() {
		signalAt = item.market.CollectedAt
	}
	preview := scoreInput(item, nil, deep, signalAt)
	signalAt = latestEvidenceTime(preview, signalAt)
	history, err := s.store.GetRecentSnapshotsBefore(ctx, task.TokenID, signalAt, 12)
	if err != nil {
		return DeepOutcome{}, fmt.Errorf("load recent snapshots %s: %w", task.TokenID, err)
	}
	input := scoreInput(item, history, deep, signalAt)
	item.market = input.Current
	item.market.TokenID = task.TokenID
	item.market.CollectedAt = signalAt
	item.market.SignalFirstSeenAt = signalAt
	item.market.Stage = domain.SnapshotStageDeep
	item.market.BaseFastSnapshotID = &task.BaseFastSnapshotID
	item.market.DeepStatus = domain.DeepStatusCompleted
	versionedScores := deepScoreSet.Evaluate(input)
	breakdown := versionedScores[0].Breakdown
	item.market.Score = domain.DataValue[float64]{Value: breakdown.RawScore, Quality: domain.QualityFresh, Source: breakdown.Version, CollectedAt: signalAt}
	item.market.Tier = string(breakdown.EffectiveTier)
	item.market.ScoreVersion = breakdown.Version
	item.market.ScoreBreakdown = breakdown
	item.market.RiskFlags = append([]string(nil), breakdown.RiskFlags...)
	if item.market.DataQuality == nil {
		item.market.DataQuality = map[string]domain.Quality{}
	}
	item.market.DataQuality["score"] = domain.QualityFresh

	inserted, err := s.store.InsertDeepScoredSnapshotIfCurrent(ctx, task.BaseFastSnapshotID, item.market, versionedScores)
	if err != nil {
		return DeepOutcome{}, fmt.Errorf("insert Deep snapshot %s: %w", task.TokenID, err)
	}
	degraded := make([]string, 0, 2)
	if deep.tradeErr != nil {
		degraded = append(degraded, fmt.Sprintf("trades %s: %v", task.TokenID, deep.tradeErr))
	}
	if deep.socialErr != nil {
		degraded = append(degraded, fmt.Sprintf("social %s: %v", task.TokenID, deep.socialErr))
	}
	sort.Strings(degraded)
	return DeepOutcome{
		Result: CandidateResult{
			Candidate: item.candidate, Snapshot: item.market, PreliminaryScore: item.prelim,
			AgeBand: decision.AgeBand, Deprioritized: decision.Deprioritized,
			LaunchType: item.market.LaunchType,
		},
		DegradedSources: degraded,
		Inserted:        inserted,
	}, nil
}

// Scan preserves the manual full-scan contract by waiting for every Deep task from this Fast run.
func (s *Service) Scan(ctx context.Context, mode Mode) (Result, error) {
	if mode != ModeManual {
		return Result{}, fmt.Errorf("unsupported scan mode %q", mode)
	}
	queue := NewDeepQueue(s, s.store, workerLimit, 64)
	if err := queue.Start(ctx); err != nil {
		return Result{}, err
	}
	defer queue.Close() //nolint:errcheck

	result, tasks, err := s.FastScan(ctx, mode)
	if err != nil {
		return result, err
	}
	deepStarted := time.Now()
	for _, task := range tasks {
		if err := queue.Enqueue(ctx, task); err != nil {
			return s.fail(ctx, result, fmt.Errorf("enqueue Deep task %s: %w", task.TokenID, err))
		}
	}
	outcomes, err := queue.WaitRun(ctx, result.ScanRunID)
	if err != nil {
		return s.fail(ctx, result, fmt.Errorf("complete Deep scan: %w", err))
	}
	result.DeepDuration = time.Since(deepStarted)
	for _, outcome := range outcomes {
		if outcome.Inserted {
			result.DeepCompleted++
		}
	}
	mergeDeepOutcomes(&result, outcomes)
	stats := queue.Stats()
	result.DeepBacklog = stats.Queued + stats.InFlight + stats.Deferred
	result.Status = StatusCompleted
	if len(result.DegradedSources) > 0 {
		result.Status = StatusDegraded
	}
	result.ErrorSummary = strings.Join(result.DegradedSources, "; ")
	if err := s.store.FinishScanRun(ctx, result.ScanRunID, result.Status, result.CandidatesSeen, result.CandidatesScored, result.ErrorSummary); err != nil {
		return s.fail(ctx, result, fmt.Errorf("finish scan run: %w", err))
	}
	return result, nil
}

func mergeDeepOutcomes(result *Result, outcomes []DeepOutcome) {
	byToken := make(map[string]int, len(result.Candidates))
	for index := range result.Candidates {
		byToken[result.Candidates[index].Candidate.ID] = index
	}
	for _, outcome := range outcomes {
		result.DegradedSources = append(result.DegradedSources, outcome.DegradedSources...)
		if !outcome.Inserted {
			continue
		}
		if index, ok := byToken[outcome.Result.Candidate.ID]; ok {
			launchReason := result.Candidates[index].LaunchReason
			result.Candidates[index] = outcome.Result
			result.Candidates[index].LaunchReason = launchReason
		}
	}
	sort.Strings(result.DegradedSources)
	sortCandidateResults(result.Candidates)
	result.CandidatesScored = countScoredCandidates(result.Candidates)
	result.EvidenceConfidenceDistribution = map[domain.EvidenceConfidence]int{}
	result.RawTierDistribution = map[domain.Tier]int{}
	result.EffectiveTierDistribution = map[domain.Tier]int{}
	result.LaunchTypeDistribution = map[domain.LaunchType]int{}
	for _, candidate := range result.Candidates {
		breakdown := candidate.Snapshot.ScoreBreakdown
		if dataAvailable(candidate.Snapshot.Score.Quality) {
			result.EvidenceConfidenceDistribution[breakdown.EvidenceConfidence]++
			result.RawTierDistribution[breakdown.RawTier]++
			result.EffectiveTierDistribution[breakdown.EffectiveTier]++
		}
		result.LaunchTypeDistribution[candidate.LaunchType]++
	}
}

func (s *Service) fail(ctx context.Context, result Result, original error) (Result, error) {
	result.Status = StatusFailed
	result.ErrorSummary = original.Error()
	_ = s.store.FinishScanRun(context.WithoutCancel(ctx), result.ScanRunID, StatusFailed, result.CandidatesSeen, len(result.Candidates), result.ErrorSummary)
	return result, original
}

func deduplicate(candidates []domain.Candidate) []domain.Candidate {
	seen := make(map[string]struct{}, len(candidates))
	result := make([]domain.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.ID = TokenID(candidate.Chain, candidate.Address)
		if candidate.Chain == domain.ChainBSC {
			candidate.Address = strings.ToLower(candidate.Address)
			candidate.PairAddress = strings.ToLower(candidate.PairAddress)
		}
		if _, duplicate := seen[candidate.ID]; duplicate {
			continue
		}
		seen[candidate.ID] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

func setFirstSeen(candidate *domain.Candidate, market domain.MarketSnapshot) {
	if candidate.DetectedAt.IsZero() {
		candidate.DetectedAt = market.CollectedAt
	}
	candidate.FirstSeenPriceUSD = market.PriceUSD
	candidate.FirstSeenMarketCapUSD = market.MarketCapUSD
}

func countScoredCandidates(candidates []CandidateResult) int {
	count := 0
	for _, candidate := range candidates {
		if dataAvailable(candidate.Snapshot.Score.Quality) {
			count++
		}
	}
	return count
}

func setTokenAgeFeature(market *domain.MarketSnapshot, candidate domain.Candidate, now time.Time) {
	if dataAvailable(market.TokenAgeSeconds.Quality) && market.TokenAgeSource != "" {
		if market.DataQuality == nil {
			market.DataQuality = map[string]domain.Quality{}
		}
		market.DataQuality["token_age_seconds"] = market.TokenAgeSeconds.Quality
		return
	}
	age, ok := tokenAge(candidate, *market, now)
	if !ok {
		market.TokenAgeSeconds = domain.DataValue[int64]{Quality: domain.QualityMissing, CollectedAt: now}
		return
	}
	market.TokenAgeSeconds = domain.DataValue[int64]{Value: int64(age / time.Second), Quality: domain.QualityFresh, CollectedAt: now}
	switch {
	case candidate.ChainCreatedAt != nil:
		market.TokenAgeSource = domain.TokenAgeSourceOnChain
	case candidate.DiscoveryPoolCreatedAt != nil || candidate.TriggerPoolCreatedAt != nil || market.SelectedPairCreatedAt != nil || market.PairCreatedAt != nil:
		market.TokenAgeSource = domain.TokenAgeSourceEarliestPair
	default:
		market.TokenAgeSource = domain.TokenAgeSourceLocalFirstSeen
	}
	if market.DataQuality == nil {
		market.DataQuality = map[string]domain.Quality{}
	}
	market.DataQuality["token_age_seconds"] = domain.QualityFresh
}

func buildMomentumEvidence(market domain.MarketSnapshot) *domain.MomentumEvidence {
	at := market.CollectedAt
	factors := map[string]domain.MomentumFactorEvidence{
		"price_momentum":           floatMomentumFactor(market.PriceChangeH1, "percent", "h1", at),
		"volume_activity":          floatMomentumFactor(market.VolumeH1USD, "usd", "h1", at),
		"buy_pressure":             unavailableMomentumFactor("ratio", "h1", at),
		"aggregate_buyer_activity": intMomentumFactor(market.AggregateBuyersH1, "addresses_aggregate", "h1", at),
		"liquidity":                floatMomentumFactor(market.LiquidityUSD, "usd", "current", at),
		"social":                   unavailableMomentumFactor("count", "current", at),
	}
	if !factors["price_momentum"].Available {
		factors["price_momentum"] = floatMomentumFactor(market.PriceChangeM5, "percent", "m5", at)
	}
	if !factors["volume_activity"].Available {
		factors["volume_activity"] = floatMomentumFactor(market.Volume24hUSD, "usd", "h24", at)
	}
	if !factors["aggregate_buyer_activity"].Available {
		factors["aggregate_buyer_activity"] = intMomentumFactor(market.AggregateBuyersM5, "addresses_aggregate", "m5", at)
	}
	buys, sells, window := market.BuysH1, market.SellsH1, "h1"
	if !dataAvailable(buys.Quality) || !dataAvailable(sells.Quality) {
		buys, sells, window = market.BuysM5, market.SellsM5, "m5"
	}
	// 零卖出时比值无定义；保留原始买卖计数，但不要把 +Inf 写进 JSON 证据。
	if dataAvailable(buys.Quality) && dataAvailable(sells.Quality) && sells.Value > 0 {
		factors["buy_pressure"] = domain.MomentumFactorEvidence{
			Available: true, Value: ratio(float64(buys.Value), float64(sells.Value)), Unit: "ratio",
			Source: buys.Source, Window: window, ObservedAt: observedAt(buys.CollectedAt, at),
		}
	}
	return &domain.MomentumEvidence{Version: "MOMENTUM_EVIDENCE_V1", Factors: factors}
}

func floatMomentumFactor(value domain.DataValue[float64], unit, window string, fallback time.Time) domain.MomentumFactorEvidence {
	if !dataAvailable(value.Quality) {
		return unavailableMomentumFactor(unit, window, fallback)
	}
	return domain.MomentumFactorEvidence{
		Available: true, Value: value.Value, Unit: unit, Source: value.Source,
		Window: window, ObservedAt: observedAt(value.CollectedAt, fallback),
	}
}

func intMomentumFactor(value domain.DataValue[int], unit, window string, fallback time.Time) domain.MomentumFactorEvidence {
	if !dataAvailable(value.Quality) {
		return unavailableMomentumFactor(unit, window, fallback)
	}
	return domain.MomentumFactorEvidence{
		Available: true, Value: float64(value.Value), Unit: unit, Source: value.Source,
		Window: window, ObservedAt: observedAt(value.CollectedAt, fallback),
	}
}

func unavailableMomentumFactor(unit, window string, at time.Time) domain.MomentumFactorEvidence {
	return domain.MomentumFactorEvidence{Available: false, Unit: unit, Window: window, ObservedAt: at}
}

func observedAt(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value
}

func momentumStateFromSnapshot(market domain.MarketSnapshot) domain.MomentumState {
	return domain.MomentumState{
		TokenID: market.TokenID, ObservedAt: market.CollectedAt, PairAddress: market.PairAddress,
		PriceUSD: market.PriceUSD, LiquidityUSD: market.LiquidityUSD,
		VolumeM5USD: market.VolumeM5USD, VolumeH1USD: market.VolumeH1USD,
		VolumeH6USD: market.VolumeH6USD, VolumeH24USD: market.Volume24hUSD,
		AggregateBuyersM5: market.AggregateBuyersM5, AggregateBuyersH1: market.AggregateBuyersH1,
		Sources: append([]domain.MomentumSource(nil), market.MomentumSources...),
	}
}

func latestEvidenceTime(input score.Input, fallback time.Time) time.Time {
	latest := fallback
	consider := func(at time.Time, quality domain.Quality) {
		if dataAvailable(quality) && !at.IsZero() && at.After(latest) {
			latest = at
		}
	}
	considerFloat := func(value domain.DataValue[float64]) { consider(value.CollectedAt, value.Quality) }
	considerInt := func(value domain.DataValue[int]) { consider(value.CollectedAt, value.Quality) }
	considerBool := func(value domain.DataValue[bool]) { consider(value.CollectedAt, value.Quality) }

	current := input.Current
	for _, value := range []domain.DataValue[float64]{
		current.PriceUSD, current.MarketCapUSD, current.FDVUSD, current.LiquidityUSD,
		current.VolumeM5USD, current.VolumeH1USD, current.Volume24hUSD,
		current.PriceChangeM5, input.BuyVolumeM5USD, input.SellVolumeM5USD,
		input.BuyerVelocity, input.VolumeVelocity, input.ParticipantGrowth,
		input.SocialMomentum, input.Top10HolderPercent,
	} {
		considerFloat(value)
	}
	for _, value := range []domain.DataValue[int]{
		current.BuysM5, current.SellsM5, current.BuyersM5, current.SellersM5,
		current.Holders, current.BoostsActive,
	} {
		considerInt(value)
	}
	consider(input.TokenAge.CollectedAt, input.TokenAge.Quality)
	for _, value := range []domain.DataValue[bool]{input.ConcentratedActivity, input.SuspectedBotActivity, input.StrongDevSelling} {
		considerBool(value)
	}
	return latest
}

func scoreInput(item readyCandidate, history []domain.MarketSnapshot, deep deepResult, now time.Time) score.Input {
	input := score.Input{
		Current:              item.market,
		Previous:             history,
		BuyerVelocity:        missingFloatValue("geckoterminal", now),
		VolumeVelocity:       missingFloatValue("geckoterminal", now),
		BuyVolumeM5USD:       missingFloatValue("geckoterminal", now),
		SellVolumeM5USD:      missingFloatValue("geckoterminal", now),
		ParticipantGrowth:    missingFloatValue("geckoterminal", now),
		SocialMomentum:       missingFloatValue("social", now),
		TokenAge:             missingDurationValue(now),
		Top10HolderPercent:   missingFloatValue("", now),
		ConcentratedActivity: missingBoolValue(now),
		SuspectedBotActivity: missingBoolValue(now),
		StrongDevSelling:     missingBoolValue(now),
	}
	if age, ok := tokenAge(item.candidate, item.market, now); ok {
		source := "selected_pair_created_at"
		if item.candidate.DiscoveryPoolCreatedAt != nil || item.market.DiscoveryPoolCreatedAt != nil {
			source = "discovery_pool_created_at"
		} else if item.candidate.ChainCreatedAt != nil {
			source = "chain_created_at"
		}
		input.TokenAge = domain.DataValue[time.Duration]{Value: age, Quality: domain.QualityFresh, Source: source, CollectedAt: now}
	}
	if deep.tradeOK {
		quality := domain.QualityFresh
		if deep.trades.Truncated {
			quality = domain.QualityDegraded
		}
		input.Current.BuyersM5 = domain.DataValue[int]{Value: deep.trades.Current.UniqueBuyers, Quality: quality, Source: "geckoterminal", CollectedAt: deep.trades.CollectedAt}
		input.Current.SellersM5 = domain.DataValue[int]{Value: deep.trades.Current.UniqueSellers, Quality: quality, Source: "geckoterminal", CollectedAt: deep.trades.CollectedAt}
		input.BuyVolumeM5USD = domain.DataValue[float64]{Value: deep.trades.Current.BuyVolumeUSD, Quality: quality, Source: "geckoterminal", CollectedAt: deep.trades.CollectedAt}
		input.SellVolumeM5USD = domain.DataValue[float64]{Value: deep.trades.Current.SellVolumeUSD, Quality: quality, Source: "geckoterminal", CollectedAt: deep.trades.CollectedAt}
		input.BuyerVelocity = domain.DataValue[float64]{Value: ratio(float64(deep.trades.Current.UniqueBuyers), float64(deep.trades.Previous.UniqueBuyers)), Quality: quality, Source: "geckoterminal", CollectedAt: deep.trades.CollectedAt}
		input.VolumeVelocity = domain.DataValue[float64]{
			Value: ratio(
				deep.trades.Current.BuyVolumeUSD+deep.trades.Current.SellVolumeUSD,
				deep.trades.Previous.BuyVolumeUSD+deep.trades.Previous.SellVolumeUSD,
			),
			Quality: quality, Source: "geckoterminal", CollectedAt: deep.trades.CollectedAt,
		}
		input.ParticipantGrowth = domain.DataValue[float64]{Value: ratio(float64(deep.trades.Current.UniqueBuyers), float64(deep.trades.Previous.UniqueBuyers)), Quality: quality, Source: "geckoterminal", CollectedAt: deep.trades.CollectedAt}
		input.ConcentratedActivity = domain.DataValue[bool]{Value: deep.trades.Current.TopAddressVolumeShare > .5, Quality: quality, Source: "geckoterminal", CollectedAt: deep.trades.CollectedAt}
		if input.Current.DataQuality == nil {
			input.Current.DataQuality = map[string]domain.Quality{}
		}
		input.Current.DataQuality["buyers_m5"] = quality
		input.Current.DataQuality["sellers_m5"] = quality
	}
	if deep.socialOK && dataAvailable(deep.social.MentionCount.Quality) {
		input.SocialMomentum = domain.DataValue[float64]{Value: float64(deep.social.MentionCount.Value), Quality: deep.social.MentionCount.Quality, Source: deep.social.MentionCount.Source, SourceTime: deep.social.MentionCount.SourceTime, CollectedAt: deep.social.CollectedAt}
	}
	return input
}

func missingFloatValue(source string, at time.Time) domain.DataValue[float64] {
	return domain.DataValue[float64]{Quality: domain.QualityMissing, Source: source, CollectedAt: at}
}

func missingDurationValue(at time.Time) domain.DataValue[time.Duration] {
	return domain.DataValue[time.Duration]{Quality: domain.QualityMissing, CollectedAt: at}
}

func missingBoolValue(at time.Time) domain.DataValue[bool] {
	return domain.DataValue[bool]{Quality: domain.QualityMissing, CollectedAt: at}
}

func parallelMap[I any, O any](ctx context.Context, inputs []I, work func(int, I) O) ([]O, error) {
	outputs := make([]O, len(inputs))
	jobs := make(chan int)
	var workers sync.WaitGroup
	workers.Add(workerLimit)
	for worker := 0; worker < workerLimit; worker++ {
		go func() {
			defer workers.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					continue
				}
				outputs[index] = work(index, inputs[index])
			}
		}()
	}
	for index := range inputs {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return nil, ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return outputs, nil
}
