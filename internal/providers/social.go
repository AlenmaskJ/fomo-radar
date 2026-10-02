package providers

import (
	"context"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

// MetadataSocialProvider exposes public links but no synthetic mention count.
type MetadataSocialProvider struct {
	now func() time.Time
}

func NewMetadataSocialProvider(now func() time.Time) *MetadataSocialProvider {
	return &MetadataSocialProvider{now: now}
}

func (p *MetadataSocialProvider) Evidence(_ context.Context, _ domain.Candidate, m domain.MarketSnapshot) (domain.SocialEvidence, error) {
	collectedAt := providerNow(p.now)
	links := append([]domain.Link(nil), m.Links...)
	return domain.SocialEvidence{
		CollectedAt:  collectedAt,
		Links:        links,
		MentionCount: domain.DataValue[int]{Quality: domain.QualityMissing, Source: "public_metadata", CollectedAt: collectedAt},
	}, nil
}
