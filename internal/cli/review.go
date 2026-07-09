package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/event"
	"reasonix/internal/tool"
)

// reviewSystemPrompt is the persona for `reasonix review` (standalone CLI).
// Subagent skills were removed — this is not loaded from the skills store.
const reviewSystemPrompt = `You are running as a code-review subagent. Inspect the changes the user is about to ship — usually the current git branch vs its upstream — and produce a focused review the parent can hand back.

How to operate:
- Default scope: the current branch's diff vs the default branch. If the task names a specific commit range or files, honor that instead.
- Discover scope first: bash git status, git diff --stat, git log --oneline. Then git diff (or git diff <base>...HEAD) for the hunks.
- Read touched files (read_file) when the diff alone lacks context — signatures, surrounding invariants, callers.
- For "any callers depending on this?" questions: use grep to find references BEFORE asserting impact.
- Stay read-only. Never commit, never write files, never propose edits as applied changes.
- Cap yourself at ~12 tool calls. If the diff is too big, pick the riskiest 2-3 files and say so.

What to look for, in priority order:
1. Correctness bugs — off-by-one, nil handling, races, wrong operator, unhandled edge cases.
2. Security — injection, secrets, missing authz, unsafe deserialization.
3. Behavior changes the diff hides — renames missing callers, removed load-bearing branches.
4. Tests — does the change have tests for the new behavior?
5. Style + consistency — only flag deviations that matter.

Your final answer:
- Lead with a one-sentence verdict: "ship as-is" / "minor nits, OK to ship after" / "blocking issues, do not ship".
- Then a short bulleted list, each with file:line + the problem in one sentence + what to change.
- Group by severity if more than 4 items: Blocking, Should-fix, Nits.
- If everything looks clean, say so plainly. Don't manufacture concerns.
Keep the final answer compact.`

func reviewCommand(args []string) int {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	base := fs.String("base", "", "base branch/commit to diff against (defaults to HEAD — reviews uncommitted working-tree changes)")
	commit := fs.String("commit", "", "review a specific commit (shows changes introduced by that commit)")
	model := fs.String("model", "", "provider name override (default: config default_model)")
	instructions := fs.String("instructions", "", "extra review instructions appended to the prompt")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// 1. Get the diff.
	diff, err := getReviewDiff(*base, *commit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if diff == "" {
		fmt.Println("No changes to review.")
		return 0
	}

	// 2. Load config and resolve model.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: failed to load config:", err)
		return 1
	}
	modelName := *model
	if modelName == "" {
		modelName = cfg.DefaultModel
	}
	entry, ok := cfg.ResolveModel(modelName)
	if !ok {
		fmt.Fprintf(os.Stderr, "error: unknown model %q — check your config\n", modelName)
		return 1
	}
	if err := cfg.Validate(modelName); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	// 3. Create provider.
	prov, err := boot.NewProviderWithProxy(entry, cfg.NetworkProxySpec())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: failed to create provider:", err)
		return 1
	}

	// 4. Build tool registry via FilterRegistry (allows all built-in tools minus meta-tools).
	reg := tool.NewRegistry()
	for _, t := range tool.Builtins() {
		reg.Add(t)
	}
	reg = agent.FilterRegistry(reg, nil, agent.SubagentMetaTools()...)

	// 5. Prepare the review prompt (no subagent skill — freeform task persona).
	task := buildReviewTask(diff, *instructions)

	// 6. Run a one-shot review sub-agent with an embedded persona.
	ctx := context.Background()
	result, err := agent.RunSubAgent(ctx, prov, reg, reviewSystemPrompt, task, agent.Options{
		MaxSteps:      12,
		Temperature:   cfg.Agent.Temperature,
		Pricing:       entry.Price,
		ContextWindow: entry.ContextWindow,
	}, event.Discard)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: review failed:", err)
		return 1
	}

	fmt.Print(result)
	return 0
}

// getReviewDiff runs the appropriate git diff command and returns its output.
// - commit="abc": shows diff of abc^..abc
// - base="main": shows diff of main...HEAD
// - neither: shows diff of uncommitted working-tree changes
func getReviewDiff(base, commit string) (string, error) {
	cwd, _ := os.Getwd()
	ctx := context.Background()
	switch {
	case commit != "":
		return runGit(ctx, cwd, "diff", commit+"^.."+commit)
	case base != "":
		return runGit(ctx, cwd, "diff", base+"...HEAD")
	default:
		// Working tree changes: staged + unstaged.
		out, err := runGit(ctx, cwd, "diff", "HEAD")
		if err != nil {
			return "", err
		}
		if out == "" {
			// No working-tree changes; check for staged-only.
			out, err = runGit(ctx, cwd, "diff", "--cached")
		}
		return out, err
	}
}

func buildReviewTask(diff string, extra string) string {
	var b strings.Builder
	b.WriteString("Review the following changes. ")
	if extra != "" {
		b.WriteString(extra)
		b.WriteString(" ")
	}
	b.WriteString("The diff is:\n\n```diff\n")
	// Truncate huge diffs to protect the review subagent's context budget.
	const maxLen = 16000
	if len(diff) > maxLen {
		b.WriteString(diff[:maxLen])
		b.WriteString("\n```\n\n(diff truncated at ")
		fmt.Fprint(&b, maxLen)
		b.WriteString(" chars — focus on the changes shown)")
	} else {
		b.WriteString(diff)
		b.WriteString("\n```")
	}
	return b.String()
}
