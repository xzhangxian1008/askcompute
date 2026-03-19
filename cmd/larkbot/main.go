package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"lab/askplanner/internal/codex"
	"lab/askplanner/internal/config"
)

const (
	recentFilePageSize = 50
	recentFileMaxPages = 3
)

type recentFilePolicy struct {
	dir      string
	lookback time.Duration
	keywords []string
}

type botIdentity struct {
	name string
}

type messageDedup struct {
	seen sync.Map // messageId -> time.Time
}

func (d *messageDedup) isDuplicate(messageID string) bool {
	_, loaded := d.seen.LoadOrStore(messageID, time.Now())
	return loaded
}

func (d *messageDedup) cleanup(maxAge time.Duration) {
	now := time.Now()
	d.seen.Range(func(key, value any) bool {
		if now.Sub(value.(time.Time)) > maxAge {
			d.seen.Delete(key)
		}
		return true
	})
}

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
	if strings.TrimSpace(cfg.FeishuBotName) == "" {
		log.Printf("[larkbot] FEISHU_BOT_NAME is empty; group @ detection will rely on text_without_at_bot only")
	}

	responder, err := codex.NewResponder(cfg)
	if err != nil {
		log.Fatalf("build codex responder: %v", err)
	}

	apiClient := lark.NewClient(cfg.FeishuAppID, cfg.FeishuAppSecret, lark.WithLogLevel(larkcore.LogLevelInfo))
	filePolicy := recentFilePolicy{
		dir:      cfg.FeishuFileDir,
		lookback: time.Duration(cfg.FeishuRecentFileWindowMin) * time.Minute,
		keywords: normalizeKeywords(cfg.FeishuRecentFileKeywords),
	}

	fileRetention := time.Duration(cfg.FeishuFileRetentionHours) * time.Hour

	dedup := &messageDedup{}
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			// 60 hours timeout
			dedup.cleanup(time.Duration(cfg.FeishuDedupTimeoutInMin) * time.Minute)
			if err := cleanupDownloadedFiles(filePolicy.dir, fileRetention); err != nil {
				log.Printf("[larkbot] cleanup downloaded files: %v", err)
			}
		}
	}()

	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			log.Printf("[larkbot] message received: %s", larkcore.Prettify(event))

			messageID := extractMessageID(event)
			if messageID == "" {
				log.Printf("[larkbot] skip: empty message_id")
				return nil
			}

			if dedup.isDuplicate(messageID) {
				log.Printf("[larkbot] skip duplicate message_id=%s", messageID)
				return nil
			}
			if ok, reason := shouldHandleEvent(event, botIdentity{name: cfg.FeishuBotName}); !ok {
				log.Printf("[larkbot] skip message_id=%s: %s", messageID, reason)
				return nil
			}

			answer := ""

			question, err := buildQuestion(ctx, apiClient, filePolicy, event)
			if err != nil {
				log.Printf("[larkbot] prepare question error: %v (message_id=%s)", err, messageID)
				answer = "Agent Error: " + err.Error()
			} else {
				if question == "" {
					question = "Please introduce your capabilities."
				}

				conversationKey := buildConversationKey(event)
				log.Printf("[larkbot] answering question: %q (message_id=%s, conversation=%s)",
					question, messageID, conversationKey)

				answer, err = responder.Answer(ctx, conversationKey, question)
				if err != nil {
					log.Printf("[larkbot] agent error: %v (message_id=%s)", err, messageID)
					answer = "Agent Error: " + err.Error()
				}
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

func shouldHandleEvent(event *larkim.P2MessageReceiveV1, bot botIdentity) (bool, string) {
	msgType := extractMessageType(event)
	if msgType == "" {
		return false, "empty message type"
	}
	if !isGroupChat(event) {
		if msgType == "text" || msgType == "file" || msgType == "image" {
			return true, ""
		}
		return false, fmt.Sprintf("unsupported p2p message type=%s", msgType)
	}
	if msgType != "text" {
		return false, fmt.Sprintf("unsupported group message type=%s", msgType)
	}
	if isTextDirectedToBot(event, bot) {
		return true, ""
	}
	return false, fmt.Sprintf("group text not addressed to bot mentions=%d bot_name_set=%t", len(extractMentionKeys(event)), bot.name != "")
}

func isGroupChat(event *larkim.P2MessageReceiveV1) bool {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Message.ChatType == nil {
		return false
	}
	switch strings.TrimSpace(*event.Event.Message.ChatType) {
	case "group", "topic_group":
		return true
	default:
		return false
	}
}

func isTextDirectedToBot(event *larkim.P2MessageReceiveV1, bot botIdentity) bool {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Message.Content == nil {
		return false
	}

	payload, ok := decodeTextMessageContent(*event.Event.Message.Content)
	if ok && payload.TextWithoutAtBot != nil {
		return true
	}
	return mentionsBot(event, bot)
}

func extractMessageID(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Message.MessageId == nil {
		return ""
	}
	return *event.Event.Message.MessageId
}

func buildQuestion(ctx context.Context, apiClient *lark.Client, filePolicy recentFilePolicy, event *larkim.P2MessageReceiveV1) (string, error) {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Message.Content == nil {
		return "", nil
	}

	raw := strings.TrimSpace(*event.Event.Message.Content)
	if raw == "" {
		return "", nil
	}

	switch extractMessageType(event) {
	case "text":
		return buildTextQuestion(ctx, apiClient, filePolicy, event, raw)
	case "file":
		return buildFileQuestion(ctx, apiClient, filePolicy.dir, extractMessageID(event), raw)
	case "image":
		return buildImageQuestion(ctx, apiClient, filePolicy.dir, extractMessageID(event), raw)
	default:
		return raw, nil
	}
}

func buildTextQuestion(ctx context.Context, apiClient *lark.Client, filePolicy recentFilePolicy, event *larkim.P2MessageReceiveV1, raw string) (string, error) {
	text := extractTextMessage(event, raw)
	if !shouldAttachRecentFile(text, filePolicy.keywords) {
		return text, nil
	}

	ref, err := findRecentFileMessage(ctx, apiClient, event, filePolicy.lookback)
	if err != nil {
		log.Printf("[larkbot] recent file lookup failed for message_id=%s: %v", extractMessageID(event), err)
		return text, nil
	}
	if ref == nil {
		return text, nil
	}

	localPath, originalName, err := downloadMessageResource(ctx, apiClient, filePolicy.dir, ref.messageID, ref.fileKey, ref.resourceType)
	if err != nil {
		log.Printf("[larkbot] recent file download failed for message_id=%s source_message_id=%s: %v",
			extractMessageID(event), ref.messageID, err)
		return text, nil
	}

	return composeQuestionWithFile(text, localPath, originalName), nil
}

func extractMessageType(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Message.MessageType == nil {
		return ""
	}
	return strings.TrimSpace(*event.Event.Message.MessageType)
}

func buildFileQuestion(ctx context.Context, apiClient *lark.Client, fileDir, messageID, raw string) (string, error) {
	var payload larkim.MessageFile
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", fmt.Errorf("parse file message content: %w", err)
	}
	if strings.TrimSpace(payload.FileKey) == "" {
		return "", fmt.Errorf("file message missing file_key")
	}

	localPath, originalName, err := downloadMessageResource(ctx, apiClient, fileDir, messageID, payload.FileKey, "file")
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"%s",
		composeQuestionWithFile("", localPath, originalName),
	), nil
}

func buildImageQuestion(ctx context.Context, apiClient *lark.Client, fileDir, messageID, raw string) (string, error) {
	var payload larkim.MessageImage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", fmt.Errorf("parse image message content: %w", err)
	}
	if strings.TrimSpace(payload.ImageKey) == "" {
		return "", fmt.Errorf("image message missing image_key")
	}

	localPath, originalName, err := downloadMessageResource(ctx, apiClient, fileDir, messageID, payload.ImageKey, "image")
	if err != nil {
		return "", err
	}

	return composeQuestionWithFile("", localPath, originalName), nil
}

func downloadMessageResource(ctx context.Context, apiClient *lark.Client, fileDir, messageID, fileKey, resourceType string) (string, string, error) {
	dir := filepath.Join(fileDir, sanitizePathSegment(messageID, "message"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("create file dir: %w", err)
	}

	var originalName string
	var writeFile func(string) error

	resp, err := apiClient.Im.V1.MessageResource.Get(ctx,
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(fileKey).
			Type(resourceType).
			Build())
	if err != nil {
		return "", "", fmt.Errorf("download %s from Feishu: %w", resourceType, err)
	}
	if resp != nil && !resp.Success() {
		return "", "", fmt.Errorf("download %s from Feishu failed: code=%d, msg=%s", resourceType, resp.Code, resp.Msg)
	}
	if resp == nil || resp.File == nil {
		return "", "", fmt.Errorf("download %s from Feishu: empty response", resourceType)
	}
	originalName = resp.FileName
	writeFile = resp.WriteFile

	if strings.TrimSpace(originalName) == "" {
		originalName = fileKey + ".png"
	}
	originalName = sanitizeFileName(originalName)
	localPath := filepath.Join(dir, originalName)
	if err := writeFile(localPath); err != nil {
		return "", "", fmt.Errorf("write file %s: %w", localPath, err)
	}

	log.Printf("[larkbot] downloaded %s message_id=%s file_key=%s path=%s", resourceType, messageID, fileKey, localPath)
	return localPath, originalName, nil
}

func shouldAttachRecentFile(text string, keywords []string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

type recentFileRef struct {
	messageID    string
	fileKey      string
	resourceType string // "file" or "image"
}

func findRecentFileMessage(ctx context.Context, apiClient *lark.Client, event *larkim.P2MessageReceiveV1, lookback time.Duration) (*recentFileRef, error) {
	chatID := extractChatID(event)
	if chatID == "" {
		return nil, nil
	}

	senderIDs := extractSenderIDs(event)
	if len(senderIDs) == 0 {
		return nil, nil
	}

	currentMessageID := extractMessageID(event)
	currentCreateAt := extractEventCreateTime(event)
	startTime := currentCreateAt.Add(-lookback)
	pageToken := ""

	for page := 0; page < recentFileMaxPages; page++ {
		req := larkim.NewListMessageReqBuilder().
			ContainerIdType("chat").
			ContainerId(chatID).
			StartTime(strconv.FormatInt(startTime.Unix(), 10)).
			EndTime(strconv.FormatInt(currentCreateAt.Unix(), 10)).
			SortType(larkim.SortTypeListMessageByCreateTimeDesc).
			PageSize(recentFilePageSize)
		if pageToken != "" {
			req.PageToken(pageToken)
		}

		resp, err := apiClient.Im.V1.Message.List(ctx, req.Build())
		if err != nil {
			return nil, fmt.Errorf("list recent messages: %w", err)
		}
		if resp == nil {
			return nil, fmt.Errorf("list recent messages: empty response")
		}
		if !resp.Success() {
			return nil, fmt.Errorf("list recent messages failed: code=%d, msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data == nil || len(resp.Data.Items) == 0 {
			return nil, nil
		}

		for _, item := range resp.Data.Items {
			ref, ok := matchRecentFileMessage(item, event, senderIDs, currentMessageID, currentCreateAt)
			if ok {
				log.Printf("[larkbot] matched recent file source_message_id=%s target_message_id=%s", ref.messageID, currentMessageID)
				return ref, nil
			}
		}

		if resp.Data.HasMore == nil || !*resp.Data.HasMore || resp.Data.PageToken == nil || strings.TrimSpace(*resp.Data.PageToken) == "" {
			return nil, nil
		}
		pageToken = strings.TrimSpace(*resp.Data.PageToken)
	}

	return nil, nil
}

func matchRecentFileMessage(item *larkim.Message, event *larkim.P2MessageReceiveV1, senderIDs map[string]struct{}, currentMessageID string, currentCreateAt time.Time) (*recentFileRef, bool) {
	if item == nil {
		return nil, false
	}
	if !sameThread(item, event) {
		return nil, false
	}

	messageID := trimPtr(item.MessageId)
	if messageID == "" || messageID == currentMessageID {
		return nil, false
	}
	msgType := trimPtr(item.MsgType)
	if msgType != "file" && msgType != "image" {
		return nil, false
	}
	if !sameSender(item, senderIDs) {
		return nil, false
	}

	itemCreateAt := parseMillis(trimPtr(item.CreateTime))
	if !itemCreateAt.IsZero() && itemCreateAt.After(currentCreateAt) {
		return nil, false
	}

	var fileKey, resourceType string
	switch msgType {
	case "file":
		fileKey = extractFileKeyFromMessage(item)
		resourceType = "file"
	case "image":
		fileKey = extractImageKeyFromMessage(item)
		resourceType = "image"
	}
	if fileKey == "" {
		return nil, false
	}

	return &recentFileRef{
		messageID:    messageID,
		fileKey:      fileKey,
		resourceType: resourceType,
	}, true
}

func extractFileKeyFromMessage(item *larkim.Message) string {
	if item == nil || item.Body == nil || item.Body.Content == nil {
		return ""
	}

	var payload larkim.MessageFile
	if err := json.Unmarshal([]byte(strings.TrimSpace(*item.Body.Content)), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.FileKey)
}

func extractImageKeyFromMessage(item *larkim.Message) string {
	if item == nil || item.Body == nil || item.Body.Content == nil {
		return ""
	}

	var payload larkim.MessageImage
	if err := json.Unmarshal([]byte(strings.TrimSpace(*item.Body.Content)), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.ImageKey)
}

func sameThread(item *larkim.Message, event *larkim.P2MessageReceiveV1) bool {
	currentThreadID := extractThreadID(event)
	itemThreadID := trimPtr(item.ThreadId)
	if currentThreadID != "" {
		return itemThreadID == currentThreadID
	}
	return itemThreadID == ""
}

func sameSender(item *larkim.Message, senderIDs map[string]struct{}) bool {
	if item == nil || item.Sender == nil || item.Sender.Id == nil {
		return false
	}
	idType := trimPtr(item.Sender.IdType)
	if idType != "" && idType != "open_id" {
		log.Printf("[larkbot] sameSender: unexpected sender id_type=%q message_id=%s, skipping", idType, trimPtr(item.MessageId))
		return false
	}
	_, ok := senderIDs[strings.TrimSpace(*item.Sender.Id)]
	return ok
}

func extractSenderIDs(event *larkim.P2MessageReceiveV1) map[string]struct{} {
	out := make(map[string]struct{})
	if event == nil || event.Event == nil || event.Event.Sender == nil || event.Event.Sender.SenderId == nil {
		return out
	}
	if v := trimUserID(event.Event.Sender.SenderId.OpenId); v != "" {
		out[v] = struct{}{}
	}
	if v := trimUserID(event.Event.Sender.SenderId.UserId); v != "" {
		out[v] = struct{}{}
	}
	if v := trimUserID(event.Event.Sender.SenderId.UnionId); v != "" {
		out[v] = struct{}{}
	}
	return out
}

func extractChatID(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return ""
	}
	return trimPtr(event.Event.Message.ChatId)
}

func extractThreadID(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return ""
	}
	return trimPtr(event.Event.Message.ThreadId)
}

func extractEventCreateTime(event *larkim.P2MessageReceiveV1) time.Time {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return time.Now()
	}
	t := parseMillis(trimPtr(event.Event.Message.CreateTime))
	if t.IsZero() {
		return time.Now()
	}
	return t
}

type textMessageContent struct {
	Text             *string `json:"text"`
	TextWithoutAtBot *string `json:"text_without_at_bot"`
}

func decodeTextMessageContent(raw string) (textMessageContent, bool) {
	var payload textMessageContent
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return textMessageContent{}, false
	}
	return payload, true
}

func extractTextMessage(event *larkim.P2MessageReceiveV1, raw string) string {
	payload, ok := decodeTextMessageContent(raw)
	if !ok {
		return raw
	}
	if payload.TextWithoutAtBot != nil {
		return strings.TrimSpace(*payload.TextWithoutAtBot)
	}
	if payload.Text != nil {
		return stripMentionKeys(strings.TrimSpace(*payload.Text), extractMentionKeys(event))
	}
	return raw
}

func extractMentionKeys(event *larkim.P2MessageReceiveV1) []string {
	if event == nil || event.Event == nil || event.Event.Message == nil || len(event.Event.Message.Mentions) == 0 {
		return nil
	}

	keys := make([]string, 0, len(event.Event.Message.Mentions))
	for _, mention := range event.Event.Message.Mentions {
		if mention == nil || mention.Key == nil {
			continue
		}
		key := strings.TrimSpace(*mention.Key)
		if key == "" {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}

func mentionsBot(event *larkim.P2MessageReceiveV1, bot botIdentity) bool {
	if strings.TrimSpace(bot.name) == "" || event == nil || event.Event == nil || event.Event.Message == nil {
		return false
	}

	for _, mention := range event.Event.Message.Mentions {
		if mention == nil || mention.Name == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(*mention.Name), bot.name) {
			return true
		}
	}
	return false
}

func stripMentionKeys(text string, keys []string) string {
	text = strings.TrimSpace(text)
	if text == "" || len(keys) == 0 {
		return text
	}

	for _, key := range keys {
		if key == "" {
			continue
		}
		text = strings.ReplaceAll(text, key, " ")
	}
	return strings.Join(strings.Fields(text), " ")
}

func composeQuestionWithFile(userText, localPath, originalName string) string {
	userText = strings.TrimSpace(userText)
	if userText == "" {
		return fmt.Sprintf(
			"The user uploaded a file in Feishu.\n"+
				"Local file path: %s\n"+
				"Original filename: %s\n\n"+
				"Please inspect this local file before answering. If the user has not provided any additional instruction yet, briefly summarize what the file contains and ask what they want to do next.",
			localPath,
			originalName,
		)
	}

	return fmt.Sprintf(
		"The user sent a message in Feishu.\n"+
			"User message: %s\n\n"+
			"A recent file from the same user in the same conversation was found.\n"+
			"Local file path: %s\n"+
			"Original filename: %s\n\n"+
			"Please inspect the local file first and answer the user's message using it when relevant.",
		userText,
		localPath,
		originalName,
	)
}

func parseMillis(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func trimPtr(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func trimUserID(s *string) string {
	return trimPtr(s)
}

func sanitizePathSegment(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}

	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}

	out := strings.Trim(b.String(), "._-")
	if out == "" {
		return fallback
	}
	return out
}

func sanitizeFileName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	base = sanitizePathSegment(base, "attachment")
	if !strings.Contains(base, ".") {
		base += ".bin"
	}
	return base
}

func normalizeKeywords(keywords []string) []string {
	out := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword == "" {
			continue
		}
		out = append(out, keyword)
	}
	return out
}

func cleanupDownloadedFiles(root string, maxAge time.Duration) error {
	root = strings.TrimSpace(root)
	if root == "" || maxAge <= 0 {
		return nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	now := time.Now()
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			log.Printf("[larkbot] cleanup stat failed path=%s: %v", path, err)
			continue
		}
		if now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			log.Printf("[larkbot] cleanup remove failed path=%s: %v", path, err)
			continue
		}
		log.Printf("[larkbot] cleaned downloaded attachment path=%s", path)
	}
	return nil
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
