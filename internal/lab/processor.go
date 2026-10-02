package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Processor struct {
	source    *Source
	repo      *Repository
	now       func() time.Time
	evaluator *Evaluator
}

func NewProcessor(source *Source, repo *Repository, now func() time.Time) *Processor {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Processor{source: source, repo: repo, now: now, evaluator: NewEvaluator(source, repo, now)}
}

func (p *Processor) ProcessCycle(ctx context.Context) (CycleResult, error) {
	if p == nil || p.source == nil || p.repo == nil {
		return CycleResult{}, fmt.Errorf("Signal Lab processor is not configured")
	}
	frontier, err := p.source.Frontier(ctx)
	if err != nil {
		return CycleResult{}, err
	}
	state, err := p.repo.State(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return CycleResult{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		activation := frontier.HighWatermark
		identity, identityErr := p.source.Identity(ctx, activation)
		if identityErr != nil {
			return CycleResult{}, identityErr
		}
		state, err = p.repo.InitializeOrValidate(ctx, SourceRegistration{
			CanonicalPath: p.source.CanonicalPath(), Identity: identity,
			ActivationSnapshotID: activation, CurrentSourceMaximum: frontier.HighWatermark,
			InitializedAt: p.now(),
		})
		if err != nil {
			return CycleResult{}, err
		}
	} else {
		identity, identityErr := p.source.Identity(ctx, state.ActivationSnapshotID)
		if identityErr != nil {
			return CycleResult{}, identityErr
		}
		state, err = p.repo.InitializeOrValidate(ctx, SourceRegistration{
			CanonicalPath: p.source.CanonicalPath(), Identity: identity,
			ActivationSnapshotID: state.ActivationSnapshotID, CurrentSourceMaximum: frontier.HighWatermark,
			InitializedAt: p.now(),
		})
		if err != nil {
			return CycleResult{}, err
		}
	}

	result := CycleResult{
		ActivationSnapshotID:      state.ActivationSnapshotID,
		SourceHighWatermark:       frontier.HighWatermark,
		SourceObservationFrontier: frontier.ObservationAt,
	}
	watermark := state.LastProcessedSnapshotID
	for watermark < frontier.HighWatermark {
		snapshots, err := p.source.Snapshots(ctx, watermark, frontier.HighWatermark, maxSourceBatch)
		if err != nil {
			return CycleResult{}, err
		}
		if len(snapshots) == 0 {
			return CycleResult{}, fmt.Errorf("Core source has no rows between watermark %d and %d", watermark, frontier.HighWatermark)
		}
		applied, err := p.repo.ApplySnapshotChunk(ctx, snapshots, state.ActivationSnapshotID, p.now())
		if err != nil {
			return CycleResult{}, err
		}
		watermark = applied.LastSnapshotID
		result.SourceSnapshotsProcessed += applied.SourceSnapshotsProcessed
		result.SignalsCreated += applied.SignalsCreated
		result.EntriesCreated += applied.EntriesCreated
	}
	outcomes, err := p.evaluator.EvaluatePending(ctx, frontier, 100)
	if err != nil {
		return CycleResult{}, err
	}
	result.OutcomesMatured = outcomes.Matured
	result.OutcomesInsufficient = outcomes.Insufficient
	if err := p.repo.RecordCycleFrontier(ctx, frontier, p.now()); err != nil {
		return CycleResult{}, err
	}
	return result, nil
}
