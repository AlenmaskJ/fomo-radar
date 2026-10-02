CREATE TABLE IF NOT EXISTS snapshot_scores (
  snapshot_id INTEGER NOT NULL REFERENCES snapshots(id),
  score_version TEXT NOT NULL REFERENCES score_versions(version),
  evaluation_role TEXT NOT NULL CHECK (evaluation_role IN ('CHAMPION','CHALLENGER')),
  raw_score REAL NOT NULL,
  raw_tier TEXT NOT NULL,
  effective_tier TEXT NOT NULL,
  evidence_available INTEGER NOT NULL,
  evidence_total INTEGER NOT NULL,
  evidence_confidence TEXT NOT NULL,
  score_breakdown_json TEXT NOT NULL,
  risk_flags_json TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(snapshot_id, score_version)
);

CREATE INDEX IF NOT EXISTS idx_snapshot_scores_version_snapshot
  ON snapshot_scores(score_version, snapshot_id);

CREATE TABLE IF NOT EXISTS score_runtime_roles (
  score_version TEXT PRIMARY KEY REFERENCES score_versions(version),
  role TEXT NOT NULL CHECK (role IN ('CHAMPION','CHALLENGER','RETIRED')),
  changed_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_score_one_champion
  ON score_runtime_roles(role) WHERE role = 'CHAMPION';

CREATE INDEX IF NOT EXISTS idx_score_runtime_roles_active
  ON score_runtime_roles(role, score_version);

CREATE TABLE IF NOT EXISTS score_role_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_type TEXT NOT NULL CHECK (event_type IN ('BOOTSTRAP','ENABLE','DISABLE','PROMOTE')),
  subject_version TEXT NOT NULL REFERENCES score_versions(version),
  from_role TEXT CHECK (from_role IS NULL OR from_role IN ('CHAMPION','CHALLENGER','RETIRED')),
  to_role TEXT NOT NULL CHECK (to_role IN ('CHAMPION','CHALLENGER','RETIRED')),
  previous_champion_version TEXT REFERENCES score_versions(version),
  new_champion_version TEXT REFERENCES score_versions(version),
  reason TEXT NOT NULL CHECK (length(trim(reason)) > 0),
  occurred_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_score_role_events_time
  ON score_role_events(occurred_at, id);

CREATE TRIGGER IF NOT EXISTS snapshot_scores_no_update
BEFORE UPDATE ON snapshot_scores
BEGIN
  SELECT RAISE(ABORT, 'snapshot_scores are immutable');
END;

CREATE TRIGGER IF NOT EXISTS snapshot_scores_no_delete
BEFORE DELETE ON snapshot_scores
BEGIN
  SELECT RAISE(ABORT, 'snapshot_scores are immutable');
END;

CREATE TRIGGER IF NOT EXISTS score_role_events_no_update
BEFORE UPDATE ON score_role_events
BEGIN
  SELECT RAISE(ABORT, 'score_role_events are immutable');
END;

CREATE TRIGGER IF NOT EXISTS score_role_events_no_delete
BEFORE DELETE ON score_role_events
BEGIN
  SELECT RAISE(ABORT, 'score_role_events are immutable');
END;

INSERT OR IGNORE INTO snapshot_scores (
  snapshot_id, score_version, evaluation_role, raw_score, raw_tier,
  effective_tier, evidence_available, evidence_total, evidence_confidence,
  score_breakdown_json, risk_flags_json, created_at
)
SELECT
  s.id,
  s.score_version,
  'CHAMPION',
  COALESCE(s.raw_score, s.score),
  COALESCE(NULLIF(s.raw_tier, ''), s.tier),
  COALESCE(NULLIF(s.effective_tier, ''), s.tier),
  s.evidence_available,
  s.evidence_total,
  s.evidence_confidence,
  s.score_breakdown_json,
  s.risk_flags_json,
  s.collected_at
FROM snapshots AS s
JOIN score_versions AS sv ON sv.version = s.score_version
WHERE s.score IS NOT NULL;
