package scanner

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/alen1/fomo-radar/internal/domain"
)

type DeepAdmission string

const (
	DeepAdmissionQueued    DeepAdmission = "queued"
	DeepAdmissionDuplicate DeepAdmission = "duplicate"
	DeepAdmissionDeferred  DeepAdmission = "deferred"
	DeepAdmissionStale     DeepAdmission = "stale"
)

type DeepQueueStats struct {
	Queued    int
	InFlight  int
	Deferred  int
	Stale     int
	Completed int
	Failed    int
}

type DeepOutcome struct {
	Result          CandidateResult
	DegradedSources []string
	Inserted        bool
}

type deepProcessor interface {
	ProcessDeepTask(context.Context, domain.DeepTask) (DeepOutcome, error)
}

type deepStore interface {
	IsCurrentFastSnapshot(context.Context, string, int64) (bool, error)
	SetDeepStatus(context.Context, int64, domain.DeepStatus) error
	ListPendingDeep(context.Context, int) ([]domain.DeepTask, error)
}

type runTracker struct {
	pending  int
	done     chan struct{}
	outcomes []DeepOutcome
	errors   []error
}

// DeepQueue runs bounded, deduplicated Deep work without blocking FastScan.
type DeepQueue struct {
	processor deepProcessor
	store     deepStore
	workers   int
	jobs      chan domain.DeepTask

	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	started   bool
	closed    bool
	active    map[string]struct{}
	seen      map[string]struct{}
	seenLimit int
	runs      map[int64]*runTracker
	stats     DeepQueueStats
	wg        sync.WaitGroup
}

func NewDeepQueue(processor deepProcessor, store deepStore, workers, capacity int) *DeepQueue {
	if workers <= 0 {
		workers = 1
	}
	if capacity <= 0 {
		capacity = 1
	}
	return &DeepQueue{
		processor: processor, store: store, workers: workers, jobs: make(chan domain.DeepTask, capacity),
		active: map[string]struct{}{}, seen: map[string]struct{}{}, seenLimit: 2*(capacity+workers) + 1,
		runs: map[int64]*runTracker{},
	}
}

func (q *DeepQueue) Start(parent context.Context) error {
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return nil
	}
	q.ctx, q.cancel = context.WithCancel(parent)
	q.started = true
	q.wg.Add(q.workers)
	for index := 0; index < q.workers; index++ {
		go q.worker()
	}
	q.mu.Unlock()

	pending, err := q.store.ListPendingDeep(q.ctx, cap(q.jobs))
	if err != nil {
		_ = q.Close()
		return fmt.Errorf("reload pending Deep tasks: %w", err)
	}
	for _, task := range pending {
		if _, err := q.TryEnqueue(task); err != nil {
			_ = q.Close()
			return fmt.Errorf("reload Deep task %s: %w", deepTaskKey(task), err)
		}
	}
	return nil
}

func (q *DeepQueue) TryEnqueue(task domain.DeepTask) (DeepAdmission, error) {
	ctx, err := q.context()
	if err != nil {
		return "", err
	}
	current, err := q.store.IsCurrentFastSnapshot(ctx, task.TokenID, task.BaseFastSnapshotID)
	if err != nil {
		return "", err
	}
	if !current {
		if err := q.store.SetDeepStatus(ctx, task.BaseFastSnapshotID, domain.DeepStatusStale); err != nil {
			return "", err
		}
		q.mu.Lock()
		q.stats.Stale++
		q.mu.Unlock()
		return DeepAdmissionStale, nil
	}
	key := deepTaskKey(task)
	if !q.reserve(key) {
		return DeepAdmissionDuplicate, nil
	}
	if err := q.store.SetDeepStatus(ctx, task.BaseFastSnapshotID, domain.DeepStatusQueued); err != nil {
		q.releaseReservation(key)
		return "", err
	}
	q.mu.Lock()
	q.stats.Queued++
	select {
	case q.jobs <- task:
		q.markSeenLocked(key)
		q.mu.Unlock()
		return DeepAdmissionQueued, nil
	default:
		q.stats.Queued--
		delete(q.active, key)
		q.stats.Deferred++
		q.mu.Unlock()
		if err := q.store.SetDeepStatus(ctx, task.BaseFastSnapshotID, domain.DeepStatusDeferred); err != nil {
			return "", err
		}
		return DeepAdmissionDeferred, nil
	}
}

// Enqueue waits for bounded queue capacity and is used by manual full scans.
func (q *DeepQueue) Enqueue(ctx context.Context, task domain.DeepTask) error {
	queueCtx, err := q.context()
	if err != nil {
		return err
	}
	current, err := q.store.IsCurrentFastSnapshot(queueCtx, task.TokenID, task.BaseFastSnapshotID)
	if err != nil {
		return err
	}
	if !current {
		if err := q.store.SetDeepStatus(queueCtx, task.BaseFastSnapshotID, domain.DeepStatusStale); err != nil {
			return err
		}
		q.mu.Lock()
		q.stats.Stale++
		q.mu.Unlock()
		return nil
	}
	key := deepTaskKey(task)
	if !q.reserve(key) {
		return nil
	}
	if err := q.store.SetDeepStatus(queueCtx, task.BaseFastSnapshotID, domain.DeepStatusQueued); err != nil {
		q.releaseReservation(key)
		return err
	}
	q.mu.Lock()
	q.registerRunLocked(deepRunID(task))
	q.stats.Queued++
	q.markSeenLocked(key)
	q.mu.Unlock()
	select {
	case q.jobs <- task:
		return nil
	case <-ctx.Done():
		q.cancelAdmission(task, key)
		return ctx.Err()
	case <-queueCtx.Done():
		q.cancelAdmission(task, key)
		return queueCtx.Err()
	}
}

func (q *DeepQueue) WaitRun(ctx context.Context, runID int64) ([]DeepOutcome, error) {
	q.mu.Lock()
	tracker := q.runs[runID]
	if tracker == nil || tracker.pending == 0 {
		var outcomes []DeepOutcome
		if tracker != nil {
			outcomes = append(outcomes, tracker.outcomes...)
		}
		q.mu.Unlock()
		return outcomes, nil
	}
	done := tracker.done
	q.mu.Unlock()
	select {
	case <-done:
		q.mu.Lock()
		tracker := q.runs[runID]
		outcomes := append([]DeepOutcome(nil), tracker.outcomes...)
		err := errors.Join(tracker.errors...)
		q.mu.Unlock()
		return outcomes, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *DeepQueue) Stats() DeepQueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stats
}

func (q *DeepQueue) Close() error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true
	cancel := q.cancel
	q.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	q.wg.Wait()
	return nil
}

func (q *DeepQueue) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.ctx.Done():
			return
		case task := <-q.jobs:
			q.beginTask()
			outcome, err := q.execute(task)
			q.finishTask(task, outcome, err)
		}
	}
}

func (q *DeepQueue) execute(task domain.DeepTask) (DeepOutcome, error) {
	current, err := q.store.IsCurrentFastSnapshot(q.ctx, task.TokenID, task.BaseFastSnapshotID)
	if err != nil {
		return DeepOutcome{}, err
	}
	if !current {
		if err := q.store.SetDeepStatus(q.ctx, task.BaseFastSnapshotID, domain.DeepStatusStale); err != nil {
			return DeepOutcome{}, err
		}
		return DeepOutcome{Inserted: false}, nil
	}
	return q.processor.ProcessDeepTask(q.ctx, task)
}

func (q *DeepQueue) beginTask() {
	q.mu.Lock()
	q.stats.Queued--
	q.stats.InFlight++
	q.mu.Unlock()
}

func (q *DeepQueue) finishTask(task domain.DeepTask, outcome DeepOutcome, err error) {
	q.mu.Lock()
	q.stats.InFlight--
	if err != nil {
		q.stats.Failed++
	} else if !outcome.Inserted {
		q.stats.Stale++
	} else {
		q.stats.Completed++
	}
	delete(q.active, deepTaskKey(task))
	tracker := q.runs[deepRunID(task)]
	if tracker != nil {
		if err == nil {
			tracker.outcomes = append(tracker.outcomes, outcome)
		} else {
			tracker.errors = append(tracker.errors, err)
		}
		tracker.pending--
		if tracker.pending == 0 {
			close(tracker.done)
		}
	}
	q.mu.Unlock()
	if err != nil {
		_ = q.store.SetDeepStatus(context.WithoutCancel(q.ctx), task.BaseFastSnapshotID, domain.DeepStatusFailed)
	}
}

func (q *DeepQueue) context() (context.Context, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started || q.closed {
		return nil, fmt.Errorf("DeepQueue is not running")
	}
	return q.ctx, nil
}

func (q *DeepQueue) reserve(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.active[key]; exists {
		return false
	}
	if _, exists := q.seen[key]; exists {
		return false
	}
	q.active[key] = struct{}{}
	return true
}

func (q *DeepQueue) releaseReservation(key string) {
	q.mu.Lock()
	delete(q.active, key)
	q.mu.Unlock()
}

func (q *DeepQueue) markSeenLocked(key string) {
	if _, exists := q.seen[key]; exists {
		return
	}
	if len(q.seen) >= q.seenLimit {
		for prior := range q.seen {
			if _, active := q.active[prior]; !active {
				delete(q.seen, prior)
				break
			}
		}
	}
	q.seen[key] = struct{}{}
}

func (q *DeepQueue) registerRunLocked(runID int64) {
	tracker := q.runs[runID]
	if tracker == nil {
		tracker = &runTracker{}
		q.runs[runID] = tracker
	}
	if tracker.pending == 0 {
		tracker.done = make(chan struct{})
	}
	tracker.pending++
}

func (q *DeepQueue) unregisterRunLocked(runID int64) {
	tracker := q.runs[runID]
	if tracker == nil || tracker.pending == 0 {
		return
	}
	tracker.pending--
	if tracker.pending == 0 {
		close(tracker.done)
	}
}

func (q *DeepQueue) cancelAdmission(task domain.DeepTask, key string) {
	q.mu.Lock()
	delete(q.active, key)
	delete(q.seen, key)
	q.stats.Queued--
	q.unregisterRunLocked(deepRunID(task))
	q.mu.Unlock()
}

func deepTaskKey(task domain.DeepTask) string {
	return fmt.Sprintf("%s:%d", task.TokenID, task.BaseFastSnapshotID)
}

func deepRunID(task domain.DeepTask) int64 {
	if task.Snapshot.ScanRunID == nil {
		return 0
	}
	return *task.Snapshot.ScanRunID
}
