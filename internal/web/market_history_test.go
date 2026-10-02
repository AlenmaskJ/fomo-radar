package web

import (
	"testing"
	"time"
)

func TestBuildOpportunityEpisodesEndsOnThirdMissAndRestarts(t *testing.T) {
	base := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	scans := []historyScan{
		historyScanAt(base, historyPresence{InstrumentID: "AAA-USDT-SWAP", Symbol: "AAA", Price: 100}),
		historyScanAt(base.Add(5*time.Minute), historyPresence{InstrumentID: "AAA-USDT-SWAP", Symbol: "AAA", Price: 102}),
		historyScanAt(base.Add(10 * time.Minute)),
		historyScanAt(base.Add(15*time.Minute), historyPresence{InstrumentID: "AAA-USDT-SWAP", Symbol: "AAA", Price: 104}),
		historyScanAt(base.Add(20 * time.Minute)),
		historyScanAt(base.Add(25 * time.Minute)),
		historyScanAt(base.Add(30 * time.Minute)),
		historyScanAt(base.Add(35*time.Minute), historyPresence{InstrumentID: "AAA-USDT-SWAP", Symbol: "AAA", Price: 110}),
	}

	rows := buildOpportunityEpisodes(scans, base, base.Add(40*time.Minute))
	if len(rows) != 1 || len(rows[0].Episodes) != 2 {
		t.Fatalf("rows = %+v, want one coin with two episodes", rows)
	}
	older, latest := rows[0].Episodes[0], rows[0].Episodes[1]
	if older.Status != historyStatusEnded || older.Appearances != 3 || older.ExperiencedScans != 7 {
		t.Fatalf("older episode = %+v", older)
	}
	if older.EndedAt == nil || !older.EndedAt.Equal(base.Add(30*time.Minute)) {
		t.Fatalf("older EndedAt = %v", older.EndedAt)
	}
	if latest.Status != historyStatusActive || latest.FirstPrice != 110 || latest.Appearances != 1 {
		t.Fatalf("latest episode = %+v", latest)
	}
}

func TestBuildOpportunityEpisodesKeepsTrueStartAcrossWindow(t *testing.T) {
	base := time.Date(2026, 9, 22, 23, 45, 0, 0, time.UTC)
	scans := []historyScan{
		historyScanAt(base, historyPresence{InstrumentID: "AAA-USDT-SWAP", Symbol: "AAA", Price: 100}),
		historyScanAt(base.Add(5*time.Minute), historyPresence{InstrumentID: "AAA-USDT-SWAP", Symbol: "AAA", Price: 101}),
		historyScanAt(base.Add(10*time.Minute), historyPresence{InstrumentID: "AAA-USDT-SWAP", Symbol: "AAA", Price: 102}),
		historyScanAt(base.Add(15 * time.Minute)),
	}

	rows := buildOpportunityEpisodes(scans, base.Add(10*time.Minute), base.Add(20*time.Minute))
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if got := rows[0]; !got.FirstListedAt.Equal(base) || got.FirstPrice != 100 || got.Appearances != 1 || got.ExperiencedScans != 2 {
		t.Fatalf("row = %+v", got)
	}
}

func TestFilterAndSortHistoryRowsPutsMissingReturnsLast(t *testing.T) {
	positive := 8.0
	negative := -2.0
	rows := []OpportunityHistoryRow{
		{InstrumentID: "MISSING", ReturnPct: nil},
		{InstrumentID: "NEG", ReturnPct: &negative},
		{InstrumentID: "POS", ReturnPct: &positive},
	}

	got := filterAndSortHistoryRows(rows, OpportunityHistoryFilter{
		Status: historyStatusAll, Sort: historySortReturn, Direction: "desc",
	})
	if got[0].InstrumentID != "POS" || got[1].InstrumentID != "NEG" || got[2].InstrumentID != "MISSING" {
		t.Fatalf("sorted = %+v", got)
	}
}

func TestFilterAndSortHistoryRowsUsesStableTiesAndStatus(t *testing.T) {
	rows := []OpportunityHistoryRow{
		{InstrumentID: "BBB", Status: historyStatusActive, ListingRate: 50, Appearances: 3},
		{InstrumentID: "AAA", Status: historyStatusActive, ListingRate: 50, Appearances: 3},
		{InstrumentID: "ENDED", Status: historyStatusEnded, ListingRate: 100, Appearances: 10},
	}

	got := filterAndSortHistoryRows(rows, OpportunityHistoryFilter{
		Status: historyStatusActive, Sort: historySortRate, Direction: "desc",
	})
	if len(got) != 2 || got[0].InstrumentID != "AAA" || got[1].InstrumentID != "BBB" || got[0].Rank != 1 || got[1].Rank != 2 {
		t.Fatalf("sorted = %+v", got)
	}
}

func historyScanAt(at time.Time, opportunities ...historyPresence) historyScan {
	items := make(map[string]historyPresence, len(opportunities))
	for _, opportunity := range opportunities {
		items[opportunity.InstrumentID] = opportunity
	}
	return historyScan{FinishedAt: at, Opportunities: items}
}
