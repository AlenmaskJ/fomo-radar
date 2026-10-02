package web

import (
	"sort"
	"time"
)

const (
	historyWindow24H = "24h"
	historyWindow7D  = "7d"

	historyStatusAll    = "all"
	historyStatusActive = "active"
	historyStatusEnded  = "ended"

	historySortRate   = "rate"
	historySortCount  = "count"
	historySortReturn = "return"
)

type OpportunityHistoryFilter struct {
	Window    string
	Status    string
	Sort      string
	Direction string
	AsOf      time.Time
}

type OpportunityHistoryPage struct {
	Title          string
	Filter         OpportunityHistoryFilter
	GeneratedAt    time.Time
	EffectiveScans int
	Rows           []OpportunityHistoryRow
}

type OpportunityHistoryRow struct {
	Rank             int
	InstrumentID     string
	Symbol           string
	Status           string
	Appearances      int
	ExperiencedScans int
	ListingRate      float64
	FirstListedAt    time.Time
	LastListedAt     time.Time
	FirstPrice       float64
	EvaluationPrice  *float64
	ReturnPct        *float64
	Episodes         []OpportunityEpisodeView
}

type OpportunityEpisodeView struct {
	StartedAt        time.Time
	EndedAt          *time.Time
	Status           string
	Appearances      int
	ExperiencedScans int
	ListingRate      float64
	FirstPrice       float64
	EvaluationPrice  *float64
	ReturnPct        *float64
	LastListedAt     time.Time
}

type historyPresence struct {
	InstrumentID string
	Symbol       string
	Price        float64
}

type historyScan struct {
	RunID         int64
	FinishedAt    time.Time
	Opportunities map[string]historyPresence
}

type episodeState struct {
	episode *OpportunityEpisodeView
	misses  int
}

func historyWindowDuration(window string) time.Duration {
	if window == historyWindow7D {
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

func buildOpportunityEpisodes(scans []historyScan, windowStart, windowEnd time.Time) []OpportunityHistoryRow {
	sortedScans := append([]historyScan(nil), scans...)
	sort.SliceStable(sortedScans, func(i, j int) bool {
		if sortedScans[i].FinishedAt.Equal(sortedScans[j].FinishedAt) {
			return sortedScans[i].RunID < sortedScans[j].RunID
		}
		return sortedScans[i].FinishedAt.Before(sortedScans[j].FinishedAt)
	})

	active := make(map[string]*episodeState)
	allEpisodes := make(map[string][]*OpportunityEpisodeView)
	symbols := make(map[string]string)

	for _, scan := range sortedScans {
		if scan.FinishedAt.After(windowEnd) {
			break
		}
		inWindow := !scan.FinishedAt.Before(windowStart)

		// 第三次缺席仍属于本轮经历过的扫描，并在该扫描完成时结束轮次。
		for instrumentID, state := range active {
			if inWindow {
				state.episode.ExperiencedScans++
			}
			presence, listed := scan.Opportunities[instrumentID]
			if listed {
				state.misses = 0
				state.episode.LastListedAt = scan.FinishedAt
				if presence.Symbol != "" {
					symbols[instrumentID] = presence.Symbol
				}
				if inWindow {
					state.episode.Appearances++
				}
				continue
			}

			state.misses++
			if state.misses == 3 {
				endedAt := scan.FinishedAt
				state.episode.EndedAt = &endedAt
				state.episode.Status = historyStatusEnded
				delete(active, instrumentID)
			}
		}

		for instrumentID, presence := range scan.Opportunities {
			if _, exists := active[instrumentID]; exists {
				continue
			}
			episode := &OpportunityEpisodeView{
				StartedAt:    scan.FinishedAt,
				Status:       historyStatusActive,
				FirstPrice:   presence.Price,
				LastListedAt: scan.FinishedAt,
			}
			if inWindow {
				episode.Appearances = 1
				episode.ExperiencedScans = 1
			}
			active[instrumentID] = &episodeState{episode: episode}
			allEpisodes[instrumentID] = append(allEpisodes[instrumentID], episode)
			symbols[instrumentID] = presence.Symbol
		}
	}

	rows := make([]OpportunityHistoryRow, 0, len(allEpisodes))
	for instrumentID, episodes := range allEpisodes {
		visible := make([]OpportunityEpisodeView, 0, len(episodes))
		for _, episode := range episodes {
			if episode.StartedAt.After(windowEnd) || (episode.EndedAt != nil && episode.EndedAt.Before(windowStart)) {
				continue
			}
			episode.ListingRate = percentage(episode.Appearances, episode.ExperiencedScans)
			visible = append(visible, *episode)
		}
		if len(visible) == 0 {
			continue
		}
		latest := visible[len(visible)-1]
		rows = append(rows, OpportunityHistoryRow{
			InstrumentID:     instrumentID,
			Symbol:           symbols[instrumentID],
			Status:           latest.Status,
			Appearances:      latest.Appearances,
			ExperiencedScans: latest.ExperiencedScans,
			ListingRate:      latest.ListingRate,
			FirstListedAt:    latest.StartedAt,
			LastListedAt:     latest.LastListedAt,
			FirstPrice:       latest.FirstPrice,
			Episodes:         visible,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].InstrumentID < rows[j].InstrumentID })
	return rows
}

func percentage(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator) * 100
}

func returnPercent(first float64, evaluation *float64) *float64 {
	if first <= 0 || evaluation == nil {
		return nil
	}
	value := (*evaluation - first) / first * 100
	return &value
}

func filterAndSortHistoryRows(rows []OpportunityHistoryRow, filter OpportunityHistoryFilter) []OpportunityHistoryRow {
	filtered := make([]OpportunityHistoryRow, 0, len(rows))
	for _, row := range rows {
		if filter.Status != "" && filter.Status != historyStatusAll && row.Status != filter.Status {
			continue
		}
		filtered = append(filtered, row)
	}

	ascending := filter.Direction == "asc"
	sort.SliceStable(filtered, func(i, j int) bool {
		left, right := filtered[i], filtered[j]
		if filter.Sort == historySortReturn {
			if left.ReturnPct == nil || right.ReturnPct == nil {
				if left.ReturnPct == nil && right.ReturnPct == nil {
					return historyTieLess(left, right)
				}
				return left.ReturnPct != nil
			}
			if *left.ReturnPct != *right.ReturnPct {
				if ascending {
					return *left.ReturnPct < *right.ReturnPct
				}
				return *left.ReturnPct > *right.ReturnPct
			}
			return historyTieLess(left, right)
		}

		if filter.Sort == historySortCount {
			if left.Appearances != right.Appearances {
				if ascending {
					return left.Appearances < right.Appearances
				}
				return left.Appearances > right.Appearances
			}
			return left.InstrumentID < right.InstrumentID
		}

		if left.ListingRate != right.ListingRate {
			if ascending {
				return left.ListingRate < right.ListingRate
			}
			return left.ListingRate > right.ListingRate
		}
		return historyTieLess(left, right)
	})
	for index := range filtered {
		filtered[index].Rank = index + 1
	}
	return filtered
}

func historyTieLess(left, right OpportunityHistoryRow) bool {
	if left.Appearances != right.Appearances {
		return left.Appearances > right.Appearances
	}
	return left.InstrumentID < right.InstrumentID
}
