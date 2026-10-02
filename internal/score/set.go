package score

import (
	"fmt"
	"sort"

	"github.com/alen1/fomo-radar/internal/domain"
)

// Evaluator 是评分集合需要的最小引擎接口。
type Evaluator interface {
	Version() string
	Evaluate(Input) domain.ScoreBreakdown
}

// Assignment 将不可变评分引擎绑定到本代捕获的运行角色。
type Assignment struct {
	Role   domain.ScoreRole
	Engine Evaluator
}

// Set 表示经过校验且顺序确定的一代评分集合。
type Set struct {
	assignments []Assignment
}

// NewSet 要求恰好一个 Champion，且最多三个 Challenger。
func NewSet(assignments []Assignment) (Set, error) {
	ordered := append([]Assignment(nil), assignments...)
	seen := make(map[string]struct{}, len(ordered))
	champions := 0
	challengers := 0
	for _, assignment := range ordered {
		version := assignment.Engine.Version()
		if version == "" {
			return Set{}, fmt.Errorf("score assignment version is required")
		}
		if _, duplicate := seen[version]; duplicate {
			return Set{}, fmt.Errorf("duplicate score assignment version %s", version)
		}
		seen[version] = struct{}{}
		switch assignment.Role {
		case domain.ScoreRoleChampion:
			champions++
		case domain.ScoreRoleChallenger:
			challengers++
		default:
			return Set{}, fmt.Errorf("score assignment %s has invalid role %q", version, assignment.Role)
		}
	}
	if champions != 1 {
		return Set{}, fmt.Errorf("score set has %d Champions, want exactly one", champions)
	}
	if challengers > 3 {
		return Set{}, fmt.Errorf("score set has %d Challengers, maximum is three", challengers)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Role != ordered[j].Role {
			return ordered[i].Role == domain.ScoreRoleChampion
		}
		return ordered[i].Engine.Version() < ordered[j].Engine.Version()
	})
	return Set{assignments: ordered}, nil
}

// Evaluate 使用同一份输入依次运行本代冻结的所有引擎。
func (s Set) Evaluate(input Input) []domain.VersionedScore {
	results := make([]domain.VersionedScore, 0, len(s.assignments))
	for _, assignment := range s.assignments {
		breakdown := assignment.Engine.Evaluate(input)
		results = append(results, domain.VersionedScore{
			Version: breakdown.Version, Role: assignment.Role, Breakdown: breakdown,
		})
	}
	return results
}

// References 返回不复制配置内容的版本和角色引用。
func (s Set) References() []domain.ScoreReference {
	refs := make([]domain.ScoreReference, 0, len(s.assignments))
	for _, assignment := range s.assignments {
		refs = append(refs, domain.ScoreReference{Version: assignment.Engine.Version(), Role: assignment.Role})
	}
	return refs
}
