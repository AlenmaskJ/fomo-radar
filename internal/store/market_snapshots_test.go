package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestDerivativesSnapshotsPersistAndLoadPreviousObservation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "fomo.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	firstAt := time.Date(2026, time.September, 20, 8, 0, 0, 0, time.UTC)
	first := domain.DerivativesSnapshot{
		AssetClass: domain.AssetClassCrypto, InstrumentID: "BTC-USDT-SWAP", Symbol: "BTC",
		CollectedAt: firstAt, SourceTime: firstAt.Add(-time.Second), PriceUSD: 60_000,
		Change24hPct: 5, Turnover24hUSD: 1_200_000_000,
		OpenInterestUSD: 500_000_000, OpenInterestAvailable: true,
	}
	stock := domain.DerivativesSnapshot{
		AssetClass: domain.AssetClassStock, InstrumentID: "AAPL-USDT-SWAP", Symbol: "AAPL",
		CollectedAt: firstAt, SourceTime: firstAt, PriceUSD: 230,
		Change24hPct: 1.2, Turnover24hUSD: 25_000_000,
		FundingRate: 0.0001, FundingRateAvailable: true,
	}
	if err := s.InsertDerivativesSnapshots(ctx, []domain.DerivativesSnapshot{first, stock}); err != nil {
		t.Fatalf("InsertDerivativesSnapshots() error = %v", err)
	}

	got, ok, err := s.LatestDerivativesSnapshotBefore(ctx, domain.AssetClassCrypto, "BTC-USDT-SWAP", firstAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("LatestDerivativesSnapshotBefore() error = %v", err)
	}
	if !ok {
		t.Fatal("LatestDerivativesSnapshotBefore() ok = false, want true")
	}
	if got.AssetClass != domain.AssetClassCrypto || got.Symbol != "BTC" || got.OpenInterestUSD != 500_000_000 || !got.OpenInterestAvailable {
		t.Fatalf("loaded snapshot = %+v", got)
	}
	if got.FundingRateAvailable {
		t.Fatalf("FundingRateAvailable = true, want false")
	}

	second := first
	second.CollectedAt = firstAt.Add(time.Minute)
	second.OpenInterestUSD = 550_000_000
	if err := s.InsertDerivativesSnapshots(ctx, []domain.DerivativesSnapshot{second}); err != nil {
		t.Fatalf("second InsertDerivativesSnapshots() error = %v", err)
	}
	previous, ok, err := s.LatestDerivativesSnapshotBefore(ctx, domain.AssetClassCrypto, "BTC-USDT-SWAP", second.CollectedAt)
	if err != nil || !ok {
		t.Fatalf("previous snapshot = (%+v, %v, %v)", previous, ok, err)
	}
	if previous.CollectedAt != firstAt || previous.OpenInterestUSD != first.OpenInterestUSD {
		t.Fatalf("previous snapshot = %+v, want first observation", previous)
	}

	_, ok, err = s.LatestDerivativesSnapshotBefore(ctx, domain.AssetClassCrypto, "MISSING-USDT-SWAP", second.CollectedAt)
	if err != nil || ok {
		t.Fatalf("missing snapshot = (ok %v, err %v), want (false, nil)", ok, err)
	}
}

func TestDerivativesSnapshotQueriesAreScopedByAssetClass(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "scoped-snapshots.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	base := time.Date(2026, 9, 30, 2, 0, 0, 0, time.UTC)
	crypto := domain.DerivativesSnapshot{
		AssetClass: domain.AssetClassCrypto, InstrumentID: "SAME-USDT-SWAP", Symbol: "SAME",
		CollectedAt: base, SourceTime: base, PriceUSD: 10, Turnover24hUSD: 100,
		OpenInterestUSD: 11, OpenInterestAvailable: true,
	}
	stock := crypto
	stock.AssetClass = domain.AssetClassStock
	stock.CollectedAt = base.Add(time.Minute)
	stock.SourceTime = stock.CollectedAt
	stock.OpenInterestUSD = 22
	if err := s.InsertDerivativesSnapshots(ctx, []domain.DerivativesSnapshot{crypto, stock}); err != nil {
		t.Fatal(err)
	}

	gotCrypto, ok, err := s.DerivativesSnapshotAtOrBefore(ctx, domain.AssetClassCrypto, crypto.InstrumentID, stock.CollectedAt)
	if err != nil || !ok || gotCrypto.AssetClass != domain.AssetClassCrypto || gotCrypto.OpenInterestUSD != 11 {
		t.Fatalf("crypto snapshot/ok/error = %+v/%v/%v", gotCrypto, ok, err)
	}
	gotStock, ok, err := s.LatestDerivativesSnapshotBefore(ctx, domain.AssetClassStock, stock.InstrumentID, stock.CollectedAt.Add(time.Minute))
	if err != nil || !ok || gotStock.AssetClass != domain.AssetClassStock || gotStock.OpenInterestUSD != 22 {
		t.Fatalf("stock snapshot/ok/error = %+v/%v/%v", gotStock, ok, err)
	}
	if _, _, err := s.DerivativesSnapshotAtOrBefore(ctx, domain.AssetClass("forex"), stock.InstrumentID, stock.CollectedAt); err == nil {
		t.Fatal("unknown asset class should be rejected")
	}
}
