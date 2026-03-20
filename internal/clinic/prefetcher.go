package clinic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"lab/askplanner/internal/clinicstore"
	"lab/askplanner/internal/codex"
	"lab/askplanner/internal/config"
)

type Prefetcher struct {
	enabled bool
	client  *Client
	store   *clinicstore.Manager
}

const promptClinicLibraryLimit = 10

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

func NewPrefetcher(cfg *config.Config) (*Prefetcher, error) {
	timeout := time.Duration(cfg.ClinicHTTPTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	var store *clinicstore.Manager
	var err error
	if cfg.ClinicEnableAutoSlowQuery {
		store, err = clinicstore.NewManager(cfg.ClinicStoreDir, cfg.ClinicStoreMaxItems)
		if err != nil {
			return nil, err
		}
	}
	return &Prefetcher{
		enabled: cfg.ClinicEnableAutoSlowQuery,
		client:  NewClient(cfg.ClinicAPIKey, timeout),
		store:   store,
	}, nil
}

func (p *Prefetcher) Enrich(ctx context.Context, userKey, question string, runtime codex.RuntimeContext) (codex.RuntimeContext, error) {
	if !p.enabled {
		return runtime, nil
	}

	runtime, loadErr := p.attachLatestStored(userKey, runtime)
	if loadErr != nil {
		return runtime, loadErr
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
	runtime.Clinic = toClinicRuntimeContext(analysis)

	if storeErr := p.saveAnalysis(userKey, analysis); storeErr != nil {
		log.Printf("[clinic] saved analysis fetch succeeded but local persistence failed for cluster_id=%s: %v", analysis.ClusterID, storeErr)
		if runtime.ClinicLibrary != nil {
			runtime.ClinicLibrary.ActiveItemName = ""
		}
		return runtime, nil
	}
	runtime, err = p.attachLatestStored(userKey, runtime)
	if err != nil {
		return runtime, err
	}
	return runtime, nil
}

func (p *Prefetcher) saveAnalysis(userKey string, analysis *AnalysisContext) error {
	if strings.TrimSpace(userKey) == "" || analysis == nil {
		return nil
	}
	payload, err := json.MarshalIndent(analysis, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal Clinic analysis: %w", err)
	}
	_, err = p.store.Save(clinicstore.SaveRequest{
		UserKey:         userKey,
		AnalysisJSON:    payload,
		SummaryMarkdown: BuildStoredSummary(analysis),
		Metadata: clinicstore.Metadata{
			SourceURL:   analysis.SourceURL,
			ClusterID:   analysis.ClusterID,
			ClusterName: analysis.ClusterName,
			OrgName:     analysis.OrgName,
			DeployType:  analysis.DeployType,
			StartTime:   analysis.StartTime.UTC(),
			EndTime:     analysis.EndTime.UTC(),
			Digest:      analysis.Digest,
			Database:    analysis.Database,
			Instance:    analysis.Instance,
			IsDetail:    analysis.IsDetail,
			SavedAt:     time.Now().UTC(),
		},
	})
	if err != nil {
		return fmt.Errorf("save Clinic analysis: %w", err)
	}
	return nil
}

func (p *Prefetcher) attachLatestStored(userKey string, runtime codex.RuntimeContext) (codex.RuntimeContext, error) {
	if strings.TrimSpace(userKey) == "" {
		return runtime, nil
	}

	library, err := p.store.Snapshot(userKey)
	if err != nil {
		return runtime, fmt.Errorf("load Clinic library snapshot: %w", err)
	}
	runtime.ClinicLibrary = toClinicLibraryContext(library)

	entry, ok, err := p.store.Latest(userKey)
	if err != nil {
		return runtime, fmt.Errorf("load latest Clinic entry: %w", err)
	}
	if !ok {
		runtime.Clinic = nil
		return runtime, nil
	}
	runtime.ClinicLibrary.ActiveItemName = entry.Item.Name

	var analysis AnalysisContext
	if err := json.Unmarshal(entry.AnalysisJSON, &analysis); err != nil {
		return runtime, fmt.Errorf("decode latest Clinic entry %s: %w", entry.Item.Name, err)
	}
	runtime.Clinic = toClinicRuntimeContext(&analysis)
	return runtime, nil
}

func toClinicLibraryContext(library clinicstore.Library) *codex.ClinicLibraryContext {
	if strings.TrimSpace(library.RootDir) == "" {
		return nil
	}
	items := library.Items
	if len(items) > promptClinicLibraryLimit {
		items = items[:promptClinicLibraryLimit]
	}
	ctxItems := make([]codex.ClinicLibraryItem, 0, len(items))
	for _, item := range items {
		ctxItems = append(ctxItems, codex.ClinicLibraryItem{
			Name:        item.Name,
			SavedAt:     item.SavedAt,
			ClusterID:   item.ClusterID,
			ClusterName: item.ClusterName,
			Digest:      item.Digest,
			Database:    item.Database,
			Instance:    item.Instance,
			IsDetail:    item.IsDetail,
		})
	}
	return &codex.ClinicLibraryContext{
		RootDir: library.RootDir,
		Items:   ctxItems,
	}
}

func toClinicRuntimeContext(analysis *AnalysisContext) *codex.ClinicContext {
	if analysis == nil {
		return nil
	}
	ctx := &codex.ClinicContext{
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
		ctx.DetailRows = append(ctx.DetailRows, codex.ClinicDetailRow{
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
		ctx.TopDigests = append(ctx.TopDigests, codex.ClinicDigestSummary{
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
	return ctx
}

func UserFacingMessage(err error) string {
	var userErr *UserError
	if errors.As(err, &userErr) {
		return userErr.Message
	}
	return ""
}
