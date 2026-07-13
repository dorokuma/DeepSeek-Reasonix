// Package rtk integrates the RTK CLI for compact shell output in Reasonix.
package rtk

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mode controls how Reasonix applies RTK rewrites to bash commands.
type Mode int

const (
	ModeRewrite Mode = iota // default: transparent rewrite before execution
	ModeSuggest             // log would-be rewrites, run original command
	ModeOff                 // disable RTK integration
)

const (
	defaultRewriteTimeout = 3 * time.Second
	defaultReadLimitRTK   = 800
	readFileLimitDefault  = 200
)

var (
	binPath      string
	rewriteCache sync.Map
)

func init() {
	ensureBin()
}

func ensureBin() bool {
	if binPath != "" {
		return true
	}
	p, err := exec.LookPath("rtk")
	if err != nil {
		return false
	}
	binPath = p
	return true
}

// ModeFromEnv reads REASONIX_RTK: rewrite (default), suggest, or off.
func ModeFromEnv() Mode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REASONIX_RTK"))) {
	case "off", "0", "false", "no":
		return ModeOff
	case "suggest":
		return ModeSuggest
	default:
		return ModeRewrite
	}
}

// Available reports whether the rtk binary is on PATH.
func Available() bool { return ensureBin() }

// Active reports whether RTK should transparently rewrite tools and builtins.
func Active() bool {
	return Available() && ModeFromEnv() == ModeRewrite
}

func rewriteTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv("REASONIX_RTK_TIMEOUT"))
	if v == "" {
		return defaultRewriteTimeout
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return defaultRewriteTimeout
}

// ReadFileDefaultLimit returns the read_file line cap when limit is unset.
// Smaller under RTK rewrite mode to nudge paging; override with REASONIX_RTK_READ_LIMIT.
func ReadFileDefaultLimit() int {
	if !Active() {
		return readFileLimitDefault
	}
	if v := strings.TrimSpace(os.Getenv("REASONIX_RTK_READ_LIMIT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultReadLimitRTK
}

// Rewrite runs "rtk rewrite <cmd>" and returns a compact equivalent, or ""
// when RTK has no filter. Exit codes follow RTK semantics: 0 and 3 mean a
// rewrite is offered; 1 and 2 mean pass-through.
func Rewrite(cmd string) string {
	return rewriteWithMode(context.Background(), cmd, ModeRewrite)
}

func rewriteWithMode(ctx context.Context, cmd string, mode Mode) string {
	if !ensureBin() || mode == ModeOff {
		return ""
	}
	if DeclineRewrite(cmd) {
		rewriteCache.Store(cmd, "")
		if mode == ModeRewrite {
			LogMissBash(cmd, "side_effect_declined")
		}
		return ""
	}
	if v, ok := rewriteCache.Load(cmd); ok {
		s, _ := v.(string)
		if mode == ModeSuggest {
			if s != "" && s != cmd {
				slog.Debug("rtk suggest", "cmd", cmd, "rewritten", s)
			}
			return ""
		}
		return s
	}
	ctx2, cancel := context.WithTimeout(ctx, rewriteTimeout())
	defer cancel()

	c := exec.CommandContext(ctx2, binPath, "rewrite", cmd)
	out, err := c.Output()
	rewritten := strings.TrimSpace(string(out))
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			LogFail("rewrite", cmd, err)
			rewriteCache.Store(cmd, "")
			return ""
		}
		exitCode = exitErr.ExitCode()
	}
	result := acceptRewrite(exitCode, rewritten)
	rewriteCache.Store(cmd, result)
	if mode == ModeSuggest {
		if result != "" && result != cmd {
			slog.Debug("rtk suggest", "cmd", cmd, "rewritten", result)
		}
		return ""
	}
	if result != "" && result != cmd && mode == ModeRewrite {
		LogHit(cmd, result)
	}
	return result
}

// acceptRewrite maps RTK rewrite exit codes to a rewritten command or "".
// Codes 0 and 3 carry a rewrite on stdout; 1 and 2 mean no rewrite.
func acceptRewrite(exitCode int, stdout string) string {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return ""
	}
	switch exitCode {
	case 0, 3:
		return stdout
	default:
		return ""
	}
}

func ApplySegmentsCtx(ctx context.Context, cmd string) string {
	mode := ModeFromEnv()
	if !ensureBin() || mode == ModeOff {
		return cmd
	}
	if r := rewriteWithMode(ctx, cmd, mode); r != "" {
		return r
	}
	segs := splitShellPipeline(cmd)
	if len(segs) <= 1 {
		if mode == ModeRewrite {
			LogMissBash(cmd, "rewrite_declined")
		}
		return cmd
	}
	var out []string
	changed := false
	for _, s := range segs {
		if r := rewriteWithMode(ctx, s.text, mode); r != "" {
			out = append(out, r)
			changed = true
		} else {
			if mode == ModeRewrite {
				LogMissBash(s.text, "rewrite_declined")
			}
			out = append(out, s.text)
		}
	}
	if !changed {
		return cmd
	}
	var b strings.Builder
	b.WriteString(out[0])
	for i := 1; i < len(segs); i++ {
		b.WriteByte(' ')
		b.WriteString(segs[i].sep)
		b.WriteByte(' ')
		b.WriteString(out[i])
	}
	return b.String()
}

type segSep struct {
	text string
	sep  string
}

func splitShellPipeline(cmd string) []segSep {
	var segs []segSep
	start := 0
	quote := byte(0)
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if i+1 < len(cmd) {
			two := cmd[i : i+2]
			if two == "&&" || two == "||" {
				segs = append(segs, segSep{text: strings.TrimSpace(cmd[start:i]), sep: two})
				i++
				start = i + 1
				continue
			}
		}
		if c == ';' || c == '|' {
			segs = append(segs, segSep{text: strings.TrimSpace(cmd[start:i]), sep: string(c)})
			start = i + 1
			continue
		}
	}
	remain := strings.TrimSpace(cmd[start:])
	if remain != "" || len(segs) == 0 {
		segs = append(segs, segSep{text: remain, sep: ""})
	}
	return segs
}

// Probe collects RTK diagnostics for reasonix doctor.
type Probe struct {
	Mode      string            `json:"mode"`
	Path      string            `json:"path,omitempty"`
	Version   string            `json:"version,omitempty"`
	RewriteOK bool              `json:"rewrite_ok"`
	GrepOK    bool              `json:"grep_ok"`
	PipeOK    bool              `json:"pipe_ok"`
	ReadLimit int               `json:"read_limit,omitempty"`
	Timeout   string            `json:"timeout,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Sample    string            `json:"sample,omitempty"`
	Warning   string            `json:"warning,omitempty"`
}

// CollectProbe returns redacted RTK status for doctor reports.
func CollectProbe() Probe {
	mode := ModeFromEnv()
	p := Probe{Mode: mode.String()}
	if !Available() {
		p.Warning = "rtk not found on PATH — bash runs without output compaction"
		return p
	}
	p.Path = binPath
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, binPath, "--version").CombinedOutput(); err == nil {
		p.Version = strings.TrimSpace(string(out))
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), rewriteTimeout())
	defer cancel2()
	c := exec.CommandContext(ctx2, binPath, "rewrite", "git status")
	out, err := c.Output()
	rewritten := strings.TrimSpace(string(out))
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	if r := acceptRewrite(exitCode, rewritten); r != "" {
		p.RewriteOK = true
		p.Sample = "git status → " + r
	} else {
		p.Warning = "rtk rewrite smoke test failed (git status)"
	}
	p.ReadLimit = ReadFileDefaultLimit()
	p.Timeout = rewriteTimeout().String()
	p.Env = EnvSnapshot()
	if Active() {
		if _, err := RunShellIfRewritten(context.Background(), "", RipgrepShell("package", "."), "grep"); err == nil {
			p.GrepOK = true
			if p.Sample != "" {
				p.Sample += "; rg → rewrite gate"
			}
		}
		pipeIn := strings.Repeat("commit abc\nAuthor: x\nDate: 2024\n\n    msg\n\n", 40)
		if out, err := PipeCompact("git-log", pipeIn); err == nil && len(out) < len(pipeIn)/2 {
			p.PipeOK = true
			if p.Sample != "" {
				p.Sample += "; pipe git-log ok"
			}
		}
	}
	return p
}

func (m Mode) String() string {
	switch m {
	case ModeOff:
		return "off"
	case ModeSuggest:
		return "suggest"
	default:
		return "rewrite"
	}
}
