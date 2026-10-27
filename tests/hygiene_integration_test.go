package tests

import (
	"context"
	"os"
	"testing"

	"github.com/seethespark/pr-reviewer/internal/config"
	"github.com/seethespark/pr-reviewer/internal/hygiene"
)

func TestHygieneEndToEnd(t *testing.T) {
	ctx := context.Background()

	// Create PR-REVIEW.md with ticked hygiene items
	content := `
## Who owns the repo
Test
## Context and intent
Test app
## Production status
Dev
## Security level
Low
## PR Hygiene
- [x] H001 README.md is present and up to date
- [x] H002 codeowners exists
## Code reviews
`
	if err := os.WriteFile("PR-REVIEW.md", []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove("PR-REVIEW.md")

	// Create README.md
	if err := os.WriteFile("README.md", []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove("README.md")

	cfg, err := config.Parse("PR-REVIEW.md")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	runner := hygiene.NewRunner("")
	var checks []hygiene.Check
	for _, h := range cfg.Hygiene {
		checks = append(checks, hygiene.Check{
			ID:     h.ID,
			Name:   h.Name,
			Ticked: h.Ticked,
		})
	}
	results := runner.Run(ctx, checks)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !results[0].Passed {
		t.Errorf("expected H001 to pass: %s", results[0].Details)
	}
	if results[1].Passed {
		t.Errorf("expected H002 to fail (CODEOWNERS missing)")
	}
}
