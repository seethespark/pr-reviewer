package review

import (
	"strings"
	"testing"

	"github.com/seethespark/pr-reviewer/internal/config"
)

func TestParseFindings_fenced(t *testing.T) {
	text := "Some preamble.\n\n```json\n" +
		`{"findings": [{"file": "a/b.go", "line": 12, "severity": "HIGH", "title": "SQL injection", "rationale": "string concat into query", "suggestion": "use placeholders"}]}` +
		"\n```\n\nSome closing notes."
	findings, notes, err := ParseFindings(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.Severity != "high" {
		t.Errorf("severity not normalized: %q", f.Severity)
	}
	if f.File != "a/b.go" || f.Line != 12 {
		t.Errorf("location wrong: %s:%d", f.File, f.Line)
	}
	if !strings.Contains(notes, "closing notes") || strings.Contains(notes, "findings") {
		t.Errorf("notes should be text outside the JSON block, got %q", notes)
	}
}

func TestParseFindings_bareJSON(t *testing.T) {
	text := `{"findings": [{"file": "x.go", "line": 1, "severity": "low", "title": "t", "rationale": "r"}]} trailing prose`
	findings, notes, err := ParseFindings(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if !strings.Contains(notes, "trailing prose") {
		t.Errorf("notes = %q", notes)
	}
}

func TestParseFindings_empty(t *testing.T) {
	findings, _, err := ParseFindings("```json\n{\"findings\": []}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %d", len(findings))
	}
}

func TestParseFindings_missing(t *testing.T) {
	if _, _, err := ParseFindings("just prose, no JSON"); err == nil {
		t.Fatal("expected error when no findings block present")
	}
}

func TestParseFindings_unknownSeverity(t *testing.T) {
	text := `{"findings": [{"file": "x.go", "line": 1, "severity": "blocker", "title": "t", "rationale": "r"}]}`
	findings, _, err := ParseFindings(text)
	if err != nil {
		t.Fatal(err)
	}
	if findings[0].Severity != "medium" {
		t.Errorf("unknown severity should coerce to medium, got %q", findings[0].Severity)
	}
}

func TestParseVerification_dismissed(t *testing.T) {
	text := "```json\n" +
		`{"findings": [], "dismissed": [{"title": "SQL injection", "reason": "parameterised two lines above"}]}` +
		"\n```"
	findings, dismissed, err := ParseVerification(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 surviving findings, got %d", len(findings))
	}
	if len(dismissed) != 1 || dismissed[0].Reason == "" {
		t.Errorf("dismissed = %+v", dismissed)
	}
}

func TestDedupeFindings(t *testing.T) {
	fs := []Finding{
		{File: "a.go", Line: 1, Title: "Dup", Severity: "high"},
		{File: "a.go", Line: 1, Title: "dup", Severity: "high"},
		{File: "a.go", Line: 2, Title: "Dup", Severity: "high"},
	}
	out := DedupeFindings(fs)
	if len(out) != 2 {
		t.Errorf("expected 2 after dedupe, got %d", len(out))
	}
}

func TestSummaryFromFindings(t *testing.T) {
	fs := []Finding{
		{Severity: "critical"}, {Severity: "high"}, {Severity: "high"},
		{Severity: "medium"}, {Severity: "low"},
	}
	s := SummaryFromFindings(fs)
	if s.Critical != 1 || s.High != 2 || s.Medium != 1 || s.Low != 1 {
		t.Errorf("summary = %+v", s)
	}
}

func TestRenderFindings(t *testing.T) {
	fs := []Finding{
		{File: "low.go", Line: 5, Severity: "low", Title: "Minor thing", Rationale: "meh"},
		{File: "bad.go", Line: 9, Severity: "critical", Title: "Big problem", Rationale: "because", Suggestion: "fix it"},
	}
	dis := []Dismissed{{Title: "Ghost", Reason: "not in diff"}}
	out := RenderFindings(fs, dis, []string{"note text"})

	if strings.Index(out, "Big problem") > strings.Index(out, "Minor thing") {
		t.Error("critical finding should render before low")
	}
	for _, want := range []string{"bad.go:9", "fix it", "Ghost", "not in diff", "note text"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q", want)
		}
	}
}

func TestRenderFindings_none(t *testing.T) {
	out := RenderFindings(nil, nil, nil)
	if !strings.Contains(out, "No issues found") {
		t.Errorf("empty render = %q", out)
	}
}

func TestFindingMarker_stable(t *testing.T) {
	f := Finding{File: "a.go", Line: 3, Title: "T"}
	if FindingMarker("agent", f) != FindingMarker("agent", f) {
		t.Error("marker must be deterministic")
	}
	if FindingMarker("agent", f) == FindingMarker("other", f) {
		t.Error("marker must differ per agent")
	}
}

func TestBuildVerificationPrompt(t *testing.T) {
	agent := config.AgentConfig{Subagent: "security-auditor"}
	fs := []Finding{{File: "a.go", Line: 1, Severity: "high", Title: "T", Rationale: "R"}}
	prompt := BuildVerificationPrompt(agent, "diff body", fs)
	for _, want := range []string{"security-auditor", "diff body", `"findings"`, "dismissed", "Do NOT add new findings"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("verification prompt missing %q", want)
		}
	}
}

func TestBuildPrompt_injectionGuardAndFindingsFormat(t *testing.T) {
	cfg := &config.Config{Owner: "N", Context: "T", ProductionStatus: "Dev", SecurityLevel: "Low"}
	prompt := BuildPrompt(PromptInput{Cfg: cfg, Agent: config.AgentConfig{Subagent: "r"}, Diff: "d"})
	if !strings.Contains(prompt, "prompt injection") {
		t.Error("prompt missing injection guard")
	}
	if !strings.Contains(prompt, `{"findings":`) {
		t.Error("prompt missing findings output format")
	}
}
