package ctxmode

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// Journal indexes session events for compaction resume (FTS5).
type Journal struct {
	db *sql.DB
}

func openJournal(dir string) (*Journal, error) {
	dsn := ":memory:"
	if dir != "" {
		dsn = filepath.Join(dir, "journal.db")
	}
	db, err := sql.Open("sqlite", dsn+"?_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	j := &Journal{db: db}
	if err := j.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return j, nil
}

func (j *Journal) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts INTEGER NOT NULL,
			kind TEXT NOT NULL,
			subject TEXT,
			detail TEXT
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
			subject, detail, content='events', content_rowid='id'
		)`,
		`CREATE TRIGGER IF NOT EXISTS events_ai AFTER INSERT ON events BEGIN
			INSERT INTO events_fts(rowid, subject, detail) VALUES (new.id, new.subject, new.detail);
		END`,
		`CREATE TRIGGER IF NOT EXISTS events_ad AFTER DELETE ON events BEGIN
			INSERT INTO events_fts(events_fts, rowid, subject, detail) VALUES('delete', old.id, old.subject, old.detail);
		END`,
		`CREATE TRIGGER IF NOT EXISTS events_au AFTER UPDATE ON events BEGIN
			INSERT INTO events_fts(events_fts, rowid, subject, detail) VALUES('delete', old.id, old.subject, old.detail);
			INSERT INTO events_fts(rowid, subject, detail) VALUES (new.id, new.subject, new.detail);
		END`,
	}
	for _, s := range stmts {
		if _, err := j.db.Exec(s); err != nil {
			return fmt.Errorf("journal migrate: %w", err)
		}
	}
	return nil
}

func (j *Journal) Close() {
	if j == nil || j.db == nil {
		return
	}
	_ = j.db.Close()
	j.db = nil
}

// Record appends one indexed event.
func (j *Journal) Record(kind, subject, detail string) {
	if j == nil || j.db == nil {
		return
	}
	subject = truncateField(subject, 400)
	detail = truncateField(detail, 800)
	if _, err := j.db.Exec(
		`INSERT INTO events(ts, kind, subject, detail) VALUES(?, ?, ?, ?)`,
		time.Now().Unix(), kind, subject, detail,
	); err != nil {
		LogJournalErr("record", err)
	}
}

// CompactGuidance returns FTS-backed facts to fold into the compaction summarizer prompt.
func (j *Journal) CompactGuidance(focus string, region []provider.Message) string {
	if j == nil || j.db == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Preserve these indexed session facts in the summary (paths and decisions verbatim):\n")
	n := 0
	for _, line := range j.recentEdits(12) {
		b.WriteString(line)
		b.WriteByte('\n')
		n++
	}
	for _, line := range j.search(focus, ftsTerms(region), 12) {
		b.WriteString(line)
		b.WriteByte('\n')
		n++
	}
	if n == 0 {
		return ""
	}
	return b.String()
}

// CompactResumeBlock returns a short post-compaction resume inserted after the summary.
func (j *Journal) CompactResumeBlock(focus string) string {
	if j == nil || j.db == nil {
		return ""
	}
	lines := j.search(focus, nil, 10)
	if len(lines) == 0 {
		lines = j.recentEdits(8)
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Resume context recovered from the session index (details may have been compacted away):\n")
	for _, ln := range lines {
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	return b.String()
}

func (j *Journal) recentEdits(limit int) []string {
	rows, err := j.db.Query(
		`SELECT kind, subject, detail FROM events
		 WHERE kind IN ('edit','write','git')
		 ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanEventLines(rows)
}

func (j *Journal) search(focus string, terms []string, limit int) []string {
	query := buildFTSQuery(focus, terms)
	if query == "" {
		return nil
	}
	rows, err := j.db.Query(
		`SELECT e.kind, e.subject, e.detail FROM events_fts f
		 JOIN events e ON e.id = f.rowid
		 WHERE events_fts MATCH ?
		 ORDER BY rank LIMIT ?`, query, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanEventLines(rows)
}

func scanEventLines(rows *sql.Rows) []string {
	var out []string
	for rows.Next() {
		var kind, subject, detail string
		if err := rows.Scan(&kind, &subject, &detail); err != nil {
			continue
		}
		line := fmt.Sprintf("- [%s]", kind)
		if subject != "" {
			line += " " + subject
		}
		if detail != "" {
			line += ": " + detail
		}
		out = append(out, line)
	}
	return out
}

func buildFTSQuery(focus string, terms []string) string {
	seen := map[string]bool{}
	var parts []string
	add := func(s string) {
		for _, tok := range tokenize(s) {
			if len(tok) < 3 || seen[tok] {
				continue
			}
			seen[tok] = true
			parts = append(parts, `"`+strings.ReplaceAll(tok, `"`, "")+`"`)
		}
	}
	add(focus)
	for _, t := range terms {
		add(t)
	}
	return strings.Join(parts, " OR ")
}

var pathLike = regexp.MustCompile(`[./][\w./_-]+`)

func ftsTerms(msgs []provider.Message) []string {
	var terms []string
	for i := len(msgs) - 1; i >= 0 && len(terms) < 20; i-- {
		m := msgs[i]
		if m.Role != provider.RoleUser && m.Role != provider.RoleAssistant {
			continue
		}
		for _, p := range pathLike.FindAllString(m.Content, 8) {
			terms = append(terms, p)
		}
		for _, tok := range tokenize(m.Content) {
			if len(tok) >= 4 {
				terms = append(terms, tok)
			}
		}
	}
	return terms
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	var tok []rune
	var out []string
	flush := func() {
		if len(tok) >= 3 {
			out = append(out, string(tok))
		}
		tok = tok[:0]
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == '/' {
			tok = append(tok, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func truncateField(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// RecordUserPrompt indexes the user's latest message.
func RecordUserPrompt(j *Journal, prompt string) {
	if j == nil {
		return
	}
	j.Record("user", "", truncateField(prompt, 500))
}

// RecordTool indexes a tool call outcome for compaction resume.
func RecordTool(j *Journal, toolName string, args json.RawMessage, result string, err error) {
	if j == nil {
		return
	}
	if err != nil {
		j.Record("error", toolName, truncateField(err.Error(), 300))
		return
	}
	switch toolName {
	case "read_file":
		var p struct{ Path string `json:"path"` }
		if json.Unmarshal(args, &p) == nil && p.Path != "" {
			j.Record("read", p.Path, "")
		}
	case "edit_file", "write_file", "multi_edit", "notebook_edit":
		var p struct {
			Path     string `json:"path"`
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal(args, &p) == nil {
			path := p.Path
			if path == "" {
				path = p.FilePath
			}
			if path != "" {
				j.Record("edit", path, truncateField(result, 200))
			}
		}
	case "grep":
		var p struct{ Pattern string `json:"pattern"` }
		if json.Unmarshal(args, &p) == nil && p.Pattern != "" {
			j.Record("grep", p.Pattern, "")
		}
	case "glob":
		var p struct{ Pattern string `json:"pattern"` }
		if json.Unmarshal(args, &p) == nil && p.Pattern != "" {
			j.Record("glob", p.Pattern, "")
		}
	case "ls":
		var p struct{ Path string `json:"path"` }
		if json.Unmarshal(args, &p) == nil {
			path := strings.TrimSpace(p.Path)
			if path == "" {
				path = "."
			}
			j.Record("ls", path, "")
		}
	case "ctx_read", "ctx_search":
		var p struct{ Ref string `json:"ref"` }
		if json.Unmarshal(args, &p) == nil && p.Ref != "" {
			j.Record("ctx", toolName, p.Ref)
		}
	case "bash":
		var p struct{ Command string `json:"command"` }
		if json.Unmarshal(args, &p) == nil {
			cmd := strings.TrimSpace(p.Command)
			if strings.HasPrefix(cmd, "git ") || cmd == "git" {
				j.Record("git", truncateField(cmd, 200), truncateField(result, 200))
			}
		}
	case "ctx_run":
		j.Record("ctx_run", "", truncateField(result, 200))
	default:
		if strings.HasPrefix(toolName, tool.MCPNamePrefix) {
			j.Record("mcp", toolName, truncateField(result, 200))
		}
	}
}