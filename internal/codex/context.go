package codex

import "time"

type RuntimeContext struct {
	Attachment AttachmentContext
	Clinic     *ClinicContext
}

type ClinicContext struct {
	SourceURL   string
	ClusterID   string
	ClusterName string
	OrgName     string
	DeployType  string
	StartTime   time.Time
	EndTime     time.Time
	Digest      string
	Database    string
	Instance    string
	Summary     ClinicSummary
	TopDigests  []ClinicDigestSummary
	NoRows      bool
}

type ClinicSummary struct {
	TotalQueries  int64
	UniqueDigests int64
	AvgQueryTime  float64
	MaxQueryTime  float64
}

type ClinicDigestSummary struct {
	Digest         string
	ExecutionCount int64
	AvgQueryTime   float64
	MaxQueryTime   float64
	MaxTotalKeys   int64
	MaxProcessKeys int64
	MaxResultRows  int64
	MaxMemBytes    int64
	MaxDiskBytes   int64
	SampleDB       string
	SampleInstance string
	SampleIndexes  string
	SampleSQL      string
}
