package clinicstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveCreatesEntryFilesAndLatest(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager(dir, 10)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	savedAt := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	result, err := manager.Save(SaveRequest{
		UserKey:         "user-1",
		AnalysisJSON:    []byte(`{"summary":{"total_queries":1}}`),
		SummaryMarkdown: "# Clinic Summary\n\n- ok\n",
		Metadata: Metadata{
			SourceURL: "https://clinic.pingcap.com/#/slowquery?clusterId=123",
			ClusterID: "123",
			Digest:    "abcdef1234567890",
			IsDetail:  true,
			SavedAt:   savedAt,
		},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if result.Item.Name == "" {
		t.Fatalf("expected generated item name")
	}
	entryDir := filepath.Join(result.UserDir, result.Item.Name)
	for _, name := range []string{metadataFileName, analysisFileName, summaryFileName} {
		if _, err := os.Stat(filepath.Join(entryDir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}

	entry, ok, err := manager.Latest("user-1")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if !ok {
		t.Fatalf("expected latest entry")
	}
	if entry.Item.Name != result.Item.Name {
		t.Fatalf("latest name = %q, want %q", entry.Item.Name, result.Item.Name)
	}
	if entry.Item.ClusterID != "123" || entry.Item.Digest != "abcdef1234567890" {
		t.Fatalf("latest item = %+v", entry.Item)
	}
}

func TestSaveEnforcesQuota(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager(dir, 2)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	saveAt := []time.Time{
		time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 20, 11, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
	}
	var firstName string
	for idx, ts := range saveAt {
		result, err := manager.Save(SaveRequest{
			UserKey:         "user-1",
			AnalysisJSON:    []byte(`{"summary":{"total_queries":1}}`),
			SummaryMarkdown: "ok",
			Metadata: Metadata{
				ClusterID: "123",
				Digest:    string(rune('a' + idx)),
				SavedAt:   ts,
			},
		})
		if err != nil {
			t.Fatalf("Save %d: %v", idx, err)
		}
		if idx == 0 {
			firstName = result.Item.Name
		}
		if idx == 2 {
			if len(result.Evicted) != 1 || result.Evicted[0].Name != firstName {
				t.Fatalf("evicted = %+v, want %q", result.Evicted, firstName)
			}
		}
	}

	library, err := manager.Snapshot("user-1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(library.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(library.Items))
	}
	if _, err := os.Stat(filepath.Join(library.RootDir, firstName)); !os.IsNotExist(err) {
		t.Fatalf("oldest item should be removed, stat err = %v", err)
	}
}

func TestSnapshotRebuildsManifestFromMetadata(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager(dir, 10)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	userDir := manager.UserDir("user-1")
	entryDir := filepath.Join(userDir, "clinic_20260320_120000_123_list")
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(entryDir, metadataFileName), []byte(`{"name":"clinic_20260320_120000_123_list","cluster_id":"123","saved_at":"2026-03-20T12:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("WriteFile metadata: %v", err)
	}

	library, err := manager.Snapshot("user-1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(library.Items) != 1 || library.Items[0].Name != "clinic_20260320_120000_123_list" {
		t.Fatalf("items = %+v", library.Items)
	}
	if _, err := os.Stat(filepath.Join(userDir, manifestFileName)); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
}
