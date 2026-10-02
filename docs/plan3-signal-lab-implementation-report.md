# Plan 3 Signal Lab Implementation Report

Date: 2026-08-22

## Status

Plan 3 Signal Lab is implemented as an isolated process and database. Core Scanner,
Dashboard, discovery, filters, provider pacing, DeepQueue, and `FOMO_SCORE_V1.0`
were not changed.

## Delivered behavior

- `fomo-lab` reads Core `fomo.db` through a strict read-only SQLite connection.
- All Lab state is stored in the independent `fomo-lab.db`.
- `-run-once` and `-watch` reuse the same incremental processing cycle.
- `-report` is the only mode that materializes threshold statistics.
- The first initialization persists `activation_snapshot_id`, canonical source path,
  stable `source_db_identity`, and the incremental high-watermark.
- Restart validation rejects a different Signal Lab algorithm version.
- Threshold signals use raw score thresholds `55, 60, 65, 70, 75, 80, 85, 90`.
- A signal uses the first observed qualifying snapshot in `snapshot_id ASC` order.
- Entry price is accepted only from the signal snapshot. Missing price produces
  `UNAVAILABLE_PRICE` and terminal `NOT_EVALUABLE` outcomes.
- Outcome reads require both a later snapshot ID and a later observation time, and
  are capped by the cycle's persisted source high-watermark.
- Horizon selection uses the approved target/tolerance windows and the first valid
  price in each window. MFE/MAE use only that horizon's real observed path.
- `MATURED`, `INSUFFICIENT_DATA`, and `NOT_EVALUABLE` are immutable terminal states.
- Reports separate `BACKFILL`, `LIVE`, and `ALL`, including chain and evidence
  confidence scopes.
- Report rows are projected strictly to their declared persisted high-watermark;
  signals above it are excluded and outcomes evaluated above it remain `PENDING`.

## Files

- `cmd/fomo-lab/main.go`
- `cmd/fomo-lab/main_test.go`
- `internal/lab/models.go`
- `internal/lab/repository.go`
- `internal/lab/source.go`
- `internal/lab/processor.go`
- `internal/lab/outcomes.go`
- `internal/lab/analysis.go`
- `internal/lab/report.go`
- `internal/lab/schema/001_signal_lab.sql`
- focused tests under `internal/lab/*_test.go`
- `docs/superpowers/specs/2026-08-22-fomo-signal-lab-design.md`
- `docs/superpowers/plans/2026-08-22-fomo-signal-lab.md`

## Live acceptance

Acceptance artifacts are under:

`/.superpowers/acceptance/plan3-signal-lab-20260822-2249/`

The source fixture started with `activation_snapshot_id=212`. A live Scanner watch
then appended real public BSC and Solana observations while Lab watch processed the
same database.

Incremental cycles:

| Cycle | Source high-watermark | Processed snapshots | New signals | Source |
|---:|---:|---:|---:|---|
| Initial | 212 | 212 | 453 | BACKFILL |
| Live 1 | 213 | 1 | 0 | LIVE |
| Live 2 | 287 | 74 | 227 | LIVE |
| Live 3 | 361 | 74 | 135 | LIVE |
| Final catch-up | 438 | 77 | 143 | LIVE |
| Idempotency rerun | 438 | 0 | 0 | none |

Persisted totals:

| Scope | Signals | Evaluable entries | Entry price coverage |
|---|---:|---:|---:|
| BACKFILL | 453 | 453 | 100% |
| LIVE | 505 | 505 | 100% |
| ALL | 958 | 958 | 100% |

Current persisted outcome state:

- BACKFILL 5m: 81 `MATURED`, 372 `INSUFFICIENT_DATA`.
- BACKFILL 15m/30m/1h/3h/6h: 453 `INSUFFICIENT_DATA` per horizon.
- BACKFILL 24h: 453 `PENDING`; its window end is after the current source frontier.
- LIVE: 505 `PENDING` per horizon; no live horizon was old enough during acceptance.
- The real sample had full entry price coverage, so `NOT_EVALUABLE` was exercised by
  regression tests rather than manufactured live data.

The report materialized 2,016 scoped statistics rows per analysis run. For BACKFILL
5m, only 8-11 outcomes were mature at each threshold, so every threshold is correctly
marked `insufficient_sample=true`; no predictive conclusion is claimed.

## No-future-data audit

Read-only SQL checks on the acceptance Lab database found zero violations for:

- duplicate `(token_id, score_version, threshold)` signal keys;
- entry snapshot differing from the signal snapshot;
- selected snapshot ID not strictly after the signal snapshot;
- selected observation time not strictly after the signal time;
- selected snapshot ID above `evaluation_source_high_watermark`;
- selected observation outside its target/tolerance window;
- terminal outcome metric/null shape;
- `UNAVAILABLE_PRICE` outcomes not marked `NOT_EVALUABLE`.

The final Lab no-op run left the source database SHA-256 and modification time
unchanged. Source identity was persisted alongside the canonical path.

## Performance

- Initial BACKFILL cycle: about 843 ms for 212 snapshots and 453 signals.
- Idempotent no-op cycle: about 363 ms.
- Lab watch peak working set: 36.66 MiB.
- Explicit report peak working set: 37.98 MiB.
- Concurrent Scanner + Lab sampled peak: about 93.07 MiB combined.
- Scanner Fast discovery cycles observed during acceptance: 54.84 s and 89.47 s.

## Commands

```powershell
.\fomo-lab.exe -run-once -source-db .\fomo.db -lab-db .\fomo-lab.db
.\fomo-lab.exe -watch -source-db .\fomo.db -lab-db .\fomo-lab.db -interval 2m
.\fomo-lab.exe -report -source-db .\fomo.db -lab-db .\fomo-lab.db -format table
.\fomo-lab.exe -report -source-db .\fomo.db -lab-db .\fomo-lab.db -format json
```

## Remaining limits

- LIVE horizon statistics are not yet mature and must accumulate during normal watch.
- Current BACKFILL 5m samples are below the approved minimum of 20 mature outcomes.
- SQLite `snapshot_id` monotonicity remains a required Core database contract.
- No Dashboard, Social, or Telegram integration is included in this plan.
