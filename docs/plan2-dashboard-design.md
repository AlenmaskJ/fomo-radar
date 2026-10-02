# Plan 2 Phase 1 Dashboard Design

## Scope

Phase 1 adds a separate, read-only Web Dashboard around the frozen Plan 1
scanner database. The refinement turns the initial token table into a Chinese
FOMO activity Radar without modifying Discovery, FOMO_SCORE_V1.0, filters,
DeepQueue, provider pacing, or historical snapshots. Telegram remains out of
scope.

Runtime remains two Go processes:

```text
fomo-scanner -watch  -> SQLite WAL writer
fomo-server -serve  -> SQLite read-only HTML/JSON
```

The Dashboard never calls DexScreener or GeckoTerminal.

## Components

- `cmd/fomo-server`: independent HTTP process and flags.
- `internal/web/repository.go`: read-only SQLite queries and persisted status.
- `internal/web/models.go`: explicit HTML/JSON view models.
- `internal/web/server.go`: routes, filters, UTF-8 responses, and UI labels.
- `internal/web/templates`: Chinese server-rendered pages.
- `internal/web/assets/style.css`: embedded responsive styling.

No Node service, frontend build, external CDN, Redis, Docker, or new database
is introduced.

## Read-only Contract

The server opens an existing database with SQLite URI `mode=ro`, then enables
`PRAGMA query_only=ON`, `busy_timeout=5000`, and a single open/idle connection.
It verifies WAL mode and the Plan 1.1 schema without running migrations.

Scanner writes remain authoritative. Missing values stay nullable and render
as `N/A`; the Dashboard never replaces missing evidence with zero. Requests
have bounded contexts and cannot insert, update, delete, or migrate data.

## Current Snapshot Lineage

Current state starts at `tokens.latest_fast_snapshot_id`. A Deep snapshot is
selected only when its `base_fast_snapshot_id` matches that exact Fast
generation. A late Deep result cannot replace a newer Fast snapshot.

Scores, tiers, evidence, factors, risk values, and timeline rows are read from
stored snapshots. Score changes and momentum deltas are display-only
subtractions between stored rows; FOMO Score is never recalculated.

## Radar

`GET /` returns at most 30 candidates:

- all `BREAKOUT`;
- all `FAST_RISING`;
- `WATCH` only when Evidence Confidence is `HIGH` or `GOOD`.

`MEDIUM` and `LOW` WATCH candidates stay on `/tokens` only.

Ordering is:

1. Effective Tier;
2. stored Score change within the preceding five minutes;
3. Raw Score;
4. Evidence Confidence;
5. younger discovery/pair age.

Each row explains current momentum with stored 5-minute volume, volume change,
buyers, buyer change, buy/sell ratio, Score change, market values, evidence,
launch/source quality, and Fast/Deep state. Insufficient history renders
`N/A`.

## Token Archive

`GET /tokens` reads up to 500 current candidates, including low confidence,
Gecko-only, partial data, failed/deferred Deep, reactivations, and `HIDDEN`
cooled candidates.

Server-side filters:

- `q`: Unicode name, Symbol, or CA substring;
- `chain`: `bsc` or `solana`;
- `tier`: Effective Tier;
- `launch`: `NEW_LAUNCH` or `REACTIVATION`;
- `evidence`: `HIGH`, `GOOD`, `MEDIUM`, or `LOW`;
- `data`: `full` or `partial`.

SQLite `instr(lower(...), lower(?))` keeps Chinese and emoji intact. No
ASCII-only normalization is used.

## Scanner Status Strip

The Radar reads persisted `scan_runs`, `discovery_coverages`, and current Fast
snapshot Deep states. It displays:

- monitoring/stopped state;
- last completed Fast Scan and next two-minute cadence time;
- actual BSC and Solana coverage spans, pages, and candidate counts;
- persisted Deep waiting/completed/failed counts;
- Gecko and Dex health inferred from the latest completed scan.

If a new watch run is already `running`, monitoring state comes from that run,
while provider health and completed counters remain from the latest finished
run. This prevents an in-progress run from erasing known provider degradation.

Deep workers do not persist a distinct `running` state. The UI therefore shows
Deep Running as `N/A`, never a fabricated zero. Coverage is always the stored
observed API window; the UI never claims two-hour coverage.

## Token Detail

`GET /token/{address}` preserves Solana address case and compares BSC addresses
case-insensitively. It displays:

- name, Symbol, chain, CA, pair, and copy-CA control;
- MC, Liquidity, FDV, volume, and age;
- Raw Score, Raw Tier, Effective Tier, and Evidence Confidence;
- all FOMO_SCORE_V1.0 factors, Early Bonus, and Risk Penalty;
- unique buyers/sellers, trades, Deep state, and `N/A` for unpersisted
  truncation state;
- launch type, discovery-pool age, selected-pair age, source, data quality,
  and Chinese risk labels;
- up to 100 immutable stored snapshots and a lightweight Score timeline.

A decline of at least 20 points across displayed stored history shows
“评分快速回落”. This is display-only and creates no trading rule.

## JSON API

`GET /api/tokens` accepts the same filters as `/tokens` and returns UTF-8 JSON.
Chinese and emoji remain literal UTF-8; missing values are JSON `null`.
Detailed factor/risk evidence remains present in API DTOs. Encoding streams to
the response to avoid an extra full-response buffer.

## Unicode and Safety

Pages use `<meta charset="utf-8">`, `Content-Type` includes `charset=utf-8`,
and Go `html/template` preserves Unicode while escaping unsafe HTML. Database
names/Symbols remain unchanged. UI enum translation is presentation-only;
persisted enum values stay English.

## Resource Model

The server uses one SQLite connection, embedded assets, bounded result counts,
and no background polling. Recommended low-resource startup sets
`GOMAXPROCS=2`; this reduced Windows mixed-route peak Working Set from about
53.4 MiB to 35.8 MiB without changing scanner behavior.

## Routes

| Route | Result |
| --- | --- |
| `GET /` | High-quality activity Radar and scanner status |
| `GET /tokens` | Filterable full current candidate archive |
| `GET /token/{address}` | Stored evidence and timeline detail |
| `GET /api/tokens` | Filterable UTF-8 JSON DTO envelope |

Unsupported methods return `405`, missing tokens return `404`, ambiguous
cross-chain addresses return `409`, and unavailable SQLite reads return opaque
`503` responses.

## Acceptance Boundary

Phase 1 completes after Unicode/search/range/Tier Gate/N/A/read-only/Coverage
tests pass, concurrent Scanner/Server WAL reads show no lock failures,
screenshots pass desktop/mobile visual QA, and resource measurements are
recorded. Work then stops; Telegram Phase is not started.
