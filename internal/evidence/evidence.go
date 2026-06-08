// Package evidence provides a per-turn ledger of tool call receipts.
// This is a minimal stub with all enforcement removed — all methods return
// permissive defaults (true / empty / nil) so callers can compile and run
// without the enforcement gates.
package evidence

import (
	"context"
	"encoding/json"
)

type TodoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
	Level      int    `json:"level,omitempty"`
}

type TodoStepMatch struct {
	Found      bool
	Index      int
	Content    string
	Status     string
	ActiveForm string
}

type Receipt struct {
	ToolName     string          `json:"tool_name"`
	Args         json.RawMessage `json:"args,omitempty"`
	Success      bool            `json:"success"`
	Command      string          `json:"command,omitempty"`
	Step         string          `json:"step,omitempty"`
	TodoStep     *TodoStepMatch  `json:"todo_step,omitempty"`
	Paths        []string        `json:"paths,omitempty"`
	Read         bool            `json:"read,omitempty"`
	Write        bool            `json:"write,omitempty"`
	Todos        []TodoItem      `json:"todos,omitempty"`
	ErrorPreview string          `json:"error_preview,omitempty"`
}

type WriteFailure struct {
	Path         string
	Tool         string
	ErrorPreview string
}

type Ledger struct{}

func NewLedger() *Ledger { return &Ledger{} }

func (l *Ledger) Reset()                                     {}
func (l *Ledger) Record(r Receipt)                           {}
func (l *Ledger) HasSuccessfulCommand(cmd string) bool       { return true }
func (l *Ledger) HasSuccessfulCommandAfter(cmd string, after int) bool { return true }
func (l *Ledger) HasSuccessfulWrite(paths []string) bool     { return true }
func (l *Ledger) HasSuccessfulReadOrWrite(paths []string) bool { return true }
func (l *Ledger) HasSuccessfulTodoWrite() bool               { return false }
func (l *Ledger) HasSuccessfulCompleteStepAfter(after int) bool { return true }
func (l *Ledger) LatestSuccessfulWriterIndex() (int, bool)   { return 0, false }
func (l *Ledger) LatestSuccessfulWriteIndex(paths []string) (int, bool) { return 0, false }
func (l *Ledger) IncompleteLatestTodos() ([]TodoStepMatch, bool) { return nil, false }
func (l *Ledger) UnresolvedWriteFailures() []WriteFailure   { return nil }
func (l *Ledger) UnverifiedCompletedTodos(current []TodoItem) ([]TodoStepMatch, bool) { return nil, false }
func (l *Ledger) MatchLatestTodoStep(step string) (TodoStepMatch, bool) { return TodoStepMatch{}, false }

func ReceiptFromToolCall(toolName string, args json.RawMessage, success bool, readOnly bool) Receipt {
	return Receipt{ToolName: toolName, Args: args, Success: success}
}

func ErrorPreviewFromToolOutput(output, errMsg string) string { return "" }

func WithLedger(ctx context.Context, ledger *Ledger) context.Context { return ctx }
func FromContext(ctx context.Context) (*Ledger, bool)               { return nil, false }

type ReadinessAudit struct {
	Result                   ReadinessAuditResult
	Recovered                bool
	MissingCompleteStep      bool
	IncompleteTodoBatches    int
	MissingProjectChecks     int
	CommandMismatchMissing   int
	IncompleteTodos          int
}

type ReadinessAuditResult int

const (
	ReadinessAllowed ReadinessAuditResult = iota
	ReadinessBlocked
	ReadinessErrored
)
