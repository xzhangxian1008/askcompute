package codex

import (
	"strings"
	"testing"
	"time"
)

func TestBuildInitialPromptIncludesAttachmentContext(t *testing.T) {
	prompt := BuildInitialPrompt("base prompt", "older summary", "analyze the file", RuntimeContext{
		Attachment: AttachmentContext{
			RootDir: "/tmp/user-a",
			Items: []AttachmentItem{
				{
					Name:         "report.sql",
					Type:         "file",
					SavedAt:      time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC),
					OriginalName: "report.sql",
				},
				{
					Name:         "image_20260320_090000_om1.png",
					Type:         "image",
					SavedAt:      time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC),
					OriginalName: "",
				},
			},
		},
	})

	wantSnippets := []string{
		"The current user's uploaded-file library is stored under: /tmp/user-a",
		"/upload_3 analyze these files",
		"If you cannot tell which file the user means, do not guess.",
		"report.sql [file] saved_at=2026-03-20T10:00:00Z",
		"image_20260320_090000_om1.png [image] saved_at=2026-03-20T09:00:00Z",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(prompt, snippet) {
			t.Fatalf("prompt missing %q:\n%s", snippet, prompt)
		}
	}
}

func TestBuildInitialPromptIncludesClinicContext(t *testing.T) {
	prompt := BuildInitialPrompt("base prompt", "", "analyze this Clinic link", RuntimeContext{
		ClinicLibrary: &ClinicLibraryContext{
			RootDir:        "/tmp/clinic-user-a",
			ActiveItemName: "clinic_20260320_100000_123_digest",
			Items: []ClinicLibraryItem{{
				Name:      "clinic_20260320_100000_123_digest",
				SavedAt:   time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC),
				ClusterID: "123",
				Digest:    "digest-1",
				IsDetail:  true,
			}},
		},
		Clinic: &ClinicContext{
			SourceURL:   "https://clinic.pingcap.com/#/slowquery?clusterId=123",
			ClusterID:   "123",
			ClusterName: "prod-a",
			OrgName:     "Acme",
			DeployType:  "premium",
			StartTime:   time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC),
			EndTime:     time.Date(2026, 3, 20, 11, 0, 0, 0, time.UTC),
			Digest:      "digest-1",
			Database:    "app",
			Instance:    "tidb-0",
			Summary: ClinicSummary{
				TotalQueries:  24,
				UniqueDigests: 3,
				AvgQueryTime:  1.25,
				MaxQueryTime:  7.5,
			},
			TopDigests: []ClinicDigestSummary{{
				Digest:         "digest-1",
				ExecutionCount: 12,
				AvgQueryTime:   1.2,
				MaxQueryTime:   7.5,
				MaxTotalKeys:   1000,
				SampleSQL:      "select * from t where a = 1 order by b limit 10",
			}},
		},
	})

	wantSnippets := []string{
		"The current user's saved Clinic slow-query library is stored under: /tmp/clinic-user-a",
		"Current active Clinic entry: clinic_20260320_100000_123_digest",
		"clinic_20260320_100000_123_digest [detail] saved_at=2026-03-20T10:00:00Z cluster_id=123 digest=digest-1",
		"Clinic slow query link detected and prefetched by the relay",
		"cluster_id=123",
		"cluster_name=prod-a",
		"org_name=Acme",
		"digest=digest-1",
		"sample_sql=select * from t where a = 1 order by b limit 10",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(prompt, snippet) {
			t.Fatalf("prompt missing %q:\n%s", snippet, prompt)
		}
	}
}

func TestBuildResumePromptIncludesEmptyClinicLibrary(t *testing.T) {
	prompt := BuildResumePrompt("what next", RuntimeContext{
		ClinicLibrary: &ClinicLibraryContext{
			RootDir: "/tmp/clinic-user-a",
		},
	})

	if !strings.Contains(prompt, "Current Clinic entries: none.") {
		t.Fatalf("prompt missing empty clinic-library marker:\n%s", prompt)
	}
}

func TestBuildResumePromptHandlesEmptyAttachmentLibrary(t *testing.T) {
	prompt := BuildResumePrompt("what next", RuntimeContext{
		Attachment: AttachmentContext{
			RootDir: "/tmp/user-a",
		},
	})

	if !strings.Contains(prompt, "Current top-level entries: none.") {
		t.Fatalf("prompt missing empty-library marker:\n%s", prompt)
	}
}
