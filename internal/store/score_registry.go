package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

const maxActiveChallengers = 3

// BootstrapChampion 创建首个运行时 Champion 及其审计事件；重复初始化不写新事件。
func (s *Store) BootstrapChampion(ctx context.Context, version string, configJSON []byte, changedAt time.Time) error {
	if version == "" || len(configJSON) == 0 {
		return fmt.Errorf("bootstrap Champion: version and config are required")
	}
	if changedAt.IsZero() {
		return fmt.Errorf("bootstrap Champion: change time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Champion bootstrap: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var roleCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM score_runtime_roles`).Scan(&roleCount); err != nil {
		return fmt.Errorf("count score roles: %w", err)
	}
	if roleCount > 0 {
		var stored string
		if err := tx.QueryRowContext(ctx, `SELECT config_json FROM score_versions WHERE version=?`, version).Scan(&stored); err != nil {
			return fmt.Errorf("read bootstrap score version %s: %w", version, err)
		}
		if stored != string(configJSON) {
			return fmt.Errorf("score version %s has conflicting config", version)
		}
		return commitScoreRoleTx(tx, "bootstrap Champion")
	}
	if err := ensureScoreVersionTx(ctx, tx, version, configJSON, changedAt); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO score_runtime_roles(score_version, role, changed_at)
		VALUES (?, 'CHAMPION', ?)`, version, timestamp(changedAt)); err != nil {
		return fmt.Errorf("insert Champion role: %w", err)
	}
	if _, err := insertScoreRoleEventTx(ctx, tx, scoreRoleEvent{
		EventType:          "BOOTSTRAP",
		SubjectVersion:     version,
		ToRole:             domain.ScoreRoleChampion,
		NewChampionVersion: version,
		Reason:             "system bootstrap",
		OccurredAt:         changedAt,
	}); err != nil {
		return err
	}
	return commitScoreRoleTx(tx, "bootstrap Champion")
}

// ActiveScoreAssignments 先返回 Champion，再按版本返回活跃 Challenger。
func (s *Store) ActiveScoreAssignments(ctx context.Context) ([]domain.ScoreAssignment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.score_version, r.role, v.config_json, v.created_at, r.changed_at
		FROM score_runtime_roles AS r
		JOIN score_versions AS v ON v.version = r.score_version
		WHERE r.role IN ('CHAMPION', 'CHALLENGER')
		ORDER BY CASE r.role WHEN 'CHAMPION' THEN 0 ELSE 1 END, r.score_version`)
	if err != nil {
		return nil, fmt.Errorf("query active score assignments: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var assignments []domain.ScoreAssignment
	champions := 0
	challengers := 0
	for rows.Next() {
		var assignment domain.ScoreAssignment
		var config string
		var createdAt, changedAt int64
		if err := rows.Scan(&assignment.Version, &assignment.Role, &config, &createdAt, &changedAt); err != nil {
			return nil, fmt.Errorf("scan active score assignment: %w", err)
		}
		assignment.ConfigJSON = []byte(config)
		assignment.CreatedAt = fromTimestamp(createdAt)
		assignment.ChangedAt = fromTimestamp(changedAt)
		switch assignment.Role {
		case domain.ScoreRoleChampion:
			champions++
		case domain.ScoreRoleChallenger:
			challengers++
		default:
			return nil, fmt.Errorf("invalid active score role %q", assignment.Role)
		}
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active score assignments: %w", err)
	}
	if champions != 1 {
		return nil, fmt.Errorf("score registry has %d Champions, want exactly one", champions)
	}
	if challengers > maxActiveChallengers {
		return nil, fmt.Errorf("score registry has %d active Challengers, maximum is three", challengers)
	}
	return assignments, nil
}

// ScoreAssignments 返回全部已注册角色，顺序为 Champion、Challenger、Retired。
func (s *Store) ScoreAssignments(ctx context.Context) ([]domain.ScoreAssignment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.score_version, r.role, v.config_json, v.created_at, r.changed_at
		FROM score_runtime_roles AS r
		JOIN score_versions AS v ON v.version=r.score_version
		ORDER BY CASE r.role WHEN 'CHAMPION' THEN 0 WHEN 'CHALLENGER' THEN 1 ELSE 2 END,
		         r.score_version`)
	if err != nil {
		return nil, fmt.Errorf("query score assignments: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	assignments := make([]domain.ScoreAssignment, 0)
	for rows.Next() {
		var assignment domain.ScoreAssignment
		var config string
		var createdAt, changedAt int64
		if err := rows.Scan(&assignment.Version, &assignment.Role, &config, &createdAt, &changedAt); err != nil {
			return nil, fmt.Errorf("scan score assignment: %w", err)
		}
		assignment.ConfigJSON = []byte(config)
		assignment.CreatedAt = fromTimestamp(createdAt)
		assignment.ChangedAt = fromTimestamp(changedAt)
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate score assignments: %w", err)
	}
	return assignments, nil
}

// EnableChallenger 注册或重新启用不可变评分版本；已启用时返回 false。
func (s *Store) EnableChallenger(ctx context.Context, version string, configJSON []byte, reason string, changedAt time.Time) (bool, error) {
	if version == "" || len(configJSON) == 0 {
		return false, fmt.Errorf("enable Challenger: version and config are required")
	}
	if strings.TrimSpace(reason) == "" {
		return false, fmt.Errorf("enable Challenger: reason is required")
	}
	if changedAt.IsZero() {
		return false, fmt.Errorf("enable Challenger: change time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin Challenger enable: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := ensureScoreVersionTx(ctx, tx, version, configJSON, changedAt); err != nil {
		return false, err
	}
	role, exists, err := scoreRoleTx(ctx, tx, version)
	if err != nil {
		return false, err
	}
	if exists {
		switch role {
		case domain.ScoreRoleChampion:
			return false, fmt.Errorf("score version %s is the current Champion", version)
		case domain.ScoreRoleChallenger:
			return false, commitScoreRoleTx(tx, "enable Challenger no-op")
		case domain.ScoreRoleRetired:
		default:
			return false, fmt.Errorf("score version %s has invalid role %q", version, role)
		}
	}

	championVersion, championConfig, err := currentChampionConfigTx(ctx, tx)
	if err != nil {
		return false, err
	}
	same, err := sameAlgorithmConfig(championConfig, configJSON)
	if err != nil {
		return false, fmt.Errorf("compare Challenger %s with Champion %s: %w", version, championVersion, err)
	}
	if same {
		return false, fmt.Errorf("Challenger %s is numerically identical to current Champion %s", version, championVersion)
	}

	var activeCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM score_runtime_roles WHERE role='CHALLENGER'`).Scan(&activeCount); err != nil {
		return false, fmt.Errorf("count active Challengers: %w", err)
	}
	if activeCount >= maxActiveChallengers {
		return false, fmt.Errorf("cannot enable Challenger %s: maximum is three", version)
	}

	if exists {
		result, err := tx.ExecContext(ctx, `UPDATE score_runtime_roles SET role='CHALLENGER', changed_at=? WHERE score_version=? AND role='RETIRED'`, timestamp(changedAt), version)
		if err != nil {
			return false, fmt.Errorf("re-enable Challenger %s: %w", version, err)
		}
		if err := requireOneRoleUpdate(result, "re-enable Challenger "+version); err != nil {
			return false, err
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO score_runtime_roles(score_version, role, changed_at) VALUES (?, 'CHALLENGER', ?)`, version, timestamp(changedAt)); err != nil {
		return false, fmt.Errorf("insert Challenger %s: %w", version, err)
	}
	var fromRole *domain.ScoreRole
	if exists {
		retired := domain.ScoreRoleRetired
		fromRole = &retired
	}
	if _, err := insertScoreRoleEventTx(ctx, tx, scoreRoleEvent{
		EventType:      "ENABLE",
		SubjectVersion: version,
		FromRole:       fromRole,
		ToRole:         domain.ScoreRoleChallenger,
		Reason:         strings.TrimSpace(reason),
		OccurredAt:     changedAt,
	}); err != nil {
		return false, err
	}
	if err := commitScoreRoleTx(tx, "enable Challenger"); err != nil {
		return false, err
	}
	return true, nil
}

// DisableChallenger 停用活跃 Challenger，但保留全部历史记录。
func (s *Store) DisableChallenger(ctx context.Context, version, reason string, changedAt time.Time) (bool, error) {
	if version == "" {
		return false, fmt.Errorf("disable Challenger: version is required")
	}
	if strings.TrimSpace(reason) == "" {
		return false, fmt.Errorf("disable Challenger: reason is required")
	}
	if changedAt.IsZero() {
		return false, fmt.Errorf("disable Challenger: change time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin Challenger disable: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	role, exists, err := scoreRoleTx(ctx, tx, version)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, fmt.Errorf("score version %s is not registered", version)
	}
	switch role {
	case domain.ScoreRoleChampion:
		return false, fmt.Errorf("cannot disable Champion %s", version)
	case domain.ScoreRoleRetired:
		return false, commitScoreRoleTx(tx, "disable Challenger no-op")
	case domain.ScoreRoleChallenger:
	default:
		return false, fmt.Errorf("score version %s has invalid role %q", version, role)
	}
	result, err := tx.ExecContext(ctx, `UPDATE score_runtime_roles SET role='RETIRED', changed_at=? WHERE score_version=? AND role='CHALLENGER'`, timestamp(changedAt), version)
	if err != nil {
		return false, fmt.Errorf("retire Challenger %s: %w", version, err)
	}
	if err := requireOneRoleUpdate(result, "retire Challenger "+version); err != nil {
		return false, err
	}
	challenger := domain.ScoreRoleChallenger
	if _, err := insertScoreRoleEventTx(ctx, tx, scoreRoleEvent{
		EventType:      "DISABLE",
		SubjectVersion: version,
		FromRole:       &challenger,
		ToRole:         domain.ScoreRoleRetired,
		Reason:         strings.TrimSpace(reason),
		OccurredAt:     changedAt,
	}); err != nil {
		return false, err
	}
	if err := commitScoreRoleTx(tx, "disable Challenger"); err != nil {
		return false, err
	}
	return true, nil
}

// PromoteChallenger 使用 CAS 语义原子切换 Champion，并返回新增审计事件 ID。
func (s *Store) PromoteChallenger(ctx context.Context, version, expectedChampion, reason string, changedAt time.Time) (int64, error) {
	if version == "" || expectedChampion == "" {
		return 0, fmt.Errorf("promote Challenger: version and expected Champion are required")
	}
	if strings.TrimSpace(reason) == "" {
		return 0, fmt.Errorf("promote Challenger: reason is required")
	}
	if changedAt.IsZero() {
		return 0, fmt.Errorf("promote Challenger: change time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin Challenger promotion: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	champion, err := currentChampionTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	if champion != expectedChampion {
		return 0, fmt.Errorf("current Champion is %s, expected %s", champion, expectedChampion)
	}
	role, exists, err := scoreRoleTx(ctx, tx, version)
	if err != nil {
		return 0, err
	}
	if !exists || role != domain.ScoreRoleChallenger {
		return 0, fmt.Errorf("score version %s is not an active Challenger", version)
	}
	for _, registered := range []string{champion, version} {
		var config string
		if err := tx.QueryRowContext(ctx, `SELECT config_json FROM score_versions WHERE version=?`, registered).Scan(&config); err != nil {
			return 0, fmt.Errorf("read score version %s config: %w", registered, err)
		}
		if strings.TrimSpace(config) == "" {
			return 0, fmt.Errorf("score version %s has empty config", registered)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE score_runtime_roles SET role='RETIRED', changed_at=? WHERE score_version=? AND role='CHAMPION'`, timestamp(changedAt), champion)
	if err != nil {
		return 0, fmt.Errorf("retire Champion %s: %w", champion, err)
	}
	if err := requireOneRoleUpdate(result, "retire Champion "+champion); err != nil {
		return 0, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE score_runtime_roles SET role='CHAMPION', changed_at=? WHERE score_version=? AND role='CHALLENGER'`, timestamp(changedAt), version)
	if err != nil {
		return 0, fmt.Errorf("promote Challenger %s: %w", version, err)
	}
	if err := requireOneRoleUpdate(result, "promote Challenger "+version); err != nil {
		return 0, err
	}
	challenger := domain.ScoreRoleChallenger
	eventID, err := insertScoreRoleEventTx(ctx, tx, scoreRoleEvent{
		EventType:               "PROMOTE",
		SubjectVersion:          version,
		FromRole:                &challenger,
		ToRole:                  domain.ScoreRoleChampion,
		PreviousChampionVersion: champion,
		NewChampionVersion:      version,
		Reason:                  strings.TrimSpace(reason),
		OccurredAt:              changedAt,
	})
	if err != nil {
		return 0, err
	}
	if err := commitScoreRoleTx(tx, "promote Challenger"); err != nil {
		return 0, err
	}
	return eventID, nil
}

func ensureScoreVersionTx(ctx context.Context, tx *sql.Tx, version string, configJSON []byte, createdAt time.Time) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO score_versions(version, config_json, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(version) DO NOTHING`, version, string(configJSON), timestamp(createdAt)); err != nil {
		return fmt.Errorf("insert score version %s: %w", version, err)
	}
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT config_json FROM score_versions WHERE version=?`, version).Scan(&stored); err != nil {
		return fmt.Errorf("read score version %s: %w", version, err)
	}
	if stored != string(configJSON) {
		return fmt.Errorf("score version %s has conflicting config", version)
	}
	return nil
}

func currentChampionTx(ctx context.Context, tx *sql.Tx) (string, error) {
	var version string
	if err := tx.QueryRowContext(ctx, `SELECT score_version FROM score_runtime_roles WHERE role='CHAMPION'`).Scan(&version); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("score registry has no Champion")
		}
		return "", fmt.Errorf("read current Champion: %w", err)
	}
	return version, nil
}

func currentChampionConfigTx(ctx context.Context, tx *sql.Tx) (string, []byte, error) {
	var version, config string
	if err := tx.QueryRowContext(ctx, `
		SELECT r.score_version, v.config_json
		FROM score_runtime_roles AS r
		JOIN score_versions AS v ON v.version=r.score_version
		WHERE r.role='CHAMPION'`).Scan(&version, &config); err != nil {
		if err == sql.ErrNoRows {
			return "", nil, fmt.Errorf("score registry has no Champion")
		}
		return "", nil, fmt.Errorf("read current Champion config: %w", err)
	}
	return version, []byte(config), nil
}

func scoreRoleTx(ctx context.Context, tx *sql.Tx, version string) (domain.ScoreRole, bool, error) {
	var role domain.ScoreRole
	if err := tx.QueryRowContext(ctx, `SELECT role FROM score_runtime_roles WHERE score_version=?`, version).Scan(&role); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read score role for %s: %w", version, err)
	}
	return role, true, nil
}

func sameAlgorithmConfig(left, right []byte) (bool, error) {
	canonical := func(raw []byte) ([]byte, error) {
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			return nil, err
		}
		if config == nil {
			return nil, fmt.Errorf("config must be a JSON object")
		}
		delete(config, "version")
		return json.Marshal(config)
	}
	a, err := canonical(left)
	if err != nil {
		return false, err
	}
	b, err := canonical(right)
	if err != nil {
		return false, err
	}
	return string(a) == string(b), nil
}

type scoreRoleEvent struct {
	EventType               string
	SubjectVersion          string
	FromRole                *domain.ScoreRole
	ToRole                  domain.ScoreRole
	PreviousChampionVersion string
	NewChampionVersion      string
	Reason                  string
	OccurredAt              time.Time
}

func insertScoreRoleEventTx(ctx context.Context, tx *sql.Tx, event scoreRoleEvent) (int64, error) {
	var fromRole any
	if event.FromRole != nil {
		fromRole = string(*event.FromRole)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO score_role_events(
			event_type, subject_version, from_role, to_role,
			previous_champion_version, new_champion_version, reason, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventType, event.SubjectVersion, fromRole, event.ToRole,
		stringOrNil(event.PreviousChampionVersion), stringOrNil(event.NewChampionVersion),
		event.Reason, timestamp(event.OccurredAt),
	)
	if err != nil {
		return 0, fmt.Errorf("insert %s score role event: %w", event.EventType, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read %s score role event ID: %w", event.EventType, err)
	}
	return id, nil
}

func commitScoreRoleTx(tx *sql.Tx, operation string) error {
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", operation, err)
	}
	return nil
}

func requireOneRoleUpdate(result sql.Result, operation string) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read %s update count: %w", operation, err)
	}
	if changed != 1 {
		return fmt.Errorf("%s changed concurrently", operation)
	}
	return nil
}
