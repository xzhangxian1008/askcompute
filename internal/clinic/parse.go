package clinic

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	urlPattern       = regexp.MustCompile(`https?://[^\s<>()]+`)
	nowFunc          = func() time.Time { return time.Now().UTC() }
	detailTimeWindow = 5 * time.Minute
)

type LinkSpec struct {
	RawURL     string
	ClusterID  string
	StartTime  time.Time
	EndTime    time.Time
	Digest     string
	Database   string
	Instance   string
	IsDetail   bool
	AnchorTime time.Time
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
	routeText := strings.ToLower(strings.Join([]string{u.Path, fragmentRoute}, " "))
	if !isSlowQueryRoute(routeText) {
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
		IsDetail:  isSlowQueryDetailRoute(routeText),
	}

	start, end, hasExplicitRange, err := parseTimeRange(values)
	if err != nil {
		return nil, true, err
	}
	if hasExplicitRange {
		spec.StartTime = start
		spec.EndTime = end
	} else if spec.IsDetail {
		at, err := parseDetailTimestamp(firstValue(values, "timestamp", "time"))
		if err != nil {
			return nil, true, err
		}
		if !at.IsZero() {
			spec.AnchorTime = at
			spec.StartTime = at.Add(-detailTimeWindow)
			spec.EndTime = at.Add(detailTimeWindow)
		}
	}

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

func isSlowQueryRoute(route string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", "/", "", " ", "").Replace(strings.ToLower(route))
	return strings.Contains(normalized, "slowquery")
}

func isSlowQueryDetailRoute(route string) bool {
	return isSlowQueryRoute(route) && strings.Contains(strings.ToLower(route), "detail")
}

func parseTimeRange(values url.Values) (time.Time, time.Time, bool, error) {
	startRaw := firstValue(values, "startTime", "start", "from", "start_at", "startAt", "start_ts", "start_time")
	endRaw := firstValue(values, "endTime", "end", "to", "end_at", "endAt", "end_ts", "end_time")
	if startRaw == "" && endRaw == "" {
		return time.Time{}, time.Time{}, false, nil
	}

	if strings.EqualFold(strings.TrimSpace(endRaw), "now") {
		end := nowFunc().UTC()
		if startRaw == "" {
			return time.Time{}, time.Time{}, true, fmt.Errorf("missing Clinic slow query start time")
		}
		if seconds, ok := parseRelativeSeconds(startRaw); ok {
			return end.Add(-seconds), end, true, nil
		}
		start, err := parseFlexibleTime(startRaw)
		if err != nil {
			return time.Time{}, time.Time{}, true, fmt.Errorf("parse Clinic slow query start time: %w", err)
		}
		return start, end, true, nil
	}

	start, err := parseFlexibleTime(startRaw)
	if err != nil {
		return time.Time{}, time.Time{}, true, fmt.Errorf("parse Clinic slow query start time: %w", err)
	}
	end, err := parseFlexibleTime(endRaw)
	if err != nil {
		return time.Time{}, time.Time{}, true, fmt.Errorf("parse Clinic slow query end time: %w", err)
	}
	return start, end, true, nil
}

func parseRelativeSeconds(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if isLikelyAbsoluteTimestamp(value) {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds * float64(time.Second)), true
}

func isLikelyAbsoluteTimestamp(value string) bool {
	digits := value
	if strings.Contains(digits, ".") {
		parts := strings.SplitN(digits, ".", 2)
		digits = parts[0]
	}
	if digits == "" {
		return false
	}
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return len(digits) >= 10
}

func parseDetailTimestamp(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}

	if ts, err := parseFlexibleTime(value); err == nil && !ts.IsZero() {
		return ts, nil
	}

	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse Clinic slow query detail timestamp: unsupported timestamp %q", value)
	}
	whole, fractional := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fractional*float64(time.Second))).UTC(), nil
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
