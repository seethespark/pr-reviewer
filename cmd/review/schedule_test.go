package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/seethespark/pr-reviewer/internal/config"
	"github.com/seethespark/pr-reviewer/internal/diff"
	"github.com/seethespark/pr-reviewer/internal/review"
)

func TestNewPromptBudget(t *testing.T) {
	// Kimi K2.6: (262144 - 8192) tokens × 3 bytes ≈ 762KB of prompt. The diff
	// chunk should land on the ceiling, not consume 60% of that.
	big := newPromptBudget(762 * 1024)
	if big.chunk != diff.MaxChunkSize {
		t.Errorf("large window: chunk = %d, want the %d ceiling", big.chunk, diff.MaxChunkSize)
	}
	if big.fileContext != maxFileContextBytes {
		t.Errorf("large window: fileContext = %d, want %d", big.fileContext, maxFileContextBytes)
	}

	// A small-window model must get proportionally smaller sections rather
	// than a prompt the server will quietly truncate.
	small := newPromptBudget(80 * 1024)
	if small.chunk >= diff.MaxChunkSize {
		t.Errorf("small window: chunk = %d, expected scaling below the ceiling", small.chunk)
	}
	if small.chunk+small.fileContext > 80*1024 {
		t.Errorf("small window: sections total %d, over the %d budget",
			small.chunk+small.fileContext, 80*1024)
	}

	// Never below the floor, however tiny the window.
	tiny := newPromptBudget(1024)
	if tiny.chunk < diff.MinChunkSize {
		t.Errorf("tiny window: chunk = %d, want at least the %d floor", tiny.chunk, diff.MinChunkSize)
	}
}

func TestPartitionGating(t *testing.T) {
	findings := []review.Finding{
		{Title: "a", Severity: "critical"},
		{Title: "b", Severity: "medium"},
		{Title: "c", Severity: "high"},
		{Title: "d", Severity: "low"},
	}
	gating, advisory := partitionGating(findings)

	if len(gating) != 2 || gating[0].Title != "a" || gating[1].Title != "c" {
		t.Errorf("gating = %+v, want the critical and high findings", gating)
	}
	if len(advisory) != 2 || advisory[0].Title != "b" || advisory[1].Title != "d" {
		t.Errorf("advisory = %+v, want the medium and low findings", advisory)
	}
}

func TestAgentOutcomeMerge(t *testing.T) {
	o := agentOutcome{done: map[int]bool{}}

	o.merge(0, chunkResult{
		findings: []review.Finding{{File: "a.go", Line: 1, Severity: "high", Title: "x"}},
		notes:    "note one",
	})
	// A duplicate from an overlapping chunk must collapse in finalize.
	o.merge(2, chunkResult{
		findings: []review.Finding{
			{File: "a.go", Line: 1, Severity: "high", Title: "x"},
			{File: "b.go", Line: 9, Severity: "low", Title: "y"},
		},
		legacy: review.SeveritySummary{Medium: 3},
	})
	// An errored chunk contributes nothing and is not marked done.
	o.merge(1, chunkResult{err: errors.New("provider exploded")})

	o.finalize(true)

	if len(o.findings) != 2 {
		t.Errorf("findings = %d, want 2 after dedupe", len(o.findings))
	}
	if o.summary.High != 1 || o.summary.Low != 1 {
		t.Errorf("summary = %+v, want High:1 Low:1 from structured findings", o.summary)
	}
	if o.summary.Medium != 3 {
		t.Errorf("summary.Medium = %d, want 3 carried from the legacy block", o.summary.Medium)
	}
	// Counted after dedupe: the one surviving low finding, not the raw
	// per-chunk total, so the note matches what the report shows.
	if o.unverifiedFindings != 1 {
		t.Errorf("unverifiedFindings = %d, want 1 (the deduped low finding)", o.unverifiedFindings)
	}
	if o.done[1] {
		t.Error("an errored chunk must not count as reviewed")
	}
	if o.err == nil {
		t.Error("the chunk error must be retained for reporting")
	}
}

func TestAgentOutcomeUnreviewedFiles(t *testing.T) {
	chunks := []string{
		"diff --git a/one.go b/one.go\n+x\n",
		"diff --git a/two.go b/two.go\n+y\n",
		// Same file split across two chunks by hunk: listed once.
		"diff --git a/two.go b/two.go\n+z\n",
	}
	o := agentOutcome{done: map[int]bool{0: true}}

	got := o.unreviewedFiles(chunks)
	if len(got) != 1 || got[0] != "two.go" {
		t.Errorf("unreviewedFiles = %v, want [two.go] deduped", got)
	}

	o.done[1], o.done[2] = true, true
	if got := o.unreviewedFiles(chunks); len(got) != 0 {
		t.Errorf("unreviewedFiles = %v, want empty when every chunk completed", got)
	}
}

func TestFormatCoverageNote(t *testing.T) {
	if got := formatCoverageNote(nil, false); got != "" {
		t.Errorf("full coverage must produce no note, got %q", got)
	}

	note := formatCoverageNote([]string{"a.go", "b.go"}, true)
	if !strings.Contains(note, "Incomplete coverage") || !strings.Contains(note, "a.go") {
		t.Errorf("note must name the unreviewed files: %q", note)
	}
	if !strings.Contains(note, "ran out of time") {
		t.Errorf("timeout must be given as the cause: %q", note)
	}

	// Long lists are capped so a pathological PR can't produce a wall of text.
	many := make([]string, 100)
	for i := range many {
		many[i] = "f.go"
	}
	long := formatCoverageNote(many, false)
	if !strings.Contains(long, "and 60 more") {
		t.Errorf("long file lists must be elided: %q", long)
	}
}

// planWork is the whole point of routing: each agent must be chunked from
// its own filtered diff, not handed chunks of the whole PR.
func TestPlanWork_scopesEachAgent(t *testing.T) {
	prDiff := "diff --git a/api/internal/store/pg.go b/api/internal/store/pg.go\n" +
		"--- a/api/internal/store/pg.go\n+++ b/api/internal/store/pg.go\n@@ -1,1 +1,1 @@\n+store\n" +
		"diff --git a/web/src/App.svelte b/web/src/App.svelte\n" +
		"--- a/web/src/App.svelte\n+++ b/web/src/App.svelte\n@@ -1,1 +1,1 @@\n+web\n"

	cfg := &config.Config{Agents: []config.AgentConfig{
		{Subagent: "postgres-pro", Paths: []string{"api/internal/store/"}},
		{Subagent: "code-reviewer"},
		{Subagent: "accessibility", Paths: []string{"nothing/matches/"}},
	}}

	work := planWork(cfg, prDiff, promptBudget{chunk: 1 << 20}, false)

	if strings.Contains(work[0].diff, "App.svelte") {
		t.Error("a scoped agent was given a file outside its paths")
	}
	if !strings.Contains(work[0].diff, "store/pg.go") {
		t.Error("a scoped agent lost the file it is scoped to")
	}
	if !strings.Contains(work[1].diff, "App.svelte") || !strings.Contains(work[1].diff, "store/pg.go") {
		t.Error("an unscoped agent must still see the whole diff")
	}
	if len(work[2].chunks) != 0 {
		t.Errorf("an agent matching nothing must get no chunks, got %d", len(work[2].chunks))
	}
}

// An agent that matches nothing is reported as out of scope rather than
// counted a failure — otherwise every PR that misses one agent's area would
// fail the check.
func TestOutOfScopeReportIsNotAFailure(t *testing.T) {
	body := review.FormatOutOfScopeReport(config.AgentConfig{
		Subagent: "postgres-pro", Paths: []string{"api/internal/store/"},
	})
	if !strings.Contains(body, "No issues found") {
		t.Errorf("out-of-scope report must not read as a failure: %q", body)
	}
	if !strings.Contains(body, "api/internal/store/") {
		t.Errorf("out-of-scope report must name the scope: %q", body)
	}
}

// Findings are verified against the hunks they point at, not the whole diff.
func TestEvidenceFor_narrowsToFindingHunks(t *testing.T) {
	agentDiff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n" +
		"@@ -1,1 +5,1 @@\n+suspect\n@@ -40,1 +90,1 @@\n+unrelated\n"

	got := evidenceFor(agentDiff, []review.Finding{{File: "a.go", Line: 5}})
	if !strings.Contains(got, "+suspect") {
		t.Errorf("evidence lost the hunk under review: %q", got)
	}
	if strings.Contains(got, "+unrelated") {
		t.Errorf("evidence should not carry hunks no finding points at: %q", got)
	}
}

// A line number just outside every hunk must not leave verification with
// nothing to judge — it falls back to the named file.
func TestEvidenceFor_fallsBackToNamedFile(t *testing.T) {
	agentDiff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +5,1 @@\n+suspect\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,1 +1,1 @@\n+other\n"

	got := evidenceFor(agentDiff, []review.Finding{{File: "a.go", Line: 9999}})
	if !strings.Contains(got, "+suspect") {
		t.Errorf("fallback must include the named file: %q", got)
	}
	if strings.Contains(got, "+other") {
		t.Errorf("fallback must not include files no finding names: %q", got)
	}
}
