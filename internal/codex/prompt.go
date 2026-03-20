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
	sb.WriteString(fmt.Sprintf(" total_queries=%d unique_digests=%d avg_query_time_sec=%.6f max_query_time_sec=%.6f\n",
		clinic.Summary.TotalQueries,
		clinic.Summary.UniqueDigests,
		clinic.Summary.AvgQueryTime,
		clinic.Summary.MaxQueryTime,
	))
	if clinic.NoRows {
		sb.WriteString("- Clinic query returned no slow query rows for this exact scope.\n")
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
