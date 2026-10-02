package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/scanner"
)

func TestRunScanClosesStoreBeforeReturningScanFailure(t *testing.T) {
	scanErr := errors.New("snapshot write failed")
	closeErr := errors.New("close also failed")
	closer := &fakeCloser{err: closeErr}

	err := runScan(context.Background(), closer, fakeScanService{err: scanErr}, io.Discard)

	if !errors.Is(err, scanErr) {
		t.Fatalf("runScan() error = %v, want original scan error", err)
	}
	if closer.calls != 1 {
		t.Fatalf("Close() calls = %d, want 1 before runScan returns", closer.calls)
	}
}

func TestRunScanReportsCompletedDeepTasks(t *testing.T) {
	closer := &fakeCloser{}
	var output bytes.Buffer
	result := scanner.Result{
		Status: "completed", DeepCompleted: 7,
		DiscoveryCoverage:              map[domain.Chain]domain.DiscoveryCoverage{},
		EvidenceConfidenceDistribution: map[domain.EvidenceConfidence]int{},
		RawTierDistribution:            map[domain.Tier]int{},
		EffectiveTierDistribution:      map[domain.Tier]int{},
		LaunchTypeDistribution:         map[domain.LaunchType]int{},
	}
	if err := runScan(context.Background(), closer, fakeScanService{result: result}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "deep_completed=7") {
		t.Fatalf("full output = %q, want current-run Deep completion count", output.String())
	}
}

func TestValidateModeRejectsScanAndWatchTogether(t *testing.T) {
	if err := validateMode(true, true); err == nil {
		t.Fatal("validateMode() accepted mutually exclusive -scan and -watch")
	}
}

func TestRunWatchStartsSecondFastRoundWithoutWaitingForDeep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := &fakeWatchService{}
	service.scan = func(call int32) (scanner.Result, []domain.DeepTask, error) {
		if call == 2 {
			cancel()
		}
		runID := int64(call)
		return scanner.Result{ScanRunID: runID, DiscoveryCoverage: map[domain.Chain]domain.DiscoveryCoverage{}}, []domain.DeepTask{{
			TokenID: "bsc:queued", BaseFastSnapshotID: runID,
		}}, nil
	}
	queue := &fakeDeepQueue{}

	err := runWatch(ctx, service, queue, io.Discard, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("runWatch() error = %v", err)
	}
	if service.calls.Load() != 2 || queue.admitted.Load() != 2 {
		t.Fatalf("Fast calls/Deep admissions = %d/%d, want 2/2 without waiting", service.calls.Load(), queue.admitted.Load())
	}
}

func TestRunWatchNeverOverlapsSlowFastRounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := &fakeWatchService{}
	service.scan = func(call int32) (scanner.Result, []domain.DeepTask, error) {
		active := service.active.Add(1)
		for {
			maximum := service.maximum.Load()
			if active <= maximum || service.maximum.CompareAndSwap(maximum, active) {
				break
			}
		}
		time.Sleep(8 * time.Millisecond)
		service.active.Add(-1)
		if call == 3 {
			cancel()
		}
		return scanner.Result{ScanRunID: int64(call), DiscoveryCoverage: map[domain.Chain]domain.DiscoveryCoverage{}}, nil, nil
	}

	if err := runWatch(ctx, service, &fakeDeepQueue{}, io.Discard, 2*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if service.calls.Load() != 3 || service.maximum.Load() != 1 {
		t.Fatalf("Fast calls/max overlap = %d/%d, want 3/1", service.calls.Load(), service.maximum.Load())
	}
}

func TestWriteScanResultReportsActualCoverageAndHardeningMetrics(t *testing.T) {
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	result := scanner.Result{
		Status: "completed", CandidatesSeen: 7, CandidatesScored: 3,
		FastDuration: 3 * time.Second, DeepDuration: 9 * time.Second,
		DiscoveryCoverage: map[domain.Chain]domain.DiscoveryCoverage{
			domain.ChainBSC: {Chain: domain.ChainBSC, NewestPoolAt: at, OldestPoolAt: at.Add(-34 * time.Minute), PagesFetched: 3, UniqueCandidates: 7},
		},
		GeckoOnlyCandidates: 2, MergedCandidates: 1, DeepBacklog: 4,
		EvidenceConfidenceDistribution: map[domain.EvidenceConfidence]int{domain.EvidenceConfidenceLow: 2},
		RawTierDistribution:            map[domain.Tier]int{domain.TierBreakout: 2},
		EffectiveTierDistribution:      map[domain.Tier]int{domain.TierWatch: 2},
		LaunchTypeDistribution:         map[domain.LaunchType]int{domain.LaunchTypeReactivation: 1},
	}
	var output bytes.Buffer
	if err := writeScanResult(&output, "full", result, scanner.DeepQueueStats{Queued: 2, InFlight: 1, Deferred: 1}); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"stage=full", "fast_duration=3s", "deep_duration=9s", "chain=bsc", "coverage=34m0s",
		"pages=3", "gecko_only=2", "merged=1", "evidence_confidence=LOW:2", "raw_tier=BREAKOUT:2",
		"effective_tier=WATCH:2", "launch_type=REACTIVATION:1", "deep_queued=2", "deep_in_flight=1", "deep_deferred=1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "2h coverage") {
		t.Fatalf("output falsely claims configured age as coverage: %s", text)
	}
}

type fakeCloser struct {
	calls int
	err   error
}

func (f *fakeCloser) Close() error {
	f.calls++
	return f.err
}

type fakeScanService struct {
	result scanner.Result
	err    error
}

func (f fakeScanService) Scan(context.Context, scanner.Mode) (scanner.Result, error) {
	return f.result, f.err
}

type fakeWatchService struct {
	calls   atomic.Int32
	active  atomic.Int32
	maximum atomic.Int32
	scan    func(int32) (scanner.Result, []domain.DeepTask, error)
}

func (f *fakeWatchService) FastScan(context.Context, scanner.Mode) (scanner.Result, []domain.DeepTask, error) {
	call := f.calls.Add(1)
	return f.scan(call)
}

type fakeDeepQueue struct{ admitted atomic.Int32 }

func (f *fakeDeepQueue) TryEnqueue(domain.DeepTask) (scanner.DeepAdmission, error) {
	f.admitted.Add(1)
	return scanner.DeepAdmissionQueued, nil
}

func (f *fakeDeepQueue) Stats() scanner.DeepQueueStats { return scanner.DeepQueueStats{} }
