CREATE TABLE IF NOT EXISTS market_opportunity_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at INTEGER NOT NULL,
  finished_at INTEGER,
  status TEXT NOT NULL CHECK(status IN ('running','completed','degraded','failed')),
  trigger TEXT NOT NULL CHECK(trigger IN ('startup','scheduled','manual','cli')),
  pool_size INTEGER NOT NULL DEFAULT 0,
  candidate_count INTEGER NOT NULL DEFAULT 0,
  error_summary TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS market_opportunities (
  run_id INTEGER NOT NULL REFERENCES market_opportunity_runs(id) ON DELETE CASCADE,
  instrument_id TEXT NOT NULL,
  symbol TEXT NOT NULL,
  fomo_score INTEGER NOT NULL,
  opportunity_score INTEGER NOT NULL,
  stage TEXT NOT NULL,
  high_volatility INTEGER NOT NULL CHECK(high_volatility IN (0,1)),
  payload_json TEXT NOT NULL,
  PRIMARY KEY(run_id, instrument_id)
);

CREATE INDEX IF NOT EXISTS idx_market_runs_finished
  ON market_opportunity_runs(status, finished_at DESC, id DESC);
