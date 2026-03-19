package config

import (
	"fmt"
	_ "io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	ProjectRoot string

	// Prompt
	PromptFile string // absolute path, default: "<ProjectRoot>/prompt"

	// Codex CLI
	CodexBin             string
	CodexModel           string
	CodexReasoningEffort string
	CodexSandbox         string
	CodexSessionStore    string // absolute path
	CodexMaxTurns        int
	CodexSessionTTLHours int
	CodexTimeoutSec      int

	// Logging
	LogFile string // absolute path

	// Lark (larkbot only)
	FeishuAppID               string
	FeishuAppSecret           string
	FeishuBotName             string
	FeishuDedupTimeoutInMin   int
	FeishuFileDir             string // absolute path
	FeishuFileRetentionHours  int
	FeishuRecentFileWindowMin int
	FeishuRecentFileKeywords  []string
}

func Load() (*Config, error) {
	projectRoot, err := detectProjectRoot()
	if err != nil {
		return nil, fmt.Errorf("detect project root: %w", err)
	}

	return &Config{
		ProjectRoot:               projectRoot,
		PromptFile:                resolvePath(projectRoot, envOrDefault("PROMPT_FILE", "prompt")),
		CodexBin:                  envOrDefault("CODEX_BIN", "codex"),
		CodexModel:                envOrDefault("CODEX_MODEL", "gpt-5.3-codex"),
		CodexReasoningEffort:      envOrDefault("CODEX_REASONING_EFFORT", "medium"),
		CodexSandbox:              envOrDefault("CODEX_SANDBOX", "read-only"),
		CodexSessionStore:         resolvePath(projectRoot, envOrDefault("CODEX_SESSION_STORE", ".askplanner/sessions.json")),
		CodexMaxTurns:             envAsInt("CODEX_MAX_TURNS", 30),
		CodexSessionTTLHours:      envAsInt("CODEX_SESSION_TTL_HOURS", 24),
		CodexTimeoutSec:           envAsInt("CODEX_TIMEOUT_SEC", 120),
		LogFile:                   resolvePath(projectRoot, envOrDefault("LOG_FILE", ".askplanner/askplanner.log")),
		FeishuAppID:               os.Getenv("FEISHU_APP_ID"),
		FeishuAppSecret:           os.Getenv("FEISHU_APP_SECRET"),
		FeishuBotName:             strings.ToLower(strings.TrimSpace(envOrDefault("FEISHU_BOT_NAME", "askplanner"))),
		FeishuDedupTimeoutInMin:   envAsInt("FEISHU_DEDUP_MESSAGE_TIMEOUT_IN_MIN", 3600),
		FeishuFileDir:             resolvePath(projectRoot, envOrDefault("FEISHU_FILE_DIR", ".askplanner/lark-files")),
		FeishuFileRetentionHours:  envAsInt("FEISHU_FILE_RETENTION_HOURS", 24),
		FeishuRecentFileWindowMin: envAsInt("FEISHU_RECENT_FILE_WINDOW_MIN", 10),
		FeishuRecentFileKeywords: envAsCSV("FEISHU_RECENT_FILE_KEYWORDS", []string{
			"file",
			"files",
			"attachment",
			"attachments",
			"image",
			"images",
			"screenshot",
			"zip",
			"replayer",
			"plan replayer",
			"文件",
			"附件",
			"图片",
			"截图",
			"压缩包",
		}),
	}, nil
}

func detectProjectRoot() (string, error) {
	if v := os.Getenv("PROJECT_ROOT"); v != "" {
		return filepath.Abs(v)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "prompt")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return os.Getwd()
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envAsInt(key string, defaultVal int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultVal
	}
	return n
}

func envAsCSV(key string, defaultVal []string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return append([]string(nil), defaultVal...)
	}

	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return append([]string(nil), defaultVal...)
	}
	return out
}

func SetupLogging(logFile string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	// log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	return f, nil
}

func resolvePath(projectRoot, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(projectRoot, path)
}
