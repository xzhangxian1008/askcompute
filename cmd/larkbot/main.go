package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"lab/askplanner/internal/codex"
	"lab/askplanner/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	logFile, err := config.SetupLogging(cfg.LogFile)
	if err != nil {
		log.Fatalf("setup logging: %v", err)
	}
	defer logFile.Close()

	if cfg.FeishuAppID == "" || cfg.FeishuAppSecret == "" {
		log.Fatalf("FEISHU_APP_ID and FEISHU_APP_SECRET are required")
	}

	responder, err := codex.NewResponder(cfg)
	if err != nil {
		log.Fatalf("build codex responder: %v", err)
	}

	apiClient := lark.NewClient(cfg.FeishuAppID, cfg.FeishuAppSecret, lark.WithLogLevel(larkcore.LogLevelInfo))

	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			log.Printf("[larkbot] message received: %s", larkcore.Prettify(event))

			messageID := extractMessageID(event)
			if messageID == "" {
				log.Printf("[larkbot] skip: empty message_id")
				return nil
			}

			question := extractQuestion(event)
			if question == "" {
				question = "Please introduce your capabilities."
			}

			conversationKey := buildConversationKey(event)
			log.Printf("[larkbot] answering question: %q (message_id=%s, conversation=%s)",
				question, messageID, conversationKey)

			answer, err := responder.Answer(ctx, conversationKey, question)
			if err != nil {
				log.Printf("[larkbot] agent error: %v (message_id=%s)", err, messageID)
				answer = "Agent Error: " + err.Error()
			}

			content, err := buildTextContent(answer)
			if err != nil {
				return fmt.Errorf("build reply content: %w", err)
			}

			if err := replyMessage(ctx, apiClient, messageID, content); err != nil {
				return fmt.Errorf("reply message: %w", err)
			}

			return nil
		})

	cli := larkws.NewClient(cfg.FeishuAppID, cfg.FeishuAppSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)

	log.Printf("[larkbot] starting websocket client...")
	if err := cli.Start(context.Background()); err != nil {
		log.Fatalf("lark websocket start: %v", err)
	}
}

func extractMessageID(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Message.MessageId == nil {
		return ""
	}
	return *event.Event.Message.MessageId
}

func extractQuestion(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Message.Content == nil {
		return ""
	}

	raw := strings.TrimSpace(*event.Event.Message.Content)
	if raw == "" {
		return ""
	}

	if event.Event.Message.MessageType != nil && *event.Event.Message.MessageType == "text" {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err == nil {
			return strings.TrimSpace(payload.Text)
		}
	}
	return raw
}

func buildConversationKey(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil {
		return "lark:unknown"
	}

	var threadID string
	var chatID string
	var senderID string
	var messageID string

	if event.Event.Message != nil {
		if event.Event.Message.ThreadId != nil {
			threadID = strings.TrimSpace(*event.Event.Message.ThreadId)
		}
		if event.Event.Message.ChatId != nil {
			chatID = strings.TrimSpace(*event.Event.Message.ChatId)
		}
		if event.Event.Message.MessageId != nil {
			messageID = strings.TrimSpace(*event.Event.Message.MessageId)
		}
	}
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		if event.Event.Sender.SenderId.OpenId != nil {
			senderID = strings.TrimSpace(*event.Event.Sender.SenderId.OpenId)
		} else if event.Event.Sender.SenderId.UserId != nil {
			senderID = strings.TrimSpace(*event.Event.Sender.SenderId.UserId)
		}
	}

	switch {
	case threadID != "":
		return "lark:thread:" + threadID
	case chatID != "" && senderID != "":
		return "lark:chat:" + chatID + ":user:" + senderID
	case chatID != "":
		return "lark:chat:" + chatID
	case messageID != "":
		return "lark:message:" + messageID
	default:
		return "lark:unknown"
	}
}

func buildTextContent(text string) (string, error) {
	payload := map[string]string{
		"text": strings.TrimSpace(text),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func replyMessage(ctx context.Context, apiClient *lark.Client, messageID, content string) error {
	log.Printf("[larkbot] replying to message_id=%s", messageID)
	resp, err := apiClient.Im.V1.Message.Reply(ctx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType("text").
				Content(content).
				Uuid("reply-"+messageID).
				Build()).
			Build())
	if err != nil {
		return fmt.Errorf("call reply API: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("reply API error: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	log.Printf("[larkbot] reply sent (message_id=%s)", messageID)
	return nil
}
