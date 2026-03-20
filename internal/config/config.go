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

	// Clinic
	ClinicEnableAutoSlowQuery bool
	ClinicAPIKey              string
	ClinicHTTPTimeoutSec      int

	// Logging
	LogFile string // absolute path

	// Lark (larkbot only)
	FeishuAppID             string
	FeishuAppSecret         string
	FeishuBotName           string
	FeishuDedupTimeoutInMin int
	FeishuFileDir           string // absolute path
	FeishuUserFileMaxItems  int
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
		ClinicEnableAutoSlowQuery: envAsBool("CLINIC_ENABLE_AUTO_SLOWQUERY", false),
		ClinicAPIKey:              strings.TrimSpace(os.Getenv("CLINIC_API_KEY")),
		ClinicHTTPTimeoutSec:      envAsInt("CLINIC_HTTP_TIMEOUT_SEC", 15),
		LogFile:                   resolvePath(projectRoot, envOrDefault("LOG_FILE", ".askplanner/askplanner.log")),
		FeishuAppID:               os.Getenv("FEISHU_APP_ID"),
		FeishuAppSecret:           os.Getenv("FEISHU_APP_SECRET"),
		FeishuBotName:             strings.ToLower(strings.TrimSpace(envOrDefault("FEISHU_BOT_NAME", "askplanner"))),
		FeishuDedupTimeoutInMin:   envAsInt("FEISHU_DEDUP_MESSAGE_TIMEOUT_IN_MIN", 3600),
		FeishuFileDir:             resolvePath(projectRoot, envOrDefault("FEISHU_FILE_DIR", ".askplanner/lark-files")),
		FeishuUserFileMaxItems:    envAsInt("FEISHU_USER_FILE_MAX_ITEMS", 100),
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

func envAsBool(key string, defaultVal bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return defaultVal
	}

	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultVal
	}
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
