package hygiene

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// npmLatest returns the registry's latest version of a package. It runs in the
// package's directory so that directory's .npmrc applies. A variable so tests
// don't need the registry.
var npmLatest = func(ctx context.Context, dir, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "npm", "view", name, "version")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// npmOutdated runs `npm outdated --json`, which exits 1 when anything is
// outdated, so only a run with no JSON on stdout is an error. A variable so
// tests don't need npm.
var npmOutdated = func(ctx context.Context, dir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "npm", "outdated", "--json")
	cmd.Dir = dir
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if err == nil || (errors.As(err, &exitErr) && len(out) > 0) {
		return out, nil
	}
	return nil, err
}

// lookupConcurrency bounds the `npm view` calls running at once.
const lookupConcurrency = 8

// staleDep is a direct dependency whose locked version is not the latest.
type staleDep struct{ name, current, latest string }

// checkNodeDepsInDir compares each direct dependency's locked version with the
// registry's latest.
//
// It reads package-lock.json rather than relying on `npm outdated` alone: in CI
// nothing is installed, and `npm outdated` with no node_modules reports every
// runtime dependency as outdated (it has no current version) and skips dev
// dependencies entirely. Installing first would run the PR's install scripts.
// With no lockfile it falls back to `npm outdated` when node_modules exists.
func (r *Runner) checkNodeDepsInDir(ctx context.Context, c Check, dir string) Result {
	fail := func(format string, args ...any) Result {
		return Result{ID: c.ID, Name: c.Name, Passed: false, Details: dir + ": " + fmt.Sprintf(format, args...)}
	}

	var stale []staleDep
	var unchecked []string
	lock, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	switch {
	case err == nil:
		pkg, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			return fail("reading package.json: %v", err)
		}
		stale, unchecked, err = staleFromLock(ctx, dir, pkg, lock)
		if err != nil {
			return fail("%v", err)
		}
	case errors.Is(err, os.ErrNotExist):
		if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
			return fail("no package-lock.json and nothing installed, so the versions in use are unknown")
		}
		out, err := npmOutdated(ctx, dir)
		if err != nil {
			return fail("npm outdated failed: %v", err)
		}
		if stale, err = staleFromOutdated(out); err != nil {
			return fail("reading npm outdated output: %v", err)
		}
	default:
		return fail("reading package-lock.json: %v", err)
	}

	if len(stale) == 0 && len(unchecked) == 0 {
		return Result{ID: c.ID, Name: c.Name, Passed: true, Details: dir + ": all npm packages up to date"}
	}
	var parts []string
	if len(stale) > 0 {
		names := make([]string, len(stale))
		for i, s := range stale {
			names[i] = fmt.Sprintf("%s %s → %s", s.name, s.current, s.latest)
		}
		parts = append(parts, fmt.Sprintf("%d outdated npm packages: %s", len(stale), strings.Join(names, ", ")))
	}
	if len(unchecked) > 0 {
		parts = append(parts, fmt.Sprintf("could not check %s", strings.Join(unchecked, ", ")))
	}
	return fail("%s", strings.Join(parts, "; "))
}

// staleFromLock finds the direct dependencies in package.json whose version in
// the lockfile is not the registry's latest. Dependencies that don't come from
// the registry (file:, git, aliases and the like) are skipped. Those whose
// version or latest can't be found are returned as unchecked.
func staleFromLock(ctx context.Context, dir string, pkgJSON, lockJSON []byte) (stale []staleDep, unchecked []string, err error) {
	var pkg struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal(pkgJSON, &pkg); err != nil {
		return nil, nil, fmt.Errorf("reading package.json: %w", err)
	}
	// Lockfile v2 and v3 key packages by install path; v1 has only "dependencies".
	var lock struct {
		Packages     map[string]struct{ Version string } `json:"packages"`
		Dependencies map[string]struct{ Version string } `json:"dependencies"`
	}
	if err := json.Unmarshal(lockJSON, &lock); err != nil {
		return nil, nil, fmt.Errorf("reading package-lock.json: %w", err)
	}
	locked := func(name string) string {
		if p, ok := lock.Packages["node_modules/"+name]; ok {
			return p.Version
		}
		return lock.Dependencies[name].Version
	}

	direct := map[string]string{}
	for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies, pkg.OptionalDependencies} {
		for name, spec := range deps {
			// A registry spec is a range or a dist-tag; anything with ":" or "/" is a
			// path, URL, git repo or alias.
			if strings.ContainsAny(spec, ":/") {
				continue
			}
			if v := locked(name); v != "" {
				direct[name] = v
			} else {
				unchecked = append(unchecked, name+" (not in the lockfile)")
			}
		}
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, lookupConcurrency)
	)
	for name, current := range direct {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			latest, err := npmLatest(ctx, dir, name)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil || latest == "":
				unchecked = append(unchecked, name+" (registry lookup failed)")
			case latest != current:
				stale = append(stale, staleDep{name, current, latest})
			}
		}()
	}
	wg.Wait()

	sort.Slice(stale, func(i, j int) bool { return stale[i].name < stale[j].name })
	sort.Strings(unchecked)
	return stale, unchecked, nil
}

// staleFromOutdated reads `npm outdated --json`, an object keyed by package
// name. A package that isn't installed has no current version and is reported
// as such.
func staleFromOutdated(out []byte) ([]staleDep, error) {
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, nil
	}
	var entries map[string]struct{ Current, Latest string }
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, err
	}
	var stale []staleDep
	for name, e := range entries {
		current := e.Current
		if current == "" {
			current = "not installed"
		}
		if current != e.Latest {
			stale = append(stale, staleDep{name, current, e.Latest})
		}
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].name < stale[j].name })
	return stale, nil
}
