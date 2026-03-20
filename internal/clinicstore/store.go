package clinicstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	manifestFileName = ".index.json"
	metadataFileName = "metadata.json"
	analysisFileName = "analysis.json"
	summaryFileName  = "summary.md"
)

type Metadata struct {
	Name        string    `json:"name"`
	SourceURL   string    `json:"source_url,omitempty"`
	ClusterID   string    `json:"cluster_id,omitempty"`
	ClusterName string    `json:"cluster_name,omitempty"`
	OrgName     string    `json:"org_name,omitempty"`
	DeployType  string    `json:"deploy_type,omitempty"`
	StartTime   time.Time `json:"start_time,omitempty"`
	EndTime     time.Time `json:"end_time,omitempty"`
	Digest      string    `json:"digest,omitempty"`
	Database    string    `json:"database,omitempty"`
	Instance    string    `json:"instance,omitempty"`
	IsDetail    bool      `json:"is_detail,omitempty"`
	SavedAt     time.Time `json:"saved_at"`
}

type Item = Metadata

type SaveRequest struct {
	UserKey         string
	Metadata        Metadata
	AnalysisJSON    []byte
	SummaryMarkdown string
}

type SaveResult struct {
	UserKey string
	UserDir string
	Item    Item
	Evicted []Item
	Library Library
}

type Library struct {
	UserKey string
	RootDir string
	Items   []Item
}

type Entry struct {
	UserKey         string
	RootDir         string
	Item            Item
	AnalysisJSON    []byte
	SummaryMarkdown string
}

type Manager struct {
	rootDir  string
	maxItems int

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewManager(rootDir string, maxItems int) (*Manager, error) {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return nil, fmt.Errorf("clinic store root dir is empty")
	}
	if maxItems <= 0 {
		maxItems = 50
	}
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, fmt.Errorf("create clinic store root dir: %w", err)
	}
	return &Manager{
		rootDir:  rootDir,
		maxItems: maxItems,
		locks:    make(map[string]*sync.Mutex),
	}, nil
}

func (m *Manager) RootDir() string {
	return m.rootDir
}

func (m *Manager) MaxItems() int {
	return m.maxItems
}

func (m *Manager) UserDir(userKey string) string {
	return filepath.Join(m.rootDir, sanitizePathSegment(userKey, "user"))
}

func (m *Manager) Save(req SaveRequest) (*SaveResult, error) {
	userKey := sanitizePathSegment(req.UserKey, "")
	if userKey == "" {
		return nil, fmt.Errorf("clinic store user key is empty")
	}
	if len(req.AnalysisJSON) == 0 {
		return nil, fmt.Errorf("clinic analysis JSON is empty")
	}

	lock := m.userLock(userKey)
	lock.Lock()
	defer lock.Unlock()

	userDir := m.UserDir(userKey)
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		return nil, fmt.Errorf("create clinic user dir: %w", err)
	}

	items, err := loadOrRebuildManifest(userDir)
	if err != nil {
		return nil, err
	}

	meta := req.Metadata
	if meta.SavedAt.IsZero() {
		meta.SavedAt = time.Now().UTC()
	} else {
		meta.SavedAt = meta.SavedAt.UTC()
	}
	meta.Name = uniqueEntryName(meta, items)

	entryDir := filepath.Join(userDir, meta.Name)
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		return nil, fmt.Errorf("create clinic entry dir: %w", err)
	}
	if err := writeJSONFile(filepath.Join(entryDir, metadataFileName), meta); err != nil {
		return nil, fmt.Errorf("write clinic metadata: %w", err)
	}
	if err := writeBytesFile(filepath.Join(entryDir, analysisFileName), req.AnalysisJSON); err != nil {
		return nil, fmt.Errorf("write clinic analysis: %w", err)
	}
	if err := writeBytesFile(filepath.Join(entryDir, summaryFileName), []byte(strings.TrimSpace(req.SummaryMarkdown)+"\n")); err != nil {
		return nil, fmt.Errorf("write clinic summary: %w", err)
	}

	items = upsertItem(items, meta)
	evicted, items, err := enforceQuota(userDir, items, m.maxItems)
	if err != nil {
		return nil, err
	}
	if err := saveManifest(userDir, items); err != nil {
		return nil, err
	}

	return &SaveResult{
		UserKey: userKey,
		UserDir: userDir,
		Item:    meta,
		Evicted: evicted,
		Library: Library{
			UserKey: userKey,
			RootDir: userDir,
			Items:   newestFirst(items),
		},
	}, nil
}

func (m *Manager) Snapshot(userKey string) (Library, error) {
	userKey = sanitizePathSegment(userKey, "")
	if userKey == "" {
		return Library{}, fmt.Errorf("clinic store user key is empty")
	}

	lock := m.userLock(userKey)
	lock.Lock()
	defer lock.Unlock()

	userDir := m.UserDir(userKey)
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		return Library{}, fmt.Errorf("create clinic user dir: %w", err)
	}
	items, err := loadOrRebuildManifest(userDir)
	if err != nil {
		return Library{}, err
	}
	return Library{
		UserKey: userKey,
		RootDir: userDir,
		Items:   newestFirst(items),
	}, nil
}

func (m *Manager) Latest(userKey string) (*Entry, bool, error) {
	userKey = sanitizePathSegment(userKey, "")
	if userKey == "" {
		return nil, false, fmt.Errorf("clinic store user key is empty")
	}

	lock := m.userLock(userKey)
	lock.Lock()
	defer lock.Unlock()

	userDir := m.UserDir(userKey)
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		return nil, false, fmt.Errorf("create clinic user dir: %w", err)
	}
	items, err := loadOrRebuildManifest(userDir)
	if err != nil {
		return nil, false, err
	}
	if len(items) == 0 {
		return nil, false, nil
	}
	items = newestFirst(items)
	entry, err := loadEntry(userKey, userDir, items[0].Name)
	if err != nil {
		return nil, false, err
	}
	return entry, true, nil
}

func (m *Manager) userLock(userKey string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.locks[userKey]
	if !ok {
		lock = &sync.Mutex{}
		m.locks[userKey] = lock
	}
	return lock
}

func uniqueEntryName(meta Metadata, items []Item) string {
	base := buildBaseName(meta)
	name := base
	for idx := 2; itemExists(items, name); idx++ {
		name = fmt.Sprintf("%s_%d", base, idx)
	}
	return name
}

func buildBaseName(meta Metadata) string {
	savedAt := meta.SavedAt.UTC().Format("20060102_150405")
	cluster := sanitizePathSegment(truncateSegment(meta.ClusterID, 18), "cluster")
	digest := "list"
	if strings.TrimSpace(meta.Digest) != "" {
		digest = sanitizePathSegment(truncateSegment(meta.Digest, 12), "digest")
	}
	return fmt.Sprintf("clinic_%s_%s_%s", savedAt, cluster, digest)
}

func truncateSegment(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func loadOrRebuildManifest(userDir string) ([]Item, error) {
	data, err := os.ReadFile(filepath.Join(userDir, manifestFileName))
	if err == nil {
		var items []Item
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("parse clinic store manifest: %w", err)
		}
		return items, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read clinic store manifest: %w", err)
	}

	items, err := rebuildManifest(userDir)
	if err != nil {
		return nil, err
	}
	if err := saveManifest(userDir, items); err != nil {
		return nil, err
	}
	return items, nil
}

func rebuildManifest(userDir string) ([]Item, error) {
	entries, err := os.ReadDir(userDir)
	if err != nil {
		return nil, fmt.Errorf("read clinic store dir: %w", err)
	}
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(userDir, entry.Name(), metadataFileName))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read clinic entry metadata %s: %w", entry.Name(), err)
		}
		var item Item
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, fmt.Errorf("parse clinic entry metadata %s: %w", entry.Name(), err)
		}
		if strings.TrimSpace(item.Name) == "" {
			item.Name = entry.Name()
		}
		items = append(items, item)
	}
	return items, nil
}

func saveManifest(userDir string, items []Item) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("encode clinic store manifest: %w", err)
	}
	tmpFile, err := os.CreateTemp(userDir, ".clinic-index-*")
	if err != nil {
		return fmt.Errorf("create clinic store manifest temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(append(data, '\n')); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write clinic store manifest: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close clinic store manifest temp file: %w", err)
	}
	return os.Rename(tmpPath, filepath.Join(userDir, manifestFileName))
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeBytesFile(path, append(data, '\n'))
}

func writeBytesFile(path string, data []byte) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(path), ".clinic-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func enforceQuota(userDir string, items []Item, maxItems int) ([]Item, []Item, error) {
	if maxItems <= 0 || len(items) <= maxItems {
		return nil, items, nil
	}

	sorted := append([]Item(nil), items...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].SavedAt.Equal(sorted[j].SavedAt) {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].SavedAt.Before(sorted[j].SavedAt)
	})

	evicted := make([]Item, 0, len(items)-maxItems)
	keep := make(map[string]struct{}, maxItems)
	for _, item := range sorted[len(sorted)-maxItems:] {
		keep[item.Name] = struct{}{}
	}

	filtered := make([]Item, 0, maxItems)
	for _, item := range items {
		if _, ok := keep[item.Name]; ok {
			filtered = append(filtered, item)
			continue
		}
		evicted = append(evicted, item)
		if err := os.RemoveAll(filepath.Join(userDir, item.Name)); err != nil && !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("remove evicted clinic entry %s: %w", item.Name, err)
		}
	}
	return evicted, filtered, nil
}

func newestFirst(items []Item) []Item {
	cloned := append([]Item(nil), items...)
	sort.Slice(cloned, func(i, j int) bool {
		if cloned[i].SavedAt.Equal(cloned[j].SavedAt) {
			return cloned[i].Name < cloned[j].Name
		}
		return cloned[i].SavedAt.After(cloned[j].SavedAt)
	})
	return cloned
}

func itemExists(items []Item, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}

func upsertItem(items []Item, item Item) []Item {
	for idx, existing := range items {
		if existing.Name == item.Name {
			items[idx] = item
			return items
		}
	}
	return append(items, item)
}

func loadEntry(userKey, userDir, itemName string) (*Entry, error) {
	entryDir := filepath.Join(userDir, itemName)
	metaData, err := os.ReadFile(filepath.Join(entryDir, metadataFileName))
	if err != nil {
		return nil, fmt.Errorf("read clinic metadata %s: %w", itemName, err)
	}
	var item Item
	if err := json.Unmarshal(metaData, &item); err != nil {
		return nil, fmt.Errorf("parse clinic metadata %s: %w", itemName, err)
	}
	analysisJSON, err := os.ReadFile(filepath.Join(entryDir, analysisFileName))
	if err != nil {
		return nil, fmt.Errorf("read clinic analysis %s: %w", itemName, err)
	}
	summaryMarkdown, err := os.ReadFile(filepath.Join(entryDir, summaryFileName))
	if err != nil {
		return nil, fmt.Errorf("read clinic summary %s: %w", itemName, err)
	}
	return &Entry{
		UserKey:         userKey,
		RootDir:         userDir,
		Item:            item,
		AnalysisJSON:    analysisJSON,
		SummaryMarkdown: strings.TrimSpace(string(summaryMarkdown)),
	}, nil
}

func sanitizePathSegment(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	var b strings.Builder
	prevUnderscore := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			prevUnderscore = false
			continue
		}
		switch r {
		case '-', '_', '.':
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	sanitized := strings.Trim(b.String(), "._")
	if sanitized == "" {
		return fallback
	}
	return sanitized
}
