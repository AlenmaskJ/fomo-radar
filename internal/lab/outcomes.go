package lab

import (
	"context"
	"fmt"
	"time"
)

type Evaluator struct {
	source *Source
	repo   *Repository
	now    func() time.Time
}

func NewEvaluator(source *Source, repo *Repository, now func() time.Time) *Evaluator {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Evaluator{source: source, repo: repo, now: now}
}

func (e *Evaluator) EvaluatePending(ctx context.Context, frontier SourceFrontier, batchSize int) (OutcomeResult, error) {
	if e == nil || e.source == nil || e.repo == nil {
		return OutcomeResult{}, fmt.Errorf("Signal Lab evaluator is not configured")
	}
	if batchSize <= 0 {
		return OutcomeResult{}, fmt.Errorf("outcome batch size must be positive")
	}
	result := OutcomeResult{}
	var afterID int64
	for {
		pendingRows, err := e.repo.PendingOutcomes(ctx, afterID, batchSize)
		if err != nil {
			return OutcomeResult{}, err
		}
		if len(pendingRows) == 0 {
			break
		}
		for _, pending := range pendingRows {
			afterID = pending.ID
			result.Examined++
			selected, err := e.source.SelectOutcomeSnapshot(ctx, pending, frontier.HighWatermark)
			if err != nil {
				return OutcomeResult{}, err
			}
			if selected != nil {
				path, err := e.source.PricePath(ctx, pending, selected.CollectedAt, frontier.HighWatermark)
				if err != nil {
					return OutcomeResult{}, err
				}
				returnDecimal := selected.PriceUSD/pending.EntryPriceUSD - 1
				mfe, mae := 0.0, 0.0
				for _, observation := range path {
					excursion := observation.PriceUSD/pending.EntryPriceUSD - 1
					if excursion > mfe {
						mfe = excursion
					}
					if excursion < mae {
						mae = excursion
					}
				}
				if err := e.repo.MatureOutcome(ctx, pending, *selected, frontier, returnDecimal, mfe, mae, e.now()); err != nil {
					return OutcomeResult{}, err
				}
				result.Matured++
				continue
			}
			if frontier.ObservationAt != nil && frontier.ObservationAt.After(pending.WindowEndAt) {
				if err := e.repo.MarkOutcomeInsufficient(ctx, pending, frontier, e.now()); err != nil {
					return OutcomeResult{}, err
				}
				result.Insufficient++
			}
		}
		if len(pendingRows) < batchSize {
			break
		}
	}
	return result, nil
}
