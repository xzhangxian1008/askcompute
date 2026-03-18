package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
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

func BuildInitialPrompt(normalizedPrompt, summary, question string) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(normalizedPrompt))
	sb.WriteString("\n\n## Runtime Context\n")
	sb.WriteString("- You are serving a TiDB query tuning chat relay backed by Codex CLI.\n")
	sb.WriteString("- Answer the user's latest message directly.\n")
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

func BuildResumePrompt(question string) string {
	return fmt.Sprintf(
		"Continue the existing TiDB query tuning conversation.\n\nNew user message:\n%s\n",
		strings.TrimSpace(question),
	)
}
