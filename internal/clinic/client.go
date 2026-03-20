package clinic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIBase       = "https://clinic.pingcap.com/clinic/api/v1"
	defaultDataProxyBase = "https://clinic.pingcap.com"
)

type Client struct {
	APIKey        string
	HTTPClient    *http.Client
	APIBaseURL    string
	DataProxyBase string
}

type AnalysisContext struct {
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
	Summary     Summary
	TopDigests  []DigestSummary
	NoRows      bool
}

type Summary struct {
	TotalQueries  int64
	UniqueDigests int64
	AvgQueryTime  float64
	MaxQueryTime  float64
}

type DigestSummary struct {
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

func NewClient(apiKey string, timeout time.Duration) *Client {
	return &Client{
		APIKey:        strings.TrimSpace(apiKey),
		HTTPClient:    &http.Client{Timeout: timeout},
		APIBaseURL:    defaultAPIBase,
		DataProxyBase: defaultDataProxyBase,
	}
}

func (c *Client) FetchSlowQueryContext(ctx context.Context, spec LinkSpec) (*AnalysisContext, error) {
	cluster, err := c.getCluster(ctx, spec.ClusterID)
	if err != nil {
		return nil, err
	}

	summary, err := c.querySummary(ctx, spec)
	if err != nil {
		return nil, err
	}
	topDigests, err := c.queryTopDigests(ctx, spec)
	if err != nil {
		return nil, err
	}

	return &AnalysisContext{
		SourceURL:   spec.RawURL,
		ClusterID:   spec.ClusterID,
		ClusterName: cluster.ClusterName,
		OrgName:     cluster.OrgName,
		DeployType:  cluster.DeployType,
		StartTime:   spec.StartTime.UTC(),
		EndTime:     spec.EndTime.UTC(),
		Digest:      spec.Digest,
		Database:    spec.Database,
		Instance:    spec.Instance,
		Summary:     summary,
		TopDigests:  topDigests,
		NoRows:      summary.TotalQueries == 0,
	}, nil
}

type clusterInfo struct {
	ClusterName string
	OrgName     string
	DeployType  string
}

func (c *Client) getCluster(ctx context.Context, clusterID string) (*clusterInfo, error) {
	params := url.Values{}
	params.Set("cluster_id", clusterID)
	params.Set("show_deleted", "true")

	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.APIBaseURL+"/dashboard/clusters", params, nil, &resp); err != nil {
		return nil, err
	}
	for _, item := range resp.Items {
		if stringValue(item["clusterID"]) != clusterID {
			continue
		}
		return &clusterInfo{
			ClusterName: firstNonEmpty(
				stringValue(item["clusterName"]),
				stringValue(item["name"]),
				stringValue(item["displayName"]),
			),
			OrgName: firstNonEmpty(
				stringValue(item["tenantName"]),
				stringValue(item["orgName"]),
			),
			DeployType: firstNonEmpty(
				stringValue(item["clusterDeployTypeV2"]),
				stringValue(item["clusterDeployType"]),
			),
		}, nil
	}
	return nil, fmt.Errorf("cluster not found in Clinic: %s", clusterID)
}

func (c *Client) querySummary(ctx context.Context, spec LinkSpec) (Summary, error) {
	sql := buildSummarySQL(spec)
	rows, err := c.runDataProxyQuery(ctx, spec.ClusterID, sql)
	if err != nil {
		return Summary{}, err
	}
	if len(rows) == 0 {
		return Summary{}, nil
	}
	row := rows[0]
	return Summary{
		TotalQueries:  int64Value(row["total_queries"]),
		UniqueDigests: int64Value(row["unique_digests"]),
		AvgQueryTime:  float64Value(row["avg_query_time"]),
		MaxQueryTime:  float64Value(row["max_query_time"]),
	}, nil
}

func (c *Client) queryTopDigests(ctx context.Context, spec LinkSpec) ([]DigestSummary, error) {
	rows, err := c.runDataProxyQuery(ctx, spec.ClusterID, buildTopDigestsSQL(spec))
	if err != nil {
		return nil, err
	}

	items := make([]DigestSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, DigestSummary{
			Digest:         stringValue(row["digest"]),
			ExecutionCount: int64Value(row["exec_count"]),
			AvgQueryTime:   float64Value(row["avg_query_time"]),
			MaxQueryTime:   float64Value(row["max_query_time"]),
			MaxTotalKeys:   int64Value(row["max_total_keys"]),
			MaxProcessKeys: int64Value(row["max_process_keys"]),
			MaxResultRows:  int64Value(row["max_result_rows"]),
			MaxMemBytes:    int64Value(row["max_mem_bytes"]),
			MaxDiskBytes:   int64Value(row["max_disk_bytes"]),
			SampleDB:       stringValue(row["sample_db"]),
			SampleInstance: stringValue(row["sample_instance"]),
			SampleIndexes:  stringValue(row["sample_indexes"]),
			SampleSQL:      stringValue(row["sample_sql"]),
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].MaxQueryTime > items[j].MaxQueryTime
	})
	return items, nil
}

func (c *Client) runDataProxyQuery(ctx context.Context, clusterID, sql string) ([]map[string]any, error) {
	payload := map[string]any{
		"sql":       sql,
		"clusterId": clusterID,
		"timeout":   60,
	}
	var resp struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}
	if err := c.doJSON(ctx, http.MethodPost, c.DataProxyBase+"/data-proxy/query", nil, payload, &resp); err != nil {
		return nil, err
	}

	results := make([]map[string]any, 0, len(resp.Rows))
	for _, row := range resp.Rows {
		item := make(map[string]any, len(resp.Columns))
		for i, col := range resp.Columns {
			if i < len(row) {
				item[col] = row[i]
			}
		}
		results = append(results, item)
	}
	return results, nil
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, params url.Values, body any, out any) error {
	if strings.TrimSpace(c.APIKey) == "" {
		return fmt.Errorf("Clinic API key is empty")
	}

	if len(params) > 0 {
		endpoint = endpoint + "?" + params.Encode()
	}

	var reader io.Reader
	if body != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return fmt.Errorf("encode Clinic request: %w", err)
		}
		reader = buf
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build Clinic request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("call Clinic API: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read Clinic response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("Clinic API auth failed: status %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Clinic API returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	if err := json.Unmarshal(bodyBytes, out); err != nil {
		return fmt.Errorf("decode Clinic response: %w", err)
	}
	return nil
}

func buildSummarySQL(spec LinkSpec) string {
	return fmt.Sprintf(`SELECT
  COUNT(*) AS total_queries,
  COUNT(DISTINCT digest) AS unique_digests,
  COALESCE(AVG(query_time), 0) AS avg_query_time,
  COALESCE(MAX(query_time), 0) AS max_query_time
FROM "clinic_data_proxy"."slow_query_logs"
WHERE %s`, buildWhereClause(spec))
}

func buildTopDigestsSQL(spec LinkSpec) string {
	return fmt.Sprintf(`SELECT
  digest,
  COUNT(*) AS exec_count,
  COALESCE(AVG(query_time), 0) AS avg_query_time,
  COALESCE(MAX(query_time), 0) AS max_query_time,
  COALESCE(MAX(TRY_CAST(total_keys AS BIGINT)), 0) AS max_total_keys,
  COALESCE(MAX(TRY_CAST(process_keys AS BIGINT)), 0) AS max_process_keys,
  COALESCE(MAX(result_rows), 0) AS max_result_rows,
  COALESCE(MAX(mem_max), 0) AS max_mem_bytes,
  COALESCE(MAX(disk_max), 0) AS max_disk_bytes,
  arbitrary(db) AS sample_db,
  arbitrary(instance) AS sample_instance,
  arbitrary(index_names) AS sample_indexes,
  arbitrary(query) AS sample_sql
FROM "clinic_data_proxy"."slow_query_logs"
WHERE %s
GROUP BY digest
ORDER BY max_query_time DESC
LIMIT 10`, buildWhereClause(spec))
}

func buildWhereClause(spec LinkSpec) string {
	partitions := partitionDates(spec.StartTime, spec.EndTime)
	quotedDates := make([]string, 0, len(partitions))
	for _, date := range partitions {
		quotedDates = append(quotedDates, "'"+date+"'")
	}

	conditions := []string{
		fmt.Sprintf(`"date" IN (%s)`, strings.Join(quotedDates, ",")),
		fmt.Sprintf(`time >= %.3f`, float64(spec.StartTime.UTC().UnixMilli())/1000),
		fmt.Sprintf(`time <= %.3f`, float64(spec.EndTime.UTC().UnixMilli())/1000),
	}
	if spec.Digest != "" {
		conditions = append(conditions, fmt.Sprintf(`digest = '%s'`, escapeSQLString(spec.Digest)))
	}
	if spec.Database != "" {
		conditions = append(conditions, fmt.Sprintf(`db = '%s'`, escapeSQLString(spec.Database)))
	}
	if spec.Instance != "" {
		conditions = append(conditions, fmt.Sprintf(`instance = '%s'`, escapeSQLString(spec.Instance)))
	}
	return strings.Join(conditions, " AND ")
}

func partitionDates(start, end time.Time) []string {
	start = start.UTC()
	end = end.UTC()
	dates := []string{}
	for current := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC); !current.After(end); current = current.AddDate(0, 0, 1) {
		dates = append(dates, current.Format("20060102"))
	}
	return dates
}

func escapeSQLString(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func int64Value(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case json.Number:
		n, _ := x.Int64()
		return n
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case string:
		if x == "" {
			return 0
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		n, _ := strconv.ParseInt(stringValue(v), 10, 64)
		return n
	}
}

func float64Value(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case json.Number:
		n, _ := x.Float64()
		return n
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		if x == "" {
			return 0
		}
		n, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return n
	default:
		n, _ := strconv.ParseFloat(stringValue(v), 64)
		return n
	}
}
