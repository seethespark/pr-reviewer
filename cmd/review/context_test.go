package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/seethespark/pr-reviewer/internal/github"
)

func TestBuildDiscussion(t *testing.T) {
	var human, bot github.Comment
	human.User.Login = "alice"
	human.Body = "The retry is intentional — upstream flakes, see #42."
	bot.User.Login = "github-actions"
	bot.Body = "## Code Review: x\n<!-- wd-auto-review:agent=x -->"

	var reply github.ReviewComment
	reply.User.Login = "bob"
	reply.Body = "Agreed, leaving as is."
	reply.Path = "retry.go"
	reply.Line = 12

	out := buildDiscussion([]github.Comment{human, bot}, []github.ReviewComment{reply})

	if !strings.Contains(out, "@alice") || !strings.Contains(out, "intentional") {
		t.Errorf("missing human comment: %q", out)
	}
	if strings.Contains(out, "wd-auto-review") {
		t.Error("bot's own comments must be excluded from discussion")
	}
	if !strings.Contains(out, "@bob (on retry.go:12)") {
		t.Errorf("missing review reply with location: %q", out)
	}
}

func TestBuildFileContext(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)

	// A file long enough that a ±contextLines window around the hunk is a
	// strict subset of it — the whole point of windowing.
	var lines []string
	for i := 1; i <= 400; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	lines[199] = "// fence breaker: ```"
	lines[10] = "FAR_FROM_ANY_HUNK"
	os.WriteFile("code.go", []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	os.WriteFile("blob.bin", []byte{0x00, 0x01, 0x02}, 0o644)

	chunk := "diff --git a/code.go b/code.go\n" +
		"--- a/code.go\n+++ b/code.go\n" +
		"@@ -200,1 +200,1 @@\n+// fence breaker: ```\n" +
		"diff --git a/blob.bin b/blob.bin\n--- a/blob.bin\n+++ b/blob.bin\n@@ -1,1 +1,1 @@\n+x\n" +
		"diff --git a/deleted.go b/deleted.go\n--- a/deleted.go\n+++ b/deleted.go\n@@ -1,1 +1,1 @@\n+x\n"

	out := buildFileContext(chunk, maxFileContextBytes)

	if !strings.Contains(out, "fence breaker") {
		t.Errorf("missing the changed line: %q", out)
	}
	if !strings.Contains(out, "````") {
		t.Error("content must be wrapped in a four-backtick fence")
	}
	if !strings.Contains(out, "  200| ") {
		t.Errorf("excerpts must carry real line numbers: %q", out)
	}
	if strings.Contains(out, "FAR_FROM_ANY_HUNK") {
		t.Error("lines far from every hunk must not be included — that is the windowing")
	}
	if strings.Contains(out, "blob.bin") {
		t.Error("binary files must be skipped")
	}
	if strings.Contains(out, "deleted.go") {
		t.Error("missing files must be skipped silently")
	}
}

// A file whose hunks are close together must be shown once, not once per
// hunk with the overlap repeated.
func TestBuildFileContext_mergesOverlappingWindows(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)

	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	os.WriteFile("a.go", []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	chunk := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n" +
		"@@ -10,1 +10,1 @@\n+x\n@@ -20,1 +20,1 @@\n+y\n"

	out := buildFileContext(chunk, maxFileContextBytes)
	if got := strings.Count(out, "   20| "); got != 1 {
		t.Errorf("line 20 appears %d times, want 1 — overlapping windows were not merged:\n%s", got, out)
	}
}
