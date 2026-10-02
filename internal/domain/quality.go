package domain

import "time"

// Quality describes the trustworthiness and availability of collected data.
type Quality string

const (
	QualityFresh    Quality = "fresh"
	QualityCached   Quality = "cached"
	QualityMissing  Quality = "missing"
	QualityDegraded Quality = "degraded"
)

// DataValue pairs provider data with its collection and quality metadata.
type DataValue[T any] struct {
	Value       T          `json:"value"`
	Quality     Quality    `json:"quality"`
	Source      string     `json:"source"`
	SourceTime  *time.Time `json:"source_time,omitempty"`
	CollectedAt time.Time  `json:"collected_at"`
}
