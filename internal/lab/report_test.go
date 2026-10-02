package lab

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestReportMaterializesOnlyOnDemandAndRendersNullUTF8(t *testing.T) {
	repo := openTestRepository(t)
	initializeAnalysisState(t, repo)
	assertCount(t, repo, "analysis_runs", 0)
	seedAnalysisSignal(t, repo, analysisSeed{TokenID: "bsc:report", Chain: "bsc", Source: "BACKFILL", Evidence: "HIGH", EntryStatus: "UNAVAILABLE_PRICE", Status: "NOT_EVALUABLE"})
	analyzer := NewAnalyzer(repo, func() time.Time { return time.Date(2026, time.August, 22, 14, 0, 0, 0, time.UTC) })
	if _, err := analyzer.Materialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, "analysis_runs", 1)
	report, err := repo.LatestAnalysis(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var table bytes.Buffer
	if err := WriteTable(&table, report); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"FomoRadar 信号实验室", "BACKFILL", "LIVE", "ALL"} {
		if !strings.Contains(table.String(), required) {
			t.Fatalf("table missing %q:\n%s", required, table.String())
		}
	}

	var encoded bytes.Buffer
	if err := WriteJSON(&encoded, report); err != nil {
		t.Fatal(err)
	}
	jsonText := encoded.String()
	if !strings.Contains(jsonText, "FomoRadar 信号实验室") || strings.Contains(jsonText, `\u4fe1`) {
		t.Fatalf("JSON is not literal UTF-8: %s", jsonText)
	}
	if !strings.Contains(jsonText, `"signal_source_scope"`) || strings.Contains(jsonText, `"source_scope"`) {
		t.Fatalf("JSON scope field mismatch: %s", jsonText)
	}
	if !strings.Contains(jsonText, `"positive_return_rate":null`) {
		t.Fatalf("JSON N/A not null: %s", jsonText)
	}

	if _, err := analyzer.Materialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, "analysis_runs", 2)
}
