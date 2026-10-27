package review

import (
	"strings"
	"testing"

	"github.com/seethespark/pr-reviewer/internal/config"
)

func TestBuildPrompt(t *testing.T) {
	cfg := &config.Config{
		Owner:            "Nick",
		Context:          "Test app",
		ProductionStatus: "Dev",
		SecurityLevel:    "Low",
	}
	agent := config.AgentConfig{
		Subagent:   "code-reviewer",
		Additional: "focus on errors",
	}
	prompt := BuildPrompt(PromptInput{Cfg: cfg, Agent: agent, Diff: "diff content"})
	if !strings.Contains(prompt, "Nick") {
		t.Error("prompt missing owner")
	}
	if !strings.Contains(prompt, "code-reviewer") {
		t.Error("prompt missing agent name")
	}
	if !strings.Contains(prompt, "focus on errors") {
		t.Error("prompt missing additional instructions")
	}
	if !strings.Contains(prompt, "diff content") {
		t.Error("prompt missing diff")
	}
	if !strings.Contains(prompt, "Severity Discipline") {
		t.Error("prompt missing severity discipline instructions")
	}
}

func TestBuildPrompt_withCoverage(t *testing.T) {
	cfg := &config.Config{Owner: "Nick", Context: "Test", ProductionStatus: "Dev", SecurityLevel: "Low"}
	agent := config.AgentConfig{Subagent: "code-reviewer"}
	prompt := BuildPrompt(PromptInput{Cfg: cfg, Agent: agent, Diff: "diff content", CoverageSummary: "github.com/foo/bar/pkg\t75.0%"})
	if !strings.Contains(prompt, "Test Coverage") {
		t.Error("prompt missing coverage section")
	}
	if !strings.Contains(prompt, "75.0%") {
		t.Error("prompt missing coverage data")
	}
}

func TestBuildPrompt_noCoverage(t *testing.T) {
	cfg := &config.Config{Owner: "Nick", Context: "Test", ProductionStatus: "Dev", SecurityLevel: "Low"}
	agent := config.AgentConfig{Subagent: "code-reviewer"}
	prompt := BuildPrompt(PromptInput{Cfg: cfg, Agent: agent, Diff: "diff content"})
	if strings.Contains(prompt, "Test Coverage") {
		t.Error("prompt should not contain coverage section when empty")
	}
}

func TestBuildPrompt_withChunkNote(t *testing.T) {
	cfg := &config.Config{Owner: "Nick", Context: "Test", ProductionStatus: "Dev", SecurityLevel: "Low"}
	agent := config.AgentConfig{Subagent: "code-reviewer"}
	note := ChunkNote(2, 3)
	prompt := BuildPrompt(PromptInput{Cfg: cfg, Agent: agent, Diff: "diff content", ChunkNote: note})
	if !strings.Contains(prompt, "Diff Coverage Notice") {
		t.Error("prompt missing diff coverage notice section")
	}
	if !strings.Contains(prompt, "chunk 2 of 3") {
		t.Error("prompt missing chunk position")
	}
}

func TestBuildPrompt_noChunkNoteWhenEmpty(t *testing.T) {
	cfg := &config.Config{Owner: "Nick", Context: "Test", ProductionStatus: "Dev", SecurityLevel: "Low"}
	agent := config.AgentConfig{Subagent: "code-reviewer"}
	prompt := BuildPrompt(PromptInput{Cfg: cfg, Agent: agent, Diff: "diff content"})
	// The Severity Discipline section references "Diff Coverage Notice" by
	// name even when there isn't one, so check for the section heading
	// itself rather than the bare phrase.
	if strings.Contains(prompt, "## Diff Coverage Notice") {
		t.Error("prompt should not contain a coverage notice section when chunkNote is empty")
	}
}

func TestBuildPrompt_withPreviousReport(t *testing.T) {
	cfg := &config.Config{Owner: "Nick", Context: "Test", ProductionStatus: "Dev", SecurityLevel: "Low"}
	agent := config.AgentConfig{Subagent: "code-reviewer"}
	prompt := BuildPrompt(PromptInput{Cfg: cfg, Agent: agent, Diff: "diff content", PreviousReport: "## Code Review: code-reviewer\n\nPrior finding text."})
	if !strings.Contains(prompt, "Your Previous Review Of This PR") {
		t.Error("prompt missing previous review section")
	}
	if !strings.Contains(prompt, "Prior finding text.") {
		t.Error("prompt missing quoted previous report content")
	}
}

func TestChunkNote_singleChunk(t *testing.T) {
	if note := ChunkNote(1, 1); note != "" {
		t.Errorf("expected empty note for a single chunk, got %q", note)
	}
}

func TestChunkNote_multipleChunks(t *testing.T) {
	note := ChunkNote(1, 4)
	if !strings.Contains(note, "chunk 1 of 4") {
		t.Errorf("note missing chunk position: %q", note)
	}
	if !strings.Contains(note, "SAME pull request") {
		t.Errorf("note missing same-PR clarification: %q", note)
	}
}

func TestParseSeverity_valid(t *testing.T) {
	text := `Here is the review.
{"critical": 1, "high": 2, "medium": 3, "low": 4}
Details follow...`
	summary, _, err := ParseSeverity(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.Critical != 1 || summary.High != 2 || summary.Medium != 3 || summary.Low != 4 {
		t.Errorf("unexpected severity: %+v", summary)
	}
}

func TestParseSeverity_missing(t *testing.T) {
	text := "No JSON here"
	_, _, err := ParseSeverity(text)
	if err == nil {
		t.Fatal("expected error for missing JSON")
	}
}

func TestParseSeverity_malformed(t *testing.T) {
	text := `{"critical": abc}`
	_, _, err := ParseSeverity(text)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestParseSeverity_extraText(t *testing.T) {
	text := `Some text
` + "```json\n" + `{"critical": 0, "high": 1}` + "\n```\nMore text"
	summary, _, err := ParseSeverity(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.High != 1 {
		t.Errorf("expected high=1, got %d", summary.High)
	}
}

// A scoped agent must be told its diff is a subset, or the standing
// "absence is not evidence" discipline gets applied to a filter artefact.
func TestBuildPrompt_scopeNote(t *testing.T) {
	cfg := &config.Config{Owner: "n", Context: "c", ProductionStatus: "p", SecurityLevel: "s"}

	scoped := BuildPrompt(PromptInput{
		Cfg:   cfg,
		Agent: config.AgentConfig{Subagent: "postgres-pro", Paths: []string{"api/internal/store/"}},
		Diff:  "d",
	})
	if !strings.Contains(scoped, "api/internal/store/") {
		t.Errorf("scope note missing the agent's paths:\n%s", scoped)
	}
	if !strings.Contains(scoped, "outside your scope than absent") {
		t.Errorf("scope note missing its warning:\n%s", scoped)
	}

	unscoped := BuildPrompt(PromptInput{
		Cfg:   cfg,
		Agent: config.AgentConfig{Subagent: "code-reviewer"},
		Diff:  "d",
	})
	if strings.Contains(unscoped, "**Scope**") {
		t.Error("an unscoped agent must get no scope note")
	}
}
