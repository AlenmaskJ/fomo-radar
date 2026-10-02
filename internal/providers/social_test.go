package providers

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestMetadataSocialProviderPreservesLinksWithoutInventingMentions(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	links := []domain.Link{{Kind: domain.LinkWebsite, URL: "https://new.example"}, {Kind: domain.LinkX, URL: "https://x.com/newcoin"}, {Kind: domain.LinkTelegram, URL: "https://t.me/newcoin"}}
	provider := NewMetadataSocialProvider(func() time.Time { return now })
	evidence, err := provider.Evidence(context.Background(), domain.Candidate{Address: "token-new"}, domain.MarketSnapshot{Links: links})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(evidence.Links, links) {
		t.Fatalf("links=%+v, want %+v", evidence.Links, links)
	}
	if evidence.MentionCount.Quality != domain.QualityMissing || evidence.MentionCount.Value != 0 {
		t.Fatalf("mention count=%+v, want missing", evidence.MentionCount)
	}
	if !evidence.CollectedAt.Equal(now) {
		t.Fatalf("CollectedAt=%s, want %s", evidence.CollectedAt, now)
	}
}
