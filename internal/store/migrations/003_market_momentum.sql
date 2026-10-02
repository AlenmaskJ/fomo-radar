CREATE TABLE IF NOT EXISTS momentum_state (
  token_id TEXT PRIMARY KEY REFERENCES tokens(id),
  observed_at INTEGER NOT NULL,
  pair_address TEXT NOT NULL DEFAULT '',
  price_usd REAL,
  liquidity_usd REAL,
  volume_m5_usd REAL,
  volume_h1_usd REAL,
  volume_h6_usd REAL,
  volume_h24_usd REAL,
  aggregate_buyers_m5 INTEGER,
  aggregate_buyers_h1 INTEGER,
  sources_json TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS momentum_universe (
  token_id TEXT PRIMARY KEY REFERENCES tokens(id),
  chain TEXT NOT NULL,
  admission_source TEXT NOT NULL,
  admission_priority INTEGER NOT NULL,
  first_added_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  last_momentum_at INTEGER,
  last_trade_at INTEGER,
  liquidity_usd REAL,
  volume_h24_usd REAL,
  last_scanned_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_momentum_universe_chain_cursor
  ON momentum_universe(chain, token_id);

CREATE TABLE IF NOT EXISTS momentum_scan_cursors (
  chain TEXT PRIMARY KEY,
  last_token_id TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS discovery_source_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scan_run_id INTEGER NOT NULL REFERENCES scan_runs(id),
  chain TEXT NOT NULL,
  provider TEXT NOT NULL,
  source TEXT NOT NULL,
  started_at INTEGER NOT NULL,
  finished_at INTEGER,
  pages_fetched INTEGER NOT NULL DEFAULT 0,
  returned_items INTEGER NOT NULL DEFAULT 0,
  unique_candidates INTEGER NOT NULL DEFAULT 0,
  newest_pool_at INTEGER,
  oldest_pool_at INTEGER,
  cursor_before TEXT NOT NULL DEFAULT '',
  cursor_after TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  UNIQUE(scan_run_id, chain, source)
);

CREATE INDEX IF NOT EXISTS idx_discovery_source_runs_run
  ON discovery_source_runs(scan_run_id, chain, source);

CREATE INDEX IF NOT EXISTS idx_snapshots_candidate_origin_time
  ON snapshots(candidate_origin, collected_at DESC);
