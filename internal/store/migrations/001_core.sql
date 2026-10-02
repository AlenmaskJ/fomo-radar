CREATE TABLE IF NOT EXISTS tokens (
  id TEXT PRIMARY KEY,
  chain TEXT NOT NULL,
  address TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  symbol TEXT NOT NULL DEFAULT '',
  pair_address TEXT NOT NULL DEFAULT '',
  first_seen_at INTEGER NOT NULL,
  first_seen_price_usd REAL,
  first_seen_market_cap_usd REAL,
  first_seen_score REAL,
  created_at INTEGER,
  UNIQUE(chain, address)
);

CREATE TABLE IF NOT EXISTS scan_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  mode TEXT NOT NULL,
  started_at INTEGER NOT NULL,
  finished_at INTEGER,
  status TEXT NOT NULL,
  candidates_seen INTEGER NOT NULL DEFAULT 0,
  candidates_scored INTEGER NOT NULL DEFAULT 0,
  error_summary TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS score_versions (
  version TEXT PRIMARY KEY,
  config_json TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_id TEXT NOT NULL REFERENCES tokens(id),
  scan_run_id INTEGER REFERENCES scan_runs(id),
  collected_at INTEGER NOT NULL,
  source_time INTEGER,
  price_usd REAL,
  market_cap_usd REAL,
  fdv_usd REAL,
  liquidity_usd REAL,
  volume_m5_usd REAL,
  volume_h1_usd REAL,
  buys_m5 INTEGER,
  sells_m5 INTEGER,
  buyers_m5 INTEGER,
  sellers_m5 INTEGER,
  price_change_m5 REAL,
  score REAL,
  tier TEXT NOT NULL,
  score_version TEXT NOT NULL,
  score_breakdown_json TEXT NOT NULL,
  risk_flags_json TEXT NOT NULL,
  data_quality_json TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_snapshots_token_time ON snapshots(token_id, collected_at DESC);
CREATE INDEX IF NOT EXISTS idx_snapshots_score_time ON snapshots(score DESC, collected_at DESC);
