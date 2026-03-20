package clinic

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"lab/askplanner/internal/codex"
	"lab/askplanner/internal/config"
)

type Prefetcher struct {
	enabled bool
	client  *Client
}

type UserError struct {
	Message string
	Cause   error
}

func (e *UserError) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func NewPrefetcher(cfg *config.Config) *Prefetcher {
	timeout := time.Duration(cfg.ClinicHTTPTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Prefetcher{
		enabled: cfg.ClinicEnableAutoSlowQuery,
		client:  NewClient(cfg.ClinicAPIKey, timeout),
	}
}

func (p *Prefetcher) Enrich(ctx context.Context, question string, runtime codex.RuntimeContext) (codex.RuntimeContext, error) {
	if !p.enabled {
		return runtime, nil
	}

	spec, matched, err := ParseSlowQueryLink(question)
	if err != nil {
		log.Printf("[clinic] parse failed for potential slow query link: %v", err)
		return runtime, &UserError{
			Message: "I detected a Clinic slow query link but could not parse its cluster ID and time range. Please send the full share link from Clinic Slow Query.",
			Cause:   err,
		}
	}
	if !matched {
		return runtime, nil
	}
	log.Printf("[clinic] parsed slow query link: cluster_id=%s start=%s end=%s digest=%s db=%s instance=%s url=%s",
		spec.ClusterID,
		spec.StartTime.UTC().Format(time.RFC3339),
		spec.EndTime.UTC().Format(time.RFC3339),
		spec.Digest,
		spec.Database,
		spec.Instance,
		spec.RawURL,
	)
	if strings.TrimSpace(p.client.APIKey) == "" {
		log.Printf("[clinic] prefetch skipped: CLINIC_API_KEY is empty for cluster_id=%s", spec.ClusterID)
		return runtime, &UserError{
			Message: "Clinic slow query auto-analysis is enabled, but `CLINIC_API_KEY` is not configured.",
		}
	}

	analysis, err := p.client.FetchSlowQueryContext(ctx, *spec)
	if err != nil {
		log.Printf("[clinic] prefetch failed for cluster_id=%s url=%s: %v", spec.ClusterID, spec.RawURL, err)
		msg := "Clinic slow query prefetch failed."
		if strings.Contains(err.Error(), "auth failed") {
			msg = "Clinic API authentication failed. Check `CLINIC_API_KEY` and verify the key can access clinic.pingcap.com."
		}
		return runtime, &UserError{
			Message: msg,
			Cause:   err,
		}
	}
	log.Printf("[clinic] prefetch succeeded: cluster_id=%s total_queries=%d unique_digests=%d top_digests=%d",
		analysis.ClusterID,
		analysis.Summary.TotalQueries,
		analysis.Summary.UniqueDigests,
		len(analysis.TopDigests),
	)

	runtime.Clinic = &codex.ClinicContext{
		SourceURL:   analysis.SourceURL,
		ClusterID:   analysis.ClusterID,
		ClusterName: analysis.ClusterName,
		OrgName:     analysis.OrgName,
		DeployType:  analysis.DeployType,
		StartTime:   analysis.StartTime,
		EndTime:     analysis.EndTime,
		Digest:      analysis.Digest,
		Database:    analysis.Database,
		Instance:    analysis.Instance,
		IsDetail:    analysis.IsDetail,
		Summary: codex.ClinicSummary{
			TotalQueries:  analysis.Summary.TotalQueries,
			UniqueDigests: analysis.Summary.UniqueDigests,
			AvgQueryTime:  analysis.Summary.AvgQueryTime,
			MaxQueryTime:  analysis.Summary.MaxQueryTime,
		},
		NoRows: analysis.NoRows,
	}
	for _, row := range analysis.DetailRows {
		runtime.Clinic.DetailRows = append(runtime.Clinic.DetailRows, codex.ClinicDetailRow{
			TimeUnix:    row.TimeUnix,
			Digest:      row.Digest,
			QueryTime:   row.QueryTime,
			ParseTime:   row.ParseTime,
			CompileTime: row.CompileTime,
			CopTime:     row.CopTime,
			ProcessTime: row.ProcessTime,
			WaitTime:    row.WaitTime,
			TotalKeys:   row.TotalKeys,
			ProcessKeys: row.ProcessKeys,
			ResultRows:  row.ResultRows,
			MemBytes:    row.MemBytes,
			DiskBytes:   row.DiskBytes,
			Database:    row.Database,
			Instance:    row.Instance,
			Indexes:     row.Indexes,
			Query:       row.Query,
		})
	}
	for _, item := range analysis.TopDigests {
		runtime.Clinic.TopDigests = append(runtime.Clinic.TopDigests, codex.ClinicDigestSummary{
			Digest:         item.Digest,
			ExecutionCount: item.ExecutionCount,
			AvgQueryTime:   item.AvgQueryTime,
			MaxQueryTime:   item.MaxQueryTime,
			MaxTotalKeys:   item.MaxTotalKeys,
			MaxProcessKeys: item.MaxProcessKeys,
			MaxResultRows:  item.MaxResultRows,
			MaxMemBytes:    item.MaxMemBytes,
			MaxDiskBytes:   item.MaxDiskBytes,
			SampleDB:       item.SampleDB,
			SampleInstance: item.SampleInstance,
			SampleIndexes:  item.SampleIndexes,
			SampleSQL:      item.SampleSQL,
		})
	}
	return runtime, nil
}

func UserFacingMessage(err error) string {
	var userErr *UserError
	if errors.As(err, &userErr) {
		return userErr.Message
	}
	return ""
}
