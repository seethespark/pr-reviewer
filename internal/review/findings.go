// Structured findings: agents report machine-readable findings instead of
// (only) freeform markdown, enabling per-finding gating, deduplication
// across chunks, verification passes, and inline PR comments.
package review

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"

	"github.com/seethespark/pr-reviewer/internal/config"
)

// Finding is one structured issue reported by an agent.
type Finding struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Severity   string `json:"severity"`
	Title      string `json:"title"`
	Rationale  string `json:"rationale"`
	Suggestion string `json:"suggestion,omitempty"`
}

// Dismissed is a finding dropped during the verification pass, kept for
// auditability so suppressed findings are visible rather than silent.
type Dismissed struct {
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

var severityRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}

var severityEmoji = map[string]string{"critical": "🔴", "high": "🟠", "medium": "🟡", "low": "🟢"}

// normalizeSeverity lowercases and validates a severity label. Unknown labels
// become "medium": not silently low (would dodge attention) and not
// critical/high (would fail the gate without evidence of intent).
func normalizeSeverity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if _, ok := severityRank[s]; ok {
		return s
	}
	return "medium"
}

var fencedJSONPattern = regexp.MustCompile("(?s)```(?:json)?\\s*\\n(.*?)```")

type findingsEnvelope struct {
	Findings  []Finding   `json:"findings"`
	Dismissed []Dismissed `json:"dismissed"`
}

// ParseFindings extracts the structured findings block from an agent
// response. It returns the findings, the remaining text outside the JSON
// block (the agent's optional markdown notes), and an error when no
// findings block could be located.
func ParseFindings(text string) ([]Finding, string, error) {
	// Preferred: a fenced JSON block containing a "findings" array.
	for _, m := range fencedJSONPattern.FindAllStringSubmatchIndex(text, -1) {
		block := text[m[2]:m[3]]
		if !strings.Contains(block, `"findings"`) {
			continue
		}
		var env findingsEnvelope
		if err := json.Unmarshal([]byte(block), &env); err != nil {
			continue
		}
		notes := strings.TrimSpace(text[:m[0]] + text[m[1]:])
		return normalizeFindings(env.Findings), notes, nil
	}

	// Fallback: a bare JSON object somewhere in the text. json.Decoder stops
	// at the end of the first value, so trailing prose is fine.
	if i := strings.Index(text, `{"findings"`); i >= 0 {
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		var env findingsEnvelope
		if err := dec.Decode(&env); err == nil {
			end := i + int(dec.InputOffset())
			notes := strings.TrimSpace(text[:i] + text[end:])
			return normalizeFindings(env.Findings), notes, nil
		}
	}

	return nil, text, fmt.Errorf("no findings JSON block found")
}

// ParseVerification parses the verification-pass response: the surviving
// findings plus what was dismissed and why.
func ParseVerification(text string) ([]Finding, []Dismissed, error) {
	findings, _, err := ParseFindings(text)
	if err != nil {
		return nil, nil, err
	}
	// Re-extract the envelope for the dismissed list.
	for _, m := range fencedJSONPattern.FindAllStringSubmatch(text, -1) {
		if !strings.Contains(m[1], `"findings"`) {
			continue
		}
		var env findingsEnvelope
		if json.Unmarshal([]byte(m[1]), &env) == nil {
			return findings, env.Dismissed, nil
		}
	}
	if i := strings.Index(text, `{"findings"`); i >= 0 {
		var env findingsEnvelope
		if json.NewDecoder(strings.NewReader(text[i:])).Decode(&env) == nil {
			return findings, env.Dismissed, nil
		}
	}
	return findings, nil, nil
}

func normalizeFindings(fs []Finding) []Finding {
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		f.Severity = normalizeSeverity(f.Severity)
		f.File = strings.TrimPrefix(strings.TrimSpace(f.File), "b/")
		if f.Title == "" {
			f.Title = "(untitled finding)"
		}
		out = append(out, f)
	}
	return out
}

// DedupeFindings removes duplicate findings (same file, line, and title),
// which occur when chunk boundaries overlap or a verification pass re-emits.
func DedupeFindings(fs []Finding) []Finding {
	seen := make(map[string]bool, len(fs))
	var out []Finding
	for _, f := range fs {
		key := fmt.Sprintf("%s:%d:%s", f.File, f.Line, strings.ToLower(f.Title))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

// SummaryFromFindings computes severity counts from structured findings.
func SummaryFromFindings(fs []Finding) SeveritySummary {
	var s SeveritySummary
	for _, f := range fs {
		switch f.Severity {
		case "critical":
			s.Critical++
		case "high":
			s.High++
		case "medium":
			s.Medium++
		case "low":
			s.Low++
		}
	}
	return s
}

// SortFindings orders findings by severity (critical first), then location.
func SortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if severityRank[fs[i].Severity] != severityRank[fs[j].Severity] {
			return severityRank[fs[i].Severity] < severityRank[fs[j].Severity]
		}
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		return fs[i].Line < fs[j].Line
	})
}

// RenderFindings produces the markdown body for a findings-based report.
func RenderFindings(findings []Finding, dismissed []Dismissed, notes []string) string {
	var b strings.Builder

	if len(findings) == 0 {
		b.WriteString("No issues found.\n")
	}
	SortFindings(findings)
	for _, f := range findings {
		loc := ""
		if f.File != "" {
			loc = fmt.Sprintf(" — `%s", f.File)
			if f.Line > 0 {
				loc += fmt.Sprintf(":%d", f.Line)
			}
			loc += "`"
		}
		fmt.Fprintf(&b, "### %s [%s] %s%s\n\n", severityEmoji[f.Severity], strings.ToUpper(f.Severity), f.Title, loc)
		if f.Rationale != "" {
			b.WriteString(f.Rationale + "\n\n")
		}
		if f.Suggestion != "" {
			fmt.Fprintf(&b, "**Suggested fix**: %s\n\n", f.Suggestion)
		}
	}

	if len(dismissed) > 0 {
		b.WriteString("<details><summary>Dismissed during verification</summary>\n\n")
		for _, d := range dismissed {
			fmt.Fprintf(&b, "- **%s** — %s\n", d.Title, d.Reason)
		}
		b.WriteString("\n</details>\n\n")
	}

	for _, n := range notes {
		if strings.TrimSpace(n) == "" {
			continue
		}
		b.WriteString("---\n\n" + strings.TrimSpace(n) + "\n\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// FindingMarker returns the HTML marker embedded in an inline PR comment for
// a finding, used to avoid re-posting the same finding on re-runs.
func FindingMarker(agent string, f Finding) string {
	h := fnv.New32a()
	fmt.Fprintf(h, "%s|%s|%d|%s", agent, f.File, f.Line, strings.ToLower(f.Title))
	return fmt.Sprintf("<!-- wd-auto-review:finding=%s:%08x -->", agent, h.Sum32())
}

// FormatInlineComment renders the body of an inline PR review comment for a
// single finding.
func FormatInlineComment(agent string, f Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s **[%s] %s**\n\n", severityEmoji[f.Severity], strings.ToUpper(f.Severity), f.Title)
	if f.Rationale != "" {
		b.WriteString(f.Rationale + "\n\n")
	}
	if f.Suggestion != "" {
		fmt.Fprintf(&b, "**Suggested fix**: %s\n\n", f.Suggestion)
	}
	fmt.Fprintf(&b, "_Reported by `%s` (automated review)_\n\n%s\n", agent, FindingMarker(agent, f))
	return b.String()
}

// BuildVerificationPrompt asks the same agent to re-examine its own findings
// against the diff and drop anything unsupported or explained as intentional.
func BuildVerificationPrompt(agent config.AgentConfig, diff string, findings []Finding) string {
	data, _ := json.MarshalIndent(findingsEnvelope{Findings: findings}, "", "  ")

	var b strings.Builder
	b.WriteString("You are a code review agent verifying your own draft findings before they are published.\n\n")
	fmt.Fprintf(&b, "## Agent Role: %s\n\n", agent.Subagent)
	b.WriteString("## Diff\n\n```diff\n")
	b.WriteString(diff)
	b.WriteString("\n```\n\n")
	b.WriteString("## Draft Findings\n\n```json\n")
	b.Write(data)
	b.WriteString("\n```\n\n")
	b.WriteString("## Instructions\n\n")
	b.WriteString(
		"Re-examine each draft finding STRICTLY against the diff above:\n\n" +
			"- KEEP a finding only if a specific line in the diff gives positive evidence for it. Quote-check yourself: if you cannot point to the exact line, dismiss it.\n" +
			"- DISMISS findings that are speculative (\"if X is not handled elsewhere...\"), that concern code not visible in the diff, or that duplicate another finding.\n" +
			"- DISMISS findings that a code comment in the diff explains as intentional with a stated reason (e.g. a `pr-review:allow` directive or an explanatory comment). Record the stated reason.\n" +
			"- DOWNGRADE severity where the evidence supports the issue but not the assigned severity.\n" +
			"- Do NOT add new findings.\n\n" +
			"Output exactly one fenced JSON block:\n\n" +
			"```json\n" +
			`{"findings": [<surviving findings, same schema>], "dismissed": [{"title": "...", "reason": "..."}]}` + "\n" +
			"```\n",
	)
	return b.String()
}
