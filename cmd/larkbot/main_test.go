package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"lab/askplanner/internal/attachments"
)

func TestParseUploadCommand(t *testing.T) {
	cmd := parseUploadCommand("/upload_3 analyze these files")
	if !cmd.ok {
		t.Fatalf("expected command to parse")
	}
	if cmd.count != 3 {
		t.Fatalf("count = %d, want 3", cmd.count)
	}
	if cmd.remainder != "analyze these files" {
		t.Fatalf("remainder = %q", cmd.remainder)
	}

	if bad := parseUploadCommand("/upload_x test"); bad.ok {
		t.Fatalf("expected invalid command to be rejected")
	}
}

func TestBuildConversationKeyUsesThreadAndUser(t *testing.T) {
	threadID := "omt-thread"
	chatID := "oc_chat"
	openID := "ou_user"
	messageID := "om_message"

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				ThreadId:  &threadID,
				ChatId:    &chatID,
				MessageId: &messageID,
			},
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{
					OpenId: &openID,
				},
			},
		},
	}

	if got := buildConversationKey(event); got != "lark:thread:omt-thread:user:ou_user" {
		t.Fatalf("conversation key = %q", got)
	}
}

func TestBuildSaveSummaryDoesNotExposeLocalPath(t *testing.T) {
	summary := buildSaveSummary("Downloaded", []attachments.SaveResult{{
		UserDir: "/home/gjt/work/askplanner/.askplanner/lark-files/ou_xxx",
		Item: attachments.Item{
			Name:      "image_20260320_091914_om_x.png",
			Type:      attachments.ItemTypeImage,
			CreatedAt: time.Now(),
		},
	}})

	if strings.Contains(summary, "/home/gjt/work/askplanner/.askplanner/lark-files/ou_xxx") {
		t.Fatalf("summary leaked local path: %s", summary)
	}
	if !strings.Contains(summary, "image_20260320_091914_om_x.png [image]") {
		t.Fatalf("summary missing file entry: %s", summary)
	}
}

func TestWithTypingReactionAddsAndDeletesReaction(t *testing.T) {
	var (
		createCalls int
		deleteCalls int
		emojiType   string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","expire":7200,"tenant_access_token":"tenant-token"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages/om_message/reactions":
			createCalls++

			var payload struct {
				ReactionType struct {
					EmojiType string `json:"emoji_type"`
				} `json:"reaction_type"`
			}
			if err := readJSONBody(r, &payload); err != nil {
				t.Fatalf("read create request body: %v", err)
			}
			emojiType = payload.ReactionType.EmojiType

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"reaction_id":"reaction-1","reaction_type":{"emoji_type":"Typing"}}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/open-apis/im/v1/messages/om_message/reactions/reaction-1":
			deleteCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"reaction_id":"reaction-1"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	apiClient := lark.NewClient("cli_a", "secret", lark.WithOpenBaseUrl(server.URL), lark.WithEnableTokenCache(false))

	callbackRun := false
	err := withTypingReaction(context.Background(), apiClient, "om_message", func() error {
		callbackRun = true
		if createCalls != 1 {
			t.Fatalf("expected create before callback, got %d", createCalls)
		}
		if deleteCalls != 0 {
			t.Fatalf("expected delete after callback, got %d", deleteCalls)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withTypingReaction returned error: %v", err)
	}
	if !callbackRun {
		t.Fatalf("expected callback to run")
	}
	if createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", createCalls)
	}
	if deleteCalls != 1 {
		t.Fatalf("deleteCalls = %d, want 1", deleteCalls)
	}
	if emojiType != typingReactionType {
		t.Fatalf("emojiType = %q, want %q", emojiType, typingReactionType)
	}
}

func TestWithTypingReactionDeletesOnCallbackError(t *testing.T) {
	var deleteCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","expire":7200,"tenant_access_token":"tenant-token"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages/om_message/reactions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"reaction_id":"reaction-1"}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/open-apis/im/v1/messages/om_message/reactions/reaction-1":
			deleteCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"reaction_id":"reaction-1"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	apiClient := lark.NewClient("cli_b", "secret", lark.WithOpenBaseUrl(server.URL), lark.WithEnableTokenCache(false))

	wantErr := errors.New("callback failed")
	err := withTypingReaction(context.Background(), apiClient, "om_message", func() error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if deleteCalls != 1 {
		t.Fatalf("deleteCalls = %d, want 1", deleteCalls)
	}
}

func TestWithTypingReactionCreateFailureDoesNotBlockRun(t *testing.T) {
	var (
		createCalls int
		deleteCalls int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","expire":7200,"tenant_access_token":"tenant-token"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages/om_message/reactions":
			createCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":231001,"msg":"reaction type is invalid."}`))
		case r.Method == http.MethodDelete:
			deleteCalls++
			t.Fatalf("delete should not be called when create fails")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	apiClient := lark.NewClient("cli_c", "secret", lark.WithOpenBaseUrl(server.URL), lark.WithEnableTokenCache(false))

	callbackRun := false
	err := withTypingReaction(context.Background(), apiClient, "om_message", func() error {
		callbackRun = true
		return nil
	})
	if err != nil {
		t.Fatalf("withTypingReaction returned error: %v", err)
	}
	if !callbackRun {
		t.Fatalf("expected callback to run")
	}
	if createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", createCalls)
	}
	if deleteCalls != 0 {
		t.Fatalf("deleteCalls = %d, want 0", deleteCalls)
	}
}

func readJSONBody(r *http.Request, dst any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}
