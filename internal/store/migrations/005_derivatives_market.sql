CREATE TABLE IF NOT EXISTS derivatives_market_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  asset_class TEXT NOT NULL CHECK (asset_class IN ('crypto', 'stock')),
  instrument_id TEXT NOT NULL,
  symbol TEXT NOT NULL,
  collected_at INTEGER NOT NULL,
  source_time INTEGER NOT NULL,
  price_usd REAL NOT NULL,
  change_24h_pct REAL NOT NULL,
  turnover_24h_usd REAL NOT NULL,
  open_interest_usd REAL,
  funding_rate REAL,
  source TEXT NOT NULL DEFAULT 'okx',
  UNIQUE(instrument_id, collected_at)
);

CREATE INDEX IF NOT EXISTS idx_derivatives_market_instrument_time
  ON derivatives_market_snapshots(instrument_id, collected_at DESC);

CREATE INDEX IF NOT EXISTS idx_derivatives_market_class_time
  ON derivatives_market_snapshots(asset_class, collected_at DESC);
