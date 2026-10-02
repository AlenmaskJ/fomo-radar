package lab

import (
	"context"
	"testing"
	"time"
)

func TestAnalysisSeparatesSignalSourceChainAndEvidence(t *testing.T) {
	repo := openTestRepository(t)
	initializeAnalysisState(t, repo)
	now := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	seedAnalysisSignal(t, repo, analysisSeed{TokenID: "bsc:1", Chain: "bsc", Source: "BACKFILL", Evidence: "HIGH", Status: "MATURED", Return: floatPtr(.1)})
	seedAnalysisSignal(t, repo, analysisSeed{TokenID: "solana:1", Chain: "solana", Source: "BACKFILL", Evidence: "LOW", Status: "MATURED", Return: floatPtr(.2)})
	seedAnalysisSignal(t, repo, analysisSeed{TokenID: "bsc:2", Chain: "bsc", Source: "LIVE", Evidence: "GOOD", Status: "MATURED", Return: floatPtr(.3)})
	seedAnalysisSignal(t, repo, analysisSeed{TokenID: "solana:2", Chain: "solana", Source: "LIVE", Evidence: "", Status: "MATURED", Return: floatPtr(-.1)})

	analyzer := NewAnalyzer(repo, func() time.Time { return now })
	if _, err := analyzer.Materialize(context.Background()); err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	report, err := repo.LatestAnalysis(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertStatisticSignals(t, report, "BACKFILL", "ALL", "ALL", 55, "5m", 2)
	assertStatisticSignals(t, report, "LIVE", "ALL", "ALL", 55, "5m", 2)
	assertStatisticSignals(t, report, "ALL", "ALL", "ALL", 55, "5m", 4)
	assertStatisticSignals(t, report, "ALL", "BSC", "ALL", 55, "5m", 2)
	assertStatisticSignals(t, report, "ALL", "SOLANA", "ALL", 55, "5m", 2)
	assertStatisticSignals(t, report, "ALL", "ALL", "HIGH_GOOD", 55, "5m", 2)
	assertStatisticSignals(t, report, "ALL", "ALL", "MEDIUM_LOW", 55, "5m", 1)
	assertStatisticSignals(t, report, "ALL", "ALL", "UNKNOWN", 55, "5m", 1)
}

func TestThresholdStatisticsUseApprovedDenominatorsAndMedian(t *testing.T) {
	repo := openTestRepository(t)
	initializeAnalysisState(t, repo)
	returns := []*float64{floatPtr(-.10), floatPtr(.10), floatPtr(.20), floatPtr(.40)}
	mfes := []*float64{floatPtr(0), floatPtr(.20), floatPtr(.40), floatPtr(.60)}
	maes := []*float64{floatPtr(-.30), floatPtr(-.20), floatPtr(-.10), floatPtr(0)}
	for i := 0; i < 10; i++ {
		seed := analysisSeed{TokenID: "bsc:denominator:" + string(rune('a'+i)), Chain: "bsc", Source: "BACKFILL", Evidence: "HIGH"}
		switch {
		case i < 2:
			seed.EntryStatus = "UNAVAILABLE_PRICE"
			seed.Status = "NOT_EVALUABLE"
		case i < 4:
			seed.Status = "PENDING"
		case i < 8:
			index := i - 4
			seed.Status = "MATURED"
			seed.Return, seed.MFE, seed.MAE = returns[index], mfes[index], maes[index]
		default:
			seed.Status = "INSUFFICIENT_DATA"
		}
		seedAnalysisSignal(t, repo, seed)
	}
	analyzer := NewAnalyzer(repo, func() time.Time { return time.Date(2026, time.August, 22, 13, 0, 0, 0, time.UTC) })
	if _, err := analyzer.Materialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := repo.LatestAnalysis(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stat := findStatistic(t, report, "BACKFILL", "BSC", "HIGH_GOOD", 55, "5m")
	if stat.Signals != 10 || stat.EvaluableEntries != 8 || stat.NotEvaluable != 2 || stat.Pending != 2 || stat.Matured != 4 || stat.InsufficientData != 2 {
		t.Fatalf("counts = %+v", stat)
	}
	assertPointerDecimal(t, "entry coverage", stat.EntryPriceCoverage, .8)
	assertPointerDecimal(t, "maturity coverage", stat.MaturityCoverage, .75)
	assertPointerDecimal(t, "result coverage", stat.ResultCoverage, 4.0/6.0)
	assertPointerDecimal(t, "positive rate", stat.PositiveReturnRate, .75)
	assertPointerDecimal(t, "median return", stat.MedianReturn, .15)
	assertPointerDecimal(t, "median MFE", stat.MedianMFE, .30)
	assertPointerDecimal(t, "median MAE", stat.MedianMAE, -.15)
	if !stat.InsufficientSample {
		t.Fatal("4 matured observations not marked insufficient")
	}
}

func TestTwentyMaturedResultsClearInsufficientSample(t *testing.T) {
	repo := openTestRepository(t)
	initializeAnalysisState(t, repo)
	for i := 0; i < 20; i++ {
		seedAnalysisSignal(t, repo, analysisSeed{TokenID: "bsc:twenty:" + string(rune('a'+i)), Chain: "bsc", Source: "LIVE", Evidence: "GOOD", Status: "MATURED", Return: floatPtr(.01)})
	}
	if _, err := NewAnalyzer(repo, time.Now).Materialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, _ := repo.LatestAnalysis(context.Background())
	stat := findStatistic(t, report, "LIVE", "BSC", "HIGH_GOOD", 55, "5m")
	if stat.InsufficientSample || stat.Matured != 20 {
		t.Fatalf("20-sample statistic = %+v", stat)
	}
}

func TestAnalysisExcludesSignalsAbovePersistedHighWatermark(t *testing.T) {
	repo := openTestRepository(t)
	initializeAnalysisState(t, repo)
	seedAnalysisSignal(t, repo, analysisSeed{TokenID: "bsc:included", Chain: "bsc", Source: "BACKFILL", Evidence: "HIGH", SnapshotID: 99, EvaluationHighWatermark: 101})
	seedAnalysisSignal(t, repo, analysisSeed{TokenID: "bsc:uncommitted-cycle", Chain: "bsc", Source: "LIVE", Evidence: "HIGH", SnapshotID: 101})

	run, err := NewAnalyzer(repo, time.Now).Materialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.PersistedThroughSnapshotID != 100 {
		t.Fatalf("persisted watermark = %d, want 100", run.PersistedThroughSnapshotID)
	}
	report, err := repo.LatestAnalysis(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertStatisticSignals(t, report, "ALL", "ALL", "ALL", 55, "5m", 1)
	assertStatisticSignals(t, report, "LIVE", "ALL", "ALL", 55, "5m", 0)
	stat := findStatistic(t, report, "ALL", "ALL", "ALL", 55, "5m")
	if stat.Pending != 1 || stat.Matured != 0 {
		t.Fatalf("outcome beyond persisted watermark = %+v, want PENDING", stat)
	}
}

func TestLatestComparisonFiltersExistingStatisticsByVersion(t *testing.T) {
	repo := openTestRepository(t)
	initializeAnalysisState(t, repo)
	seedAnalysisSignal(t, repo, analysisSeed{TokenID: "bsc:v1", Chain: "bsc", Source: "LIVE", Evidence: "HIGH", ScoreVersion: "V1", Status: "PENDING"})
	seedAnalysisSignal(t, repo, analysisSeed{TokenID: "bsc:v2", Chain: "bsc", Source: "LIVE", Evidence: "HIGH", ScoreVersion: "V2", Status: "PENDING"})
	if _, err := NewAnalyzer(repo, time.Now).Materialize(context.Background()); err != nil {
		t.Fatal(err)
	}

	comparison, err := repo.LatestComparison(context.Background(), []string{"V2"})
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Run.ID == 0 || len(comparison.Statistics) == 0 {
		t.Fatalf("comparison = %+v", comparison)
	}
	for _, statistic := range comparison.Statistics {
		if statistic.ScoreVersion != "V2" {
			t.Fatalf("comparison leaked score version %q", statistic.ScoreVersion)
		}
		if statistic.MedianReturn != nil {
			t.Fatalf("pending-only median = %v, want nil", *statistic.MedianReturn)
		}
	}
}

type analysisSeed struct {
	TokenID                 string
	Chain                   string
	Source                  string
	Evidence                string
	SnapshotID              int64
	EvaluationHighWatermark int64
	EntryStatus             string
	Status                  string
	Return                  *float64
	MFE                     *float64
	MAE                     *float64
	ScoreVersion            string
}

func initializeAnalysisState(t *testing.T, repo *Repository) {
	t.Helper()
	_, err := repo.InitializeOrValidate(context.Background(), SourceRegistration{
		CanonicalPath: `C:\data\fomo.db`, Identity: "fixture", ActivationSnapshotID: 100,
		CurrentSourceMaximum: 100, InitializedAt: time.Date(2026, time.August, 22, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	frontier := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	if err := repo.RecordCycleFrontier(context.Background(), SourceFrontier{HighWatermark: 100, ObservationAt: &frontier}, frontier); err != nil {
		t.Fatal(err)
	}
}

func seedAnalysisSignal(t *testing.T, repo *Repository, seed analysisSeed) {
	t.Helper()
	if seed.ScoreVersion == "" {
		seed.ScoreVersion = "FOMO_SCORE_V1.0"
	}
	if seed.EntryStatus == "" {
		seed.EntryStatus = "EVALUABLE"
	}
	if seed.Status == "" {
		seed.Status = "MATURED"
	}
	if seed.Status == "MATURED" {
		if seed.Return == nil {
			seed.Return = floatPtr(0)
		}
		if seed.MFE == nil {
			value := 0.0
			if *seed.Return > 0 {
				value = *seed.Return
			}
			seed.MFE = floatPtr(value)
		}
		if seed.MAE == nil {
			value := 0.0
			if *seed.Return < 0 {
				value = *seed.Return
			}
			seed.MAE = floatPtr(value)
		}
	}
	if seed.EvaluationHighWatermark == 0 {
		seed.EvaluationHighWatermark = 100
	}
	now := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC).UnixMilli()
	var snapshotID int64
	if seed.SnapshotID != 0 {
		snapshotID = seed.SnapshotID
	} else {
		if err := repo.db.QueryRow(`SELECT COALESCE(MAX(signal_snapshot_id),0)+1 FROM signals`).Scan(&snapshotID); err != nil {
			t.Fatal(err)
		}
	}
	result, err := repo.db.Exec(`
		INSERT INTO signals
		(token_id,chain,address,name,symbol,signal_source,threshold,signal_snapshot_id,signal_at,
		 raw_score,raw_tier,effective_tier,evidence_confidence,evidence_available,evidence_total,
		 score_version,score_config_json,score_breakdown_json,risk_flags_json,data_quality_json,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,55,'WATCH','WATCH',?,5,5,?,'{}','{}','[]','{}',?)`,
		seed.TokenID, seed.Chain, seed.TokenID, "牛来", "🐂", seed.Source, 55, snapshotID, now, seed.Evidence, seed.ScoreVersion, now)
	if err != nil {
		t.Fatal(err)
	}
	signalID, _ := result.LastInsertId()
	var entryPrice any
	if seed.EntryStatus == "EVALUABLE" {
		entryPrice = 1.0
	}
	if _, err := repo.db.Exec(`
		INSERT INTO paper_entries(signal_id,entry_snapshot_id,entry_at,entry_status,entry_price_usd,created_at)
		VALUES (?,?,?,?,?,?)`, signalID, snapshotID, now, seed.EntryStatus, entryPrice, now); err != nil {
		t.Fatal(err)
	}
	finalized := any(nil)
	if seed.Status != "PENDING" {
		finalized = now + 500000
	}
	if _, err := repo.db.Exec(`
		INSERT INTO horizon_outcomes
		(signal_id,horizon,target_at,tolerance_seconds,window_end_at,status,status_reason,
		 return_decimal,mfe_decimal,mae_decimal,evaluation_source_high_watermark,
		 source_observation_frontier_at,finalized_at,created_at)
		VALUES (?,'5m',?,180,?,?,?,?,?,?,?,?,?,?)`,
		signalID, now+300000, now+480000, seed.Status, "", floatOrNil(seed.Return), floatOrNil(seed.MFE), floatOrNil(seed.MAE), seed.EvaluationHighWatermark, now+500000, finalized, now); err != nil {
		t.Fatal(err)
	}
}

func assertStatisticSignals(t *testing.T, report ThresholdReport, source, chain, evidence string, threshold int, horizon string, want int) {
	t.Helper()
	stat := findStatistic(t, report, source, chain, evidence, threshold, horizon)
	if stat.Signals != want {
		t.Fatalf("%s/%s/%s/%d/%s signals = %d, want %d", source, chain, evidence, threshold, horizon, stat.Signals, want)
	}
}

func findStatistic(t *testing.T, report ThresholdReport, source, chain, evidence string, threshold int, horizon string) ThresholdStatistic {
	t.Helper()
	for _, stat := range report.Statistics {
		if stat.SignalSourceScope == source && stat.ChainScope == chain && stat.EvidenceScope == evidence && stat.Threshold == threshold && stat.Horizon == horizon && stat.ScoreVersion == "FOMO_SCORE_V1.0" {
			return stat
		}
	}
	t.Fatalf("statistic not found: %s/%s/%s/%d/%s", source, chain, evidence, threshold, horizon)
	return ThresholdStatistic{}
}

func assertPointerDecimal(t *testing.T, label string, got *float64, want float64) {
	t.Helper()
	if got == nil || *got < want-1e-9 || *got > want+1e-9 {
		t.Fatalf("%s = %v, want %.9f", label, got, want)
	}
}
