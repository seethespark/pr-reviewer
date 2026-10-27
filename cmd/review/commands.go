package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/seethespark/pr-reviewer/internal/config"
)

// commentSink is where review reports go: the GitHub API normally,
// stdout under --dry-run.
type commentSink interface {
	PostOrUpdate(ctx context.Context, pr int, marker string, body string) error
}

// stdoutSink prints comments instead of posting them (--dry-run).
type stdoutSink struct{}

func (stdoutSink) PostOrUpdate(_ context.Context, _ int, marker string, body string) error {
	fmt.Printf("\n═══════════ dry-run comment %s ═══════════\n\n%s\n", marker, body)
	return nil
}

// runCheckConfig implements "pr-reviewer check-config [--config path]":
// validate PR-REVIEW.md and describe what a review would run, so a repo can
// gate config changes in CI instead of discovering breakage on the next PR.
func runCheckConfig(args []string) int {
	fs := flag.NewFlagSet("check-config", flag.ExitOnError)
	path := fs.String("config", "PR-REVIEW.md", "Path to PR-REVIEW.md")
	fs.Parse(args)
	// Also accept a bare positional path: "pr-reviewer check-config docs/PR-REVIEW.md"
	if fs.NArg() > 0 {
		*path = fs.Arg(0)
	}

	cfg, err := config.Parse(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %s is invalid: %v\n", *path, err)
		return 1
	}

	fmt.Printf("✅ %s is valid\n\n", *path)
	fmt.Printf("Owner:             %s\n", firstLine(cfg.Owner))
	fmt.Printf("Production status: %s\n", firstLine(cfg.ProductionStatus))
	fmt.Printf("Security level:    %s\n", firstLine(cfg.SecurityLevel))

	ticked := 0
	for _, h := range cfg.Hygiene {
		if h.Ticked {
			ticked++
		}
	}
	fmt.Printf("Hygiene checks:    %d configured, %d ticked (will run)\n", len(cfg.Hygiene), ticked)

	if len(cfg.Agents) == 0 {
		fmt.Println("Review agents:     none — no AI review will run")
	} else {
		fmt.Printf("Review agents:     %d\n", len(cfg.Agents))
		for _, a := range cfg.Agents {
			extra := ""
			if a.Additional != "" {
				extra = " — " + a.Additional
			}
			fmt.Printf("  - %s/%s%s\n", a.Plugin, a.Subagent, extra)
			// Scoping decides what this agent is never shown, so it is worth
			// printing plainly: a typo'd pattern is otherwise invisible until
			// a PR comes back reviewed by fewer eyes than intended.
			include, exclude := a.Scope()
			switch {
			case len(include) == 0 && len(exclude) == 0:
				fmt.Printf("      scope: the whole diff\n")
			default:
				scope := "the whole diff"
				if len(include) > 0 {
					scope = strings.Join(include, ", ")
				}
				if len(exclude) > 0 {
					scope += " (excluding " + strings.Join(exclude, ", ") + ")"
				}
				fmt.Printf("      scope: %s\n", scope)
			}
		}
	}

	if len(cfg.ExcludedPaths) > 0 {
		fmt.Printf("\nExcluded from every agent: %s\n", strings.Join(cfg.ExcludedPaths, ", "))
	}
	return 0
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
