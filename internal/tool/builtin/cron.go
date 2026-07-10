package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/robfig/cron/v3"
	"reasonix/internal/tool"
)

func init() {
	tool.RegisterBuiltin(scheduleTask{})
	tool.RegisterBuiltin(listScheduledTasks{})
	tool.RegisterBuiltin(deleteScheduledTask{})
}

type CronTask struct {
	ID      int64  `json:"id"`
	ChatID  int64  `json:"chat_id"`
	Spec    string `json:"spec"`
	Prompt  string `json:"prompt"`
	RunOnce bool   `json:"run_once,omitempty"`
}

// userConfigRoot returns ~/.config/reasonix (or $XDG_CONFIG_HOME/reasonix).
// Kept local so builtin does not import config (avoids reverse dependency).
func userConfigRoot() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "reasonix")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "reasonix")
}

func getCronTasksPath() (string, error) {
	path := os.Getenv("REASONIX_CRON_TASKS_PATH")

	// Default: store inside the user config directory
	base := userConfigRoot()
	if base == "" {
		return "", fmt.Errorf("cannot determine user config directory")
	}

	if path == "" {
		return filepath.Join(base, "cron_tasks.json"), nil
	}

	// Clean and validate the user-supplied path
	path = filepath.Clean(path)

	// Reject absolute paths (they bypass the allowed base directory)
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("REASONIX_CRON_TASKS_PATH must be a relative path, got absolute: %s", path)
	}

	fullPath := filepath.Join(base, path)

	// Prevent directory traversal: verify the resolved path still lives under base
	baseClean := filepath.Clean(base)
	if !strings.HasPrefix(fullPath, baseClean+string(filepath.Separator)) && fullPath != baseClean {
		return "", fmt.Errorf("REASONIX_CRON_TASKS_PATH escapes allowed directory")
	}

	return fullPath, nil
}

func getChatID() (int64, error) {
	cidStr := os.Getenv("REASONIX_CHAT_ID")
	if cidStr == "" {
		return 0, fmt.Errorf("REASONIX_CHAT_ID environment variable is not set")
	}

	// Length limit to prevent abuse
	if len(cidStr) > 32 {
		return 0, fmt.Errorf("REASONIX_CHAT_ID too long: %d characters (max 32)", len(cidStr))
	}

	// Character whitelist: only digits and optional leading minus sign
	for i, c := range cidStr {
		if i == 0 && c == '-' {
			continue
		}
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid REASONIX_CHAT_ID %q: only digits and optional leading minus allowed", cidStr)
		}
	}

	cid, err := strconv.ParseInt(cidStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid REASONIX_CHAT_ID %q: %w", cidStr, err)
	}
	return cid, nil
}

func readTasks(path string, exLock bool) ([]*CronTask, *os.File, error) {
	var flag int
	if exLock {
		flag = os.O_RDWR | os.O_CREATE
	} else {
		flag = os.O_RDONLY
	}

	f, err := os.OpenFile(path, flag, 0644)
	if err != nil {
		return nil, nil, err
	}

	var lockType int
	if exLock {
		lockType = syscall.LOCK_EX
	} else {
		lockType = syscall.LOCK_SH
	}

	if err := syscall.Flock(int(f.Fd()), lockType); err != nil {
		f.Close()
		f = nil
		return nil, nil, fmt.Errorf("flock failed: %w", err)
	}

	fi, err := f.Stat()
	if err != nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, nil, err
	}

	if fi.Size() == 0 {
		return []*CronTask{}, f, nil
	}

	data := make([]byte, fi.Size())
	_, err = f.ReadAt(data, 0)
	if err != nil && err != io.EOF {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, nil, err
	}

	var tasks []*CronTask
	if err := json.Unmarshal(data, &tasks); err != nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, nil, fmt.Errorf("unmarshal json failed: %w", err)
	}

	return tasks, f, nil
}

func writeTasksAndClose(f *os.File, tasks []*CronTask) error {
	defer func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}()

	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json failed: %w", err)
	}

	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate file failed: %w", err)
	}

	if _, err := f.WriteAt(data, 0); err != nil {
		return fmt.Errorf("write file failed: %w", err)
	}

	return nil
}

func closeFile(f *os.File) {
	if f != nil {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			slog.Warn("cron: closeFile flock unlock failed", "err", err)
		}
		if err := f.Close(); err != nil {
			slog.Warn("cron: closeFile Close failed", "err", err)
		}
	}
}

// scheduleTask
type scheduleTask struct{}

func (scheduleTask) Name() string { return "schedule_task" }
func (scheduleTask) Description() string {
	return "Schedule a recurring prompt execution using a cron expression. The cron_expression format should be 'minute hour day_of_month month day_of_week' (5 fields). The prompt is the task instruction."
}
func (scheduleTask) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"cron_expression": {
				"type": "string",
				"description": "Standard cron expression with 5 fields, e.g. '*/5 * * * *'"
			},
			"prompt": {
				"type": "string",
				"description": "The instruction/prompt to be executed"
			},
			"run_once": {
				"type": "boolean",
				"description": "When true, the task is automatically deleted after its first execution."
			}
		},
		"required": ["cron_expression", "prompt"]
	}`)
}
func (scheduleTask) ReadOnly() bool { return false }

func (scheduleTask) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		CronExpression string `json:"cron_expression"`
		Prompt         string `json:"prompt"`
		RunOnce        bool   `json:"run_once"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if params.CronExpression == "" || params.Prompt == "" {
		return "", fmt.Errorf("both cron_expression and prompt are required")
	}

	// Validate cron expression format (5 fields)
	if _, err := cron.ParseStandard(params.CronExpression); err != nil {
		return "", fmt.Errorf("invalid cron expression %q: %w", params.CronExpression, err)
	}

	path, err := getCronTasksPath()
	if err != nil {
		return "", err
	}
	chatID, err := getChatID()
	if err != nil {
		return "", err
	}

	tasks, f, err := readTasks(path, true)
	if err != nil {
		return "", fmt.Errorf("read tasks failed: %w", err)
	}

	var maxID int64 = 0
	for _, t := range tasks {
		if t.ID > maxID {
			maxID = t.ID
		}
	}
	newID := maxID + 1

	newTask := &CronTask{
		ID:      newID,
		ChatID:  chatID,
		Spec:    params.CronExpression,
		Prompt:  params.Prompt,
		RunOnce: params.RunOnce,
	}
	tasks = append(tasks, newTask)

	if err := writeTasksAndClose(f, tasks); err != nil {
		return "", fmt.Errorf("write tasks failed: %w", err)
	}

	return fmt.Sprintf("Successfully scheduled task ID %d with expression %q", newID, params.CronExpression), nil
}

// listScheduledTasks
type listScheduledTasks struct{}

func (listScheduledTasks) Name() string { return "list_scheduled_tasks" }
func (listScheduledTasks) Description() string {
	return "List all currently scheduled cron tasks."
}
func (listScheduledTasks) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (listScheduledTasks) ReadOnly() bool { return true }

func (listScheduledTasks) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	path, err := getCronTasksPath()
	if err != nil {
		return "", err
	}

	tasks, f, err := readTasks(path, false)
	if err != nil {
		if os.IsNotExist(err) {
			return "No scheduled tasks found.", nil
		}
		return "", fmt.Errorf("read tasks failed: %w", err)
	}
	closeFile(f)

	if len(tasks) == 0 {
		return "No scheduled tasks found.", nil
	}

	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal tasks failed: %w", err)
	}
	return string(data), nil
}

// deleteScheduledTask
type deleteScheduledTask struct{}

func (deleteScheduledTask) Name() string { return "delete_scheduled_task" }
func (deleteScheduledTask) Description() string {
	return "Delete a scheduled cron task by its ID."
}
func (deleteScheduledTask) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"task_id": {
				"type": "integer",
				"description": "The ID of the scheduled task to delete"
			}
		},
		"required": ["task_id"]
	}`)
}
func (deleteScheduledTask) ReadOnly() bool { return false }

func (deleteScheduledTask) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}

	path, err := getCronTasksPath()
	if err != nil {
		return "", err
	}

	tasks, f, err := readTasks(path, true)
	if err != nil {
		return "", fmt.Errorf("read tasks failed: %w", err)
	}

	found := false
	var updatedTasks []*CronTask
	for _, t := range tasks {
		if t.ID == params.TaskID {
			found = true
			continue
		}
		updatedTasks = append(updatedTasks, t)
	}

	if !found {
		closeFile(f)
		return "", fmt.Errorf("task ID %d not found", params.TaskID)
	}

	if err := writeTasksAndClose(f, updatedTasks); err != nil {
		return "", fmt.Errorf("write tasks failed: %w", err)
	}

	return fmt.Sprintf("Successfully deleted task ID %d", params.TaskID), nil
}
