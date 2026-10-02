package lab

import (
	"encoding/json"
	"fmt"
	"io"
)

func WriteJSON(w io.Writer, report ThresholdReport) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("encode Signal Lab JSON report: %w", err)
	}
	return nil
}

func WriteTable(w io.Writer, report ThresholdReport) error {
	if _, err := fmt.Fprintf(w, "%s\n算法：%s\n生成：%s\n源 Snapshot 高水位：%d\n\n",
		report.Title, report.Run.AlgorithmVersion, report.Run.GeneratedAt.Format("2006-01-02 15:04:05Z07:00"),
		report.Run.PersistedThroughSnapshotID); err != nil {
		return fmt.Errorf("write Signal Lab table header: %w", err)
	}
	currentSource := ""
	for _, stat := range report.Statistics {
		if stat.SignalSourceScope != currentSource {
			currentSource = stat.SignalSourceScope
			if _, err := fmt.Fprintf(w, "[%s]\n", currentSource); err != nil {
				return fmt.Errorf("write Signal Lab source section: %w", err)
			}
		}
		if _, err := fmt.Fprintf(w,
			"%s/%s v=%s score>=%d horizon=%s signals=%d evaluable=%d entry=%s matured=%d result=%s win=%s median=%s mfe=%s mae=%s sample_low=%t\n",
			stat.ChainScope, stat.EvidenceScope, stat.ScoreVersion, stat.Threshold, stat.Horizon,
			stat.Signals, stat.EvaluableEntries, formatPercent(stat.EntryPriceCoverage), stat.Matured,
			formatPercent(stat.ResultCoverage), formatPercent(stat.PositiveReturnRate), formatPercent(stat.MedianReturn),
			formatPercent(stat.MedianMFE), formatPercent(stat.MedianMAE), stat.InsufficientSample); err != nil {
			return fmt.Errorf("write Signal Lab statistic: %w", err)
		}
	}
	return nil
}

func formatPercent(value *float64) string {
	if value == nil {
		return "N/A"
	}
	return fmt.Sprintf("%.2f%%", *value*100)
}
