package clinic

import (
	"testing"
	"time"
)

func TestParseSlowQueryLinkFromHashRoute(t *testing.T) {
	spec, matched, err := ParseSlowQueryLink("please inspect https://clinic.pingcap.com/#/slowquery?clusterId=123&startTime=2026-03-20T01:02:03Z&endTime=2026-03-20T02:02:03Z&digest=abc&db=test&instance=tidb-1")
	if err != nil {
		t.Fatalf("ParseSlowQueryLink returned error: %v", err)
	}
	if !matched {
		t.Fatalf("expected Clinic slow query link to match")
	}
	if spec.ClusterID != "123" || spec.Digest != "abc" || spec.Database != "test" || spec.Instance != "tidb-1" {
		t.Fatalf("unexpected spec: %+v", spec)
	}
	if got, want := spec.StartTime, time.Date(2026, 3, 20, 1, 2, 3, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("start time = %s, want %s", got, want)
	}
}

func TestParseSlowQueryLinkFromQueryStringWithUnixMillis(t *testing.T) {
	spec, matched, err := ParseSlowQueryLink("https://clinic.pingcap.com/slowquery?clusterID=456&start=1760000000000&end=1760003600000")
	if err != nil {
		t.Fatalf("ParseSlowQueryLink returned error: %v", err)
	}
	if !matched {
		t.Fatalf("expected Clinic slow query link to match")
	}
	if spec.ClusterID != "456" {
		t.Fatalf("cluster ID = %q, want 456", spec.ClusterID)
	}
	if got := spec.EndTime.Sub(spec.StartTime); got != time.Hour {
		t.Fatalf("time range = %s, want 1h", got)
	}
}

func TestParseSlowQueryLinkMissingRequiredFields(t *testing.T) {
	_, matched, err := ParseSlowQueryLink("https://clinic.pingcap.com/#/slowquery?startTime=2026-03-20T01:02:03Z&endTime=2026-03-20T02:02:03Z")
	if !matched {
		t.Fatalf("expected malformed slow query link to still be recognized")
	}
	if err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestParseSlowQueryLinkIgnoresOtherClinicPages(t *testing.T) {
	_, matched, err := ParseSlowQueryLink("https://clinic.pingcap.com/#/clusters?clusterId=123")
	if err != nil {
		t.Fatalf("ParseSlowQueryLink returned error: %v", err)
	}
	if matched {
		t.Fatalf("expected non-slowquery Clinic URL to be ignored")
	}
}
