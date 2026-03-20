package clinic

import (
	"fmt"
	"strings"
	"time"
)

func BuildStoredSummary(analysis *AnalysisContext) string {
	if analysis == nil {
		return "# Clinic Slow Query\n\nNo Clinic analysis data was saved.\n"
	}

	var sb strings.Builder
	sb.WriteString("# Clinic Slow Query Snapshot\n\n")
	if analysis.SourceURL != "" {
		sb.WriteString("- Source URL: ")
		sb.WriteString(strings.TrimSpace(analysis.SourceURL))
		sb.WriteByte('\n')
	}
	sb.WriteString("- Cluster ID: ")
	sb.WriteString(strings.TrimSpace(analysis.ClusterID))
	if analysis.ClusterName != "" {
		sb.WriteString(" (")
		sb.WriteString(strings.TrimSpace(analysis.ClusterName))
		sb.WriteByte(')')
	}
	sb.WriteByte('\n')
	if analysis.OrgName != "" {
		sb.WriteString("- Org: ")
		sb.WriteString(strings.TrimSpace(analysis.OrgName))
		sb.WriteByte('\n')
	}
	if analysis.DeployType != "" {
		sb.WriteString("- Deploy Type: ")
		sb.WriteString(strings.TrimSpace(analysis.DeployType))
		sb.WriteByte('\n')
	}
	if !analysis.StartTime.IsZero() && !analysis.EndTime.IsZero() {
		sb.WriteString("- Time Range (UTC): ")
		sb.WriteString(analysis.StartTime.UTC().Format(time.RFC3339))
		sb.WriteString(" to ")
		sb.WriteString(analysis.EndTime.UTC().Format(time.RFC3339))
		sb.WriteByte('\n')
	}
	if analysis.Digest != "" || analysis.Database != "" || analysis.Instance != "" {
		sb.WriteString("- Filters:")
		if analysis.Digest != "" {
			sb.WriteString(" digest=")
			sb.WriteString(analysis.Digest)
		}
		if analysis.Database != "" {
			sb.WriteString(" db=")
			sb.WriteString(analysis.Database)
		}
		if analysis.Instance != "" {
			sb.WriteString(" instance=")
			sb.WriteString(analysis.Instance)
		}
		sb.WriteByte('\n')
	}
	sb.WriteString(fmt.Sprintf("- Summary: total_queries=%d avg_query_time_sec=%.6f max_query_time_sec=%.6f\n",
		analysis.Summary.TotalQueries,
		analysis.Summary.AvgQueryTime,
		analysis.Summary.MaxQueryTime,
	))
	if analysis.NoRows {
		sb.WriteString("- No slow query rows were returned for this Clinic scope.\n")
		return sb.String()
	}

	if analysis.IsDetail && len(analysis.DetailRows) > 0 {
		sb.WriteString("\n## Detail Rows\n\n")
		for _, row := range analysis.DetailRows {
			sb.WriteString("- ")
			sb.WriteString(fmt.Sprintf("time_unix=%.6f query_time_sec=%.6f", row.TimeUnix, row.QueryTime))
			if row.Digest != "" {
				sb.WriteString(" digest=")
				sb.WriteString(row.Digest)
			}
			if row.Database != "" {
				sb.WriteString(" db=")
				sb.WriteString(row.Database)
			}
			if row.Instance != "" {
				sb.WriteString(" instance=")
				sb.WriteString(row.Instance)
			}
			if row.Query != "" {
				sb.WriteString("\n  sql: ")
				sb.WriteString(compactSummaryText(row.Query, 400))
			}
			sb.WriteByte('\n')
		}
		return sb.String()
	}

	sb.WriteString("\n## Top Digests\n\n")
	if len(analysis.TopDigests) == 0 {
		sb.WriteString("- No grouped digest rows were returned.\n")
		return sb.String()
	}
	for _, item := range analysis.TopDigests {
		sb.WriteString("- digest=")
		sb.WriteString(item.Digest)
		sb.WriteString(fmt.Sprintf(" exec_count=%d avg_sec=%.6f max_sec=%.6f",
			item.ExecutionCount,
			item.AvgQueryTime,
			item.MaxQueryTime,
		))
		if item.SampleDB != "" {
			sb.WriteString(" db=")
			sb.WriteString(item.SampleDB)
		}
		if item.SampleInstance != "" {
			sb.WriteString(" instance=")
			sb.WriteString(item.SampleInstance)
		}
		if item.SampleSQL != "" {
			sb.WriteString("\n  sample_sql: ")
			sb.WriteString(compactSummaryText(item.SampleSQL, 400))
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func compactSummaryText(text string, maxLen int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if maxLen <= 0 || len(text) <= maxLen {
		return text
	}
	if maxLen <= 3 {
		return text[:maxLen]
	}
	return text[:maxLen-3] + "..."
}
