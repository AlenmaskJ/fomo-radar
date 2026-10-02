package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/alen1/fomo-radar/internal/domain"
)

// InsertScoredSnapshot 原子保存一个市场观察及其全部版本评分。
func (s *Store) InsertScoredSnapshot(ctx context.Context, snapshot domain.MarketSnapshot, scores []domain.VersionedScore) (int64, error) {
	champion, err := validateVersionedScores(scores)
	if err != nil {
		return 0, err
	}
	snapshot = projectChampion(snapshot, champion)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin scored snapshot insert: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	id, err := s.insertScoredSnapshotTx(ctx, tx, snapshot, scores)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit scored snapshot insert: %w", err)
	}
	return id, nil
}

func (s *Store) insertScoredSnapshotTx(ctx context.Context, tx *sql.Tx, snapshot domain.MarketSnapshot, scores []domain.VersionedScore) (int64, error) {
	id, err := s.insertSnapshotTx(ctx, tx, snapshot)
	if err != nil {
		return 0, err
	}
	if err := insertSnapshotScoresTx(ctx, tx, id, snapshot.CollectedAt.UnixMilli(), scores); err != nil {
		return 0, err
	}
	return id, nil
}

func insertSnapshotScoresTx(ctx context.Context, tx *sql.Tx, snapshotID, createdAt int64, scores []domain.VersionedScore) error {
	for _, scored := range scores {
		breakdownJSON, err := json.Marshal(scored.Breakdown)
		if err != nil {
			return fmt.Errorf("marshal %s score breakdown: %w", scored.Version, err)
		}
		riskFlagsJSON, err := json.Marshal(nonNilStrings(scored.Breakdown.RiskFlags))
		if err != nil {
			return fmt.Errorf("marshal %s risk flags: %w", scored.Version, err)
		}
		breakdown := scored.Breakdown
		rawTier := breakdown.RawTier
		if rawTier == "" {
			rawTier = breakdown.Tier
		}
		effectiveTier := breakdown.EffectiveTier
		if effectiveTier == "" {
			effectiveTier = breakdown.Tier
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO snapshot_scores(
				snapshot_id, score_version, evaluation_role, raw_score, raw_tier,
				effective_tier, evidence_available, evidence_total, evidence_confidence,
				score_breakdown_json, risk_flags_json, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			snapshotID, scored.Version, scored.Role, breakdown.RawScore, rawTier,
			effectiveTier, breakdown.EvidenceAvailable, breakdown.EvidenceTotal,
			breakdown.EvidenceConfidence, string(breakdownJSON), string(riskFlagsJSON), createdAt,
		); err != nil {
			return fmt.Errorf("insert snapshot %d score %s: %w", snapshotID, scored.Version, err)
		}
	}
	return nil
}

func validateVersionedScores(scores []domain.VersionedScore) (domain.VersionedScore, error) {
	if len(scores) == 0 {
		return domain.VersionedScore{}, fmt.Errorf("scored snapshot requires at least one score")
	}
	seen := make(map[string]struct{}, len(scores))
	champions := 0
	challengers := 0
	var champion domain.VersionedScore
	for _, scored := range scores {
		if scored.Version == "" {
			return domain.VersionedScore{}, fmt.Errorf("score version is required")
		}
		if _, duplicate := seen[scored.Version]; duplicate {
			return domain.VersionedScore{}, fmt.Errorf("duplicate score version %s", scored.Version)
		}
		seen[scored.Version] = struct{}{}
		if scored.Breakdown.Version != scored.Version {
			return domain.VersionedScore{}, fmt.Errorf("score version %s conflicts with breakdown version %s", scored.Version, scored.Breakdown.Version)
		}
		switch scored.Role {
		case domain.ScoreRoleChampion:
			champions++
			champion = scored
		case domain.ScoreRoleChallenger:
			challengers++
		default:
			return domain.VersionedScore{}, fmt.Errorf("score version %s has invalid evaluation role %q", scored.Version, scored.Role)
		}
	}
	if champions != 1 {
		return domain.VersionedScore{}, fmt.Errorf("scored snapshot has %d Champions, want exactly one", champions)
	}
	if challengers > maxActiveChallengers {
		return domain.VersionedScore{}, fmt.Errorf("scored snapshot has %d Challengers, maximum is three", challengers)
	}
	return champion, nil
}

func projectChampion(snapshot domain.MarketSnapshot, champion domain.VersionedScore) domain.MarketSnapshot {
	breakdown := champion.Breakdown
	collectedAt := snapshot.Score.CollectedAt
	if collectedAt.IsZero() {
		collectedAt = snapshot.CollectedAt
	}
	snapshot.Score = domain.DataValue[float64]{
		Value:       breakdown.RawScore,
		Quality:     domain.QualityFresh,
		Source:      champion.Version,
		CollectedAt: collectedAt,
	}
	snapshot.Tier = string(breakdown.EffectiveTier)
	if snapshot.Tier == "" {
		snapshot.Tier = string(breakdown.Tier)
	}
	snapshot.ScoreVersion = champion.Version
	snapshot.ScoreBreakdown = breakdown
	snapshot.RiskFlags = append([]string(nil), breakdown.RiskFlags...)
	if snapshot.DataQuality == nil {
		snapshot.DataQuality = map[string]domain.Quality{}
	}
	snapshot.DataQuality["score"] = domain.QualityFresh
	return snapshot
}

// SnapshotScoreAssignments 返回父快照评分时冻结的版本、角色和不可变配置。
func (s *Store) SnapshotScoreAssignments(ctx context.Context, snapshotID int64) ([]domain.ScoreAssignment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ss.score_version, ss.evaluation_role, v.config_json, v.created_at, ss.created_at
		FROM snapshot_scores AS ss
		JOIN score_versions AS v ON v.version=ss.score_version
		WHERE ss.snapshot_id=?
		ORDER BY CASE ss.evaluation_role WHEN 'CHAMPION' THEN 0 ELSE 1 END, ss.score_version`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("query snapshot %d score assignments: %w", snapshotID, err)
	}
	defer rows.Close() //nolint:errcheck
	assignments := make([]domain.ScoreAssignment, 0, 4)
	for rows.Next() {
		var assignment domain.ScoreAssignment
		var config string
		var createdAt, changedAt int64
		if err := rows.Scan(&assignment.Version, &assignment.Role, &config, &createdAt, &changedAt); err != nil {
			return nil, fmt.Errorf("scan snapshot %d score assignment: %w", snapshotID, err)
		}
		assignment.ConfigJSON = []byte(config)
		assignment.CreatedAt = fromTimestamp(createdAt)
		assignment.ChangedAt = fromTimestamp(changedAt)
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate snapshot %d score assignments: %w", snapshotID, err)
	}
	return assignments, nil
}
