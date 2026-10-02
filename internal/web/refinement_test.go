package web

import (
	"context"
	"database/sql"
	"html"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestDashboardSeparatesRadarFromFilteredTokenAPI(t *testing.T) {
	databasePath := seedRefinementDatabase(t)
	repository := openReader(t, databasePath)
	handler, err := NewServer(repository, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	radar := serveRequest(t, handler, http.MethodGet, "/")
	if radar.Code != http.StatusOK {
		t.Fatalf("GET / = %d: %s", radar.Code, radar.Body.String())
	}
	for _, want := range []string{"牛来", "Burn牛来", "黑马"} {
		if !strings.Contains(radar.Body.String(), want) {
			t.Fatalf("Radar missing eligible token %q: %s", want, radar.Body.String())
		}
	}
	for _, reject := range []string{"我踏马来了", "已降温"} {
		if strings.Contains(radar.Body.String(), reject) {
			t.Fatalf("Radar included non-Radar token %q: %s", reject, radar.Body.String())
		}
	}

	search := serveRequest(t, handler, http.MethodGet, "/api/tokens?q=%E7%89%9B%E6%9D%A5")
	if search.Code != http.StatusOK {
		t.Fatalf("GET /api/tokens?q=牛来 = %d: %s", search.Code, search.Body.String())
	}
	for _, want := range []string{"牛来", "Burn牛来"} {
		if !strings.Contains(search.Body.String(), want) {
			t.Fatalf("Chinese search missing %q: %s", want, search.Body.String())
		}
	}
	for _, reject := range []string{"黑马", "我踏马来了", "0xcooled"} {
		if strings.Contains(search.Body.String(), reject) {
			t.Fatalf("Chinese search included %q: %s", reject, search.Body.String())
		}
	}

	filtered := serveRequest(t, handler, http.MethodGet, "/api/tokens?chain=bsc&evidence=GOOD&data=full")
	if filtered.Code != http.StatusOK || !strings.Contains(filtered.Body.String(), "Burn牛来") {
		t.Fatalf("filtered Tokens missing GOOD BSC token: %d %s", filtered.Code, filtered.Body.String())
	}
	if strings.Contains(filtered.Body.String(), "牛来 🚀") || strings.Contains(filtered.Body.String(), "黑马") {
		t.Fatalf("filtered Tokens ignored filters: %s", filtered.Body.String())
	}
}

func TestDashboardRendersChineseMomentumNAAndEffectiveTierGate(t *testing.T) {
	databasePath := seedRefinementDatabase(t)
	repository := openReader(t, databasePath)
	handler, err := NewServer(repository, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	radar := serveRequest(t, handler, http.MethodGet, "/")
	radarHTML := html.UnescapeString(radar.Body.String())
	for _, want := range []string{
		"雷达", "主流币雷达", "SQLite · 只读", "牛来 🚀", "黑马", "5分钟成交量",
		"18 → 64", "61.00 → 81.00", "+20.00", "买卖比", "N/A",
	} {
		if !strings.Contains(radarHTML, want) {
			t.Fatalf("Radar missing %q: %s", want, radar.Body.String())
		}
	}
	if strings.Contains(radarHTML, "$0.00") {
		t.Fatalf("Radar rendered missing market evidence as zero: %s", radar.Body.String())
	}
	if strings.Contains(radarHTML, "全部代币") || strings.Contains(radarHTML, `href="/tokens"`) {
		t.Fatalf("Radar contains removed token archive navigation: %s", radar.Body.String())
	}

	low := serveRequest(t, handler, http.MethodGet, "/token/0xlow?chain=bsc")
	for _, want := range []string{"我踏马来了", "原始分", "100.00", "有效状态", "观察", "证据", "低", "2 / 5"} {
		if !strings.Contains(low.Body.String(), want) {
			t.Fatalf("Low-evidence detail missing %q: %s", want, low.Body.String())
		}
	}
	if strings.Contains(low.Body.String(), `class="tier BREAKOUT"`) {
		t.Fatalf("Low-evidence Raw 100 rendered as effective BREAKOUT: %s", low.Body.String())
	}

	api := serveRequest(t, handler, http.MethodGet, "/api/tokens?q=%E7%89%9B%E6%9D%A5")
	if !strings.Contains(api.Body.String(), "牛来") || !strings.Contains(api.Body.String(), "🚀") {
		t.Fatalf("API lost UTF-8 data: %s", api.Body.String())
	}
	if strings.Contains(api.Body.String(), `\u725b`) {
		t.Fatalf("API escaped Chinese instead of returning UTF-8: %s", api.Body.String())
	}
}

func TestDashboardShowsPersistedCoverageAndDoesNotWrite(t *testing.T) {
	databasePath := seedRefinementDatabase(t)
	before := databaseCounts(t, databasePath)
	repository := openReader(t, databasePath)
	handler, err := NewServer(repository, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	response := serveRequest(t, handler, http.MethodGet, "/")
	for _, want := range []string{"监控中", "BSC覆盖", "31分钟", "SOL覆盖", "4分钟", "Gecko", "Dex", "Deep"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("status strip missing %q: %s", want, response.Body.String())
		}
	}
	_ = serveRequest(t, handler, http.MethodGet, "/api/tokens?q=%E9%BB%91%E9%A9%AC")
	_ = serveRequest(t, handler, http.MethodGet, "/token/0xlow?chain=bsc")
	after := databaseCounts(t, databasePath)
	if before != after {
		t.Fatalf("Dashboard GET wrote database: before=%v after=%v", before, after)
	}
}

func TestDashboardStatusKeepsLastFinishedProviderHealthWhileNextWatchRunIsRunning(t *testing.T) {
	databasePath := t.TempDir() + "\\fomo.db"
	writer := openWriter(t, databasePath)
	ctx := context.Background()
	finishedRun, err := writer.StartScanRun(ctx, "watch")
	if err != nil {
		t.Fatalf("start finished run: %v", err)
	}
	if err := writer.FinishScanRun(ctx, finishedRun, "degraded", 48, 30, "discover solana: geckoterminal rate_limited"); err != nil {
		t.Fatalf("finish degraded run: %v", err)
	}
	if _, err := writer.StartScanRun(ctx, "watch"); err != nil {
		t.Fatalf("start next running watch: %v", err)
	}

	repository := openReader(t, databasePath)
	status, err := repository.DashboardStatus(ctx)
	if err != nil {
		t.Fatalf("DashboardStatus() error = %v", err)
	}
	if !status.Monitoring || status.RunStatus != "running" {
		t.Fatalf("current watch state = monitoring %v status %q, want running", status.Monitoring, status.RunStatus)
	}
	if status.GeckoStatus != "degraded" || status.DexStatus != "healthy" {
		t.Fatalf("provider status = Gecko %q Dex %q, want degraded/healthy", status.GeckoStatus, status.DexStatus)
	}
	if status.CandidatesSeen != 48 || status.CandidatesScored != 30 || status.LastScanAt == nil {
		t.Fatalf("last completed scan evidence lost: %+v", status)
	}
}

func seedRefinementDatabase(t *testing.T) string {
	t.Helper()
	databasePath := t.TempDir() + "\\fomo.db"
	writer := openWriter(t, databasePath)
	ctx := context.Background()
	base := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)

	insert := func(candidate domain.Candidate, snapshot domain.MarketSnapshot) {
		t.Helper()
		if _, err := writer.InsertFastSnapshot(ctx, candidate, snapshot); err != nil {
			t.Fatalf("insert %s: %v", candidate.ID, err)
		}
	}

	launch := testCandidate("solana:launch", domain.ChainSolana, "MintLaunch", "牛来 🚀", base)
	previous := refinementSnapshot(base, 61, domain.TierWatch, domain.EvidenceConfidenceGood, 4)
	previous.VolumeM5USD = testFloat(3200, base)
	previous.BuyersM5 = testInt(18, base)
	previous.SellersM5 = testInt(12, base)
	insert(launch, previous)
	current := refinementSnapshot(base.Add(4*time.Minute), 81, domain.TierBreakout, domain.EvidenceConfidenceHigh, 5)
	current.VolumeM5USD = testFloat(12800, current.CollectedAt)
	current.BuyersM5 = testInt(64, current.CollectedAt)
	current.SellersM5 = testInt(32, current.CollectedAt)
	insert(launch, current)

	goodWatch := testCandidate("bsc:good", domain.ChainBSC, "0xgood", "Burn牛来", base)
	insert(goodWatch, refinementSnapshot(base.Add(time.Minute), 64, domain.TierWatch, domain.EvidenceConfidenceGood, 4))
	highWatch := testCandidate("solana:high", domain.ChainSolana, "MintHigh", "黑马", base)
	insert(highWatch, refinementSnapshot(base.Add(2*time.Minute), 66, domain.TierWatch, domain.EvidenceConfidenceHigh, 5))
	lowWatch := testCandidate("bsc:low", domain.ChainBSC, "0xlow", "我踏马来了", base)
	lowSnapshot := refinementSnapshot(base.Add(3*time.Minute), 100, domain.TierWatch, domain.EvidenceConfidenceLow, 2)
	lowSnapshot.ScoreBreakdown.RawTier = domain.TierBreakout
	insert(lowWatch, lowSnapshot)
	cooled := testCandidate("bsc:cooled", domain.ChainBSC, "0xcooled", "已降温", base)
	insert(cooled, refinementSnapshot(base.Add(5*time.Minute), 20, domain.TierHidden, domain.EvidenceConfidenceLow, 2))

	runID, err := writer.StartScanRun(ctx, "watch")
	if err != nil {
		t.Fatalf("start watch run: %v", err)
	}
	scanAt := time.Now().UTC()
	for _, coverage := range []domain.DiscoveryCoverage{
		{Chain: domain.ChainBSC, ScanStartedAt: scanAt, NewestPoolAt: scanAt.Add(-time.Minute), OldestPoolAt: scanAt.Add(-32 * time.Minute), PagesFetched: 2, UniqueCandidates: 42},
		{Chain: domain.ChainSolana, ScanStartedAt: scanAt, NewestPoolAt: scanAt.Add(-30 * time.Second), OldestPoolAt: scanAt.Add(-4*time.Minute - 30*time.Second), PagesFetched: 1, UniqueCandidates: 20},
	} {
		if err := writer.InsertDiscoveryCoverage(ctx, runID, coverage); err != nil {
			t.Fatalf("insert coverage: %v", err)
		}
	}
	if err := writer.FinishScanRun(ctx, runID, "completed", 62, 5, ""); err != nil {
		t.Fatalf("finish watch run: %v", err)
	}
	return databasePath
}

func refinementSnapshot(collectedAt time.Time, rawScore float64, effectiveTier domain.Tier, confidence domain.EvidenceConfidence, available int) domain.MarketSnapshot {
	snapshot := testSnapshot(collectedAt, rawScore, effectiveTier, domain.DeepStatusCompleted, 120000)
	snapshot.FDVUSD = testFloat(160000, collectedAt)
	snapshot.VolumeM5USD = domain.DataValue[float64]{Quality: domain.QualityMissing, CollectedAt: collectedAt}
	snapshot.BuysM5 = testInt(12, collectedAt)
	snapshot.SellsM5 = testInt(6, collectedAt)
	snapshot.BuyersM5 = domain.DataValue[int]{Quality: domain.QualityMissing, CollectedAt: collectedAt}
	snapshot.SellersM5 = domain.DataValue[int]{Quality: domain.QualityMissing, CollectedAt: collectedAt}
	snapshot.ScoreBreakdown.RawScore = rawScore
	snapshot.ScoreBreakdown.RawTier = effectiveTier
	snapshot.ScoreBreakdown.EvidenceAvailable = available
	snapshot.ScoreBreakdown.EvidenceTotal = 5
	snapshot.ScoreBreakdown.EvidenceConfidence = confidence
	snapshot.ScoreBreakdown.EffectiveTier = effectiveTier
	snapshot.MarketDataSource = domain.MarketDataSourceMerged
	snapshot.MarketDataQuality = domain.MarketDataQualityFull
	return snapshot
}

func testInt(value int, collectedAt time.Time) domain.DataValue[int] {
	return domain.DataValue[int]{Value: value, Quality: domain.QualityFresh, CollectedAt: collectedAt}
}

func databaseCounts(t *testing.T, databasePath string) [3]int {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open database for counts: %v", err)
	}
	defer database.Close()
	var counts [3]int
	for index, table := range []string{"tokens", "snapshots", "scan_runs"} {
		if err := database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&counts[index]); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
	}
	return counts
}
