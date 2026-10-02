package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/lab"
)

func TestParseOptionsRequiresExactlyOneMode(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"-run-once", "-watch"},
		{"-run-once", "-report"},
		{"-watch", "-report"},
	} {
		if _, err := parseOptions(args, nil); err == nil {
			t.Fatalf("parseOptions(%v) accepted invalid mode count", args)
		}
	}
	for _, args := range [][]string{{"-run-once"}, {"-watch"}, {"-report"}} {
		if _, err := parseOptions(args, nil); err != nil {
			t.Fatalf("parseOptions(%v) error = %v", args, err)
		}
	}
}

func TestParseOptionsUsesEnvironmentDefaultsAndValidatesFormat(t *testing.T) {
	getenv := func(name string) string {
		return map[string]string{"FOMO_DB": "core-from-env.db", "FOMO_LAB_DB": "lab-from-env.db"}[name]
	}
	options, err := parseOptions([]string{"-watch"}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if options.sourceDB != "core-from-env.db" || options.labDB != "lab-from-env.db" || options.interval != 2*time.Minute || options.format != "table" {
		t.Fatalf("options = %+v", options)
	}
	if _, err := parseOptions([]string{"-watch", "-interval", "0s"}, getenv); err == nil {
		t.Fatal("zero watch interval accepted")
	}
	if _, err := parseOptions([]string{"-report", "-format", "xml"}, getenv); err == nil {
		t.Fatal("unsupported report format accepted")
	}
}

func TestRunOnceAndWatchUseSameCycleFunction(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, "", 0)
	calls := 0
	cycle := func(context.Context) (lab.CycleResult, error) {
		calls++
		return lab.CycleResult{SourceSnapshotsProcessed: 1}, nil
	}
	if err := executeCycles(context.Background(), modeRunOnce, nil, cycle, logger); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("run-once calls = %d", calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time, 2)
	ticks <- time.Now()
	ticks <- time.Now()
	calls = 0
	cycle = func(context.Context) (lab.CycleResult, error) {
		calls++
		if calls == 1 {
			return lab.CycleResult{}, errors.New("fixture cycle failure")
		}
		if calls == 3 {
			cancel()
		}
		return lab.CycleResult{}, nil
	}
	var logs bytes.Buffer
	if err := executeCycles(ctx, modeWatch, ticks, cycle, log.New(&logs, "", 0)); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || !strings.Contains(logs.String(), "fixture cycle failure") {
		t.Fatalf("watch calls=%d logs=%q", calls, logs.String())
	}
}

func TestReportModeDoesNotOpenCoreSource(t *testing.T) {
	labPath := filepath.Join(t.TempDir(), "fomo-lab.db")
	repo, err := lab.OpenRepository(labPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.InitializeOrValidate(context.Background(), lab.SourceRegistration{
		CanonicalPath: `C:\missing\fomo.db`, Identity: "fixture", ActivationSnapshotID: 0,
		CurrentSourceMaximum: 0, InitializedAt: time.Date(2026, time.August, 22, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err = run(context.Background(), []string{"-report", "-source-db", filepath.Join(t.TempDir(), "does-not-exist.db"), "-lab-db", labPath}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("report run error = %v", err)
	}
	if !strings.Contains(stdout.String(), "FomoRadar 信号实验室") {
		t.Fatalf("report output = %q", stdout.String())
	}
}
