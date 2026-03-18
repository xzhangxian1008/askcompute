package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxStoredTurns = 12

type Turn struct {
	User      string    `json:"user"`
	Assistant string    `json:"assistant"`
	At        time.Time `json:"at"`
}

type SessionRecord struct {
	ConversationKey string    `json:"conversation_key"`
	SessionID       string    `json:"session_id"`
	PromptHash      string    `json:"prompt_hash"`
	WorkDir         string    `json:"work_dir"`
	CreatedAt       time.Time `json:"created_at"`
	LastActiveAt    time.Time `json:"last_active_at"`
	TurnCount       int       `json:"turn_count"`
	Turns           []Turn    `json:"turns,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

type FileSessionStore struct {
	path    string
	mu      sync.Mutex
	records map[string]SessionRecord
}

func NewFileSessionStore(path string) (*FileSessionStore, error) {
	store := &FileSessionStore{
		path:    path,
		records: make(map[string]SessionRecord),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *FileSessionStore) Get(key string) (SessionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[key]
	return record, ok
}

func (s *FileSessionStore) Put(record SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.records[record.ConversationKey] = record
	return s.saveLocked()
}

func (s *FileSessionStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.records, key)
	return s.saveLocked()
}

func (s *FileSessionStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read session store: %w", err)
	}
	if len(data) == 0 {
		return nil
	}

	var records map[string]SessionRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("parse session store: %w", err)
	}
	s.records = records
	return nil
}

func (s *FileSessionStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create session store dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(s.path), "codex-session-store-*.json")
	if err != nil {
		return fmt.Errorf("create temp session store: %w", err)
	}

	encoder := json.NewEncoder(tmpFile)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(s.orderedRecordsLocked()); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return fmt.Errorf("encode session store: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpFile.Name())
		return fmt.Errorf("close temp session store: %w", err)
	}

	if err := os.Rename(tmpFile.Name(), s.path); err != nil {
		_ = os.Remove(tmpFile.Name())
		return fmt.Errorf("replace session store: %w", err)
	}
	return nil
}

func (s *FileSessionStore) orderedRecordsLocked() map[string]SessionRecord {
	keys := make([]string, 0, len(s.records))
	for key := range s.records {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	ordered := make(map[string]SessionRecord, len(keys))
	for _, key := range keys {
		ordered[key] = s.records[key]
	}
	return ordered
}

func (r *SessionRecord) appendTurn(user, assistant string) {
	r.Turns = append(r.Turns, Turn{
		User:      strings.TrimSpace(user),
		Assistant: strings.TrimSpace(assistant),
		At:        time.Now().UTC(),
	})
	if len(r.Turns) > maxStoredTurns {
		r.Turns = r.Turns[len(r.Turns)-maxStoredTurns:]
	}
}
