CREATE TABLE IF NOT EXISTS lab_schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS lab_state (
  singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
  schema_version INTEGER NOT NULL,
  algorithm_version TEXT NOT NULL,
  canonical_source_path TEXT NOT NULL,
  source_db_identity TEXT NOT NULL,
  activation_snapshot_id INTEGER NOT NULL CHECK (activation_snapshot_id >= 0),
  last_processed_snapshot_id INTEGER NOT NULL CHECK (last_processed_snapshot_id >= 0),
  last_cycle_source_high_watermark INTEGER NOT NULL CHECK (last_cycle_source_high_watermark >= 0),
  source_observation_frontier_at INTEGER,
  initialized_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS signals (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_id TEXT NOT NULL,
  chain TEXT NOT NULL,
  address TEXT NOT NULL,
  name TEXT NOT NULL,
  symbol TEXT NOT NULL,
  signal_source TEXT NOT NULL CHECK (signal_source IN ('BACKFILL','LIVE')),
  threshold INTEGER NOT NULL CHECK (threshold IN (55,60,65,70,75,80,85,90)),
  signal_snapshot_id INTEGER NOT NULL,
  signal_at INTEGER NOT NULL,
  source_time INTEGER,
  price_usd REAL,
  market_cap_usd REAL,
  liquidity_usd REAL,
  token_age_seconds INTEGER,
  raw_score REAL NOT NULL,
  raw_tier TEXT NOT NULL,
  effective_tier TEXT NOT NULL,
  evidence_confidence TEXT NOT NULL,
  evidence_available INTEGER NOT NULL,
  evidence_total INTEGER NOT NULL,
  score_version TEXT NOT NULL,
  score_config_json TEXT NOT NULL,
  score_breakdown_json TEXT NOT NULL,
  risk_flags_json TEXT NOT NULL,
  data_quality_json TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(token_id, score_version, threshold),
  UNIQUE(id, signal_snapshot_id)
);

CREATE TABLE IF NOT EXISTS paper_entries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  signal_id INTEGER NOT NULL UNIQUE,
  entry_snapshot_id INTEGER NOT NULL,
  entry_at INTEGER NOT NULL,
  entry_status TEXT NOT NULL CHECK (entry_status IN ('EVALUABLE','UNAVAILABLE_PRICE')),
  entry_price_usd REAL,
  created_at INTEGER NOT NULL,
  FOREIGN KEY(signal_id, entry_snapshot_id) REFERENCES signals(id, signal_snapshot_id),
  CHECK (
    (entry_status='EVALUABLE' AND entry_price_usd IS NOT NULL AND entry_price_usd > 0) OR
    (entry_status='UNAVAILABLE_PRICE' AND entry_price_usd IS NULL)
  )
);

CREATE TABLE IF NOT EXISTS horizon_outcomes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  signal_id INTEGER NOT NULL REFERENCES signals(id),
  horizon TEXT NOT NULL CHECK (horizon IN ('5m','15m','30m','1h','3h','6h','24h')),
  target_at INTEGER NOT NULL,
  tolerance_seconds INTEGER NOT NULL,
  window_end_at INTEGER NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('PENDING','MATURED','INSUFFICIENT_DATA','NOT_EVALUABLE')),
  status_reason TEXT NOT NULL DEFAULT '',
  selected_source_snapshot_id INTEGER,
  selected_snapshot_at INTEGER,
  selection_delay_seconds INTEGER,
  return_decimal REAL,
  mfe_decimal REAL,
  mae_decimal REAL,
  evaluation_source_high_watermark INTEGER,
  source_observation_frontier_at INTEGER,
  finalized_at INTEGER,
  created_at INTEGER NOT NULL,
  UNIQUE(signal_id, horizon)
);

CREATE TABLE IF NOT EXISTS analysis_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  algorithm_version TEXT NOT NULL,
  generated_at INTEGER NOT NULL,
  persisted_through_snapshot_id INTEGER NOT NULL,
  source_observation_frontier_at INTEGER
);

CREATE TABLE IF NOT EXISTS threshold_statistics (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  analysis_run_id INTEGER NOT NULL REFERENCES analysis_runs(id),
  signal_source_scope TEXT NOT NULL CHECK (signal_source_scope IN ('BACKFILL','LIVE','ALL')),
  chain_scope TEXT NOT NULL CHECK (chain_scope IN ('BSC','SOLANA','ALL')),
  evidence_scope TEXT NOT NULL CHECK (evidence_scope IN ('HIGH_GOOD','MEDIUM_LOW','UNKNOWN','ALL')),
  score_version TEXT NOT NULL,
  threshold INTEGER NOT NULL CHECK (threshold IN (55,60,65,70,75,80,85,90)),
  horizon TEXT NOT NULL CHECK (horizon IN ('5m','15m','30m','1h','3h','6h','24h')),
  signals_count INTEGER NOT NULL,
  evaluable_entries INTEGER NOT NULL,
  not_evaluable INTEGER NOT NULL,
  entry_price_coverage REAL,
  pending_count INTEGER NOT NULL,
  matured_count INTEGER NOT NULL,
  insufficient_data_count INTEGER NOT NULL,
  maturity_coverage REAL,
  result_coverage REAL,
  positive_return_rate REAL,
  median_return_decimal REAL,
  median_mfe_decimal REAL,
  median_mae_decimal REAL,
  insufficient_sample INTEGER NOT NULL CHECK (insufficient_sample IN (0,1)),
  UNIQUE(analysis_run_id, signal_source_scope, chain_scope, evidence_scope,
         score_version, threshold, horizon)
);

CREATE INDEX IF NOT EXISTS idx_signals_source_threshold
  ON signals(signal_source, score_version, threshold);
CREATE INDEX IF NOT EXISTS idx_entries_status ON paper_entries(entry_status, signal_id);
CREATE INDEX IF NOT EXISTS idx_outcomes_pending
  ON horizon_outcomes(status, id) WHERE status='PENDING';
CREATE INDEX IF NOT EXISTS idx_statistics_run ON threshold_statistics(analysis_run_id, id);

CREATE TRIGGER IF NOT EXISTS trg_terminal_outcome_update
BEFORE UPDATE ON horizon_outcomes
WHEN OLD.status IN ('MATURED','INSUFFICIENT_DATA','NOT_EVALUABLE')
BEGIN
  SELECT RAISE(ABORT, 'terminal outcome is immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_terminal_outcome_delete
BEFORE DELETE ON horizon_outcomes
WHEN OLD.status IN ('MATURED','INSUFFICIENT_DATA','NOT_EVALUABLE')
BEGIN
  SELECT RAISE(ABORT, 'terminal outcome is immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_analysis_run_update
BEFORE UPDATE ON analysis_runs BEGIN SELECT RAISE(ABORT, 'analysis run is immutable'); END;
CREATE TRIGGER IF NOT EXISTS trg_analysis_run_delete
BEFORE DELETE ON analysis_runs BEGIN SELECT RAISE(ABORT, 'analysis run is immutable'); END;
CREATE TRIGGER IF NOT EXISTS trg_statistic_update
BEFORE UPDATE ON threshold_statistics BEGIN SELECT RAISE(ABORT, 'threshold statistic is immutable'); END;
CREATE TRIGGER IF NOT EXISTS trg_statistic_delete
BEFORE DELETE ON threshold_statistics BEGIN SELECT RAISE(ABORT, 'threshold statistic is immutable'); END;
