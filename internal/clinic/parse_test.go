package clinic

import (
	"testing"
	"time"
)

func withNow(t *testing.T, now time.Time) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() {
		nowFunc = prev
	})
}

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

func TestParseSlowQueryLinkSupportsSlowQueryUnderscoreRoute(t *testing.T) {
	spec, matched, err := ParseSlowQueryLink("https://clinic.pingcap.com/portal/dashboard/cloud/ngm.html?clusterId=10324983984131567830#/slow_query?from=1773936000&row=0&to=1773936060")
	if err != nil {
		t.Fatalf("ParseSlowQueryLink returned error: %v", err)
	}
	if !matched {
		t.Fatalf("expected Clinic slow query link to match")
	}
	if spec.ClusterID != "10324983984131567830" {
		t.Fatalf("cluster ID = %q", spec.ClusterID)
	}
	if got := spec.EndTime.Sub(spec.StartTime); got != time.Minute {
		t.Fatalf("time range = %s, want 1m", got)
	}
}

func TestParseSlowQueryLinkSupportsRelativeWindowToNow(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	withNow(t, now)

	spec, matched, err := ParseSlowQueryLink("https://clinic.pingcap.com/portal/dashboard/cloud/ngm.html?clusterId=123#/slow_query?from=300&row=0&to=now")
	if err != nil {
		t.Fatalf("ParseSlowQueryLink returned error: %v", err)
	}
	if !matched {
		t.Fatalf("expected Clinic slow query link to match")
	}
	if !spec.EndTime.Equal(now) {
		t.Fatalf("end time = %s, want %s", spec.EndTime, now)
	}
	if got := spec.EndTime.Sub(spec.StartTime); got != 5*time.Minute {
		t.Fatalf("time range = %s, want 5m", got)
	}
}

func TestParseSlowQueryDetailLinkUsesTimestampWindow(t *testing.T) {
	spec, matched, err := ParseSlowQueryLink("https://clinic.pingcap.com/portal/dashboard/cloud/ngm.html?clusterId=10324983984131567830#/slow_query/detail?digest=abc&connection_id=2372076364&timestamp=1773973859.727374")
	if err != nil {
		t.Fatalf("ParseSlowQueryLink returned error: %v", err)
	}
	if !matched {
		t.Fatalf("expected Clinic slow query detail link to match")
	}
	if spec.Digest != "abc" {
		t.Fatalf("digest = %q, want abc", spec.Digest)
	}
	if !spec.IsDetail {
		t.Fatalf("expected detail route")
	}
	if got := spec.EndTime.Sub(spec.StartTime); got != 2*detailTimeWindow {
		t.Fatalf("time range = %s, want %s", got, 2*detailTimeWindow)
	}
	if center := spec.StartTime.Add(detailTimeWindow); center.Unix() != 1773973859 {
		t.Fatalf("derived center unix = %d, want 1773973859", center.Unix())
	}
}

func TestParseSlowQueryDetailExplicitRangeOverridesTimestamp(t *testing.T) {
	spec, matched, err := ParseSlowQueryLink("https://clinic.pingcap.com/portal/dashboard/cloud/ngm.html?clusterId=123#/slow_query/detail?timestamp=1773973859.727374&from=1773936000&to=1773936060")
	if err != nil {
		t.Fatalf("ParseSlowQueryLink returned error: %v", err)
	}
	if !matched {
		t.Fatalf("expected Clinic slow query detail link to match")
	}
	if spec.StartTime.Unix() != 1773936000 || spec.EndTime.Unix() != 1773936060 {
		t.Fatalf("explicit range was not preserved: start=%d end=%d", spec.StartTime.Unix(), spec.EndTime.Unix())
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

func TestParseSlowQueryDetailLinkInvalidTimestamp(t *testing.T) {
	_, matched, err := ParseSlowQueryLink("https://clinic.pingcap.com/portal/dashboard/cloud/ngm.html?clusterId=123#/slow_query/detail?timestamp=bad-value")
	if !matched {
		t.Fatalf("expected malformed slow query detail link to still be recognized")
	}
	if err == nil {
		t.Fatalf("expected parse error")
	}
}
