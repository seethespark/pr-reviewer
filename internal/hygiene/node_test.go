package hygiene

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeNpm replaces the npm calls for one test: latest maps a package to its
// registry version, and a package missing from it fails the lookup.
func fakeNpm(t *testing.T, latest map[string]string, outdated string) {
	t.Helper()
	origLatest, origOutdated := npmLatest, npmOutdated
	t.Cleanup(func() { npmLatest, npmOutdated = origLatest, origOutdated })
	npmLatest = func(_ context.Context, _, name string) (string, error) {
		if v, ok := latest[name]; ok {
			return v, nil
		}
		return "", errors.New("E404")
	}
	npmOutdated = func(context.Context, string) ([]byte, error) { return []byte(outdated), nil }
}

func writeFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runH003(t *testing.T) Result {
	t.Helper()
	results := NewRunner("").Run(context.Background(), []Check{{ID: "H003", Name: "versions", Ticked: true}})
	if len(results) != 1 {
		t.Fatalf("expected one result, got %+v", results)
	}
	return results[0]
}

const pkgJSON = `{
  "dependencies": {"postgres": "^3.4.9", "mermaid": "^11.17.2", "local": "file:../local"},
  "devDependencies": {"vite": "^8.3.4"}
}`

const lockV3 = `{
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "app"},
    "node_modules/postgres": {"version": "3.4.9"},
    "node_modules/mermaid": {"version": "11.17.2"},
    "node_modules/vite": {"version": "8.3.4"}
  }
}`

// Nothing is installed, as in CI: dev dependencies are checked, and runtime
// ones at the latest version pass.
func TestRunner_H003_lockfile(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFiles(t, map[string]string{"package.json": pkgJSON, "package-lock.json": lockV3})
	fakeNpm(t, map[string]string{"postgres": "3.4.9", "mermaid": "12.2.0", "vite": "8.3.5"}, "")

	res := runH003(t)
	want := ".: 2 outdated npm packages: mermaid 11.17.2 → 12.2.0, vite 8.3.4 → 8.3.5"
	if res.Passed || res.Details != want {
		t.Errorf("got %+v, want details %q", res, want)
	}
}

func TestRunner_H003_lockfile_up_to_date(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFiles(t, map[string]string{"package.json": pkgJSON, "package-lock.json": lockV3})
	fakeNpm(t, map[string]string{"postgres": "3.4.9", "mermaid": "11.17.2", "vite": "8.3.4"}, "")

	if res := runH003(t); !res.Passed {
		t.Errorf("expected H003 to pass, got %+v", res)
	}
}

func TestRunner_H003_lockfile_v1(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFiles(t, map[string]string{
		"package.json":      `{"dependencies": {"unified": "^10.0.0"}}`,
		"package-lock.json": `{"lockfileVersion": 1, "dependencies": {"unified": {"version": "10.1.2"}}}`,
	})
	fakeNpm(t, map[string]string{"unified": "11.0.5"}, "")

	if res := runH003(t); res.Passed || !strings.Contains(res.Details, "unified 10.1.2 → 11.0.5") {
		t.Errorf("got %+v", res)
	}
}

func TestRunner_H003_unchecked(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFiles(t, map[string]string{
		"package.json":      `{"dependencies": {"gone": "^1.0.0", "private": "^2.0.0"}}`,
		"package-lock.json": `{"lockfileVersion": 3, "packages": {"node_modules/private": {"version": "2.0.0"}}}`,
	})
	fakeNpm(t, nil, "")

	res := runH003(t)
	want := ".: could not check gone (not in the lockfile), private (registry lookup failed)"
	if res.Passed || res.Details != want {
		t.Errorf("got %+v, want details %q", res, want)
	}
}

// Each directory with a package.json is checked and named in the report.
func TestRunner_H003_nested(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFiles(t, map[string]string{
		"package.json":                `{"dependencies": {"yaml": "^2.0.0"}}`,
		"package-lock.json":           `{"lockfileVersion": 3, "packages": {"node_modules/yaml": {"version": "2.8.0"}}}`,
		"spikes/a/package.json":       `{"dependencies": {"unified": "^11.0.0"}}`,
		"spikes/a/package-lock.json":  `{"lockfileVersion": 3, "packages": {"node_modules/unified": {"version": "11.0.4"}}}`,
		"node_modules/x/package.json": `{"dependencies": {"ignored": "1"}}`,
	})
	fakeNpm(t, map[string]string{"yaml": "2.8.0", "unified": "11.0.5"}, "")

	res := runH003(t)
	want := filepath.Join("spikes", "a") + ": 1 outdated npm packages: unified 11.0.4 → 11.0.5"
	if res.Passed || res.Details != want {
		t.Errorf("got %+v, want details %q", res, want)
	}
}

// The output that was misread before: field names were counted as packages.
func TestRunner_H003_npm_outdated(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFiles(t, map[string]string{"package.json": pkgJSON, "node_modules/.package-lock.json": "{}"})
	fakeNpm(t, nil, `{
  "mermaid": {"current": "11.17.2", "wanted": "11.17.2", "latest": "12.2.0", "dependent": "app", "location": "node_modules/mermaid"},
  "vite": {"wanted": "8.3.5", "latest": "8.3.5", "dependent": "app"}
}`)

	res := runH003(t)
	want := ".: 2 outdated npm packages: mermaid 11.17.2 → 12.2.0, vite not installed → 8.3.5"
	if res.Passed || res.Details != want {
		t.Errorf("got %+v, want details %q", res, want)
	}
}

func TestRunner_H003_nothing_to_read(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFiles(t, map[string]string{"package.json": pkgJSON})
	fakeNpm(t, nil, "")

	if res := runH003(t); res.Passed || !strings.Contains(res.Details, "no package-lock.json") {
		t.Errorf("got %+v", res)
	}
}
