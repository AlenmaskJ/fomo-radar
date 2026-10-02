CREATE INDEX IF NOT EXISTS idx_market_runs_class_finished
  ON market_opportunity_runs(asset_class, status, finished_at DESC, id DESC);
