package clinic

import (
	"context"
	"testing"
	"time"

	"lab/askplanner/internal/codex"
	"lab/askplanner/internal/config"
)

func TestPrefetcherDisabledKeepsRuntimeContext(t *testing.T) {
	prefetcher := NewPrefetcher(&config.Config{})
	runtimeCtx := codex.RuntimeContext{
		Attachment: codex.AttachmentContext{RootDir: "/tmp/user-a"},
	}

	enriched, err := prefetcher.Enrich(context.Background(), "no link here", runtimeCtx)
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}
	if enriched.Attachment.RootDir != "/tmp/user-a" || enriched.Clinic != nil {
		t.Fatalf("unexpected runtime context: %+v", enriched)
	}
}

func TestPrefetcherReturnsUserErrorWhenAPIKeyMissing(t *testing.T) {
	prefetcher := NewPrefetcher(&config.Config{
		ClinicEnableAutoSlowQuery: true,
		ClinicHTTPTimeoutSec:      5,
	})

	_, err := prefetcher.Enrich(context.Background(), "https://clinic.pingcap.com/#/slowquery?clusterId=123&startTime=2026-03-20T01:02:03Z&endTime=2026-03-20T02:02:03Z", codex.RuntimeContext{})
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := UserFacingMessage(err); got == "" {
		t.Fatalf("expected user-facing error, got %v", err)
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
