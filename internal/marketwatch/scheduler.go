package marketwatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

var ErrScanCooldown = errors.New("manual scan is cooling down")

type ScanRunner interface {
	Scan(context.Context, ScanRequest, ProgressFunc) (domain.OpportunityRun, error)
	RefreshSnapshots(context.Context) error
}

type ScanStatus struct {
	ID              string     `json:"id"`
	State           string     `json:"state"`
	Trigger         string     `json:"trigger"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	NextScanAt      time.Time  `json:"next_scan_at"`
	PoolSize        int        `json:"pool_size"`
	CandidateCount  int        `json:"candidate_count"`
	ProgressCurrent int        `json:"progress_current"`
	ProgressTotal   int        `json:"progress_total"`
	Error           string     `json:"error,omitempty"`
}

type scanJob struct {
	statusID     string
	request      ScanRequest
	snapshotOnly bool
}

type Scheduler struct {
	scanner   ScanRunner
	namespace string
	now       func() time.Time

	mu               sync.Mutex
	started          bool
	sequence         uint64
	status           ScanStatus
	jobs             chan scanJob
	lastManualFinish time.Time
	lastSnapshot     time.Time
	lastRadar        time.Time
	lastUniverse     time.Time
	last1H           time.Time
	last4H           time.Time
	last1D           time.Time
}

func NewScheduler(scanner ScanRunner, namespace string, now func() time.Time) *Scheduler {
	if now == nil {
		now = time.Now
	}
	return &Scheduler{scanner: scanner, namespace: namespace, now: now, jobs: make(chan scanJob, 1)}
}

func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	job := s.newJobLocked(fullScanRequest("startup"), false)
	s.mu.Unlock()
	go s.worker(ctx)
	s.jobs <- job
	go s.scheduleLoop(ctx)
}

func (s *Scheduler) Trigger(ctx context.Context) (ScanStatus, error) {
	if err := ctx.Err(); err != nil {
		return ScanStatus{}, err
	}
	s.mu.Lock()
	if s.status.State == "queued" || s.status.State == "running" {
		status := s.status
		s.mu.Unlock()
		return status, nil
	}
	now := s.now().UTC()
	if !s.lastManualFinish.IsZero() && now.Sub(s.lastManualFinish) < time.Minute {
		status := s.status
		remaining := time.Minute - now.Sub(s.lastManualFinish)
		s.mu.Unlock()
		return status, fmt.Errorf("%w: %s remaining", ErrScanCooldown, remaining.Round(time.Second))
	}
	job := s.newJobLocked(fullScanRequest("manual"), false)
	status := s.status
	s.mu.Unlock()
	select {
	case s.jobs <- job:
		return status, nil
	case <-ctx.Done():
		return ScanStatus{}, ctx.Err()
	}
}

func (s *Scheduler) Status() ScanStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Scheduler) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-s.jobs:
			s.runJob(ctx, job)
		}
	}
}

func (s *Scheduler) runJob(ctx context.Context, job scanJob) {
	startedAt := s.now().UTC()
	s.mu.Lock()
	if s.status.ID != job.statusID {
		s.mu.Unlock()
		return
	}
	s.status.State = "running"
	s.status.StartedAt = timePointer(startedAt)
	s.mu.Unlock()

	var run domain.OpportunityRun
	var err error
	if job.snapshotOnly {
		err = s.scanner.RefreshSnapshots(ctx)
	} else {
		run, err = s.scanner.Scan(ctx, job.request, func(current, total int) {
			s.mu.Lock()
			if s.status.ID == job.statusID {
				s.status.ProgressCurrent = current
				s.status.ProgressTotal = total
			}
			s.mu.Unlock()
		})
	}
	finishedAt := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.ID != job.statusID {
		return
	}
	s.status.FinishedAt = timePointer(finishedAt)
	s.status.PoolSize = run.PoolSize
	s.status.CandidateCount = run.CandidateCount
	if err != nil {
		s.status.State = "failed"
		s.status.Error = err.Error()
	} else if run.Status == domain.OpportunityRunDegraded {
		s.status.State = "degraded"
		s.status.Error = run.ErrorSummary
	} else {
		s.status.State = "completed"
	}
	s.markCompletedLocked(job, finishedAt)
	if job.request.Trigger == "manual" {
		s.lastManualFinish = finishedAt
	}
	s.status.NextScanAt = s.nextScanAtLocked(finishedAt)
}

func (s *Scheduler) scheduleLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.enqueueDue(s.now().UTC())
		}
	}
}

func (s *Scheduler) enqueueDue(now time.Time) {
	s.mu.Lock()
	if s.status.State == "queued" || s.status.State == "running" {
		s.mu.Unlock()
		return
	}
	request, snapshotOnly := s.dueRequest(now)
	if !snapshotOnly && len(request.Frames) == 0 {
		s.mu.Unlock()
		return
	}
	job := s.newJobLocked(request, snapshotOnly)
	s.mu.Unlock()
	select {
	case s.jobs <- job:
	default:
	}
}

func (s *Scheduler) dueRequest(now time.Time) (ScanRequest, bool) {
	radarDue := s.lastRadar.IsZero() || now.Sub(s.lastRadar) >= 5*time.Minute
	oneHourDue := s.last1H.IsZero() || now.Sub(s.last1H) >= 15*time.Minute
	fourHourDue := s.last4H.IsZero() || now.Sub(s.last4H) >= time.Hour
	dailyDue := s.last1D.IsZero() || now.Sub(s.last1D) >= 4*time.Hour
	universeDue := s.lastUniverse.IsZero() || now.Sub(s.lastUniverse) >= 30*time.Minute
	snapshotDue := s.lastSnapshot.IsZero() || now.Sub(s.lastSnapshot) >= 2*time.Minute
	if !radarDue && !oneHourDue && !fourHourDue && !dailyDue && !universeDue {
		return ScanRequest{}, snapshotDue
	}
	request := ScanRequest{Trigger: "scheduled", RefreshUniverse: universeDue}
	request.Frames = append(request.Frames, domain.Timeframe15m)
	if oneHourDue {
		request.Frames = append(request.Frames, domain.Timeframe1H)
	}
	if fourHourDue {
		request.Frames = append(request.Frames, domain.Timeframe4H)
	}
	if dailyDue {
		request.Frames = append(request.Frames, domain.Timeframe1D)
	}
	return request, false
}

func (s *Scheduler) newJobLocked(request ScanRequest, snapshotOnly bool) scanJob {
	s.sequence++
	now := s.now().UTC()
	id := fmt.Sprintf("%s-scan-%d-%d", s.namespace, now.UnixMilli(), s.sequence)
	trigger := request.Trigger
	if snapshotOnly {
		trigger = "snapshot"
	}
	s.status = ScanStatus{ID: id, State: "queued", Trigger: trigger, NextScanAt: s.nextScanAtLocked(now)}
	return scanJob{statusID: id, request: request, snapshotOnly: snapshotOnly}
}

func (s *Scheduler) markCompletedLocked(job scanJob, at time.Time) {
	if job.snapshotOnly {
		s.lastSnapshot = at
		return
	}
	s.lastSnapshot = at
	s.lastRadar = at
	if job.request.RefreshUniverse || job.request.Force {
		s.lastUniverse = at
	}
	if job.request.Force || hasTimeframe(job.request.Frames, domain.Timeframe1H) {
		s.last1H = at
	}
	if job.request.Force || hasTimeframe(job.request.Frames, domain.Timeframe4H) {
		s.last4H = at
	}
	if job.request.Force || hasTimeframe(job.request.Frames, domain.Timeframe1D) {
		s.last1D = at
	}
}

func (s *Scheduler) nextScanAtLocked(now time.Time) time.Time {
	if s.lastRadar.IsZero() {
		return now
	}
	return s.lastRadar.Add(5 * time.Minute)
}

func hasTimeframe(frames []domain.Timeframe, want domain.Timeframe) bool {
	for _, frame := range frames {
		if frame == want {
			return true
		}
	}
	return false
}

func timePointer(value time.Time) *time.Time { return &value }
