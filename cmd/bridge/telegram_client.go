// telegram_client.go — self-implemented Telegram Bot API client (no tgbotapi dependency).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Core Telegram API types (mirrors a subset of the Bot API response objects)
// ---------------------------------------------------------------------------

type User struct {
	ID       int64  `json:"id"`
	UserName string `json:"username"`
	IsBot    bool   `json:"is_bot"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type Message struct {
	MessageID      int                    `json:"message_id"`
	From           *User                  `json:"from,omitempty"`
	Chat           *Chat                  `json:"chat,omitempty"`
	Date           int                    `json:"date"`
	Text           string                 `json:"text,omitempty"`
	Photo          []PhotoSize            `json:"photo,omitempty"`
	Video          *Video                 `json:"video,omitempty"`
	Animation      *Animation             `json:"animation,omitempty"`
	Audio          *Audio                 `json:"audio,omitempty"`
	Document       *Document              `json:"document,omitempty"`
	Sticker        *Sticker               `json:"sticker,omitempty"`
	MediaGroupID   string                 `json:"media_group_id,omitempty"`
	ReplyToMessage *Message               `json:"reply_to_message,omitempty"`
	Caption        string                 `json:"caption,omitempty"`
	ReplyMarkup    *InlineKeyboardMarkup  `json:"reply_markup,omitempty"`
}

type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int    `json:"file_size,omitempty"`
}

type Video struct {
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	Width        int        `json:"width"`
	Height       int        `json:"height"`
	Duration     int        `json:"duration"`
	FileSize     int        `json:"file_size,omitempty"`
	Thumbnail    *PhotoSize `json:"thumbnail,omitempty"`
}

type Animation struct {
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	Width        int        `json:"width"`
	Height       int        `json:"height"`
	Duration     int        `json:"duration"`
	FileSize     int        `json:"file_size,omitempty"`
	Thumbnail    *PhotoSize `json:"thumbnail,omitempty"`
}

type Audio struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	FileSize     int    `json:"file_size,omitempty"`
	Title        string `json:"title,omitempty"`
	MIMEType     string `json:"mime_type,omitempty"`
}

type Document struct {
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	FileSize     int        `json:"file_size,omitempty"`
	FileName     string     `json:"file_name,omitempty"`
	MIMEType     string     `json:"mime_type,omitempty"`
	Thumbnail    *PhotoSize `json:"thumbnail,omitempty"`
}

type Sticker struct {
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	Width        int        `json:"width"`
	Height       int        `json:"height"`
	IsAnimated   bool       `json:"is_animated"`
	IsVideo      bool       `json:"is_video"`
	Thumbnail    *PhotoSize `json:"thumbnail,omitempty"`
	Emoji        string     `json:"emoji,omitempty"`
	SetName      string     `json:"set_name,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type APIResponse struct {
	Ok          bool            `json:"ok"`
	Result      json.RawMessage `json:"result,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Description string          `json:"description,omitempty"`
}

type File struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int    `json:"file_size,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

// ---------------------------------------------------------------------------
// Inline keyboard types (used widely across the codebase)
// ---------------------------------------------------------------------------

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string  `json:"text"`
	CallbackData *string `json:"callback_data,omitempty"`
	URL          *string `json:"url,omitempty"`
}

func NewInlineKeyboardMarkup(rows ...[]InlineKeyboardButton) *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

func NewInlineKeyboardRow(buttons ...InlineKeyboardButton) []InlineKeyboardButton {
	return buttons
}

func NewInlineKeyboardButtonData(text, data string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: text, CallbackData: strPtr(data)}
}

func NewInlineKeyboardButtonURL(text, urlStr string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: text, URL: strPtr(urlStr)}
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string {
	return &s
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const bridgeVersion = "v2.0.0-stub"

const (
	ModeMarkdownV2 = "MarkdownV2"
	ChatTyping     = "typing"

	maxUploadSize = 50 * 1024 * 1024 // 50 MB — safety limit for multipart uploads
)

// ---------------------------------------------------------------------------
// File upload types
// ---------------------------------------------------------------------------

// FileReader represents a file to be uploaded with its filename.
type FileReader struct {
	Name   string
	Reader io.ReadCloser
}

// UploadPath is a string type for local file path uploads.
type UploadPath string

// ---------------------------------------------------------------------------
// Parameter type used by MakeRequest
// ---------------------------------------------------------------------------

// Params is a convenience alias used by MakeRequest.
type Params map[string]string

// Chattable is the interface all config types implement.
type Chattable interface {
	method() string
	params() (url.Values, error)
	files() map[string]FileReader
}

// ---------------------------------------------------------------------------
// TelegramClient
// ---------------------------------------------------------------------------

// TelegramClient is a self-contained Telegram Bot API client.
type TelegramClient struct {
	Token   string
	BaseURL string // e.g. "https://api.telegram.org"
	Client  *http.Client
	Self    User // populated by NewTelegramClient via getMe
}

// NewTelegramClient creates a client, calls getMe to populate Self.
func NewTelegramClient(token string) (*TelegramClient, error) {
	c := &TelegramClient{
		Token:   token,
		BaseURL: "https://api.telegram.org",
		Client: &http.Client{
			Timeout: 65 * time.Second,
		},
	}
	// Populate Self via getMe
	var me User
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.DoRequest(ctx, "getMe", nil, &me); err != nil {
		return nil, fmt.Errorf("getMe: %w", err)
	}
	c.Self = me
	return c, nil
}

// apiURL returns the full URL for a Bot API method.

// telegramIOTimeout caps any single Bot API call so a hung Telegram edge
// cannot freeze the agent turn (which holds the per-chat submit mutex).
const telegramIOTimeout = 25 * time.Second

func withTelegramTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, telegramIOTimeout)
}

func (c *TelegramClient) apiURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", c.BaseURL, c.Token, method)
}

// fileURL returns the base URL for file downloads.
func (c *TelegramClient) fileURL() string {
	return fmt.Sprintf("https://api.telegram.org/file/bot%s", c.Token)
}

// ---------------------------------------------------------------------------
// Retry helpers for DoRequest / DoMultipart / MakeRequest
// ---------------------------------------------------------------------------

// retryableError wraps an error that should trigger a retry.
type retryableError struct {
	error
	retryAfter time.Duration // 0 means use standard exponential backoff (1s, 2s, 4s)
}

func (e *retryableError) Unwrap() error { return e.error }

// doWithRetry calls fn repeatedly on retryable errors.
// max 3 attempts; backoff is exponential (1s, 2s, 4s) unless retryAfter is set.
func (c *TelegramClient) doWithRetry(ctx context.Context, fn func() error) error {
	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			var delay time.Duration
			var re *retryableError
			if errors.As(lastErr, &re) && re.retryAfter > 0 {
				delay = re.retryAfter
			} else {
				delay = time.Duration(1<<uint(attempt-1)) * time.Second
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		var re *retryableError
		if errors.As(err, &re) {
			continue
		}
		return err
	}
	return lastErr
}

// parseRetryAfter parses the Retry-After header value (seconds as int).
func parseRetryAfter(s string) time.Duration {
	if s == "" {
		return 0
	}
	sec, err := strconv.Atoi(s)
	if err != nil || sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

// DoRequest sends a POST request to the Telegram Bot API with URL-encoded form data.
// If result is non-nil, the response's "result" field is JSON-unmarshalled into it.
// Returns an error wrapping Description if the API response has ok=false.
func (c *TelegramClient) DoRequest(ctx context.Context, method string, params url.Values, result interface{}) error {
	return c.doWithRetry(ctx, func() error {
		body := ""
		if params != nil {
			body = params.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL(method), strings.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		if params != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}

		resp, err := c.Client.Do(req)
		if err != nil {
			return &retryableError{error: fmt.Errorf("request failed: %w", err)}
		}
		defer resp.Body.Close()

		var apiResp APIResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
			return fmt.Errorf("parse response: %w", err)
		}

		if !apiResp.Ok {
			// 429 flood control — retryable
			if apiResp.ErrorCode == 429 {
				retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
				if retryAfter == 0 {
					retryAfter = 5 * time.Second
				}
				return &retryableError{
					error:      fmt.Errorf("too many requests (code=%d): %s", apiResp.ErrorCode, apiResp.Description),
					retryAfter: retryAfter,
				}
			}
			// 5xx server errors — retryable with standard backoff
			if apiResp.ErrorCode >= 500 {
				return &retryableError{error: fmt.Errorf("api error (code=%d): %s", apiResp.ErrorCode, apiResp.Description)}
			}
			// Other (401, 403, 400 etc.) — not retryable
			if apiResp.ErrorCode == 401 {
				return fmt.Errorf("unauthorized (code=%d): %s", apiResp.ErrorCode, apiResp.Description)
			}
			return fmt.Errorf("api error (code=%d): %s", apiResp.ErrorCode, apiResp.Description)
		}

		if result != nil && len(apiResp.Result) > 0 {
			if err := json.Unmarshal(apiResp.Result, result); err != nil {
				return fmt.Errorf("parse result: %w", err)
			}
		}
		return nil
	})
}

// DoMultipart sends a multipart/form-data POST request.
func (c *TelegramClient) DoMultipart(ctx context.Context, method string, fields map[string]string, files map[string]FileReader, result interface{}) error {
	// Build multipart body once so retries can re-read from the same buffer.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// Write form fields
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return fmt.Errorf("write field %s: %w", k, err)
		}
	}

	// Write files
	for fieldName, fr := range files {
		fw, err := w.CreateFormFile(fieldName, fr.Name)
		if err != nil {
			return fmt.Errorf("create form file %s: %w", fieldName, err)
		}
		lr := io.LimitReader(fr.Reader, maxUploadSize+1)
		n, err := io.Copy(fw, lr)
		if n > maxUploadSize {
			fr.Reader.Close()
			return fmt.Errorf("file too large: %d bytes (max %d)", n, maxUploadSize)
		}
		if err != nil {
			fr.Reader.Close()
			return fmt.Errorf("copy file %s: %w", fieldName, err)
		}
		fr.Reader.Close()
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	contentType := w.FormDataContentType()
	bodyBytes := buf.Bytes()

	return c.doWithRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL(method), bytes.NewReader(bodyBytes))
		if err != nil {
			return fmt.Errorf("create multipart request: %w", err)
		}
		req.Header.Set("Content-Type", contentType)

		resp, err := c.Client.Do(req)
		if err != nil {
			return &retryableError{error: fmt.Errorf("multipart request failed: %w", err)}
		}
		defer resp.Body.Close()

		var apiResp APIResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
			return fmt.Errorf("parse multipart response: %w", err)
		}

		if !apiResp.Ok {
			if apiResp.ErrorCode == 429 {
				retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
				if retryAfter == 0 {
					retryAfter = 5 * time.Second
				}
				return &retryableError{
					error:      fmt.Errorf("too many requests (code=%d): %s", apiResp.ErrorCode, apiResp.Description),
					retryAfter: retryAfter,
				}
			}
			if apiResp.ErrorCode >= 500 {
				return &retryableError{error: fmt.Errorf("api error (code=%d): %s", apiResp.ErrorCode, apiResp.Description)}
			}
			return fmt.Errorf("api error (code=%d): %s", apiResp.ErrorCode, apiResp.Description)
		}

		if result != nil && len(apiResp.Result) > 0 {
			if err := json.Unmarshal(apiResp.Result, result); err != nil {
				return fmt.Errorf("parse result: %w", err)
			}
		}
		return nil
	})
}

// SendMessageDraft streams a partial message via Bot API sendMessageDraft
// (typewriter / live draft). Same draft_id updates the same ephemeral bubble.
// Draft is temporary (~30s); finalize with sendMessage / sendRichMessage.
// Returns nil on success (API returns True, not a Message).
func (c *TelegramClient) SendMessageDraft(ctx context.Context, chatID int64, draftID int64, text string) error {
	if draftID == 0 {
		draftID = 1
	}
	// Preview is last 4096 runes (Telegram draft limit).
	text = telegramPreviewTail(text, telegramMaxMessageRunes)
	params := Params{
		"chat_id":  strconv.FormatInt(chatID, 10),
		"draft_id": strconv.FormatInt(draftID, 10),
		"text":     text,
	}
	ctx, cancel := withTelegramTimeout(ctx)
	defer cancel()
	_, err := c.MakeRequest(ctx, "sendMessageDraft", params)
	return err
}

// SendRichMessageDraft streams a rich markdown draft (Bot API 10.1+ sendRichMessageDraft).
// rich_message = {"markdown": "..."}; same draft_id updates the typewriter bubble.
func (c *TelegramClient) SendRichMessageDraft(ctx context.Context, chatID int64, draftID int64, markdown string) error {
	if draftID == 0 {
		draftID = 1
	}
	markdown = telegramPreviewTail(markdown, telegramMaxMessageRunes)
	rich, err := json.Marshal(map[string]any{"markdown": markdown})
	if err != nil {
		return err
	}
	params := Params{
		"chat_id":      strconv.FormatInt(chatID, 10),
		"draft_id":     strconv.FormatInt(draftID, 10),
		"rich_message": string(rich),
	}
	ctx, cancel := withTelegramTimeout(ctx)
	defer cancel()
	_, err = c.MakeRequest(ctx, "sendRichMessageDraft", params)
	return err
}

// SendRichMessage persists a rich message (Bot API 10.1+ sendRichMessage).
// This is the finalize step after sendRichMessageDraft streaming.
func (c *TelegramClient) SendRichMessage(ctx context.Context, chatID int64, markdown string) (int, error) {
	runes := []rune(markdown)
	if len(runes) > 32768 {
		markdown = string(runes[:32768])
	}
	rich, err := json.Marshal(map[string]any{"markdown": markdown})
	if err != nil {
		return 0, err
	}
	params := Params{
		"chat_id":      strconv.FormatInt(chatID, 10),
		"rich_message": string(rich),
	}
	ctx, cancel := withTelegramTimeout(ctx)
	defer cancel()
	resp, err := c.MakeRequest(ctx, "sendRichMessage", params)
	if err != nil {
		return 0, err
	}
	var msg struct {
		MessageID int `json:"message_id"`
	}
	if len(resp.Result) > 0 {
		_ = json.Unmarshal(resp.Result, &msg)
	}
	return msg.MessageID, nil
}

// PushDraft prefers sendRichMessageDraft (typewriter); falls back to sendMessageDraft.
func (c *TelegramClient) PushDraft(ctx context.Context, chatID int64, draftID int64, text string) error {
	if err := c.SendRichMessageDraft(ctx, chatID, draftID, text); err == nil {
		return nil
	} else {
		if ferr := c.SendMessageDraft(ctx, chatID, draftID, text); ferr == nil {
			return nil
		} else {
			return fmt.Errorf("rich draft: %v; plain draft: %w", err, ferr)
		}
	}
}

// MakeRequest sends a raw request to an arbitrary Telegram Bot API endpoint with
// string-based params. Returns the full APIResponse for callers that need to
// inspect the raw JSON result (e.g. sendRichMessage, sendRichMessageDraft).
func (c *TelegramClient) MakeRequest(ctx context.Context, endpoint string, params Params) (*APIResponse, error) {
	var apiResp APIResponse
	err := c.doWithRetry(ctx, func() error {
		values := url.Values{}
		for k, v := range params {
			values.Set(k, v)
		}
		body := values.Encode()

		req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL(endpoint), strings.NewReader(body))
		if err != nil {
			return fmt.Errorf("create MakeRequest: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := c.Client.Do(req)
		if err != nil {
			return &retryableError{error: fmt.Errorf("MakeRequest failed: %w", err)}
		}
		defer resp.Body.Close()

		// Reset apiResp for each attempt
		apiResp = APIResponse{}
		if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
			return fmt.Errorf("parse MakeRequest response: %w", err)
		}

		if !apiResp.Ok {
			if apiResp.ErrorCode == 429 {
				retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
				if retryAfter == 0 {
					retryAfter = 5 * time.Second
				}
				return &retryableError{
					error:      fmt.Errorf("too many requests (code=%d): %s", apiResp.ErrorCode, apiResp.Description),
					retryAfter: retryAfter,
				}
			}
			if apiResp.ErrorCode >= 500 {
				return &retryableError{error: fmt.Errorf("api error (code=%d): %s", apiResp.ErrorCode, apiResp.Description)}
			}
			return fmt.Errorf("api error (code=%d): %s", apiResp.ErrorCode, apiResp.Description)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &apiResp, nil
}

// ---------------------------------------------------------------------------
// Convenience methods
// ---------------------------------------------------------------------------

// GetUpdates long-polls for updates.
func (c *TelegramClient) GetUpdates(ctx context.Context, offset int, timeout int) ([]Update, error) {
	params := url.Values{}
	params.Set("offset", strconv.Itoa(offset))
	if timeout > 0 {
		params.Set("timeout", strconv.Itoa(timeout))
	}
	var updates []Update
	if err := c.DoRequest(ctx, "getUpdates", params, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// GetFile returns metadata for a file given its file_id.
func (c *TelegramClient) GetFile(ctx context.Context, fileID string) (File, error) {
	params := url.Values{}
	params.Set("file_id", fileID)
	var file File
	if err := c.DoRequest(ctx, "getFile", params, &file); err != nil {
		return File{}, err
	}
	return file, nil
}

// Send sends a Chattable. Message-returning methods parse Message; methods that
// return a boolean True (deleteMessage, answerCallbackQuery, sendChatAction, …)
// succeed without requiring a Message body (fixes bool→Message unmarshal errors).
func (c *TelegramClient) Send(ctx context.Context, msg Chattable) (*Message, error) {
	ctx, cancel := withTelegramTimeout(ctx)
	defer cancel()
	params, err := msg.params()
	if err != nil {
		return nil, err
	}
	files := msg.files()
	method := msg.method()

	// Boolean-result Bot API methods.
	switch method {
	case "deleteMessage", "answerCallbackQuery", "sendChatAction", "banChatMember",
		"unbanChatMember", "restrictChatMember", "approveChatJoinRequest",
		"declineChatJoinRequest", "setMessageReaction":
		if len(files) > 0 {
			return nil, fmt.Errorf("%s: unexpected multipart", method)
		}
		_, err := c.MakeRequest(ctx, method, paramsToMap(params))
		if err != nil {
			return nil, err
		}
		return &Message{}, nil
	}

	var message Message
	if len(files) > 0 {
		fields := make(map[string]string, len(params))
		for k, v := range params {
			if len(v) > 0 {
				fields[k] = v[0]
			}
		}
		if err := c.DoMultipart(ctx, method, fields, files, &message); err != nil {
			return nil, err
		}
	} else {
		if err := c.DoRequest(ctx, method, params, &message); err != nil {
			return nil, err
		}
	}
	return &message, nil
}

// Request sends a Chattable and returns the raw APIResponse (for commands, callback answers, etc.).
func (c *TelegramClient) Request(ctx context.Context, msg Chattable) (*APIResponse, error) {
	ctx, cancel := withTelegramTimeout(ctx)
	defer cancel()
	params, err := msg.params()
	if err != nil {
		return nil, err
	}
	files := msg.files()
	method := msg.method()

	if len(files) > 0 {
		fields := make(map[string]string, len(params))
		for k, v := range params {
			if len(v) > 0 {
				fields[k] = v[0]
			}
		}
		var apiResp APIResponse
		if err := c.DoMultipart(ctx, method, fields, files, &apiResp); err != nil {
			return nil, err
		}
		return &apiResp, nil
	}

	return c.MakeRequest(ctx, method, paramsToMap(params))
}

// paramsToMap converts url.Values to Params (map[string]string).
func paramsToMap(v url.Values) Params {
	m := make(Params, len(v))
	for k, vals := range v {
		if len(vals) > 0 {
			m[k] = vals[0]
		}
	}
	return m
}
