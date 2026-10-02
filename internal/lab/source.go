package lab

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const maxSourceBatch = 500

const sourceSnapshotColumns = `
	s.id, s.token_id, t.chain, t.address, t.name, t.symbol,
	s.collected_at, s.source_time, s.price_usd, s.market_cap_usd, s.liquidity_usd,
	ss.raw_score, COALESCE(ss.raw_tier,''), COALESCE(ss.effective_tier,''),
	COALESCE(ss.evidence_confidence,''), COALESCE(ss.evidence_available,0),
	COALESCE(ss.evidence_total,0), COALESCE(ss.score_version,''), v.config_json,
	COALESCE(ss.score_breakdown_json,'{}'), COALESCE(ss.risk_flags_json,'[]'), s.data_quality_json,
	s.discovery_pool_created_at, s.selected_pair_created_at`

type Source struct {
	db            *sql.DB
	canonicalPath string
}

func OpenSource(path string) (*Source, error) {
	canonical, err := canonicalSourcePath(path)
	if err != nil {
		return nil, err
	}
	dsn := sourceReadOnlyDSN(canonical)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open Core source: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	closeOnError := func(err error) (*Source, error) {
		_ = db.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(fmt.Errorf("open Core source read-only: %w", err))
	}
	if _, err := db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return closeOnError(fmt.Errorf("enable Core source query-only mode: %w", err))
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		return closeOnError(fmt.Errorf("configure Core source busy timeout: %w", err))
	}
	rows, err := db.QueryContext(ctx, `SELECT `+sourceSnapshotColumns+`
		FROM snapshots s JOIN tokens t ON t.id=s.token_id
		LEFT JOIN snapshot_scores ss ON ss.snapshot_id=s.id
		LEFT JOIN score_versions v ON v.version=ss.score_version LIMIT 0`)
	if err != nil {
		return closeOnError(fmt.Errorf("validate Core source schema: %w", err))
	}
	if err := rows.Close(); err != nil {
		return closeOnError(fmt.Errorf("close Core source schema validation: %w", err))
	}
	return &Source{db: db, canonicalPath: canonical}, nil
}

func (s *Source) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Source) CanonicalPath() string { return s.canonicalPath }

func (s *Source) Activation(ctx context.Context) (int64, error) {
	var maximum int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM snapshots`).Scan(&maximum); err != nil {
		return 0, fmt.Errorf("read Core activation snapshot: %w", err)
	}
	return maximum, nil
}

func (s *Source) Frontier(ctx context.Context) (SourceFrontier, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SourceFrontier{}, fmt.Errorf("begin Core frontier read: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	var result SourceFrontier
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM snapshots`).Scan(&result.HighWatermark); err != nil {
		return SourceFrontier{}, fmt.Errorf("read Core source high watermark: %w", err)
	}
	var observed sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(collected_at) FROM snapshots WHERE id <= ?`, result.HighWatermark).Scan(&observed); err != nil {
		return SourceFrontier{}, fmt.Errorf("read Core observation frontier: %w", err)
	}
	if observed.Valid {
		value := time.UnixMilli(observed.Int64).UTC()
		result.ObservationAt = &value
	}
	if err := tx.Commit(); err != nil {
		return SourceFrontier{}, fmt.Errorf("commit Core frontier read: %w", err)
	}
	return result, nil
}

func (s *Source) Snapshots(ctx context.Context, afterID, throughID int64, limit int) ([]SourceSnapshot, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("source snapshot limit must be positive")
	}
	if limit > maxSourceBatch {
		limit = maxSourceBatch
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH page AS (
			SELECT id FROM snapshots
			WHERE id > ? AND id <= ?
			ORDER BY id ASC LIMIT ?
		)
		SELECT `+sourceSnapshotColumns+`
		FROM page
		JOIN snapshots s ON s.id=page.id
		JOIN tokens t ON t.id=s.token_id
		LEFT JOIN snapshot_scores ss ON ss.snapshot_id=s.id
		LEFT JOIN score_versions v ON v.version=ss.score_version
		ORDER BY s.id ASC,
		         CASE ss.evaluation_role WHEN 'CHAMPION' THEN 0 WHEN 'CHALLENGER' THEN 1 ELSE 2 END,
		         ss.score_version ASC`, afterID, throughID, limit)
	if err != nil {
		return nil, fmt.Errorf("query Core snapshots: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	result := make([]SourceSnapshot, 0, limit)
	for rows.Next() {
		var snapshot SourceSnapshot
		var collectedAt int64
		var sourceTime, discoveryCreated, selectedCreated sql.NullInt64
		var price, marketCap, liquidity, rawScore sql.NullFloat64
		var config sql.NullString
		if err := rows.Scan(
			&snapshot.ID, &snapshot.TokenID, &snapshot.Chain, &snapshot.Address, &snapshot.Name, &snapshot.Symbol,
			&collectedAt, &sourceTime, &price, &marketCap, &liquidity, &rawScore, &snapshot.RawTier,
			&snapshot.EffectiveTier, &snapshot.EvidenceConfidence, &snapshot.EvidenceAvailable,
			&snapshot.EvidenceTotal, &snapshot.ScoreVersion, &config, &snapshot.ScoreBreakdownJSON,
			&snapshot.RiskFlagsJSON, &snapshot.DataQualityJSON, &discoveryCreated, &selectedCreated,
		); err != nil {
			return nil, fmt.Errorf("scan Core snapshot: %w", err)
		}
		snapshot.CollectedAt = time.UnixMilli(collectedAt).UTC()
		snapshot.SourceTime = nullableTime(sourceTime)
		snapshot.PriceUSD = nullableFloatPointer(price)
		snapshot.MarketCapUSD = nullableFloatPointer(marketCap)
		snapshot.LiquidityUSD = nullableFloatPointer(liquidity)
		snapshot.RawScore = nullableFloatPointer(rawScore)
		snapshot.DiscoveryPoolCreatedAt = nullableTime(discoveryCreated)
		snapshot.SelectedPairCreatedAt = nullableTime(selectedCreated)
		snapshot.TokenAgeSeconds = sourceTokenAge(snapshot.CollectedAt, snapshot.DiscoveryPoolCreatedAt, snapshot.SelectedPairCreatedAt)
		if rawScore.Valid {
			if !config.Valid || strings.TrimSpace(config.String) == "" {
				return nil, fmt.Errorf("snapshot %d score config %q is unavailable", snapshot.ID, snapshot.ScoreVersion)
			}
			snapshot.ScoreConfigJSON = config.String
		}
		result = append(result, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Core snapshots: %w", err)
	}
	return result, nil
}

func (s *Source) Identity(ctx context.Context, activationID int64) (string, error) {
	if activationID < 0 {
		return "", fmt.Errorf("activation snapshot ID must not be negative")
	}
	first, err := s.identityAnchor(ctx, activationID, true)
	if err != nil {
		return "", err
	}
	activation, err := s.identityAnchor(ctx, activationID, false)
	if err != nil {
		return "", err
	}
	payload := strings.Join([]string{
		"fomoradar-source-v1",
		s.canonicalPath,
		fmt.Sprintf("%d", activationID),
		"first:" + first,
		"activation:" + activation,
	}, "\n")
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum[:]), nil
}

func (s *Source) SelectOutcomeSnapshot(ctx context.Context, pending PendingOutcome, sourceHighWatermark int64) (*PriceObservation, error) {
	var observed PriceObservation
	var collectedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id,collected_at,price_usd FROM snapshots
		WHERE token_id=? AND id>? AND id<=? AND collected_at>? AND collected_at>=? AND collected_at<=? AND price_usd>0
		ORDER BY collected_at ASC,id ASC LIMIT 1`,
		pending.TokenID, pending.SignalSnapshotID, sourceHighWatermark, pending.SignalAt.UnixMilli(),
		pending.TargetAt.UnixMilli(), pending.WindowEndAt.UnixMilli()).Scan(
		&observed.SnapshotID, &collectedAt, &observed.PriceUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select %s outcome snapshot for signal %d: %w", pending.Horizon, pending.SignalID, err)
	}
	observed.CollectedAt = time.UnixMilli(collectedAt).UTC()
	return &observed, nil
}

func (s *Source) PricePath(ctx context.Context, pending PendingOutcome, selectedAt time.Time, sourceHighWatermark int64) ([]PriceObservation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,collected_at,price_usd FROM snapshots
		WHERE token_id=? AND id>? AND id<=? AND collected_at>? AND collected_at<=? AND price_usd>0
		ORDER BY collected_at ASC,id ASC`,
		pending.TokenID, pending.SignalSnapshotID, sourceHighWatermark,
		pending.SignalAt.UnixMilli(), selectedAt.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query %s price path for signal %d: %w", pending.Horizon, pending.SignalID, err)
	}
	defer rows.Close() //nolint:errcheck
	result := make([]PriceObservation, 0)
	for rows.Next() {
		var observed PriceObservation
		var collectedAt int64
		if err := rows.Scan(&observed.SnapshotID, &collectedAt, &observed.PriceUSD); err != nil {
			return nil, fmt.Errorf("scan %s price path for signal %d: %w", pending.Horizon, pending.SignalID, err)
		}
		observed.CollectedAt = time.UnixMilli(collectedAt).UTC()
		result = append(result, observed)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s price path for signal %d: %w", pending.Horizon, pending.SignalID, err)
	}
	return result, nil
}

func (s *Source) identityAnchor(ctx context.Context, activationID int64, first bool) (string, error) {
	if activationID == 0 {
		return "<empty>", nil
	}
	query := `SELECT s.id,s.token_id,s.collected_at,s.score_version,t.chain,t.address
		FROM snapshots s JOIN tokens t ON t.id=s.token_id WHERE s.id=?`
	args := []any{activationID}
	if first {
		query = `SELECT s.id,s.token_id,s.collected_at,s.score_version,t.chain,t.address
			FROM snapshots s JOIN tokens t ON t.id=s.token_id
			WHERE s.id <= ? ORDER BY s.id ASC LIMIT 1`
	}
	var id, collectedAt int64
	var tokenID, scoreVersion, chain, address string
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&id, &tokenID, &collectedAt, &scoreVersion, &chain, &address)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("source identity anchor %d is unavailable", activationID)
		}
		return "", fmt.Errorf("read source identity anchor: %w", err)
	}
	return fmt.Sprintf("%d|%s|%d|%s|%s|%s", id, tokenID, collectedAt, scoreVersion, chain, address), nil
}

func canonicalSourcePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Core source path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if runtime.GOOS == "windows" {
		absolute = strings.ToLower(absolute)
	}
	return absolute, nil
}

func sourceReadOnlyDSN(path string) string {
	normalized := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	uri := url.URL{Scheme: "file", Path: normalized}
	query := uri.Query()
	query.Set("mode", "ro")
	uri.RawQuery = query.Encode()
	return uri.String()
}

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := time.UnixMilli(value.Int64).UTC()
	return &result
}

func nullableFloatPointer(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func sourceTokenAge(collectedAt time.Time, discoveryCreated, selectedCreated *time.Time) *int64 {
	created := discoveryCreated
	if created == nil {
		created = selectedCreated
	}
	if created == nil || collectedAt.Before(*created) {
		return nil
	}
	seconds := int64(collectedAt.Sub(*created) / time.Second)
	return &seconds
}
