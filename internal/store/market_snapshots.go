package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

// InsertDerivativesSnapshots 原样保存一次 OKX 市场扫描，供后续计算持仓变化。
func (s *Store) InsertDerivativesSnapshots(ctx context.Context, snapshots []domain.DerivativesSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin derivatives snapshot insert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO derivatives_market_snapshots (
			asset_class, instrument_id, symbol, collected_at, source_time,
			price_usd, change_24h_pct, turnover_24h_usd, open_interest_usd, funding_rate
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare derivatives snapshot insert: %w", err)
	}
	defer stmt.Close()

	for _, snapshot := range snapshots {
		var openInterest, funding any
		if snapshot.OpenInterestAvailable {
			openInterest = snapshot.OpenInterestUSD
		}
		if snapshot.FundingRateAvailable {
			funding = snapshot.FundingRate
		}
		if _, err := stmt.ExecContext(ctx,
			snapshot.AssetClass, snapshot.InstrumentID, snapshot.Symbol,
			snapshot.CollectedAt.UnixMilli(), snapshot.SourceTime.UnixMilli(),
			snapshot.PriceUSD, snapshot.Change24hPct, snapshot.Turnover24hUSD,
			openInterest, funding,
		); err != nil {
			return fmt.Errorf("insert derivatives snapshot %s: %w", snapshot.InstrumentID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit derivatives snapshots: %w", err)
	}
	return nil
}

// LatestDerivativesSnapshotBefore 返回指定合约在本次采集前最近的一条记录。
func (s *Store) LatestDerivativesSnapshotBefore(ctx context.Context, assetClass domain.AssetClass, instrumentID string, before time.Time) (domain.DerivativesSnapshot, bool, error) {
	return s.derivativesSnapshotAt(ctx, assetClass, instrumentID, before, false)
}

// DerivativesSnapshotAtOrBefore 返回基准时刻本身或之前的最近快照。
func (s *Store) DerivativesSnapshotAtOrBefore(ctx context.Context, assetClass domain.AssetClass, instrumentID string, at time.Time) (domain.DerivativesSnapshot, bool, error) {
	return s.derivativesSnapshotAt(ctx, assetClass, instrumentID, at, true)
}

func (s *Store) derivativesSnapshotAt(ctx context.Context, assetClass domain.AssetClass, instrumentID string, at time.Time, inclusive bool) (domain.DerivativesSnapshot, bool, error) {
	if !assetClass.Valid() {
		return domain.DerivativesSnapshot{}, false, fmt.Errorf("invalid derivatives asset class %q", assetClass)
	}
	var snapshot domain.DerivativesSnapshot
	var storedClass string
	var collectedAt, sourceTime int64
	var openInterest, funding sql.NullFloat64
	operator := "<"
	if inclusive {
		operator = "<="
	}
	query := `
		SELECT asset_class, instrument_id, symbol, collected_at, source_time,
		       price_usd, change_24h_pct, turnover_24h_usd, open_interest_usd, funding_rate
		FROM derivatives_market_snapshots
		WHERE asset_class = ? AND instrument_id = ? AND collected_at ` + operator + ` ?
		ORDER BY collected_at DESC
		LIMIT 1`
	err := s.db.QueryRowContext(ctx, query, assetClass, instrumentID, at.UnixMilli()).Scan(
		&storedClass, &snapshot.InstrumentID, &snapshot.Symbol, &collectedAt, &sourceTime,
		&snapshot.PriceUSD, &snapshot.Change24hPct, &snapshot.Turnover24hUSD,
		&openInterest, &funding,
	)
	if err == sql.ErrNoRows {
		return domain.DerivativesSnapshot{}, false, nil
	}
	if err != nil {
		return domain.DerivativesSnapshot{}, false, fmt.Errorf("load previous derivatives snapshot %s: %w", instrumentID, err)
	}

	snapshot.AssetClass = domain.AssetClass(storedClass)
	snapshot.CollectedAt = time.UnixMilli(collectedAt).UTC()
	snapshot.SourceTime = time.UnixMilli(sourceTime).UTC()
	snapshot.OpenInterestAvailable = openInterest.Valid
	snapshot.OpenInterestUSD = openInterest.Float64
	snapshot.FundingRateAvailable = funding.Valid
	snapshot.FundingRate = funding.Float64
	return snapshot, true, nil
}
