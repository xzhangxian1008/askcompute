package clinic

import (
	"context"
	"testing"
	"time"

	"lab/askplanner/internal/codex"
	"lab/askplanner/internal/config"
)

func TestPrefetcherDisabledKeepsRuntimeContext(t *testing.T) {
	prefetcher, err := NewPrefetcher(&config.Config{ClinicStoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewPrefetcher: %v", err)
	}
	runtimeCtx := codex.RuntimeContext{
		Attachment: codex.AttachmentContext{RootDir: "/tmp/user-a"},
	}

	enriched, err := prefetcher.Enrich(context.Background(), "user-a", "no link here", runtimeCtx)
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}
	if enriched.Attachment.RootDir != "/tmp/user-a" || enriched.Clinic != nil {
		t.Fatalf("unexpected runtime context: %+v", enriched)
	}
}

func TestPrefetcherReturnsUserErrorWhenAPIKeyMissing(t *testing.T) {
	prefetcher, err := NewPrefetcher(&config.Config{
		ClinicEnableAutoSlowQuery: true,
		ClinicHTTPTimeoutSec:      5,
		ClinicStoreDir:            t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewPrefetcher: %v", err)
	}

	_, err = prefetcher.Enrich(context.Background(), "user-a", "https://clinic.pingcap.com/#/slowquery?clusterId=123&startTime=2026-03-20T01:02:03Z&endTime=2026-03-20T02:02:03Z", codex.RuntimeContext{})
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := UserFacingMessage(err); got == "" {
		t.Fatalf("expected user-facing error, got %v", err)
	}
}

func TestPrefetcherLoadsLatestStoredClinicContextWithoutNewLink(t *testing.T) {
	prefetcher, err := NewPrefetcher(&config.Config{
		ClinicEnableAutoSlowQuery: true,
		ClinicStoreDir:            t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewPrefetcher: %v", err)
	}
	analysis := &AnalysisContext{
		SourceURL:   "https://clinic.pingcap.com/#/slowquery?clusterId=123",
		ClusterID:   "123",
		ClusterName: "prod-a",
		StartTime:   time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 3, 20, 11, 0, 0, 0, time.UTC),
		Digest:      "digest-1",
		IsDetail:    true,
		Summary: Summary{
			TotalQueries: 1,
			AvgQueryTime: 1.2,
			MaxQueryTime: 1.2,
		},
		DetailRows: []SlowQueryDetailRow{{
			TimeUnix:  1774000800,
			Digest:    "digest-1",
			QueryTime: 1.2,
			Query:     "select * from t",
		}},
	}
	if err := prefetcher.saveAnalysis("user-a", analysis); err != nil {
		t.Fatalf("saveAnalysis: %v", err)
	}

	enriched, err := prefetcher.Enrich(context.Background(), "user-a", "what should I tune next?", codex.RuntimeContext{})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if enriched.Clinic == nil || enriched.Clinic.ClusterID != "123" || enriched.Clinic.Digest != "digest-1" {
		t.Fatalf("unexpected clinic context: %+v", enriched.Clinic)
	}
	if enriched.ClinicLibrary == nil || enriched.ClinicLibrary.ActiveItemName == "" || len(enriched.ClinicLibrary.Items) != 1 {
		t.Fatalf("unexpected clinic library context: %+v", enriched.ClinicLibrary)
	}
}

func TestPartitionDatesSingleDay(t *testing.T) {
	got := partitionDates(
		time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 20, 11, 0, 0, 0, time.UTC),
	)
	if len(got) != 1 || got[0] != "20260320" {
		t.Fatalf("partitionDates = %v", got)
	}
}
