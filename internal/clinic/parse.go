package clinic

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var urlPattern = regexp.MustCompile(`https?://[^\s<>()]+`)

type LinkSpec struct {
	RawURL    string
	ClusterID string
	StartTime time.Time
	EndTime   time.Time
	Digest    string
	Database  string
	Instance  string
}

func ParseSlowQueryLink(text string) (*LinkSpec, bool, error) {
	for _, raw := range urlPattern.FindAllString(text, -1) {
		spec, matched, err := parseSlowQueryURL(strings.TrimRight(raw, ".,;:!?"))
		if !matched {
			continue
		}
		return spec, true, err
	}
	return nil, false, nil
}

func parseSlowQueryURL(raw string) (*LinkSpec, bool, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, false, nil
	}
	if !strings.EqualFold(u.Hostname(), "clinic.pingcap.com") {
		return nil, false, nil
	}

	fragmentRoute, fragmentValues := parseFragment(u.Fragment)
	pathJoined := strings.ToLower(strings.Join([]string{u.Path, fragmentRoute}, " "))
	if !strings.Contains(pathJoined, "slowquery") && !strings.Contains(pathJoined, "slow-query") {
		return nil, false, nil
	}

	values := url.Values{}
	copyValues(values, u.Query())
	copyValues(values, fragmentValues)

	spec := &LinkSpec{
		RawURL:    raw,
		ClusterID: firstValue(values, "clusterId", "clusterID", "cluster_id"),
		Digest:    firstValue(values, "digest", "sqlDigest", "sql_digest", "queryDigest"),
		Database:  firstValue(values, "db", "database", "schema", "schema_name"),
		Instance:  firstValue(values, "instance", "tidbAddr", "tidb_addr", "address", "node"),
	}

	start, err := parseFlexibleTime(firstValue(values, "startTime", "start", "from", "start_at", "startAt", "start_ts", "start_time"))
	if err != nil {
		return nil, true, fmt.Errorf("parse Clinic slow query start time: %w", err)
	}
	end, err := parseFlexibleTime(firstValue(values, "endTime", "end", "to", "end_at", "endAt", "end_ts", "end_time"))
	if err != nil {
		return nil, true, fmt.Errorf("parse Clinic slow query end time: %w", err)
	}
	spec.StartTime = start
	spec.EndTime = end

	switch {
	case spec.ClusterID == "":
		return nil, true, fmt.Errorf("missing cluster ID in Clinic slow query link")
	case spec.StartTime.IsZero() || spec.EndTime.IsZero():
		return nil, true, fmt.Errorf("missing time range in Clinic slow query link")
	case spec.EndTime.Before(spec.StartTime):
		return nil, true, fmt.Errorf("invalid time range in Clinic slow query link")
	default:
		return spec, true, nil
	}
}

func parseFragment(fragment string) (string, url.Values) {
	if fragment == "" {
		return "", url.Values{}
	}

	route := fragment
	queryText := ""
	if strings.Contains(fragment, "?") {
		parts := strings.SplitN(fragment, "?", 2)
		route = parts[0]
		queryText = parts[1]
	} else if strings.Contains(fragment, "=") {
		queryText = fragment
		route = ""
	}

	values, _ := url.ParseQuery(queryText)
	return route, values
}

func copyValues(dst, src url.Values) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func firstValue(values url.Values, keys ...string) string {
	for _, key := range keys {
		for existing, vals := range values {
			if !strings.EqualFold(existing, key) || len(vals) == 0 {
				continue
			}
			v := strings.TrimSpace(vals[0])
			if v != "" {
				return v
			}
		}
	}
	return ""
}

func parseFlexibleTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		switch len(value) {
		case 10:
			return time.Unix(n, 0).UTC(), nil
		case 13:
			return time.UnixMilli(n).UTC(), nil
		case 16:
			return time.UnixMicro(n).UTC(), nil
		case 19:
			return time.Unix(0, n).UTC(), nil
		}
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04",
	}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}
