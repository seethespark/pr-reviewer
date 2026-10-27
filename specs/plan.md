# Implementation Plan: Automate PR Review

**Feature Branch**: `015-automate-pr-review`  
**Created**: 2026-05-20  
**Status**: Draft  

---

## 1. Architecture Overview

The system is a Go CLI tool (`pr-review`) invoked by a GitHub Actions workflow. It:

1. Reads `PR-REVIEW.md` from the checked-out repository.
2. Runs ticked hygiene checks locally.
3. Computes the PR diff via `git diff`.
4. Invokes configured AI review agents sequentially through a pluggable `Provider` interface.
5. Posts or updates PR comments via the GitHub REST API.
6. Exits with a non-zero status if critical/high findings are found or if infrastructure fails.

```
GitHub Actions (ubuntu-latest)
  │
  ├─ checkout repo (merge commit)
  │
  └─ run pr-review CLI
        │
        ├─ Parse PR-REVIEW.md ──→ context + hygiene config + agent config
        │
        ├─ Run hygiene checks ──→ H001, H002, H003 (if ticked), H006
        │
        ├─ Compute PR diff ─────→ git diff base...head
        │
        ├─ For each agent:
        │     ├─ Build prompt (context + agent instructions + diff)
        │     ├─ Call Provider.Generate(ctx, model, prompt)
        │     ├─ Parse severity JSON from response
        │     └─ Collect report markdown
        │
        ├─ Post hygiene comment (update-in-place if exists)
        ├─ Post agent comments (update-in-place if exists)
        │
        └─ Exit code:
              0  → all clear or only medium/low findings
              1  → critical/high findings found
              1  → infrastructure error (ollama down, auth failed)
```

---

## 2. Directory Structure

```
woodendollars/
├── .github/
│   └── workflows/
│       └── pr-review.yml          # GitHub Actions workflow definition
├── tools/
│   └── pr-review/                 # Go module for the review CLI
│       ├── go.mod
│       ├── go.sum
│       ├── main.go                # entry point: flag parsing, orchestration
│       ├── cmd/
│       │   └── review.go          # cobra-style command or simple main
│       ├── internal/
│       │   ├── config/
│       │   │   └── parser.go      # PR-REVIEW.md markdown parser
│       │   ├── hygiene/
│       │   │   └── checker.go     # H001-H006 implementations
│       │   ├── provider/
│       │   │   ├── provider.go    # Provider interface
│       │   │   └── ollama.go      # Ollama HTTP API provider
│       │   ├── github/
│       │   │   └── client.go      # PR comment CRUD via REST API
│       │   ├── review/
│       │   │   └── review.go      # Agent prompt builder, report formatter
│       │   └── diff/
│       │       └── diff.go        # Git diff computation
│       └── tests/
│           └── ...
└── specs/015-automate-pr-review/
    └── ...
```

---

## 3. Components

### 3.1 PR-REVIEW.md Parser (`internal/config/parser.go`)

**Responsibility**: Parse the 9-section markdown file into a typed struct.

**Input**: `PR-REVIEW.md` bytes.
**Output**: `Config` struct:
```go
type Config struct {
    Owner           string
    Context         string
    ProductionStatus string
    ConnectedSystems string
    IntendedAudience string
    AuditLevel      string
    SecurityLevel   string
    Hygiene         []HygieneCheck
    Agents          []AgentConfig
}

type HygieneCheck struct {
    ID      string // "H001"
    Name    string
    Ticked  bool
}

type AgentConfig struct {
    Plugin     string
    Subagent   string
    Additional string
}
```

**Parsing strategy**: Section headers are markdown `##` lines. The hygiene section contains a `- [ ]` / `- [x]` tick list. The agents section is a markdown table. Use simple line scanning — no external markdown parser needed.

**Edge cases handled**:
- Missing file → return error (FR-003).
- Malformed table → skip malformed rows, log warning.
- Extra whitespace → normalize.

---

### 3.2 Hygiene Checker (`internal/hygiene/checker.go`)

**Responsibility**: Execute ticked hygiene checks and return a report.

**Checks**:

| ID   | Name | Implementation |
|------|------|----------------|
| H001 | README present and accurate | `os.Stat("README.md")` |
| H002 | CODEOWNERS exists | `os.Stat(".github/CODEOWNERS")` |
| H003 | Dependencies up to date | `go list -u -m all` (Go). Node: each direct dependency's version in `package-lock.json` against `npm view <name> version`, so nothing is installed and no install scripts run; `npm outdated --json` only when there is no lockfile but `node_modules` exists. Detect language by `go.mod` / `package.json` presence. Only runs if ticked. |
| H004 | Devcontainer definition exists | `os.Stat(".devcontainer/devcontainer.json")` |

**Output**: `[]CheckResult` with pass/fail status and optional explanation.

---

### 3.3 Diff Computer (`internal/diff/diff.go`)

**Responsibility**: Compute the PR diff from the checked-out merge commit.

**Command**: `git diff --merge-base origin/main...HEAD` or `git diff base...head` depending on Actions checkout depth.

**Output**: Diff text as string. If diff exceeds a configurable size limit (default 500KB), truncate and include a warning.

**Exclusions**: `vendor/`, `node_modules/`, `*.lock`, generated files. Configurable via a hardcoded list for v1.

---

### 3.4 Provider Interface (`internal/provider/provider.go`)

```go
type Provider interface {
    // Generate sends a prompt to the AI and returns the response.
    Generate(ctx context.Context, model string, prompt string) (Response, error)
}

type Response struct {
    Text string // raw response including severity JSON block
}
```

**First implementation**: `OllamaProvider` (`internal/provider/ollama.go`)

- Endpoint: `https://ollama.com/api/generate`
- Auth: `Authorization: Bearer $OLLAMA_API_KEY`
- Request body: `{"model": "kimi-k2.6:cloud", "prompt": "...", "stream": false}`
- Response: parse JSON, extract `response` field.

**Error handling**:
- HTTP timeout (10s dial, 120s overall) → retry once, then fail.
- Non-2xx status → fail immediately.
- Empty response → fail.

---

### 3.5 Review Orchestrator (`internal/review/review.go`)

**Responsibility**: Build agent prompts, invoke providers, parse responses, format reports.

**Prompt construction**:
1. Preamble: "You are a code review agent. Review the following diff and report findings."
2. Context block: sections 1-7 from PR-REVIEW.md concatenated.
3. Agent-specific instructions: from the `Subagent` name and `Additional` column.
4. Diff block: the computed PR diff.
5. Severity instruction: "Output a JSON severity summary block before your markdown report, like: `{"critical": 0, "high": 0, "medium": 0, "low": 0}`"

**Response parsing**:
- Extract the first JSON block matching the severity pattern.
- Parse into `SeveritySummary` struct.
- The remainder of the response is the markdown report.

**Report formatting**:
- Each agent report is markdown with a header containing the agent name.
- A hidden HTML marker is included for comment tracking: `<!-- wd-auto-review:agent=architect-reviewer -->`

---

### 3.6 GitHub Client (`internal/github/client.go`)

**Responsibility**: Post, update, and list PR comments.

**API**: GitHub REST API v3.
- `GET /repos/{owner}/{repo}/issues/{pr_number}/comments` — list existing comments.
- `POST /repos/{owner}/{repo}/issues/{pr_number}/comments` — create new comment.
- `PATCH /repos/{owner}/{repo}/issues/comments/{comment_id}` — update existing comment.

**Comment tracking**:
- Each comment includes a unique HTML marker: `<!-- wd-auto-review:type=hygiene -->` or `<!-- wd-auto-review:agent=architect-reviewer -->`.
- Before posting, list existing comments and search for the marker.
- If found, update that comment. If not, create a new one.

**Authentication**: `Authorization: token $GITHUB_TOKEN`.

---

### 3.7 GitHub Actions Workflow (`.github/workflows/pr-review.yml`)

```yaml
name: PR Review
on:
  pull_request:
    branches: [main]
    types: [opened, synchronize, reopened]

permissions:
  contents: read
  pull-requests: write

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0  # needed for git diff base...head

      - name: Run PR Review
        run: |
          # Download pr-review binary (or build from tools/pr-review/)
          cd tools/pr-review
          go build -o pr-review ./cmd/review
          ./pr-review \
            --pr ${{ github.event.pull_request.number }} \
            --repo ${{ github.repository }} \
            --base ${{ github.event.pull_request.base.sha }} \
            --head ${{ github.event.pull_request.head.sha }}
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          OLLAMA_API_KEY: ${{ secrets.OLLAMA_API_KEY }}
```

**Note**: For v1, build from source in the Action. For v2, publish a release binary and download it.

---

## 4. Implementation Phases

### Phase 1: Foundation (Day 1-2)

1. **Bootstrap Go module** (`tools/pr-review/go.mod`).
2. **Implement PR-REVIEW.md parser** with table-driven tests.
3. **Implement hygiene checker** (H001, H002, H006; H003 skeleton).
4. **Implement diff computer** using `os/exec` to call `git diff`.
5. **Implement GitHub client** for comment CRUD.
6. **Wire together in `main.go`**:
   - Parse flags.
   - Parse PR-REVIEW.md.
   - Run hygiene checks.
   - Compute diff.
   - Post hygiene report.
   - Exit.

**Deliverable**: Hygiene checks working end-to-end on a test PR.

### Phase 2: AI Provider Integration (Day 3-4)

1. **Define `Provider` interface**.
2. **Implement `OllamaProvider`** with timeout, retry, and error handling.
3. **Implement review orchestrator**:
   - Prompt builder.
   - Severity JSON parser.
   - Report formatter.
4. **Wire agent loop into `main.go`**.
5. **Add update-in-place logic** to GitHub client.

**Deliverable**: Full review pipeline working on a test PR.

### Phase 3: GitHub Actions Workflow (Day 5)

1. **Write `.github/workflows/pr-review.yml`**.
2. **Test on a fork or test repo**:
   - Verify trigger on PR open.
   - Verify comment posting.
   - Verify update-in-place on new commits.
   - Verify exit code (pass/fail) matches severity findings.
3. **Add `PR-REVIEW.md` to the woodendollars repo** with appropriate config.

**Deliverable**: Workflow running on real PRs.

### Phase 4: Hardening (Day 6)

1. **Handle large diffs**: truncate with warning.
2. **Handle long agent responses**: chunk or truncate comments.
3. **Add structured logging** (`slog`) for debugging in Actions logs.
4. **Add unit tests** for parser, severity parser, and GitHub client (mock HTTP).
5. **Add integration test**: mock ollama server, mock GitHub API, run full pipeline.

**Deliverable**: 80%+ test coverage, clean CI logs.

### Phase 5: Documentation (Day 7)

1. **Document `PR-REVIEW.md` format** in `tools/pr-review/README.md`.
2. **Document environment variables and flags**.
3. **Document adding a new provider**.
4. **Update woodenollars README** with a note about automated PR reviews.

**Deliverable**: Complete documentation.

---

## 5. Testing Strategy

### 5.1 Unit Tests

| Component | Coverage Target | Key Tests |
|-----------|-----------------|-----------|
| Parser | 90%+ | Valid PR-REVIEW.md, missing sections, malformed table, empty file |
| Hygiene | 80%+ | Each check pass/fail, H003 with/without go.mod/package.json |
| Diff | 70%+ | Small diff, large diff, empty diff, excluded files |
| Severity Parser | 90%+ | Valid JSON, missing JSON, malformed JSON, extra text |
| GitHub Client | 80%+ | Create, update, list comments, auth failure, rate limit |

### 5.2 Integration Tests

- **Mock ollama server**: A local HTTP server that returns canned responses with severity JSON. Run the full CLI against it.
- **Mock GitHub server**: A local HTTP server that accepts comment POST/PATCH and returns expected responses.
- **End-to-end**: A shell script that creates a temp repo with PR-REVIEW.md, runs the CLI, and asserts exit code + comment content.

### 5.3 CI

- Run `go test -race ./...` in `tools/pr-review/`.
- Run `go vet ./...`.
- Run `golangci-lint`.

---

## 6. Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| Go, not Python | Consistency with existing codebase (api/, cli/). Single compiled binary, no runtime dependencies. |
| CLI tool, not a library | Simplest integration with GitHub Actions. Can be published as a standalone binary later. |
| Provider interface | Future-proof. Adding Anthropic, OpenAI, or local ollama is a new file implementing the interface. |
| Issues Comments API, not Review API | Simpler — no line mapping needed. One comment per agent. Can migrate to Review API in v2. |
| Update-in-place via HTML markers | Clean PR threads. No duplicate comments. Marker is invisible in rendered markdown. |
| Sequential agent execution | Respects ollama/cloud provider rate limits and keeps complexity low. Parallel is a v2 enhancement. |
| `git diff` locally, not GitHub API | No API rate limits. Works with private repos automatically. Faster. |
| H003 only if ticked | Expensive (network calls). Must be opt-in. |

---

## 7. Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Ollama API latency >5min | PR review misses SLA | Add a 4.5min timeout; if exceeded, fail fast with a clear message. |
| Ollama API returns unstructured response | Severity parsing fails; can't determine pass/fail | If JSON block not found, treat as advisory (pass) and log a warning. |
| GitHub token lacks `pull-requests: write` | Comments can't be posted; Action fails | Document required permissions clearly in README. |
| Large diff (>500KB) exceeds model context | Review is truncated | Truncate diff, add warning to prompt, and note truncation in report. |
| Agent produces hallucinated findings | False positives annoy developers | Start as advisory (doesn't block merge unless CRITICAL/HIGH). Tune prompts over time. |

---

## 8. Success Criteria Mapping

| Spec SC | How Verified |
|---------|-------------|
| SC-001 (<5min) | CI timing on test PRs. Timeout set to 4.5min. |
| SC-002 (100% hygiene results) | Unit tests for each check. Integration test with ticked items. |
| SC-003 (critical/high fail) | Integration test with mock ollama returning critical findings. Assert exit code 1. |
| SC-004 (no duplicates) | Integration test: run twice, assert same comment ID updated. |
| SC-005 (infrastructure fails loudly) | Integration test with mock ollama returning 500. Assert exit code 1, no comments. |
| SC-006 (context-aware reviews) | Manual test: two repos, different PR-REVIEW.md, verify different agents invoked. |
| SC-007 (30% time savings) | Measured post-deployment via developer survey or time-tracking. |

---

## 9. Out of Scope (v1)

- Inline PR review comments (line-level annotations) — v2.
- Parallel agent execution — v2.
- Support for GitLab, Bitbucket, etc. — v2.
- Caching of diff or agent responses — v2.
- Dashboard or metrics collection — v2.
- Automatic fixing of hygiene issues — v2.
- Support for PRs against branches other than `main` — v2.
