package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

func LoadPrompt(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read prompt file %s: %w", path, err)
	}
	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return "", fmt.Errorf("prompt file %s is empty", path)
	}
	return prompt, nil
}

func PromptHash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

type AttachmentItem struct {
	Name         string
	Type         string
	SavedAt      time.Time
	OriginalName string
}

type AttachmentContext struct {
	RootDir string
	Items   []AttachmentItem
}

func BuildInitialPrompt(normalizedPrompt, summary, question string, runtime RuntimeContext) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(normalizedPrompt))
	sb.WriteString("\n\n## Runtime Context\n")
	sb.WriteString("- You are serving a TiDB query tuning chat relay backed by Codex CLI.\n")
	sb.WriteString("- Answer the user's latest message directly.\n")
	writeAttachmentContext(&sb, runtime.Attachment)
	writeClinicLibraryContext(&sb, runtime.ClinicLibrary)
	writeClinicContext(&sb, runtime.Clinic)
	if strings.TrimSpace(summary) != "" {
		sb.WriteString("\n## Conversation Summary\n")
		sb.WriteString(strings.TrimSpace(summary))
		sb.WriteByte('\n')
	}
	sb.WriteString("\n## User Message\n")
	sb.WriteString(strings.TrimSpace(question))
	sb.WriteByte('\n')
	return sb.String()
}

func BuildResumePrompt(question string, runtime RuntimeContext) string {
	var sb strings.Builder
	sb.WriteString("Continue the existing TiDB query tuning conversation.\n")
	writeAttachmentContext(&sb, runtime.Attachment)
	writeClinicLibraryContext(&sb, runtime.ClinicLibrary)
	writeClinicContext(&sb, runtime.Clinic)
	sb.WriteString("\nNew user message:\n")
	sb.WriteString(strings.TrimSpace(question))
	sb.WriteByte('\n')
	return sb.String()
}

func writeAttachmentContext(sb *strings.Builder, attachment AttachmentContext) {
	rootDir := strings.TrimSpace(attachment.RootDir)
	if rootDir == "" {
		return
	}

	sb.WriteString("- The current user's uploaded-file library is stored under: ")
	sb.WriteString(rootDir)
	sb.WriteString("\n")
	sb.WriteString("- If the user asks you to inspect or analyze a file, first inspect this user library.\n")
	sb.WriteString("- In group chats, the user can load recent attachments into this library with `/upload_<n> your question`, for example `/upload_3 analyze these files`. If the user asks how to use that command, explain this format.\n")
	sb.WriteString("- If you cannot tell which file the user means, do not guess. Use only the visible top-level entries below and ask the user which one to inspect.\n")

	items := append([]AttachmentItem(nil), attachment.Items...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].SavedAt.Equal(items[j].SavedAt) {
			return items[i].Name < items[j].Name
		}
		return items[i].SavedAt.After(items[j].SavedAt)
	})
	if len(items) == 0 {
		sb.WriteString("- Current top-level entries: none.\n")
		return
	}

	sb.WriteString("- Current top-level entries (newest first):\n")
	for _, item := range items {
		sb.WriteString("  - ")
		sb.WriteString(item.Name)
		if strings.TrimSpace(item.Type) != "" {
			sb.WriteString(" [")
			sb.WriteString(strings.TrimSpace(item.Type))
			sb.WriteByte(']')
		}
		if !item.SavedAt.IsZero() {
			sb.WriteString(" saved_at=")
			sb.WriteString(item.SavedAt.Format(time.RFC3339))
		}
		if original := strings.TrimSpace(item.OriginalName); original != "" && original != item.Name {
			sb.WriteString(" original_name=")
			sb.WriteString(original)
		}
		sb.WriteByte('\n')
	}
}

func writeClinicLibraryContext(sb *strings.Builder, library *ClinicLibraryContext) {
	if library == nil || strings.TrimSpace(library.RootDir) == "" {
		return
	}

	sb.WriteString("- The current user's saved Clinic slow-query library is stored under: ")
	sb.WriteString(strings.TrimSpace(library.RootDir))
	sb.WriteString("\n")
	sb.WriteString("- Each Clinic entry is a top-level directory containing `metadata.json`, `analysis.json`, and `summary.md`.\n")
	sb.WriteString("- If the user asks a follow-up question about Clinic slow-query data without sending a new link, default to the active Clinic entry below.\n")
	sb.WriteString("- If the user clearly refers to another visible Clinic entry, inspect that entry instead. If you still cannot tell which entry they mean, do not guess; ask the user which Clinic entry to use.\n")

	items := append([]ClinicLibraryItem(nil), library.Items...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].SavedAt.Equal(items[j].SavedAt) {
			return items[i].Name < items[j].Name
		}
		return items[i].SavedAt.After(items[j].SavedAt)
	})
	if len(items) == 0 {
		sb.WriteString("- Current Clinic entries: none.\n")
		return
	}
	if active := strings.TrimSpace(library.ActiveItemName); active != "" {
		sb.WriteString("- Current active Clinic entry: ")
		sb.WriteString(active)
		sb.WriteByte('\n')
	}
	sb.WriteString("- Current Clinic entries (newest first):\n")
	for _, item := range items {
		sb.WriteString("  - ")
		sb.WriteString(item.Name)
		if item.IsDetail {
			sb.WriteString(" [detail]")
		} else {
			sb.WriteString(" [list]")
		}
		if !item.SavedAt.IsZero() {
			sb.WriteString(" saved_at=")
			sb.WriteString(item.SavedAt.Format(time.RFC3339))
		}
		if item.ClusterID != "" {
			sb.WriteString(" cluster_id=")
			sb.WriteString(item.ClusterID)
		}
		if item.ClusterName != "" {
			sb.WriteString(" cluster_name=")
			sb.WriteString(item.ClusterName)
		}
		if item.Digest != "" {
			sb.WriteString(" digest=")
			sb.WriteString(item.Digest)
		}
		if item.Database != "" {
			sb.WriteString(" db=")
			sb.WriteString(item.Database)
		}
		if item.Instance != "" {
			sb.WriteString(" instance=")
			sb.WriteString(item.Instance)
		}
		sb.WriteByte('\n')
	}
}

func writeClinicContext(sb *strings.Builder, clinic *ClinicContext) {
	if clinic == nil {
		return
	}

	sb.WriteString("- Clinic slow query link detected and prefetched by the relay. Treat the fetched data below as the ground truth for this turn.\n")
	sb.WriteString("- Clinic source URL: ")
	sb.WriteString(strings.TrimSpace(clinic.SourceURL))
	sb.WriteByte('\n')
	sb.WriteString("- Clinic cluster scope: cluster_id=")
	sb.WriteString(strings.TrimSpace(clinic.ClusterID))
	if v := strings.TrimSpace(clinic.ClusterName); v != "" {
		sb.WriteString(" cluster_name=")
		sb.WriteString(v)
	}
	if v := strings.TrimSpace(clinic.OrgName); v != "" {
		sb.WriteString(" org_name=")
		sb.WriteString(v)
	}
	if v := strings.TrimSpace(clinic.DeployType); v != "" {
		sb.WriteString(" deploy_type=")
		sb.WriteString(v)
	}
	sb.WriteByte('\n')
	if !clinic.StartTime.IsZero() && !clinic.EndTime.IsZero() {
		sb.WriteString("- Clinic time range (UTC): ")
		sb.WriteString(clinic.StartTime.UTC().Format(time.RFC3339))
		sb.WriteString(" to ")
		sb.WriteString(clinic.EndTime.UTC().Format(time.RFC3339))
		sb.WriteByte('\n')
	}
	if clinic.Digest != "" || clinic.Database != "" || clinic.Instance != "" {
		sb.WriteString("- Clinic filters:")
		if clinic.Digest != "" {
			sb.WriteString(" digest=")
			sb.WriteString(clinic.Digest)
		}
		if clinic.Database != "" {
			sb.WriteString(" db=")
			sb.WriteString(clinic.Database)
		}
		if clinic.Instance != "" {
			sb.WriteString(" instance=")
			sb.WriteString(clinic.Instance)
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("- Clinic aggregate slow query stats:")
	sb.WriteString(fmt.Sprintf(" total_queries=%d", clinic.Summary.TotalQueries))
	if clinic.Summary.UniqueDigests > 0 {
		sb.WriteString(fmt.Sprintf(" unique_digests=%d", clinic.Summary.UniqueDigests))
	}
	sb.WriteString(fmt.Sprintf(" avg_query_time_sec=%.6f max_query_time_sec=%.6f\n",
		clinic.Summary.AvgQueryTime,
		clinic.Summary.MaxQueryTime,
	))
	if clinic.NoRows {
		sb.WriteString("- Clinic query returned no slow query rows for this exact scope.\n")
		return
	}
	if clinic.IsDetail && len(clinic.DetailRows) > 0 {
		sb.WriteString("- Clinic slow-query detail rows:\n")
		for _, row := range clinic.DetailRows {
			sb.WriteString("  - ")
			sb.WriteString(fmt.Sprintf("time_unix=%.6f digest=%s query_time_sec=%.6f parse_sec=%.6f compile_sec=%.6f cop_sec=%.6f process_sec=%.6f wait_sec=%.6f",
				row.TimeUnix,
				row.Digest,
				row.QueryTime,
				row.ParseTime,
				row.CompileTime,
				row.CopTime,
				row.ProcessTime,
				row.WaitTime,
			))
			if row.TotalKeys > 0 {
				sb.WriteString(fmt.Sprintf(" total_keys=%d", row.TotalKeys))
			}
			if row.ProcessKeys > 0 {
				sb.WriteString(fmt.Sprintf(" process_keys=%d", row.ProcessKeys))
			}
			if row.ResultRows > 0 {
				sb.WriteString(fmt.Sprintf(" result_rows=%d", row.ResultRows))
			}
			if row.MemBytes > 0 {
				sb.WriteString(fmt.Sprintf(" mem_bytes=%d", row.MemBytes))
			}
			if row.DiskBytes > 0 {
				sb.WriteString(fmt.Sprintf(" disk_bytes=%d", row.DiskBytes))
			}
			if row.Database != "" {
				sb.WriteString(" db=")
				sb.WriteString(row.Database)
			}
			if row.Instance != "" {
				sb.WriteString(" instance=")
				sb.WriteString(row.Instance)
			}
			if row.Indexes != "" {
				sb.WriteString(" indexes=")
				sb.WriteString(compactText(row.Indexes, 120))
			}
			if row.Query != "" {
				sb.WriteString(" query=")
				sb.WriteString(compactText(row.Query, 240))
			}
			sb.WriteByte('\n')
		}
		return
	}
	if len(clinic.TopDigests) == 0 {
		sb.WriteString("- Clinic did not return grouped slow-query digests.\n")
		return
	}

	sb.WriteString("- Clinic top slow-query digests (grouped):\n")
	for _, item := range clinic.TopDigests {
		sb.WriteString("  - digest=")
		sb.WriteString(item.Digest)
		sb.WriteString(fmt.Sprintf(" exec_count=%d avg_sec=%.6f max_sec=%.6f",
			item.ExecutionCount,
			item.AvgQueryTime,
			item.MaxQueryTime,
		))
		if item.MaxTotalKeys > 0 {
			sb.WriteString(fmt.Sprintf(" max_total_keys=%d", item.MaxTotalKeys))
		}
		if item.MaxProcessKeys > 0 {
			sb.WriteString(fmt.Sprintf(" max_process_keys=%d", item.MaxProcessKeys))
		}
		if item.MaxResultRows > 0 {
			sb.WriteString(fmt.Sprintf(" max_result_rows=%d", item.MaxResultRows))
		}
		if item.MaxMemBytes > 0 {
			sb.WriteString(fmt.Sprintf(" max_mem_bytes=%d", item.MaxMemBytes))
		}
		if item.MaxDiskBytes > 0 {
			sb.WriteString(fmt.Sprintf(" max_disk_bytes=%d", item.MaxDiskBytes))
		}
		if item.SampleDB != "" {
			sb.WriteString(" db=")
			sb.WriteString(item.SampleDB)
		}
		if item.SampleInstance != "" {
			sb.WriteString(" instance=")
			sb.WriteString(item.SampleInstance)
		}
		if item.SampleIndexes != "" {
			sb.WriteString(" indexes=")
			sb.WriteString(compactText(item.SampleIndexes, 120))
		}
		if item.SampleSQL != "" {
			sb.WriteString(" sample_sql=")
			sb.WriteString(compactText(item.SampleSQL, 240))
		}
		sb.WriteByte('\n')
	}
}
