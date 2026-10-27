// Package hygiene runs repository hygiene checks.
package hygiene

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Result is the outcome of a single hygiene check.
type Result struct {
	ID      string
	Name    string
	Passed  bool
	Details string
}

// Runner executes ticked hygiene checks.
type Runner struct {
	coverageSummary string
}

// NewRunner creates a new hygiene runner.
func NewRunner(coverageSummary string) *Runner {
	return &Runner{coverageSummary: coverageSummary}
}

// Run executes the given checks and returns results.
func (r *Runner) Run(ctx context.Context, checks []Check) []Result {
	var results []Result
	for _, c := range checks {
		if !c.Ticked {
			continue
		}
		res := r.runOne(ctx, c)
		results = append(results, res)
	}
	return results
}

// Check is a single hygiene check definition.
type Check struct {
	ID     string
	Name   string
	Ticked bool
}

func (r *Runner) runOne(ctx context.Context, c Check) Result {
	switch c.ID {
	case "H001":
		return checkFileExists(c, "README.md")
	case "H002":
		return checkFileExists(c, ".github/CODEOWNERS")
	case "H003":
		return r.checkDependencies(ctx, c)
	case "H005":
		return r.checkCoverage(c)
	case "H006":
		if path, ok := findUpward(".devcontainer/devcontainer.json"); ok {
			return Result{ID: c.ID, Name: c.Name, Passed: true, Details: path + " exists"}
		}
		return Result{ID: c.ID, Name: c.Name, Passed: false, Details: ".devcontainer/devcontainer.json not found"}
	default:
		return Result{ID: c.ID, Name: c.Name, Passed: false, Details: "unknown check ID"}
	}
}

var pctPattern = regexp.MustCompile(`(\d+(?:\.\d+)?)%`)

const defaultCoverageThreshold = 95.0

// checkCoverage parses `go tool cover -func` output and checks the total meets
// the threshold extracted from the rule name (e.g. "above 80%"). Falls back to
// 95% if no percentage is found in the name.
func (r *Runner) checkCoverage(c Check) Result {
	threshold := defaultCoverageThreshold
	if m := pctPattern.FindStringSubmatch(c.Name); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			threshold = v
		}
	}

	if r.coverageSummary == "" {
		return Result{ID: c.ID, Name: c.Name, Passed: false, Details: "no coverage report provided — run CI with --coverage-file"}
	}
	for _, line := range strings.Split(r.coverageSummary, "\n") {
		if !strings.HasPrefix(line, "total:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			break
		}
		pct, err := strconv.ParseFloat(strings.TrimSuffix(fields[len(fields)-1], "%"), 64)
		if err != nil {
			return Result{ID: c.ID, Name: c.Name, Passed: false, Details: fmt.Sprintf("could not parse coverage total: %v", err)}
		}
		if pct >= threshold {
			return Result{ID: c.ID, Name: c.Name, Passed: true, Details: fmt.Sprintf("%.1f%% (threshold %.0f%%)", pct, threshold)}
		}
		return Result{ID: c.ID, Name: c.Name, Passed: false, Details: fmt.Sprintf("%.1f%% is below %.0f%% threshold", pct, threshold)}
	}
	return Result{ID: c.ID, Name: c.Name, Passed: false, Details: "no total line found in coverage report"}
}

func checkFileExists(c Check, path string) Result {
	if _, err := os.Stat(path); err != nil {
		return Result{ID: c.ID, Name: c.Name, Passed: false, Details: fmt.Sprintf("%s not found", path)}
	}
	return Result{ID: c.ID, Name: c.Name, Passed: true, Details: fmt.Sprintf("%s exists", path)}
}

// skipDirs are directory names that are never descended into during dependency walks.
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	".git":         true,
}

func (r *Runner) checkDependencies(ctx context.Context, c Check) Result {
	var goModDirs, pkgJSONDirs []string

	_ = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		switch d.Name() {
		case "go.mod":
			goModDirs = append(goModDirs, filepath.Dir(path))
		case "package.json":
			pkgJSONDirs = append(pkgJSONDirs, filepath.Dir(path))
		}
		return nil
	})

	if len(goModDirs) == 0 && len(pkgJSONDirs) == 0 {
		return Result{ID: c.ID, Name: c.Name, Passed: false, Details: "no go.mod or package.json found in repository"}
	}

	var failures []string
	for _, dir := range goModDirs {
		if res := r.checkGoDepsInDir(ctx, c, dir); !res.Passed {
			failures = append(failures, res.Details)
		}
	}
	for _, dir := range pkgJSONDirs {
		if res := r.checkNodeDepsInDir(ctx, c, dir); !res.Passed {
			failures = append(failures, res.Details)
		}
	}

	if len(failures) > 0 {
		return Result{ID: c.ID, Name: c.Name, Passed: false, Details: strings.Join(failures, "; ")}
	}
	return Result{ID: c.ID, Name: c.Name, Passed: true, Details: "all dependencies up to date"}
}

func (r *Runner) checkGoDepsInDir(ctx context.Context, c Check, dir string) Result {
	cmd := exec.CommandContext(ctx, "go", "list", "-u", "-m", "all")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return Result{ID: c.ID, Name: c.Name, Passed: false, Details: fmt.Sprintf("%s: go list failed: %v", dir, err)}
	}

	var outdated []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "go ") {
			continue
		}
		// Format: "module version [update]"
		if strings.Contains(line, "[") && strings.Contains(line, "]") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				outdated = append(outdated, parts[0])
			}
		}
	}

	if len(outdated) > 0 {
		return Result{
			ID:      c.ID,
			Name:    c.Name,
			Passed:  false,
			Details: fmt.Sprintf("%s: %d outdated Go modules: %s", dir, len(outdated), strings.Join(outdated, ", ")),
		}
	}
	return Result{ID: c.ID, Name: c.Name, Passed: true, Details: fmt.Sprintf("%s: all Go modules up to date", dir)}
}

// FormatReport returns a markdown report from results.
func FormatReport(results []Result) string {
	if len(results) == 0 {
		return "## PR Hygiene\n\nNo hygiene checks were ticked.\n"
	}
	var b strings.Builder
	b.WriteString("## PR Hygiene\n\n")
	b.WriteString("| Check | Status | Details |\n")
	b.WriteString("|-------|--------|---------|\n")
	for _, r := range results {
		status := "✅ Pass"
		if !r.Passed {
			status = "❌ Fail"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s |\n", r.ID+" "+r.Name, status, r.Details))
	}
	b.WriteString("\n<!-- wd-auto-review:type=hygiene -->\n")
	return b.String()
}

// FormatReportWithContext returns a markdown report that includes the project context sections.
func FormatReportWithContext(results []Result, context string) string {
	if context != "" {
		return "## Context\n\n" + context + "\n\n" + FormatReport(results)
	}
	return FormatReport(results)
}

// Helper to find if a path exists within a project root
func findUpward(name string) (string, bool) {
	dir, _ := os.Getwd()
	for {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}
