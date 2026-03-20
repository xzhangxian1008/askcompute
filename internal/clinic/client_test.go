package clinic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBuildWhereClauseIncludesPartitionsAndFilters(t *testing.T) {
	spec := LinkSpec{
		ClusterID: "123",
		StartTime: time.Date(2026, 3, 20, 23, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 3, 21, 1, 0, 0, 0, time.UTC),
		Digest:    "digest-1",
		Database:  "app",
		Instance:  "tidb-0",
	}

	where := buildWhereClause(spec)
	wantSnippets := []string{
		`"date" IN ('20260320','20260321')`,
		`digest = 'digest-1'`,
		`db = 'app'`,
		`instance = 'tidb-0'`,
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(where, snippet) {
			t.Fatalf("where clause missing %q: %s", snippet, where)
		}
	}
}

func TestFetchSlowQueryContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/clinic/api/v1/dashboard/clusters":
			io.WriteString(w, `{"items":[{"clusterID":"123","clusterName":"prod-a","tenantName":"Acme","clusterDeployTypeV2":"premium"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/data-proxy/query":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			sql, _ := payload["sql"].(string)
			if strings.Contains(sql, "COUNT(*) AS total_queries") {
				io.WriteString(w, `{"columns":["total_queries","unique_digests","avg_query_time","max_query_time"],"rows":[[24,3,1.25,7.5]]}`)
				return
			}
			io.WriteString(w, `{"columns":["digest","exec_count","avg_query_time","max_query_time","max_total_keys","max_process_keys","max_result_rows","max_mem_bytes","max_disk_bytes","sample_db","sample_instance","sample_indexes","sample_sql"],"rows":[["digest-1",12,1.2,7.5,1000,800,10,2048,0,"app","tidb-0","idx_a","select * from t where a = 1"]]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient("token", 5*time.Second)
	client.APIBaseURL = server.URL + "/clinic/api/v1"
	client.DataProxyBase = server.URL

	result, err := client.FetchSlowQueryContext(context.Background(), LinkSpec{
		RawURL:    "https://clinic.pingcap.com/#/slowquery?clusterId=123",
		ClusterID: "123",
		StartTime: time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 3, 20, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("FetchSlowQueryContext returned error: %v", err)
	}
	if result.ClusterName != "prod-a" || result.OrgName != "Acme" || result.DeployType != "premium" {
		t.Fatalf("unexpected cluster metadata: %+v", result)
	}
	if result.Summary.TotalQueries != 24 || len(result.TopDigests) != 1 {
		t.Fatalf("unexpected query result: %+v", result)
	}
	if result.NoRows {
		t.Fatalf("expected NoRows=false")
	}
}

func TestDoJSONReturnsAuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer server.Close()

	client := NewClient("bad-token", 5*time.Second)
	client.APIBaseURL = server.URL

	var out map[string]any
	err := client.doJSON(context.Background(), http.MethodGet, server.URL, nil, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("expected auth failure, got %v", err)
	}
}
