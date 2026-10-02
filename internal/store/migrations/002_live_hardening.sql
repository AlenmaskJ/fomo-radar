CREATE TABLE IF NOT EXISTS discovery_coverages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scan_run_id INTEGER NOT NULL REFERENCES scan_runs(id),
  chain TEXT NOT NULL,
  scan_started_at INTEGER NOT NULL,
  newest_pool_at INTEGER,
  oldest_pool_at INTEGER,
  pages_fetched INTEGER NOT NULL,
  unique_candidates INTEGER NOT NULL,
  UNIQUE(scan_run_id, chain)
);

CREATE INDEX IF NOT EXISTS idx_discovery_coverages_run ON discovery_coverages(scan_run_id, chain);
CREATE UNIQUE INDEX IF NOT EXISTS idx_snapshots_one_deep_per_fast
  ON snapshots(base_fast_snapshot_id)
  WHERE stage = 'DEEP' AND base_fast_snapshot_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_snapshots_pending_deep
  ON snapshots(stage, deep_status, id)
  WHERE stage = 'FAST';
