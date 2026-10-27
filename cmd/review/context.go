package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/seethespark/pr-reviewer/internal/diff"
	"github.com/seethespark/pr-reviewer/internal/github"
)

const (
	// Budgets for prompt context sections. Generous enough to be useful,
	// bounded so a huge PR can't balloon every prompt.
	maxDiscussionBytes  = 16 * 1024
	maxCommentBytes     = 2 * 1024
	maxFileContextBytes = 32 * 1024
	maxRepoTreeBytes    = 8 * 1024

	// contextLines is how much source to show either side of a hunk. Wide
	// enough to carry the enclosing function and its guard clauses, which is
	// what the section exists for; the whole file is not.
	contextLines = 60

	// maxPreviousReportBytes bounds the agent's own prior report. It is
	// quoted verbatim into every chunk prompt, and it grows with each re-run
	// as findings accumulate, so an uncapped one lets prompt size creep
	// upward on exactly the PRs that are already slow — the ones being
	// pushed to repeatedly.
	maxPreviousReportBytes = 12 * 1024

	// maxCoverageBytes bounds `go tool cover -func` output, which is one line
	// per function and unbounded on a large repository.
	maxCoverageBytes = 12 * 1024
)

// clip truncates s to at most n bytes, appending an explicit note so a
// shortened section never looks complete to the agent reading it.
func clip(s string, n int, what string) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n… [%s truncated at %d bytes]", what, n)
}

// buildDiscussion renders human PR conversation (issue comments and inline
// review replies) for agent prompts, so explanations of intentional behavior
// reach the reviewer. Our own bot comments are skipped — agents get those
// separately as their previous report.
func buildDiscussion(comments []github.Comment, reviewComments []github.ReviewComment) string {
	var b strings.Builder

	add := func(author, location, body string) {
		if strings.Contains(body, "wd-auto-review") {
			return // our own output, or a quote of it
		}
		body = strings.TrimSpace(body)
		if body == "" {
			return
		}
		if len(body) > maxCommentBytes {
			body = body[:maxCommentBytes] + "… [comment truncated]"
		}
		entry := fmt.Sprintf("@%s%s:\n%s\n\n", author, location, body)
		if b.Len()+len(entry) > maxDiscussionBytes {
			return
		}
		b.WriteString(entry)
	}

	for _, c := range comments {
		add(c.User.Login, "", c.Body)
	}
	for _, rc := range reviewComments {
		loc := ""
		if rc.Path != "" {
			loc = fmt.Sprintf(" (on %s:%d)", rc.Path, rc.Line)
		}
		add(rc.User.Login, loc, rc.Body)
	}

	return strings.TrimSpace(b.String())
}

// buildFileContext shows the source around each hunk of a diff chunk, read
// from the checked-out working tree (which is head).
//
// It used to dump WHOLE files, in diff order, until a 96KB budget ran out.
// On a large PR that meant the first four or five changed files arrived
// complete and the remaining thirty arrived as a one-line "content omitted"
// note — the agent got saturating detail about an arbitrary prefix of the
// diff and nothing at all about the rest. Windowing on hunks inverts both
// halves of that: every changed file gets context, and the context it gets
// is the part a reviewer would actually read. The section is about a third
// of its old size as a side effect, which is most of the point — it was
// costing nearly as many prompt tokens as the diff itself.
//
// Excerpts carry real line numbers because findings must cite a new-side
// line that appears in the diff, and an unnumbered excerpt left the model
// counting lines by hand.
func buildFileContext(chunk string, budget int) string {
	ranges := hunkWindows(chunk)
	paths := diff.FilePaths(chunk)
	if len(paths) == 0 || budget <= 0 {
		return ""
	}

	// An equal share each, so no file is starved by its position in the
	// diff. Files that use less than their share hand the remainder on.
	var b strings.Builder
	var omitted []string
	remaining := budget
	left := len(paths)

	for _, p := range paths {
		share := remaining / max(left, 1)
		left--

		rs, ok := ranges[p]
		if !ok {
			continue // no hunks (binary, rename): the diff header says it all
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue // deleted in this PR, or unreadable — the diff still shows it
		}
		if isBinary(data) {
			continue
		}
		excerpt := excerptRanges(string(data), rs, share)
		if excerpt == "" {
			omitted = append(omitted, p)
			continue
		}
		// Four-backtick fence so file contents containing ``` don't break out.
		section := fmt.Sprintf("### `%s`\n\n````\n%s\n````\n\n", p, excerpt)
		b.WriteString(section)
		remaining -= len(section)
		if remaining < 0 {
			remaining = 0
		}
	}

	if len(omitted) > 0 {
		fmt.Fprintf(&b, "_Context omitted for %d file(s) (budget): %s_\n",
			len(omitted), strings.Join(omitted, ", "))
	}
	return strings.TrimSpace(b.String())
}

// hunkWindows returns, per file, the line ranges to excerpt: each hunk
// widened by contextLines on both sides, with overlaps merged so a file with
// several nearby hunks is shown once rather than repeatedly.
func hunkWindows(chunk string) map[string][][2]int {
	out := make(map[string][][2]int)
	for file, rs := range diff.HunkRanges(chunk) {
		widened := make([][2]int, 0, len(rs))
		for _, r := range rs {
			widened = append(widened, [2]int{max(r[0]-contextLines, 1), r[1] + contextLines})
		}
		sort.Slice(widened, func(i, j int) bool { return widened[i][0] < widened[j][0] })

		merged := widened[:0:0]
		for _, r := range widened {
			if n := len(merged); n > 0 && r[0] <= merged[n-1][1]+1 {
				merged[n-1][1] = max(merged[n-1][1], r[1])
				continue
			}
			merged = append(merged, r)
		}
		out[file] = merged
	}
	return out
}

// excerptRanges renders the given line ranges of a file with line numbers,
// separated by an elision marker, stopping once budget bytes are used.
func excerptRanges(content string, ranges [][2]int, budget int) string {
	lines := strings.Split(content, "\n")
	var b strings.Builder

	for i, r := range ranges {
		if b.Len() >= budget {
			fmt.Fprintf(&b, "… [remaining context for this file omitted: budget]\n")
			break
		}
		if i > 0 {
			b.WriteString("…\n")
		}
		end := min(r[1], len(lines))
		for n := r[0]; n <= end; n++ {
			if b.Len() >= budget {
				fmt.Fprintf(&b, "… [truncated at line %d: budget]\n", n)
				break
			}
			fmt.Fprintf(&b, "%5d| %s\n", n, lines[n-1])
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// isBinary reports whether data looks like a binary file (NUL byte in the
// first 8KB, the same heuristic git uses).
func isBinary(data []byte) bool {
	probe := data
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	return bytes.IndexByte(probe, 0) >= 0
}
