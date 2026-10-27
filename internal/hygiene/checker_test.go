package hygiene

import (
	"context"
	"os"
	"testing"
)

// The checks look in the current directory, so each test runs in its own
// temporary one rather than writing into the package directory.

func TestRunner_H001_pass(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("README.md", nil, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	runner := NewRunner("")
	results := runner.Run(ctx, []Check{{ID: "H001", Name: "README", Ticked: true}})
	if len(results) != 1 || !results[0].Passed {
		t.Errorf("expected H001 to pass, got %+v", results[0])
	}
}

func TestRunner_H001_fail(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()
	runner := NewRunner("")
	results := runner.Run(ctx, []Check{{ID: "H001", Name: "README", Ticked: true}})
	if len(results) != 1 || results[0].Passed {
		t.Errorf("expected H001 to fail, got %+v", results[0])
	}
}

func TestRunner_H002_pass(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(".github", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".github/CODEOWNERS", nil, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	runner := NewRunner("")
	results := runner.Run(ctx, []Check{{ID: "H002", Name: "CODEOWNERS", Ticked: true}})
	if len(results) != 1 || !results[0].Passed {
		t.Errorf("expected H002 to pass, got %+v", results[0])
	}
}

func TestRunner_H006_pass(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(".devcontainer", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".devcontainer/devcontainer.json", nil, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	runner := NewRunner("")
	results := runner.Run(ctx, []Check{{ID: "H006", Name: "Devcontainer", Ticked: true}})
	if len(results) != 1 || !results[0].Passed {
		t.Errorf("expected H006 to pass, got %+v", results[0])
	}
}

func TestRunner_noTickedItems(t *testing.T) {
	ctx := context.Background()
	runner := NewRunner("")
	results := runner.Run(ctx, []Check{
		{ID: "H001", Name: "README", Ticked: false},
	})
	if len(results) != 0 {
		t.Errorf("expected no results for unticked items, got %d", len(results))
	}
}

func TestRunner_H005_pass(t *testing.T) {
	summary := "github.com/foo/bar.go:10:\tFoo\t100.0%\ntotal:\t\t\t(statements)\t97.3%"
	runner := NewRunner(summary)
	results := runner.Run(context.Background(), []Check{{ID: "H005", Name: "Test coverage is above 95%", Ticked: true}})
	if len(results) != 1 || !results[0].Passed {
		t.Errorf("expected H005 to pass, got %+v", results[0])
	}
}

func TestRunner_H005_fail_below_threshold(t *testing.T) {
	summary := "github.com/foo/bar.go:10:\tFoo\t80.0%\ntotal:\t\t\t(statements)\t82.1%"
	runner := NewRunner(summary)
	results := runner.Run(context.Background(), []Check{{ID: "H005", Name: "Test coverage is above 95%", Ticked: true}})
	if len(results) != 1 || results[0].Passed {
		t.Errorf("expected H005 to fail, got %+v", results[0])
	}
}

func TestRunner_H005_custom_threshold(t *testing.T) {
	summary := "total:\t\t\t(statements)\t85.0%"
	runner := NewRunner(summary)
	// 80% threshold extracted from name — 85% should pass
	results := runner.Run(context.Background(), []Check{{ID: "H005", Name: "Test coverage is above 80%", Ticked: true}})
	if len(results) != 1 || !results[0].Passed {
		t.Errorf("expected H005 to pass with 80%% threshold, got %+v", results[0])
	}
}

func TestRunner_H005_default_threshold_fallback(t *testing.T) {
	summary := "total:\t\t\t(statements)\t94.0%"
	runner := NewRunner(summary)
	// No % in name — falls back to 95%, so 94% should fail
	results := runner.Run(context.Background(), []Check{{ID: "H005", Name: "Test coverage check", Ticked: true}})
	if len(results) != 1 || results[0].Passed {
		t.Errorf("expected H005 to fail with default 95%% threshold, got %+v", results[0])
	}
}

func TestRunner_H005_fail_no_summary(t *testing.T) {
	runner := NewRunner("")
	results := runner.Run(context.Background(), []Check{{ID: "H005", Name: "Test coverage is above 95%", Ticked: true}})
	if len(results) != 1 || results[0].Passed {
		t.Errorf("expected H005 to fail with no summary, got %+v", results[0])
	}
}

func TestFormatReport(t *testing.T) {
	results := []Result{
		{ID: "H001", Name: "README", Passed: true, Details: "found"},
		{ID: "H002", Name: "CODEOWNERS", Passed: false, Details: "missing"},
	}
	report := FormatReport(results)
	if report == "" {
		t.Error("expected non-empty report")
	}
}
