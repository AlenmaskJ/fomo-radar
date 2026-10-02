package scanner

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestDeepQueueBoundsCapacityAndDeduplicatesTaskKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	processor := deepProcessorFunc(func(context.Context, domain.DeepTask) (DeepOutcome, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return DeepOutcome{Inserted: true}, nil
	})
	database := newQueueStore()
	queue := NewDeepQueue(processor, database, 1, 2)
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Close() })

	task1 := queueTask(1, 1)
	if admission, err := queue.TryEnqueue(task1); err != nil || admission != DeepAdmissionQueued {
		t.Fatalf("task1 admission = %q, %v", admission, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start task1")
	}
	for _, task := range []domain.DeepTask{queueTask(1, 2), queueTask(1, 3)} {
		if admission, err := queue.TryEnqueue(task); err != nil || admission != DeepAdmissionQueued {
			t.Fatalf("buffer admission = %q, %v", admission, err)
		}
	}
	if admission, err := queue.TryEnqueue(queueTask(1, 2)); err != nil || admission != DeepAdmissionDuplicate {
		t.Fatalf("duplicate admission = %q, %v", admission, err)
	}
	if admission, err := queue.TryEnqueue(queueTask(1, 4)); err != nil || admission != DeepAdmissionDeferred {
		t.Fatalf("overflow admission = %q, %v", admission, err)
	}
	stats := queue.Stats()
	if stats.Queued != 2 || stats.InFlight != 1 || stats.Deferred != 1 {
		t.Fatalf("bounded stats = %+v", stats)
	}
	close(release)
	if _, err := queue.WaitRun(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
}

func TestDeepQueueSkipsQueuedStaleGenerationBeforeHTTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database := newQueueStore()
	blocking := queueTask(2, 1)
	stale := queueTask(2, 2)
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	queue := NewDeepQueue(deepProcessorFunc(func(context.Context, domain.DeepTask) (DeepOutcome, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return DeepOutcome{Inserted: true}, nil
	}), database, 1, 2)
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Close() })
	if err := queue.Enqueue(context.Background(), blocking); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := queue.Enqueue(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	database.mu.Lock()
	database.current[taskKey(stale)] = false
	database.mu.Unlock()
	close(release)
	if _, err := queue.WaitRun(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || queue.Stats().Stale != 1 {
		t.Fatalf("processor calls/stats = %d/%+v", calls.Load(), queue.Stats())
	}
}

func TestDeepQueueRejectsInFlightResultAfterNewFastGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database := newQueueStore()
	task := queueTask(3, 1)
	database.current[taskKey(task)] = true
	started := make(chan struct{})
	release := make(chan struct{})
	queue := NewDeepQueue(deepProcessorFunc(func(context.Context, domain.DeepTask) (DeepOutcome, error) {
		close(started)
		<-release
		return DeepOutcome{Inserted: false}, nil
	}), database, 1, 2)
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Close() })
	if err := queue.Enqueue(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Deep task did not enter processor")
	}
	database.mu.Lock()
	database.current[taskKey(task)] = false
	database.mu.Unlock()
	close(release)
	if _, err := queue.WaitRun(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if stats := queue.Stats(); stats.Stale != 1 || stats.Completed != 0 {
		t.Fatalf("late result stats = %+v", stats)
	}
}

func TestDeepQueueDeduplicatesRecentlyCompletedTask(t *testing.T) {
	database := newQueueStore()
	var calls atomic.Int32
	queue := NewDeepQueue(deepProcessorFunc(func(context.Context, domain.DeepTask) (DeepOutcome, error) {
		calls.Add(1)
		return DeepOutcome{Inserted: true}, nil
	}), database, 1, 2)
	if err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Close() })
	task := queueTask(4, 1)
	if err := queue.Enqueue(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.WaitRun(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	if admission, err := queue.TryEnqueue(task); err != nil || admission != DeepAdmissionDuplicate {
		t.Fatalf("completed duplicate admission = %q, %v", admission, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("processor calls = %d, want one", calls.Load())
	}
}

func TestDeepQueueWatchBookkeepingRemainsBoundedAcrossRuns(t *testing.T) {
	queue := NewDeepQueue(deepProcessorFunc(func(context.Context, domain.DeepTask) (DeepOutcome, error) {
		return DeepOutcome{Inserted: true}, nil
	}), newQueueStore(), 1, 2)
	if err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Close() })

	for runID := int64(1); runID <= 300; runID++ {
		task := queueTask(runID, runID)
		if admission, err := queue.TryEnqueue(task); err != nil || admission != DeepAdmissionQueued {
			t.Fatalf("run %d admission = %q, %v", runID, admission, err)
		}
		deadline := time.Now().Add(time.Second)
		for queue.Stats().Completed < int(runID) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.runs) != 0 {
		t.Fatalf("watch run trackers = %d, want 0", len(queue.runs))
	}
	if len(queue.seen) > queue.seenLimit {
		t.Fatalf("completed dedupe entries = %d, limit %d", len(queue.seen), queue.seenLimit)
	}
}

func queueTask(runID, fastID int64) domain.DeepTask {
	return domain.DeepTask{
		TokenID: "bsc:0xqueue", BaseFastSnapshotID: fastID,
		Candidate: domain.Candidate{ID: "bsc:0xqueue", Chain: domain.ChainBSC, Address: "0xqueue"},
		Snapshot:  domain.MarketSnapshot{ID: fastID, TokenID: "bsc:0xqueue", ScanRunID: &runID},
	}
}

type deepProcessorFunc func(context.Context, domain.DeepTask) (DeepOutcome, error)

func (f deepProcessorFunc) ProcessDeepTask(ctx context.Context, task domain.DeepTask) (DeepOutcome, error) {
	return f(ctx, task)
}

type queueStore struct {
	mu       sync.Mutex
	current  map[string]bool
	statuses map[int64]domain.DeepStatus
	pending  []domain.DeepTask
}

func newQueueStore() *queueStore {
	return &queueStore{current: map[string]bool{}, statuses: map[int64]domain.DeepStatus{}}
}

func (s *queueStore) IsCurrentFastSnapshot(_ context.Context, tokenID string, fastID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.current[fmt.Sprintf("%s:%d", tokenID, fastID)]
	if !exists {
		return true, nil
	}
	return current, nil
}

func (s *queueStore) SetDeepStatus(_ context.Context, fastID int64, status domain.DeepStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[fastID] = status
	return nil
}

func (s *queueStore) ListPendingDeep(context.Context, int) ([]domain.DeepTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.DeepTask(nil), s.pending...), nil
}

func taskKey(task domain.DeepTask) string {
	return fmt.Sprintf("%s:%d", task.TokenID, task.BaseFastSnapshotID)
}
