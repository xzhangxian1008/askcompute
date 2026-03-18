package codex

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"lab/askplanner/internal/config"
)

type Responder struct {
	runner     *Runner
	store      *FileSessionStore
	prompt     string
	promptHash string
	maxTurns   int
	sessionTTL time.Duration
	timeout    time.Duration
}

func NewResponder(cfg *config.Config) (*Responder, error) {
	prompt, err := LoadPrompt(cfg.PromptFile)
	if err != nil {
		return nil, fmt.Errorf("load prompt: %w", err)
	}

	store, err := NewFileSessionStore(cfg.CodexSessionStore)
	if err != nil {
		return nil, fmt.Errorf("init session store: %w", err)
	}

	return &Responder{
		runner: &Runner{
			Bin:             cfg.CodexBin,
			Model:           cfg.CodexModel,
			ReasoningEffort: cfg.CodexReasoningEffort,
			WorkDir:         cfg.ProjectRoot,
			Sandbox:         cfg.CodexSandbox,
		},
		store:      store,
		prompt:     prompt,
		promptHash: PromptHash(prompt),
		maxTurns:   cfg.CodexMaxTurns,
		sessionTTL: time.Duration(cfg.CodexSessionTTLHours) * time.Hour,
		timeout:    time.Duration(cfg.CodexTimeoutSec) * time.Second,
	}, nil
}

func (r *Responder) Answer(ctx context.Context, conversationKey, question string) (string, error) {
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	now := time.Now().UTC()
	record, ok := r.store.Get(conversationKey)

	if ok && r.canResume(record, now) {
		result, err := r.runner.RunResume(ctx, record.SessionID, BuildResumePrompt(question))
		if err == nil {
			record.LastActiveAt = now
			record.TurnCount++
			record.LastError = ""
			record.appendTurn(question, result.Answer)
			if err := r.store.Put(record); err != nil {
				return "", err
			}
			return result.Answer, nil
		}
		record.LastError = err.Error()
		if saveErr := r.store.Put(record); saveErr != nil {
			log.Printf("[codex] persist resume failure for %s: %v", conversationKey, saveErr)
		}
		log.Printf("[codex] resume failed for %s, starting a new session: %v", conversationKey, err)
	}

	initialPrompt := BuildInitialPrompt(r.prompt, summarizeTurns(record.Turns), question)
	result, err := r.runner.RunNew(ctx, initialPrompt)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return "", fmt.Errorf("codex did not return a session id")
	}

	record = SessionRecord{
		ConversationKey: conversationKey,
		SessionID:       result.SessionID,
		PromptHash:      r.promptHash,
		WorkDir:         r.runner.WorkDir,
		CreatedAt:       now,
		LastActiveAt:    now,
		TurnCount:       1,
		Turns: []Turn{{
			User:      strings.TrimSpace(question),
			Assistant: strings.TrimSpace(result.Answer),
			At:        now,
		}},
	}
	if err := r.store.Put(record); err != nil {
		return "", err
	}
	return result.Answer, nil
}

func (r *Responder) Reset(conversationKey string) error {
	return r.store.Delete(conversationKey)
}

func (r *Responder) canResume(record SessionRecord, now time.Time) bool {
	if strings.TrimSpace(record.SessionID) == "" {
		return false
	}
	if record.PromptHash != r.promptHash {
		return false
	}
	if record.WorkDir != r.runner.WorkDir {
		return false
	}
	if r.maxTurns > 0 && record.TurnCount >= r.maxTurns {
		return false
	}
	if r.sessionTTL > 0 && now.Sub(record.LastActiveAt) > r.sessionTTL {
		return false
	}
	return true
}

func summarizeTurns(turns []Turn) string {
	if len(turns) == 0 {
		return ""
	}
	if len(turns) > 6 {
		turns = turns[len(turns)-6:]
	}

	var sb strings.Builder
	for i, turn := range turns {
		fmt.Fprintf(&sb, "Turn %d user: %s\n", i+1, compactText(turn.User, 300))
		fmt.Fprintf(&sb, "Turn %d assistant: %s\n", i+1, compactText(turn.Assistant, 500))
	}
	return strings.TrimSpace(sb.String())
}

func compactText(s string, max int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
