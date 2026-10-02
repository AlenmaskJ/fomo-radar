package scanner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/score"
)

func TestFastDeepGenerationKeepsFrozenRolesAcrossPromotion(t *testing.T) {
	ctx := context.Background()
	now := scanTime()
	championConfig := scoreConfigForTest(t, "v1", 25)
	challengerConfig := scoreConfigForTest(t, "v1.1", 24)
	database := &fakeStore{activeScores: []domain.ScoreAssignment{
		{Version: "v1", Role: domain.ScoreRoleChampion, ConfigJSON: championConfig, ChangedAt: now},
		{Version: "v1.1", Role: domain.ScoreRoleChallenger, ConfigJSON: challengerConfig, ChangedAt: now},
	}}
	candidate := candidate("0xfrozen", domain.ChainBSC, now)
	service := NewService(
		testConfig(),
		fakeDiscoverer{candidates: map[domain.Chain][]domain.Candidate{domain.ChainBSC: {candidate}}},
		fakeEnricher{markets: map[string]domain.MarketSnapshot{candidate.Address: activeMarket(candidate.Address, now)}},
		successfulTrades(now), missingSocial{}, score.NewEngine(), database, func() time.Time { return now },
	)

	_, tasks, err := service.FastScan(ctx, ModeWatch)
	if err != nil {
		t.Fatalf("FastScan() error = %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("Deep tasks = %d, want 1", len(tasks))
	}
	assertVersionedRoles(t, database.fastScores[tasks[0].BaseFastSnapshotID], "v1:CHAMPION", "v1.1:CHALLENGER")

	// 模拟 Fast 入队后发生晋升；基础快照保存的本代角色不能被改写。
	database.activeScores = []domain.ScoreAssignment{
		{Version: "v1.1", Role: domain.ScoreRoleChampion, ConfigJSON: challengerConfig, ChangedAt: now.Add(1)},
	}
	if _, err := service.ProcessDeepTask(ctx, tasks[0]); err != nil {
		t.Fatalf("ProcessDeepTask() error = %v", err)
	}
	assertVersionedRoles(t, database.deepScores[tasks[0].BaseFastSnapshotID], "v1:CHAMPION", "v1.1:CHALLENGER")

	_, nextTasks, err := service.FastScan(ctx, ModeWatch)
	if err != nil {
		t.Fatalf("next FastScan() error = %v", err)
	}
	if len(nextTasks) != 1 {
		t.Fatalf("next Deep tasks = %d, want 1", len(nextTasks))
	}
	assertVersionedRoles(t, database.fastScores[nextTasks[0].BaseFastSnapshotID], "v1.1:CHAMPION")
}

func scoreConfigForTest(t *testing.T, version string, buyerVelocityMaximum float64) []byte {
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
	factors := config["factors"].(map[string]any)
	buyerVelocity := factors["buyer_velocity"].(map[string]any)
	buyerVelocity["maximum"] = buyerVelocityMaximum
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := score.ParseConfig(raw); err != nil {
		t.Fatalf("test config invalid: %v", err)
	}
	return raw
}

func assertVersionedRoles(t *testing.T, scores []domain.VersionedScore, want ...string) {
	t.Helper()
	if len(scores) != len(want) {
		t.Fatalf("scores = %d, want %d", len(scores), len(want))
	}
	for index, scored := range scores {
		got := scored.Version + ":" + string(scored.Role)
		if got != want[index] {
			t.Fatalf("score[%d] = %s, want %s", index, got, want[index])
		}
	}
}
