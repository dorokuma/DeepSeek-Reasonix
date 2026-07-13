// telegram_types.go — Config types and constructors implementing Chattable.
package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// UpdateConfig (used for long-polling configuration)
// ---------------------------------------------------------------------------

type UpdateConfig struct {
	Offset  int
	Limit   int
	Timeout int
}

func NewUpdate(offset int) UpdateConfig {
	return UpdateConfig{Offset: offset, Timeout: 60}
}

// ---------------------------------------------------------------------------
// MessageConfig
// ---------------------------------------------------------------------------

type MessageConfig struct {
	ChatID              int64
	Text                string
	ParseMode           string
	DisableWebPagePreview bool
	DisableNotification bool
	ReplyToMessageID    int
	ReplyMarkup         *InlineKeyboardMarkup
}

func NewMessage(chatID int64, text string) MessageConfig {
	return MessageConfig{
		ChatID:              chatID,
		Text:                text,
		DisableWebPagePreview: true, // link preview disabled by default
	}
}

func (m MessageConfig) method() string { return "sendMessage" }

func (m MessageConfig) params() (url.Values, error) {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(m.ChatID, 10))
	v.Set("text", m.Text)
	if m.ParseMode != "" {
		v.Set("parse_mode", m.ParseMode)
	}
	if m.DisableWebPagePreview {
		v.Set("disable_web_page_preview", "true")
	}
	if m.DisableNotification {
		v.Set("disable_notification", "true")
	}
	if m.ReplyToMessageID != 0 {
		v.Set("reply_to_message_id", strconv.Itoa(m.ReplyToMessageID))
	}
	if m.ReplyMarkup != nil {
		markupJSON, err := jsonMarshal(m.ReplyMarkup)
		if err != nil {
			return nil, fmt.Errorf("marshal reply_markup: %w", err)
		}
		v.Set("reply_markup", markupJSON)
	}
	return v, nil
}

func (m MessageConfig) files() map[string]FileReader { return nil }

// ---------------------------------------------------------------------------
// EditMessageTextConfig
// ---------------------------------------------------------------------------

type EditMessageTextConfig struct {
	ChatID      int64
	MessageID   int
	Text        string
	ParseMode   string
	ReplyMarkup *InlineKeyboardMarkup
}

func NewEditMessageText(chatID int64, msgID int, text string) EditMessageTextConfig {
	return EditMessageTextConfig{
		ChatID:    chatID,
		MessageID: msgID,
		Text:      text,
	}
}

func (e EditMessageTextConfig) method() string { return "editMessageText" }

func (e EditMessageTextConfig) params() (url.Values, error) {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(e.ChatID, 10))
	v.Set("message_id", strconv.Itoa(e.MessageID))
	v.Set("text", e.Text)
	if e.ParseMode != "" {
		v.Set("parse_mode", e.ParseMode)
	}
	if e.ReplyMarkup != nil {
		markupJSON, err := jsonMarshal(e.ReplyMarkup)
		if err != nil {
			return nil, fmt.Errorf("marshal reply_markup: %w", err)
		}
		v.Set("reply_markup", markupJSON)
	}
	return v, nil
}

func (e EditMessageTextConfig) files() map[string]FileReader { return nil }

// ---------------------------------------------------------------------------
// EditMessageReplyMarkupConfig
// ---------------------------------------------------------------------------

type EditMessageReplyMarkupConfig struct {
	ChatID      int64
	MessageID   int
	ReplyMarkup *InlineKeyboardMarkup
}

func NewEditMessageReplyMarkup(chatID int64, msgID int, markup *InlineKeyboardMarkup) EditMessageReplyMarkupConfig {
	return EditMessageReplyMarkupConfig{
		ChatID:      chatID,
		MessageID:   msgID,
		ReplyMarkup: markup,
	}
}

func (e EditMessageReplyMarkupConfig) method() string { return "editMessageReplyMarkup" }

func (e EditMessageReplyMarkupConfig) params() (url.Values, error) {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(e.ChatID, 10))
	v.Set("message_id", strconv.Itoa(e.MessageID))
	if e.ReplyMarkup != nil {
		markupJSON, err := jsonMarshal(e.ReplyMarkup)
		if err != nil {
			return nil, fmt.Errorf("marshal reply_markup: %w", err)
		}
		v.Set("reply_markup", markupJSON)
	}
	return v, nil
}

func (e EditMessageReplyMarkupConfig) files() map[string]FileReader { return nil }

// ---------------------------------------------------------------------------
// CallbackConfig (answerCallbackQuery)
// ---------------------------------------------------------------------------

type CallbackConfig struct {
	CallbackQueryID string
	Text            string
	ShowAlert       bool
}

func NewCallback(id, text string) CallbackConfig {
	return CallbackConfig{
		CallbackQueryID: id,
		Text:            text,
	}
}

func (c CallbackConfig) method() string { return "answerCallbackQuery" }

func (c CallbackConfig) params() (url.Values, error) {
	v := url.Values{}
	v.Set("callback_query_id", c.CallbackQueryID)
	v.Set("text", c.Text)
	if c.ShowAlert {
		v.Set("show_alert", "true")
	}
	return v, nil
}

func (c CallbackConfig) files() map[string]FileReader { return nil }

// ---------------------------------------------------------------------------
// DeleteMessageConfig
// ---------------------------------------------------------------------------

type DeleteMessageConfig struct {
	ChatID    int64
	MessageID int
}

func NewDeleteMessage(chatID int64, msgID int) DeleteMessageConfig {
	return DeleteMessageConfig{
		ChatID:    chatID,
		MessageID: msgID,
	}
}

func (d DeleteMessageConfig) method() string { return "deleteMessage" }

func (d DeleteMessageConfig) params() (url.Values, error) {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(d.ChatID, 10))
	v.Set("message_id", strconv.Itoa(d.MessageID))
	return v, nil
}

func (d DeleteMessageConfig) files() map[string]FileReader { return nil }

// ---------------------------------------------------------------------------
// CopyMessageConfig
// ---------------------------------------------------------------------------

type CopyMessageConfig struct {
	ChatID     int64
	FromChatID int64
	MessageID  int
}

func NewCopyMessage(chatID, fromChatID int64, msgID int) CopyMessageConfig {
	return CopyMessageConfig{
		ChatID:     chatID,
		FromChatID: fromChatID,
		MessageID:  msgID,
	}
}

func (c CopyMessageConfig) method() string { return "copyMessage" }

func (c CopyMessageConfig) params() (url.Values, error) {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(c.ChatID, 10))
	v.Set("from_chat_id", strconv.FormatInt(c.FromChatID, 10))
	v.Set("message_id", strconv.Itoa(c.MessageID))
	return v, nil
}

func (c CopyMessageConfig) files() map[string]FileReader { return nil }

// ---------------------------------------------------------------------------
// ChatActionConfig (sendChatAction)
// ---------------------------------------------------------------------------

type ChatActionConfig struct {
	ChatID int64
	Action string
}

func NewChatAction(chatID int64, action string) ChatActionConfig {
	return ChatActionConfig{
		ChatID: chatID,
		Action: action,
	}
}

func (c ChatActionConfig) method() string { return "sendChatAction" }

func (c ChatActionConfig) params() (url.Values, error) {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(c.ChatID, 10))
	v.Set("action", c.Action)
	return v, nil
}

func (c ChatActionConfig) files() map[string]FileReader { return nil }

// ---------------------------------------------------------------------------
// FileConfig (getFile)
// ---------------------------------------------------------------------------

type FileConfig struct {
	FileID string
}

func NewFileConfig(fileID string) FileConfig {
	return FileConfig{FileID: fileID}
}

// ---------------------------------------------------------------------------
// PhotoConfig
// ---------------------------------------------------------------------------

type PhotoConfig struct {
	ChatID              int64
	File                string   // file_id or "file:///path"
	FileReader          FileReader // set for multipart upload
	Caption             string
	ParseMode           string
	DisableNotification bool
	ReplyToMessageID    int
	ReplyMarkup         *InlineKeyboardMarkup
}

func NewPhoto(chatID int64, file interface{}) PhotoConfig {
	c := PhotoConfig{ChatID: chatID}
	switch f := file.(type) {
	case string:
		c.File = f
	case FileReader:
		c.FileReader = f
	case UploadPath:
		c.File = string(f)
	}
	return c
}

func (p PhotoConfig) method() string { return "sendPhoto" }

func (p PhotoConfig) params() (url.Values, error) {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(p.ChatID, 10))
	if p.File != "" && p.FileReader.Reader == nil {
		v.Set("photo", p.File)
	}
	if p.Caption != "" {
		v.Set("caption", p.Caption)
	}
	if p.ParseMode != "" {
		v.Set("parse_mode", p.ParseMode)
	}
	if p.DisableNotification {
		v.Set("disable_notification", "true")
	}
	if p.ReplyToMessageID != 0 {
		v.Set("reply_to_message_id", strconv.Itoa(p.ReplyToMessageID))
	}
	if p.ReplyMarkup != nil {
		markupJSON, err := jsonMarshal(p.ReplyMarkup)
		if err != nil {
			return nil, fmt.Errorf("marshal reply_markup: %w", err)
		}
		v.Set("reply_markup", markupJSON)
	}
	return v, nil
}

func (p PhotoConfig) files() map[string]FileReader {
	if p.FileReader.Reader != nil {
		return map[string]FileReader{"photo": p.FileReader}
	}
	if strings.HasPrefix(p.File, "file://") || strings.HasPrefix(p.File, "/") {
		path := strings.TrimPrefix(p.File, "file://")
		if fi, err := os.Stat(path); err != nil || fi.Size() > maxUploadSize {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = path
		}
		f, err := os.Open(resolved)
		if err != nil {
			return nil
		}
		return map[string]FileReader{"photo": {Name: filepath.Base(path), Reader: f}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// VideoConfig
// ---------------------------------------------------------------------------

type VideoConfig struct {
	ChatID              int64
	File                string
	FileReader          FileReader
	Caption             string
	ParseMode           string
	DisableNotification bool
	ReplyToMessageID    int
	ReplyMarkup         *InlineKeyboardMarkup
}

func NewVideo(chatID int64, file interface{}) VideoConfig {
	c := VideoConfig{ChatID: chatID}
	switch f := file.(type) {
	case string:
		c.File = f
	case FileReader:
		c.FileReader = f
	case UploadPath:
		c.File = string(f)
	}
	return c
}

func (v VideoConfig) method() string { return "sendVideo" }

func (v VideoConfig) params() (url.Values, error) {
	params := url.Values{}
	params.Set("chat_id", strconv.FormatInt(v.ChatID, 10))
	if v.File != "" && v.FileReader.Reader == nil {
		params.Set("video", v.File)
	}
	if v.Caption != "" {
		params.Set("caption", v.Caption)
	}
	if v.ParseMode != "" {
		params.Set("parse_mode", v.ParseMode)
	}
	if v.DisableNotification {
		params.Set("disable_notification", "true")
	}
	if v.ReplyToMessageID != 0 {
		params.Set("reply_to_message_id", strconv.Itoa(v.ReplyToMessageID))
	}
	if v.ReplyMarkup != nil {
		markupJSON, err := jsonMarshal(v.ReplyMarkup)
		if err != nil {
			return nil, fmt.Errorf("marshal reply_markup: %w", err)
		}
		params.Set("reply_markup", markupJSON)
	}
	return params, nil
}

func (v VideoConfig) files() map[string]FileReader {
	if v.FileReader.Reader != nil {
		return map[string]FileReader{"video": v.FileReader}
	}
	if strings.HasPrefix(v.File, "file://") || strings.HasPrefix(v.File, "/") {
		path := strings.TrimPrefix(v.File, "file://")
		if fi, err := os.Stat(path); err != nil || fi.Size() > maxUploadSize {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = path
		}
		f, err := os.Open(resolved)
		if err != nil {
			return nil
		}
		return map[string]FileReader{"video": {Name: filepath.Base(path), Reader: f}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// AnimationConfig
// ---------------------------------------------------------------------------

type AnimationConfig struct {
	ChatID              int64
	File                string
	FileReader          FileReader
	Caption             string
	ParseMode           string
	DisableNotification bool
	ReplyToMessageID    int
	ReplyMarkup         *InlineKeyboardMarkup
}

func NewAnimation(chatID int64, file interface{}) AnimationConfig {
	c := AnimationConfig{ChatID: chatID}
	switch f := file.(type) {
	case string:
		c.File = f
	case FileReader:
		c.FileReader = f
	case UploadPath:
		c.File = string(f)
	}
	return c
}

func (a AnimationConfig) method() string { return "sendAnimation" }

func (a AnimationConfig) params() (url.Values, error) {
	params := url.Values{}
	params.Set("chat_id", strconv.FormatInt(a.ChatID, 10))
	if a.File != "" && a.FileReader.Reader == nil {
		params.Set("animation", a.File)
	}
	if a.Caption != "" {
		params.Set("caption", a.Caption)
	}
	if a.ParseMode != "" {
		params.Set("parse_mode", a.ParseMode)
	}
	if a.DisableNotification {
		params.Set("disable_notification", "true")
	}
	if a.ReplyToMessageID != 0 {
		params.Set("reply_to_message_id", strconv.Itoa(a.ReplyToMessageID))
	}
	if a.ReplyMarkup != nil {
		markupJSON, err := jsonMarshal(a.ReplyMarkup)
		if err != nil {
			return nil, fmt.Errorf("marshal reply_markup: %w", err)
		}
		params.Set("reply_markup", markupJSON)
	}
	return params, nil
}

func (a AnimationConfig) files() map[string]FileReader {
	if a.FileReader.Reader != nil {
		return map[string]FileReader{"animation": a.FileReader}
	}
	if strings.HasPrefix(a.File, "file://") || strings.HasPrefix(a.File, "/") {
		path := strings.TrimPrefix(a.File, "file://")
		if fi, err := os.Stat(path); err != nil || fi.Size() > maxUploadSize {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = path
		}
		f, err := os.Open(resolved)
		if err != nil {
			return nil
		}
		return map[string]FileReader{"animation": {Name: filepath.Base(path), Reader: f}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// AudioConfig
// ---------------------------------------------------------------------------

type AudioConfig struct {
	ChatID              int64
	File                string
	FileReader          FileReader
	Caption             string
	ParseMode           string
	DisableNotification bool
	ReplyToMessageID    int
	ReplyMarkup         *InlineKeyboardMarkup
}

func NewAudio(chatID int64, file interface{}) AudioConfig {
	c := AudioConfig{ChatID: chatID}
	switch f := file.(type) {
	case string:
		c.File = f
	case FileReader:
		c.FileReader = f
	case UploadPath:
		c.File = string(f)
	}
	return c
}

func (a AudioConfig) method() string { return "sendAudio" }

func (a AudioConfig) params() (url.Values, error) {
	params := url.Values{}
	params.Set("chat_id", strconv.FormatInt(a.ChatID, 10))
	if a.File != "" && a.FileReader.Reader == nil {
		params.Set("audio", a.File)
	}
	if a.Caption != "" {
		params.Set("caption", a.Caption)
	}
	if a.ParseMode != "" {
		params.Set("parse_mode", a.ParseMode)
	}
	if a.DisableNotification {
		params.Set("disable_notification", "true")
	}
	if a.ReplyToMessageID != 0 {
		params.Set("reply_to_message_id", strconv.Itoa(a.ReplyToMessageID))
	}
	if a.ReplyMarkup != nil {
		markupJSON, err := jsonMarshal(a.ReplyMarkup)
		if err != nil {
			return nil, fmt.Errorf("marshal reply_markup: %w", err)
		}
		params.Set("reply_markup", markupJSON)
	}
	return params, nil
}

func (a AudioConfig) files() map[string]FileReader {
	if a.FileReader.Reader != nil {
		return map[string]FileReader{"audio": a.FileReader}
	}
	if strings.HasPrefix(a.File, "file://") || strings.HasPrefix(a.File, "/") {
		path := strings.TrimPrefix(a.File, "file://")
		if fi, err := os.Stat(path); err != nil || fi.Size() > maxUploadSize {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = path
		}
		f, err := os.Open(resolved)
		if err != nil {
			return nil
		}
		return map[string]FileReader{"audio": {Name: filepath.Base(path), Reader: f}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// DocumentConfig
// ---------------------------------------------------------------------------

type DocumentConfig struct {
	ChatID              int64
	File                string
	FileReader          FileReader
	Caption             string
	ParseMode           string
	DisableNotification bool
	ReplyToMessageID    int
	ReplyMarkup         *InlineKeyboardMarkup
}

func NewDocument(chatID int64, file interface{}) DocumentConfig {
	c := DocumentConfig{ChatID: chatID}
	switch f := file.(type) {
	case string:
		c.File = f
	case FileReader:
		c.FileReader = f
	case UploadPath:
		c.File = string(f)
	}
	return c
}

func (d DocumentConfig) method() string { return "sendDocument" }

func (d DocumentConfig) params() (url.Values, error) {
	params := url.Values{}
	params.Set("chat_id", strconv.FormatInt(d.ChatID, 10))
	if d.File != "" && d.FileReader.Reader == nil {
		params.Set("document", d.File)
	}
	if d.Caption != "" {
		params.Set("caption", d.Caption)
	}
	if d.ParseMode != "" {
		params.Set("parse_mode", d.ParseMode)
	}
	if d.DisableNotification {
		params.Set("disable_notification", "true")
	}
	if d.ReplyToMessageID != 0 {
		params.Set("reply_to_message_id", strconv.Itoa(d.ReplyToMessageID))
	}
	if d.ReplyMarkup != nil {
		markupJSON, err := jsonMarshal(d.ReplyMarkup)
		if err != nil {
			return nil, fmt.Errorf("marshal reply_markup: %w", err)
		}
		params.Set("reply_markup", markupJSON)
	}
	return params, nil
}

func (d DocumentConfig) files() map[string]FileReader {
	if d.FileReader.Reader != nil {
		return map[string]FileReader{"document": d.FileReader}
	}
	if strings.HasPrefix(d.File, "file://") || strings.HasPrefix(d.File, "/") {
		path := strings.TrimPrefix(d.File, "file://")
		if fi, err := os.Stat(path); err != nil || fi.Size() > maxUploadSize {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = path
		}
		f, err := os.Open(resolved)
		if err != nil {
			return nil
		}
		return map[string]FileReader{"document": {Name: filepath.Base(path), Reader: f}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// InputMedia types for MediaGroup
// ---------------------------------------------------------------------------

type InputMediaPhoto struct {
	Type  string `json:"type"`
	Media string `json:"media"`
}

func NewInputMediaPhoto(file interface{}) InputMediaPhoto {
	media := ""
	switch f := file.(type) {
	case string:
		media = f
	case FileReader:
		media = f.Name
	case UploadPath:
		media = string(f)
	}
	return InputMediaPhoto{Type: "photo", Media: media}
}

type InputMediaVideo struct {
	Type  string `json:"type"`
	Media string `json:"media"`
}

func NewInputMediaVideo(file interface{}) InputMediaVideo {
	media := ""
	switch f := file.(type) {
	case string:
		media = f
	case FileReader:
		media = f.Name
	case UploadPath:
		media = string(f)
	}
	return InputMediaVideo{Type: "video", Media: media}
}

// ---------------------------------------------------------------------------
// MediaGroupConfig
// ---------------------------------------------------------------------------

type MediaGroupConfig struct {
	ChatID              int64
	Media               []interface{} // InputMediaPhoto or InputMediaVideo
	DisableNotification bool
	ReplyToMessageID    int
}

func NewMediaGroup(chatID int64, media []interface{}) MediaGroupConfig {
	return MediaGroupConfig{
		ChatID: chatID,
		Media:  media,
	}
}

func (m MediaGroupConfig) method() string { return "sendMediaGroup" }

func (m MediaGroupConfig) params() (url.Values, error) {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(m.ChatID, 10))
	mediaJSON, err := jsonMarshal(m.Media)
	if err != nil {
		return nil, fmt.Errorf("marshal media: %w", err)
	}
	v.Set("media", mediaJSON)
	if m.DisableNotification {
		v.Set("disable_notification", "true")
	}
	if m.ReplyToMessageID != 0 {
		v.Set("reply_to_message_id", strconv.Itoa(m.ReplyToMessageID))
	}
	return v, nil
}

func (m MediaGroupConfig) files() map[string]FileReader { return nil }

// ---------------------------------------------------------------------------
// SetMyCommandsConfig
// ---------------------------------------------------------------------------

type SetMyCommandsConfig struct {
	Commands []BotCommand
}

func NewSetMyCommands(commands ...BotCommand) SetMyCommandsConfig {
	return SetMyCommandsConfig{Commands: commands}
}

func (s SetMyCommandsConfig) method() string { return "setMyCommands" }

func (s SetMyCommandsConfig) params() (url.Values, error) {
	v := url.Values{}
	cmdsJSON, err := jsonMarshal(s.Commands)
	if err != nil {
		return nil, fmt.Errorf("marshal commands: %w", err)
	}
	v.Set("commands", cmdsJSON)
	return v, nil
}

func (s SetMyCommandsConfig) files() map[string]FileReader { return nil }

// ---------------------------------------------------------------------------
// Helper: jsonMarshal wraps json.Marshal, returns string
// ---------------------------------------------------------------------------

func jsonMarshal(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
