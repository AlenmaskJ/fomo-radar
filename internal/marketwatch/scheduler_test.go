package marketwatch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestSchedulerDeduplicatesManualAndScheduledScan(t *testing.T) {
	now := time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC)
	fake := newBlockingScanRunner()
	scheduler := NewScheduler(fake, "crypto", func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	select {
	case <-fake.started:
	case <-time.After(time.Second):
		t.Fatal("startup scan did not start")
	}
	first, err := scheduler.Trigger(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := scheduler.Trigger(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || fake.concurrentMax != 1 {
		t.Fatalf("first/second/max = %+v/%+v/%d", first, second, fake.concurrentMax)
	}
	close(fake.release)
}

func TestSchedulerManualCooldownStartsAtFinish(t *testing.T) {
	now := time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC)
	fake := &instantScanRunner{now: func() time.Time { return now }}
	scheduler := NewScheduler(fake, "crypto", func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	waitForSchedulerState(t, scheduler, "completed")
	now = now.Add(61 * time.Second)
	if _, err := scheduler.Trigger(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForSchedulerState(t, scheduler, "completed")
	if _, err := scheduler.Trigger(context.Background()); !errors.Is(err, ErrScanCooldown) {
		t.Fatalf("cooldown error = %v", err)
	}
}

func TestSchedulerBuildsCheapestCombinedDueRequest(t *testing.T) {
	now := time.Date(2026, 9, 22, 4, 0, 0, 0, time.UTC)
	scheduler := NewScheduler(&instantScanRunner{now: func() time.Time { return now }}, "crypto", func() time.Time { return now })
	scheduler.lastSnapshot = now.Add(-3 * time.Minute)
	scheduler.lastRadar = now.Add(-6 * time.Minute)
	scheduler.last1H = now.Add(-16 * time.Minute)
	scheduler.last4H = now.Add(-61 * time.Minute)
	scheduler.last1D = now.Add(-3 * time.Hour)
	scheduler.lastUniverse = now.Add(-31 * time.Minute)
	request, snapshotOnly := scheduler.dueRequest(now)
	if snapshotOnly || !request.RefreshUniverse || request.Force || !hasFrame(request.Frames, domain.Timeframe15m) || !hasFrame(request.Frames, domain.Timeframe1H) || !hasFrame(request.Frames, domain.Timeframe4H) || hasFrame(request.Frames, domain.Timeframe1D) {
		t.Fatalf("due request/snapshot = %+v/%v", request, snapshotOnly)
	}
}

func TestSchedulerCancellationStopsBlockedScan(t *testing.T) {
	now := time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC)
	fake := newBlockingScanRunner()
	scheduler := NewScheduler(fake, "crypto", func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	scheduler.Start(ctx)
	<-fake.started
	cancel()
	waitForSchedulerState(t, scheduler, "failed")
}

func TestSchedulersKeepIdentityStatusAndCooldownIndependent(t *testing.T) {
	now := time.Date(2026, 9, 30, 4, 0, 0, 0, time.UTC)
	cryptoRunner := &switchableScanRunner{now: func() time.Time { return now }}
	stockRunner := &switchableScanRunner{now: func() time.Time { return now }}
	crypto := NewScheduler(cryptoRunner, "crypto", func() time.Time { return now })
	stock := NewScheduler(stockRunner, "stock", func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	crypto.Start(ctx)
	stock.Start(ctx)
	waitForSchedulerState(t, crypto, "completed")
	waitForSchedulerState(t, stock, "completed")
	if crypto.Status().ID == stock.Status().ID || !strings.HasPrefix(crypto.Status().ID, "crypto-scan-") || !strings.HasPrefix(stock.Status().ID, "stock-scan-") {
		t.Fatalf("scheduler IDs = %q / %q", crypto.Status().ID, stock.Status().ID)
	}

	now = now.Add(61 * time.Second)
	if _, err := crypto.Trigger(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForSchedulerState(t, crypto, "completed")
	cryptoCompleted := crypto.Status()
	if _, err := crypto.Trigger(context.Background()); !errors.Is(err, ErrScanCooldown) {
		t.Fatalf("crypto cooldown error = %v", err)
	}
	if _, err := stock.Trigger(context.Background()); err != nil {
		t.Fatalf("crypto cooldown blocked stock: %v", err)
	}
	waitForSchedulerState(t, stock, "completed")

	now = now.Add(61 * time.Second)
	stockRunner.setError(errors.New("stock feed failed"))
	if _, err := stock.Trigger(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForSchedulerState(t, stock, "failed")
	if got := crypto.Status(); got.State != cryptoCompleted.State || got.ID != cryptoCompleted.ID {
		t.Fatalf("stock failure changed crypto status: before=%+v after=%+v", cryptoCompleted, got)
	}
}

type blockingScanRunner struct {
	started       chan struct{}
	release       chan struct{}
	once          sync.Once
	mu            sync.Mutex
	concurrent    int
	concurrentMax int
}

func newBlockingScanRunner() *blockingScanRunner {
	return &blockingScanRunner{started: make(chan struct{}), release: make(chan struct{})}
}

func (f *blockingScanRunner) Scan(ctx context.Context, request ScanRequest, progress ProgressFunc) (domain.OpportunityRun, error) {
	f.mu.Lock()
	f.concurrent++
	if f.concurrent > f.concurrentMax {
		f.concurrentMax = f.concurrent
	}
	f.mu.Unlock()
	f.once.Do(func() { close(f.started) })
	if progress != nil {
		progress(1, 2)
	}
	select {
	case <-f.release:
		f.mu.Lock()
		f.concurrent--
		f.mu.Unlock()
		return domain.OpportunityRun{ID: 1, Trigger: request.Trigger, Status: domain.OpportunityRunCompleted, PoolSize: 2, CandidateCount: 1, FinishedAt: time.Now()}, nil
	case <-ctx.Done():
		f.mu.Lock()
		f.concurrent--
		f.mu.Unlock()
		return domain.OpportunityRun{ID: 1, Trigger: request.Trigger, Status: domain.OpportunityRunFailed, FinishedAt: time.Now()}, ctx.Err()
	}
}
func (f *blockingScanRunner) RefreshSnapshots(context.Context) error { return nil }

type instantScanRunner struct{ now func() time.Time }

func (f *instantScanRunner) Scan(_ context.Context, request ScanRequest, progress ProgressFunc) (domain.OpportunityRun, error) {
	if progress != nil {
		progress(1, 1)
	}
	return domain.OpportunityRun{ID: 1, Trigger: request.Trigger, Status: domain.OpportunityRunCompleted, PoolSize: 1, CandidateCount: 0, FinishedAt: f.now()}, nil
}
func (f *instantScanRunner) RefreshSnapshots(context.Context) error { return nil }

type switchableScanRunner struct {
	mu  sync.Mutex
	now func() time.Time
	err error
}

func (f *switchableScanRunner) setError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *switchableScanRunner) Scan(_ context.Context, request ScanRequest, progress ProgressFunc) (domain.OpportunityRun, error) {
	if progress != nil {
		progress(1, 1)
	}
	f.mu.Lock()
	err := f.err
	f.mu.Unlock()
	status := domain.OpportunityRunCompleted
	if err != nil {
		status = domain.OpportunityRunFailed
	}
	return domain.OpportunityRun{ID: 1, Trigger: request.Trigger, Status: status, PoolSize: 1, FinishedAt: f.now()}, err
}

func (f *switchableScanRunner) RefreshSnapshots(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func waitForSchedulerState(t *testing.T, scheduler *Scheduler, state string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if scheduler.Status().State == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state = %q, want %q", scheduler.Status().State, state)
}

func hasFrame(frames []domain.Timeframe, want domain.Timeframe) bool {
	for _, frame := range frames {
		if frame == want {
			return true
		}
	}
	return false
}
