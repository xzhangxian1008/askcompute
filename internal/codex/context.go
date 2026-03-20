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
	IsDetail    bool
	Summary     ClinicSummary
	TopDigests  []ClinicDigestSummary
	DetailRows  []ClinicDetailRow
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

type ClinicDetailRow struct {
	TimeUnix    float64
	Digest      string
	QueryTime   float64
	ParseTime   float64
	CompileTime float64
	CopTime     float64
	ProcessTime float64
	WaitTime    float64
	TotalKeys   int64
	ProcessKeys int64
	ResultRows  int64
	MemBytes    int64
	DiskBytes   int64
	Database    string
	Instance    string
	Indexes     string
	Query       string
}
