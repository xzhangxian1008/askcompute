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

	"lab/askplanner/internal/attachments"
	"lab/askplanner/internal/clinic"
	"lab/askplanner/internal/codex"
	"lab/askplanner/internal/config"
)

const (
	messagePageSize              = 50
	maxUploadCommandPages        = 20
	promptAttachmentSummaryLimit = 20
	typingReactionType           = "Typing"
	feishuReactionTimeout        = 10 * time.Second
)

type botIdentity struct {
	name string
}

type messageDedup struct {
	seen sync.Map // messageId -> time.Time
}

type preparedReply struct {
	question        string
	prefix          string
	directReply     string
	skipCodex       bool
	attachmentCtx   codex.AttachmentContext
	conversationKey string
	userKey         string
}

type uploadCommand struct {
	count     int
	remainder string
	ok        bool
}

type attachmentRef struct {
	messageID    string
	fileKey      string
	resourceType string
	createdAt    time.Time
}

type downloadedResource struct {
	tempPath      string
	originalName  string
	resourceType  string
	messageID     string
	fileKey       string
	messageCreate time.Time
}

type replyBody struct {
	msgType string
	content string
}

type postMessageContent struct {
	ZhCN postLocale `json:"zh_cn"`
}

type postLocale struct {
	Title   string         `json:"title,omitempty"`
	Content [][]postMDNode `json:"content"`
}

type postMDNode struct {
	Tag  string `json:"tag"`
	Text string `json:"text"`
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
	prefetcher, err := clinic.NewPrefetcher(cfg)
	if err != nil {
		log.Fatalf("build clinic prefetcher: %v", err)
	}
	attachmentManager, err := attachments.NewManager(cfg.FeishuFileDir, cfg.FeishuUserFileMaxItems)
	if err != nil {
		log.Fatalf("build attachment manager: %v", err)
	}

	apiClient := lark.NewClient(cfg.FeishuAppID, cfg.FeishuAppSecret, lark.WithLogLevel(larkcore.LogLevelInfo))

	dedup := &messageDedup{}
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			dedup.cleanup(time.Duration(cfg.FeishuDedupTimeoutInMin) * time.Minute)
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

			return withTypingReaction(ctx, apiClient, messageID, func() error {
				answer, err := handleEvent(ctx, apiClient, responder, prefetcher, attachmentManager, event)
				if err != nil {
					log.Printf("[larkbot] handle event error: %v (message_id=%s)", err, messageID)
					answer = "Agent Error: " + err.Error()
				}

			reply, err := buildReplyBody(answer)
			if err != nil {
				return fmt.Errorf("build reply body: %w", err)
			}
			if err := replyMessage(ctx, apiClient, messageID, reply); err != nil {
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

func handleEvent(ctx context.Context, apiClient *lark.Client, responder *codex.Responder, prefetcher *clinic.Prefetcher, manager *attachments.Manager, event *larkim.P2MessageReceiveV1) (string, error) {
	prepared, err := prepareReply(ctx, apiClient, manager, event)
	if err != nil {
		return "", err
	}
	if prepared.skipCodex {
		return prepared.directReply, nil
	}

	question := strings.TrimSpace(prepared.question)
	if question == "" {
		question = "Please introduce your capabilities."
	}
	log.Printf("[larkbot] answering question: %q (message_id=%s, conversation=%s)",
		question, extractMessageID(event), prepared.conversationKey)

	enriched, err := prefetcher.Enrich(ctx, prepared.userKey, question, codex.RuntimeContext{
		Attachment: prepared.attachmentCtx,
	})
	if err != nil {
		if msg := clinic.UserFacingMessage(err); msg != "" {
			log.Printf("[larkbot] clinic prefetch user-visible error: %v (message_id=%s, conversation=%s)",
				err, extractMessageID(event), prepared.conversationKey)
			return msg, nil
		}
		return "", err
	}
	if strings.TrimSpace(enriched.IntroReply) != "" {
		if strings.TrimSpace(prepared.prefix) != "" {
			return prepared.prefix + "\n\n" + enriched.IntroReply, nil
		}
		return enriched.IntroReply, nil
	}

	answer, err := responder.AnswerWithContext(ctx, prepared.conversationKey, question, enriched.RuntimeContext)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(prepared.prefix) != "" {
		return prepared.prefix + "\n\n" + answer, nil
	}
	return answer, nil
}

func prepareReply(ctx context.Context, apiClient *lark.Client, manager *attachments.Manager, event *larkim.P2MessageReceiveV1) (*preparedReply, error) {
	userKey := extractPreferredSenderID(event)
	if userKey == "" {
		return nil, fmt.Errorf("missing sender id")
	}
	conversationKey := buildConversationKey(event)

	switch extractMessageType(event) {
	case "text":
		text := extractTextMessage(event, trimPtr(event.Event.Message.Content))
		command := parseUploadCommand(text)
		if command.ok {
			if command.count > manager.MaxItems() {
				command.count = manager.MaxItems()
			}
			summary, attachmentCtx, err := downloadRecentAttachments(ctx, apiClient, manager, event, userKey, command.count)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(command.remainder) == "" {
				return &preparedReply{
					directReply:     summary,
					skipCodex:       true,
					conversationKey: conversationKey,
					userKey:         userKey,
				}, nil
			}
			return &preparedReply{
				question:        command.remainder,
				prefix:          summary,
				attachmentCtx:   attachmentCtx,
				conversationKey: conversationKey,
				userKey:         userKey,
			}, nil
		}

		attachmentCtx, err := buildAttachmentContext(manager, userKey)
		if err != nil {
			return nil, err
		}
		return &preparedReply{
			question:        text,
			attachmentCtx:   attachmentCtx,
			conversationKey: conversationKey,
			userKey:         userKey,
		}, nil
	case "file", "image":
		if isGroupChat(event) {
			return nil, fmt.Errorf("group %s messages should not reach prepareReply", extractMessageType(event))
		}
		summary, err := saveDirectAttachment(ctx, apiClient, manager, event, userKey)
		if err != nil {
			return nil, err
		}
		return &preparedReply{
			directReply:     summary,
			skipCodex:       true,
			conversationKey: conversationKey,
			userKey:         userKey,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported message type: %s", extractMessageType(event))
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

func extractMessageType(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Message.MessageType == nil {
		return ""
	}
	return strings.TrimSpace(*event.Event.Message.MessageType)
}

func saveDirectAttachment(ctx context.Context, apiClient *lark.Client, manager *attachments.Manager, event *larkim.P2MessageReceiveV1, userKey string) (string, error) {
	resource, err := downloadResourceFromEvent(ctx, apiClient, manager.RootDir(), event)
	if err != nil {
		return "", err
	}
	defer os.Remove(resource.tempPath)

	result, err := manager.Import(attachments.ImportRequest{
		UserKey:      userKey,
		OriginalName: resource.originalName,
		MessageID:    resource.messageID,
		FileKey:      resource.fileKey,
		ResourceType: resource.resourceType,
		SourcePath:   resource.tempPath,
		ImportedAt:   resource.messageCreate,
	})
	if err != nil {
		return "", err
	}
	return buildSaveSummary("Saved", []attachments.SaveResult{*result}), nil
}

func downloadRecentAttachments(ctx context.Context, apiClient *lark.Client, manager *attachments.Manager, event *larkim.P2MessageReceiveV1, userKey string, count int) (string, codex.AttachmentContext, error) {
	refs, err := findRecentAttachmentMessages(ctx, apiClient, event, count)
	if err != nil {
		return "", codex.AttachmentContext{}, err
	}

	results := make([]attachments.SaveResult, 0, len(refs))
	for _, ref := range refs {
		resource, err := downloadMessageResourceToTemp(ctx, apiClient, manager.RootDir(), ref.messageID, ref.fileKey, ref.resourceType, ref.createdAt)
		if err != nil {
			return "", codex.AttachmentContext{}, err
		}
		result, importErr := manager.Import(attachments.ImportRequest{
			UserKey:      userKey,
			OriginalName: resource.originalName,
			MessageID:    resource.messageID,
			FileKey:      resource.fileKey,
			ResourceType: resource.resourceType,
			SourcePath:   resource.tempPath,
			ImportedAt:   resource.messageCreate,
		})
		_ = os.Remove(resource.tempPath)
		if importErr != nil {
			return "", codex.AttachmentContext{}, importErr
		}
		results = append(results, *result)
	}

	attachmentCtx, err := buildAttachmentContext(manager, userKey)
	if err != nil {
		return "", codex.AttachmentContext{}, err
	}
	return buildSaveSummary("Downloaded", results), attachmentCtx, nil
}

func buildAttachmentContext(manager *attachments.Manager, userKey string) (codex.AttachmentContext, error) {
	library, err := manager.Snapshot(userKey)
	if err != nil {
		return codex.AttachmentContext{}, err
	}
	items := library.Items
	if len(items) > promptAttachmentSummaryLimit {
		items = items[:promptAttachmentSummaryLimit]
	}
	ctxItems := make([]codex.AttachmentItem, 0, len(items))
	for _, item := range items {
		ctxItems = append(ctxItems, codex.AttachmentItem{
			Name:         item.Name,
			Type:         string(item.Type),
			SavedAt:      item.CreatedAt,
			OriginalName: item.OriginalName,
		})
	}
	return codex.AttachmentContext{
		RootDir: library.RootDir,
		Items:   ctxItems,
	}, nil
}

func buildSaveSummary(verb string, results []attachments.SaveResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %d item(s).", verb, len(results))
	if len(results) == 0 {
		sb.WriteString(" No matching attachments were found.")
		return sb.String()
	}
	sb.WriteString("\n")
	for _, result := range results {
		sb.WriteString("- ")
		sb.WriteString(result.Item.Name)
		if result.Item.Type != "" {
			sb.WriteString(" [")
			sb.WriteString(string(result.Item.Type))
			sb.WriteByte(']')
		}
		if result.Replaced {
			sb.WriteString(" replaced_existing")
		}
		if len(result.Evicted) > 0 {
			sb.WriteString(" evicted=")
			names := make([]string, 0, len(result.Evicted))
			for _, item := range result.Evicted {
				names = append(names, item.Name)
			}
			sb.WriteString(strings.Join(names, ","))
		}
		sb.WriteByte('\n')
	}
	return strings.TrimSpace(sb.String())
}

func parseUploadCommand(text string) uploadCommand {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/upload_") {
		return uploadCommand{}
	}
	rest := strings.TrimPrefix(text, "/upload_")
	if rest == "" {
		return uploadCommand{}
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return uploadCommand{}
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil || n <= 0 {
		return uploadCommand{}
	}
	remainder := strings.TrimSpace(strings.TrimPrefix(rest, fields[0]))
	return uploadCommand{
		count:     n,
		remainder: remainder,
		ok:        true,
	}
}

func findRecentAttachmentMessages(ctx context.Context, apiClient *lark.Client, event *larkim.P2MessageReceiveV1, count int) ([]attachmentRef, error) {
	if count <= 0 {
		return nil, nil
	}
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
	pageToken := ""
	refs := make([]attachmentRef, 0, count)

	for page := 0; page < maxUploadCommandPages && len(refs) < count; page++ {
		req := larkim.NewListMessageReqBuilder().
			ContainerIdType("chat").
			ContainerId(chatID).
			EndTime(strconv.FormatInt(currentCreateAt.Unix(), 10)).
			SortType(larkim.SortTypeListMessageByCreateTimeDesc).
			PageSize(messagePageSize)
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
			break
		}

		for _, item := range resp.Data.Items {
			ref, ok := matchAttachmentMessage(item, event, senderIDs, currentMessageID, currentCreateAt)
			if !ok {
				continue
			}
			refs = append(refs, *ref)
			if len(refs) >= count {
				break
			}
		}

		if resp.Data.HasMore == nil || !*resp.Data.HasMore || resp.Data.PageToken == nil || strings.TrimSpace(*resp.Data.PageToken) == "" {
			break
		}
		pageToken = strings.TrimSpace(*resp.Data.PageToken)
	}

	return refs, nil
}

func matchAttachmentMessage(item *larkim.Message, event *larkim.P2MessageReceiveV1, senderIDs map[string]struct{}, currentMessageID string, currentCreateAt time.Time) (*attachmentRef, bool) {
	if item == nil || !sameThread(item, event) || !sameSender(item, senderIDs) {
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
	itemCreateAt := parseMillis(trimPtr(item.CreateTime))
	if !itemCreateAt.IsZero() && itemCreateAt.After(currentCreateAt) {
		return nil, false
	}

	var fileKey string
	switch msgType {
	case "file":
		fileKey = extractFileKeyFromMessage(item)
	case "image":
		fileKey = extractImageKeyFromMessage(item)
	}
	if fileKey == "" {
		return nil, false
	}

	return &attachmentRef{
		messageID:    messageID,
		fileKey:      fileKey,
		resourceType: msgType,
		createdAt:    itemCreateAt,
	}, true
}

func downloadResourceFromEvent(ctx context.Context, apiClient *lark.Client, tempRoot string, event *larkim.P2MessageReceiveV1) (*downloadedResource, error) {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Message.Content == nil {
		return nil, fmt.Errorf("message content is empty")
	}
	raw := trimPtr(event.Event.Message.Content)
	messageID := extractMessageID(event)
	messageCreate := extractEventCreateTime(event)

	switch extractMessageType(event) {
	case "file":
		var payload larkim.MessageFile
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return nil, fmt.Errorf("parse file message content: %w", err)
		}
		if strings.TrimSpace(payload.FileKey) == "" {
			return nil, fmt.Errorf("file message missing file_key")
		}
		return downloadMessageResourceToTemp(ctx, apiClient, tempRoot, messageID, payload.FileKey, "file", messageCreate)
	case "image":
		var payload larkim.MessageImage
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return nil, fmt.Errorf("parse image message content: %w", err)
		}
		if strings.TrimSpace(payload.ImageKey) == "" {
			return nil, fmt.Errorf("image message missing image_key")
		}
		return downloadMessageResourceToTemp(ctx, apiClient, tempRoot, messageID, payload.ImageKey, "image", messageCreate)
	default:
		return nil, fmt.Errorf("unsupported direct attachment type: %s", extractMessageType(event))
	}
}

func downloadMessageResourceToTemp(ctx context.Context, apiClient *lark.Client, tempRoot, messageID, fileKey, resourceType string, createdAt time.Time) (*downloadedResource, error) {
	resp, err := apiClient.Im.V1.MessageResource.Get(ctx,
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(fileKey).
			Type(resourceType).
			Build())
	if err != nil {
		return nil, fmt.Errorf("download %s from Feishu: %w", resourceType, err)
	}
	if resp != nil && !resp.Success() {
		return nil, fmt.Errorf("download %s from Feishu failed: code=%d, msg=%s", resourceType, resp.Code, resp.Msg)
	}
	if resp == nil || resp.File == nil {
		return nil, fmt.Errorf("download %s from Feishu: empty response", resourceType)
	}

	fileName := strings.TrimSpace(resp.FileName)
	if fileName == "" && resourceType == "file" {
		fileName = fileKey + ".bin"
	}
	ext := filepath.Ext(fileName)
	if ext == "" {
		if resourceType == "image" {
			ext = ".png"
		} else {
			ext = ".bin"
		}
	}

	tempFile, err := os.CreateTemp(tempRoot, ".download-*"+ext)
	if err != nil {
		return nil, fmt.Errorf("create temp attachment file: %w", err)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("close temp attachment file: %w", err)
	}
	if err := resp.WriteFile(tempPath); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("write temp attachment file: %w", err)
	}

	log.Printf("[larkbot] downloaded %s message_id=%s file_key=%s temp=%s", resourceType, messageID, fileKey, tempPath)
	return &downloadedResource{
		tempPath:      tempPath,
		originalName:  fileName,
		resourceType:  resourceType,
		messageID:     messageID,
		fileKey:       fileKey,
		messageCreate: createdAt,
	}, nil
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

func extractPreferredSenderID(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil || event.Event.Sender == nil || event.Event.Sender.SenderId == nil {
		return ""
	}
	if v := trimUserID(event.Event.Sender.SenderId.OpenId); v != "" {
		return v
	}
	if v := trimUserID(event.Event.Sender.SenderId.UserId); v != "" {
		return v
	}
	return ""
}

func trimUserID(s *string) string {
	return trimPtr(s)
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

func buildConversationKey(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil {
		return "lark:unknown"
	}

	threadID := extractThreadID(event)
	chatID := extractChatID(event)
	senderID := sanitizePathSegment(extractPreferredSenderID(event), "")
	messageID := extractMessageID(event)

	switch {
	case threadID != "" && senderID != "":
		return "lark:thread:" + threadID + ":user:" + senderID
	case chatID != "" && senderID != "":
		return "lark:chat:" + chatID + ":user:" + senderID
	case threadID != "":
		return "lark:thread:" + threadID
	case chatID != "":
		return "lark:chat:" + chatID
	case messageID != "":
		return "lark:message:" + messageID
	default:
		return "lark:unknown"
	}
}

func buildReplyBody(text string) (replyBody, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = " "
	}

	payload := postMessageContent{
		ZhCN: postLocale{
			Content: [][]postMDNode{{
				{
					Tag:  "md",
					Text: text,
				},
			}},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return replyBody{}, err
	}
	return replyBody{
		msgType: "post",
		content: string(b),
	}, nil
}

func withTypingReaction(ctx context.Context, apiClient *lark.Client, messageID string, run func() error) error {
	reactionID, err := addTypingReaction(ctx, apiClient, messageID)
	if err != nil {
		log.Printf("[larkbot] add typing reaction failed: %v (message_id=%s)", err, messageID)
		return run()
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), feishuReactionTimeout)
		defer cancel()
		if err := deleteMessageReaction(cleanupCtx, apiClient, messageID, reactionID); err != nil {
			log.Printf("[larkbot] delete typing reaction failed: %v (message_id=%s, reaction_id=%s)", err, messageID, reactionID)
		}
	}()

	return run()
}

func addTypingReaction(ctx context.Context, apiClient *lark.Client, messageID string) (string, error) {
	reactionCtx, cancel := context.WithTimeout(ctx, feishuReactionTimeout)
	defer cancel()

	resp, err := apiClient.Im.V1.MessageReaction.Create(reactionCtx,
		larkim.NewCreateMessageReactionReqBuilder().
			MessageId(messageID).
			Body(larkim.NewCreateMessageReactionReqBodyBuilder().
				ReactionType(larkim.NewEmojiBuilder().
					EmojiType(typingReactionType).
					Build()).
				Build()).
			Build())
	if err != nil {
		return "", fmt.Errorf("call create reaction API: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("create reaction API error: code=%d, msg=%s", resp.Code, resp.Msg)
	}

	reactionID := ""
	if resp.Data != nil {
		reactionID = trimPtr(resp.Data.ReactionId)
	}
	if reactionID == "" {
		return "", fmt.Errorf("create reaction API returned empty reaction_id")
	}

	log.Printf("[larkbot] typing reaction added (message_id=%s, reaction_id=%s)", messageID, reactionID)
	return reactionID, nil
}

func deleteMessageReaction(ctx context.Context, apiClient *lark.Client, messageID, reactionID string) error {
	resp, err := apiClient.Im.V1.MessageReaction.Delete(ctx,
		larkim.NewDeleteMessageReactionReqBuilder().
			MessageId(messageID).
			ReactionId(reactionID).
			Build())
	if err != nil {
		return fmt.Errorf("call delete reaction API: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("delete reaction API error: code=%d, msg=%s", resp.Code, resp.Msg)
	}

	log.Printf("[larkbot] typing reaction deleted (message_id=%s, reaction_id=%s)", messageID, reactionID)
	return nil
}

func replyMessage(ctx context.Context, apiClient *lark.Client, messageID string, body replyBody) error {
	log.Printf("[larkbot] replying to message_id=%s", messageID)
	resp, err := apiClient.Im.V1.Message.Reply(ctx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(body.msgType).
				Content(body.content).
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
