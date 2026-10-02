package score

import (
	"bytes"
	"testing"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestSetEvaluatesChampionFirstAndUsesSameInput(t *testing.T) {
	champion := engineWithVersion(t, "champion")
	challenger := engineWithVersion(t, "challenger")
	set, err := NewSet([]Assignment{
		{Role: domain.ScoreRoleChallenger, Engine: challenger},
		{Role: domain.ScoreRoleChampion, Engine: champion},
	})
	if err != nil {
		t.Fatal(err)
	}

	input := maxInput()
	got := set.Evaluate(input)
	if len(got) != 2 {
		t.Fatalf("scores = %d, want 2", len(got))
	}
	if got[0].Role != domain.ScoreRoleChampion || got[0].Version != "champion" {
		t.Fatalf("first score = %+v, want Champion", got[0])
	}
	if got[1].Role != domain.ScoreRoleChallenger || got[1].Version != "challenger" {
		t.Fatalf("second score = %+v, want Challenger", got[1])
	}
	if got[0].Breakdown.RawScore != got[1].Breakdown.RawScore {
		t.Fatalf("same config/input scores differ: %v vs %v", got[0].Breakdown.RawScore, got[1].Breakdown.RawScore)
	}
}

func TestNewSetRejectsInvalidRolesAndCardinality(t *testing.T) {
	champion := Assignment{Role: domain.ScoreRoleChampion, Engine: engineWithVersion(t, "champion")}
	challenger := func(version string) Assignment {
		return Assignment{Role: domain.ScoreRoleChallenger, Engine: engineWithVersion(t, version)}
	}
	for _, test := range []struct {
		name        string
		assignments []Assignment
	}{
		{name: "no champion", assignments: []Assignment{challenger("c1")}},
		{name: "two champions", assignments: []Assignment{champion, {Role: domain.ScoreRoleChampion, Engine: engineWithVersion(t, "other")}}},
		{name: "duplicate version", assignments: []Assignment{champion, {Role: domain.ScoreRoleChallenger, Engine: engineWithVersion(t, "champion")}}},
		{name: "four challengers", assignments: []Assignment{champion, challenger("c1"), challenger("c2"), challenger("c3"), challenger("c4")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSet(test.assignments); err == nil {
				t.Fatal("NewSet() error = nil, want rejection")
			}
		})
	}
}

func engineWithVersion(t *testing.T, version string) Engine {
	t.Helper()
	raw, err := NewEngine().ConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"version":"FOMO_SCORE_V1.0"`), []byte(`"version":"`+version+`"`), 1)
	engine, err := ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}
