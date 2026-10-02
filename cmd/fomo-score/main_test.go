package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/lab"
	"github.com/alen1/fomo-radar/internal/score"
)

func TestRunRejectsInvalidSubcommandsAndMissingArguments(t *testing.T) {
	getenv := func(string) string { return "" }
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing subcommand", want: "需要子命令"},
		{name: "unknown", args: []string{"unknown"}, want: "未知子命令"},
		{name: "add missing config", args: []string{"add", "--reason", "trial"}, want: "--config"},
		{name: "add missing reason", args: []string{"add", "--config", "x.json"}, want: "--reason"},
		{name: "disable missing reason", args: []string{"disable", "--version", "v2"}, want: "--reason"},
		{name: "promote missing expected", args: []string{"promote", "--version", "v2", "--reason", "win"}, want: "--expected-champion"},
		{name: "extra list argument", args: []string{"list", "extra"}, want: "多余参数"},
		{name: "extra compare argument", args: []string{"compare", "extra"}, want: "多余参数"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := run(test.args, getenv, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run(%v) error = %v, want containing %q", test.args, err, test.want)
			}
		})
	}
}

func TestListUsesEnvironmentDatabaseAndBootstrapsChampion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "env.db")
	getenv := func(key string) string {
		if key == "FOMO_DB" {
			return dbPath
		}
		return ""
	}
	var output bytes.Buffer
	if err := run([]string{"list"}, getenv, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), score.Version) || !strings.Contains(output.String(), "CHAMPION") {
		t.Fatalf("list output = %q", output.String())
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("environment database not created: %v", err)
	}
}

func TestAddPromoteDisableWorkflow(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "core.db")
	challengerPath := filepath.Join(dir, "challenger.json")
	writeScoreConfig(t, challengerPath, "FOMO_SCORE_V1.1", 24)
	getenv := func(string) string { return "" }
	var output bytes.Buffer

	if err := run([]string{"add", "--db", dbPath, "--config", challengerPath, "--reason", "影子测试"}, getenv, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "已启用 Challenger") {
		t.Fatalf("add output = %q", output.String())
	}
	output.Reset()
	if err := run([]string{"add", "--db", dbPath, "--config", challengerPath, "--reason", "重复"}, getenv, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "无变更") {
		t.Fatalf("duplicate add output = %q", output.String())
	}

	output.Reset()
	if err := run([]string{"promote", "--db", dbPath, "--version", "FOMO_SCORE_V1.1", "--expected-champion", score.Version, "--reason", "人工确认"}, getenv, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "晋升成功") || !strings.Contains(output.String(), "FOMO_SCORE_V1.1") {
		t.Fatalf("promote output = %q", output.String())
	}

	output.Reset()
	if err := run([]string{"list", "--db", dbPath}, getenv, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "FOMO_SCORE_V1.1\tCHAMPION") || !strings.Contains(output.String(), score.Version+"\tRETIRED") {
		t.Fatalf("list after promote = %q", output.String())
	}
}

func TestBundledChallengerFixtureIsValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "fomo_score_v1_1_challenger.json"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := score.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if engine.Version() != "FOMO_SCORE_V1.1" {
		t.Fatalf("fixture version = %q", engine.Version())
	}
}

func TestCompareValidatesLabIdentityBeforeOutput(t *testing.T) {
	dir := t.TempDir()
	corePath := filepath.Join(dir, "core.db")
	labPath := filepath.Join(dir, "lab.db")
	getenv := func(string) string { return "" }
	if err := run([]string{"list", "--db", corePath}, getenv, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := run([]string{"compare", "--db", corePath, "--lab-db", labPath}, getenv, &output, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "尚未初始化") || output.Len() != 0 {
		t.Fatalf("uninitialized compare error/output = %v/%q", err, output.String())
	}

	source, err := lab.OpenSource(corePath)
	if err != nil {
		t.Fatal(err)
	}
	activation, err := source.Activation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := source.Identity(context.Background(), activation)
	if err != nil {
		t.Fatal(err)
	}
	canonicalPath := source.CanonicalPath()
	repository, err := lab.OpenRepository(labPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.InitializeOrValidate(context.Background(), lab.SourceRegistration{
		CanonicalPath: canonicalPath, Identity: identity, ActivationSnapshotID: activation,
		CurrentSourceMaximum: activation, InitializedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	_ = repository.Close()
	_ = source.Close()
	seedComparisonAnalysis(t, labPath)

	output.Reset()
	if err := run([]string{"compare", "--db", corePath, "--lab-db", labPath}, getenv, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), score.Version) || !strings.Contains(output.String(), "CHAMPION") || !strings.Contains(output.String(), "N/A") {
		t.Fatalf("compare output = %q", output.String())
	}

	mismatchPath := filepath.Join(dir, "mismatch.db")
	mismatch, err := lab.OpenRepository(mismatchPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mismatch.InitializeOrValidate(context.Background(), lab.SourceRegistration{
		CanonicalPath: canonicalPath, Identity: "wrong", ActivationSnapshotID: activation,
		CurrentSourceMaximum: activation, InitializedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	_ = mismatch.Close()
	output.Reset()
	err = run([]string{"compare", "--db", corePath, "--lab-db", mismatchPath}, getenv, &output, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "身份不匹配") || output.Len() != 0 {
		t.Fatalf("mismatch compare error/output = %v/%q", err, output.String())
	}
}

func seedComparisonAnalysis(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck
	now := time.Now().UTC().UnixMilli()
	result, err := db.Exec(`INSERT INTO analysis_runs(algorithm_version,generated_at,persisted_through_snapshot_id) VALUES (?,?,0)`, lab.AlgorithmVersion, now)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO threshold_statistics(
		analysis_run_id,signal_source_scope,chain_scope,evidence_scope,score_version,threshold,horizon,
		signals_count,evaluable_entries,not_evaluable,pending_count,matured_count,
		insufficient_data_count,insufficient_sample
	) VALUES (?,'ALL','ALL','ALL',?,55,'5m',0,0,0,0,0,0,1)`, runID, score.Version); err != nil {
		t.Fatal(err)
	}
}

func writeScoreConfig(t *testing.T, path, version string, buyerVelocityMaximum float64) {
	t.Helper()
	raw, err := score.NewEngine().ConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	config["version"] = version
	config["factors"].(map[string]any)["buyer_velocity"].(map[string]any)["maximum"] = buyerVelocityMaximum
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
