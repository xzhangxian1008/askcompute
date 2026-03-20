package main

import (
	"strings"
	"testing"
	"time"

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
