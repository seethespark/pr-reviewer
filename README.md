# pr-reviewer

AI-powered pull request review tool. It reads a `PR-REVIEW.md` configuration file from your repository, runs deterministic hygiene checks, and dispatches AI agents (via Ollama) to review the diff from multiple angles — then posts the results as comments on the pull request.

Every merge to `main` publishes a new binary release automatically.

---

## How it works

1. Reads `PR-REVIEW.md` from the repository root to learn the project's context and which agents/checks to run
2. Computes the diff between base and head commits (excluding vendored files, lock files, generated code)
3. Runs ticked hygiene checks deterministically — no AI required
4. Gathers review context: the PR title and description, human discussion on the PR, the full contents of changed files, and a repository file listing — so agents judge the change in context instead of guessing from hunks
5. Splits the diff into chunks sized against the model's context window, then runs every agent×chunk pair through a shared worker pool (`--concurrency`); agents report **structured findings** (file, line, severity, rationale, suggested fix)
6. Runs a **verification pass** on the critical/high findings — the ones that gate the check — dismissing anything speculative or explained as intentional (dismissals are listed in the report for auditability). Medium/low findings are published without it and marked as such
7. Posts (or updates) a comment per agent on the pull request, plus **inline review comments** on the exact lines where findings map to the diff
8. Exits with code `1` if any verified finding is critical or high severity — or, by default, if the review itself could not complete (see fail modes)


### Intentional findings

Reviews respect the author's stated intent, from three sources:

- **PR description and discussion** — human explanations on the PR ("the retry is deliberate, upstream flakes") are included in agent prompts and honored.
- **`pr-review:allow` directives** — an added code comment of the form `// pr-review:allow <topic> <reason>` suppresses matching findings for that code. A directive **without a reason is ignored**, and honored suppressions are surfaced in the report rather than silently dropped.
- **Previous reviews** — on re-runs, each agent sees its own prior report and does not blindly re-raise addressed findings.

### Fail modes

By default the tool **fails closed**: if agents error out, time out, or return unparseable results, the check fails — a green tick always means the code was actually reviewed. Set `--fail-mode open` to make infrastructure failures advisory (exit 0) while still failing on critical/high findings.

On timeout, whatever findings the agents had already produced are still posted, and each report names the files that were not reviewed. The check still fails in closed mode — a partial review is not a pass — but the work already done isn't discarded.

### Large pull requests

Every chunk is a separate provider round-trip, so chunk count, not diff size, is what drives review time. Three things keep it bounded:

- **Each agent is chunked from its own filtered diff.** The `Paths` column scopes an agent to the files it actually reviews, and the filtering happens *before* chunking — a database reviewer scoped to the store package gets one small chunk instead of every chunk of the PR. This is usually the largest single saving on a big PR; without it, review cost is agents × chunks no matter how narrow each role is.
- **Chunks are sized from the model's context window**, not a fixed constant. Kimi K2.6's 262K-token window comfortably holds a 150 KB diff chunk plus file context, so most PRs are a single call per agent. A model with a smaller window automatically gets smaller chunks and less file context rather than a prompt the server would silently truncate.
- **Agent×chunk pairs run through one shared worker pool.** Raising `--concurrency` shortens large reviews directly; lower it if you hit provider rate limits.
- **File context is a window around each hunk**, not the whole file — roughly a third of the tokens, spread evenly over every changed file instead of dumping the first few in full and omitting the rest.
- **Verification is one call per agent**, batched after the review grid and given only the hunks its findings point at, rather than one call per chunk carrying the whole chunk.

Nothing is dropped to save time: every file in an agent's scope is chunked and reviewed, oversized files are split at hunk boundaries rather than truncated, and if the budget runs out the report says which files it missed. An agent whose paths match nothing in a PR reports that plainly and does not fail the check. If reviews still time out, widen `--review-timeout`, raise `--concurrency`, or narrow the `Paths` columns before considering `--verify=false` or `--file-context=false`.

---

## Installation

### Download binary (recommended)

Download the latest release from the [Releases page](https://github.com/seethespark/pr-reviewer/releases) for your platform:

| Platform | Archive |
|---|---|
| Linux x86-64 | `pr-reviewer_linux_amd64.tar.gz` |
| Linux ARM64 | `pr-reviewer_linux_arm64.tar.gz` |
| macOS x86-64 | `pr-reviewer_darwin_amd64.tar.gz` |
| macOS ARM64 | `pr-reviewer_darwin_arm64.tar.gz` |
| Windows x86-64 | `pr-reviewer_windows_amd64.zip` |

### Install with Go

```bash
go install github.com/seethespark/pr-reviewer/cmd/review@latest
```

### Build from source

```bash
git clone https://github.com/seethespark/pr-reviewer.git
cd pr-reviewer
make build        # produces ./pr-reviewer binary
make test         # run tests with -race
```

---

## Usage

```bash
pr-reviewer \
  --pr 42 \
  --repo owner/repo \
  --base abc123 \
  --head def456 \
  --config PR-REVIEW.md
```

### Subcommands

```bash
pr-reviewer check-config [path]   # validate PR-REVIEW.md and show what a review would run
```

Run `check-config` in CI (or a pre-commit hook) on repos that edit their `PR-REVIEW.md`, so a config typo is caught when it's introduced rather than on the next PR.

### Flags

| Flag | Default | Description |
|---|---|---|
| `--pr` | required | Pull request number |
| `--repo` | required | Repository in `owner/name` format |
| `--base` | required | Base commit SHA |
| `--head` | required | Head commit SHA |
| `--config` | `PR-REVIEW.md` | Path to the config file |
| `--provider` | `ollama` | AI provider: `ollama` or `anthropic` |
| `--model` | provider default | Model name (`kimi-k2.6:cloud` / `claude-sonnet-5`) |
| `--fail-mode` | `closed` | `closed`: failures/timeouts/unparseable results fail the check; `open`: advisory |
| `--verify` | true | Second-pass verification of each agent's findings |
| `--inline-comments` | true | Post findings as inline review comments on diff lines |
| `--file-context` | true | Include source around each hunk (budgeted) in prompts |
| `--review-timeout` | 600 | Seconds allowed for the AI review phase (0 = unlimited) |
| `--concurrency` | 6 | Maximum provider calls in flight at once, across all agents and chunks |
| `--max-chunk-bytes` | 0 | Override the per-chunk diff byte ceiling (0 = derive from the model's context window) |
| `--skip-agents` | false | Run hygiene checks only, skip AI agents |
| `--dry-run` | false | Print reports to stdout instead of posting (only `--base`/`--head` required) |
| `--coverage-file` | | Path to `go tool cover -func` output to include in prompts |
| `--debug` | false | Enable debug logging |

### Environment variables

| Variable | Description |
|---|---|
| `GITHUB_TOKEN` | GitHub token with `pull-requests: write` permission |
| `OLLAMA_API_KEY` | API key for the Ollama endpoint (provider `ollama`) |
| `OLLAMA_ENDPOINT` | Override the Ollama API URL (default: `https://ollama.com/api/chat`) |
| `ANTHROPIC_API_KEY` | API key for the Anthropic API (provider `anthropic`) |

---

## PR-REVIEW.md configuration

Place a `PR-REVIEW.md` file in the root of your repository. The tool reads this file on every run. See [`example/PR-REVIEW.md`](example/PR-REVIEW.md) for a complete template.

### Required sections

```markdown
## Owner
Your Name / Team Name

## Context
Describe what this repository does in 2–3 sentences. The agents use this as
system context when reviewing the diff.

## Production Status
Live / Beta / Internal / Staging

## Security Level
High / Medium / Low
```

### Optional sections

```markdown
## Connected Systems
- PostgreSQL database
- Stripe payments API
- Downstream analytics pipeline

## Intended Audience
Internal engineering teams; external API consumers via documented contracts.

## Audit Level
High
```

### PR Hygiene

Enable deterministic checks with `- [x]`. Disable with `- [ ]`.

```markdown
## PR Hygiene
- [x] H001 README exists
- [x] H002 CODEOWNERS exists
- [ ] H003 Dependencies up to date
- [x] H006 Dev container configured
```

| Check | ID | What it verifies |
|---|---|---|
| README exists | H001 | `README.md` present at repo root |
| CODEOWNERS exists | H002 | `.github/CODEOWNERS` present |
| Dependencies up to date | H003 | `go list -u -m all` (Go) or `npm outdated` (Node) |
| Dev container configured | H006 | `.devcontainer/devcontainer.json` present |

### Code Review Agents

Configure agents as a Markdown table. Each row is one agent invocation.

```markdown
## Code Reviews
| Plugin | Subagent | Paths | Additional |
| voltagent-qa-sec | architect-reviewer | | |
| voltagent-qa-sec | penetration-tester | `api/`, `!*_test.go` | focus on input validation |
| voltagent-data-ai | database-optimizer | `internal/store/`, `*.sql` | |
```

**Plugin**: the agent plugin namespace (used in the system prompt).  
**Subagent**: the specific reviewer role within the plugin.  
**Paths**: optional file patterns scoping what this agent reviews — see below.  
**Additional**: optional extra instructions appended to that agent's prompt.

Columns are matched by **header name**, not position, so the order is free and
a table without a `Paths` column keeps working unchanged.

#### Scoping agents with `Paths`

An empty `Paths` cell means the whole diff. Otherwise the agent is sent only
the matching files, chunked on its own, and its prompt says so — an agent must
know it is seeing a subset, or it will report code it cannot see as missing.

Patterns are comma- or space-separated, and a leading `!` subtracts:

| Pattern | Matches |
| ------- | ------- |
| `internal/store/` or `internal/store/**` | that directory, at any depth |
| `web/src/lib/server/*.ts` | a path glob (contains `/`) |
| `*.sql` | a basename glob |
| `spec.md` | that exact path, or that basename anywhere |
| `!*_test.go` | subtracted from whatever the other patterns matched |

Scoping is a real trade: what an agent is not sent, it cannot find. Keep at
least one agent unscoped so every changed file is read by somebody, and use
`review check-config` to print each agent's resolved scope before relying on it.

#### Excluding paths repository-wide

```markdown
## Excluded Paths
- `PROGRESS.md`
- `generated/`
```

Kept out of every agent's diff, on top of the built-in defaults (vendored
trees, lockfiles, generated code, `CLAUDE.md`, `PR-REVIEW.md`). Same pattern
vocabulary, without `!`.

---

## GitHub Actions integration

The recommended path for rolling out across multiple repositories is the bundled composite action — each repo gets a five-line workflow and upgrades are centralized:

```yaml
name: PR Review

on:
  pull_request:
    branches: [main]
    types: [opened, synchronize, reopened]

permissions:
  contents: read
  pull-requests: write

concurrency:
  group: pr-review-${{ github.event.pull_request.number }}
  cancel-in-progress: true

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0   # required: the reviewer diffs base..head locally

      - uses: seethespark/pr-reviewer@main   # or pin: seethespark/pr-reviewer@v1.2.3
        with:
          api-key: ${{ secrets.OLLAMA_API_KEY }}
          # provider: anthropic          # optional overrides
          # fail-mode: open
          # concurrency: "10"            # more calls in flight on big PRs
          # extra-args: --verify=false
```

Add `OLLAMA_API_KEY` (or `ANTHROPIC_API_KEY` via `provider: anthropic`) as a repository or organization secret. The `github-token` input defaults to the workflow's own token.

**Fork PRs**: the standard `pull_request` trigger does not expose secrets to PRs from forks, so AI review silently cannot run there — with the default `fail-mode: closed`, those runs fail explicitly rather than pretending to have reviewed. Keep human review mandatory for fork PRs.

<details>
<summary>Manual installation (without the composite action)</summary>

```yaml
      - name: Install pr-reviewer
        run: |
          gh release download \
            --repo seethespark/pr-reviewer \
            --pattern 'pr-reviewer_linux_amd64.tar.gz' \
            -D /tmp/pr-reviewer
          tar -xzf /tmp/pr-reviewer/pr-reviewer_linux_amd64.tar.gz -C /usr/local/bin
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}

      - name: Run PR Review
        run: |
          pr-reviewer \
            --pr "${{ github.event.pull_request.number }}" \
            --repo "${{ github.repository }}" \
            --base "${{ github.event.pull_request.base.sha }}" \
            --head "${{ github.event.pull_request.head.sha }}" \
            --config PR-REVIEW.md
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          OLLAMA_API_KEY: ${{ secrets.OLLAMA_API_KEY }}
```

</details>

---

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Review completed; no critical or high findings |
| `1` | Critical or high severity finding detected — or, with the default `--fail-mode closed`, the review could not complete (agent failure, timeout, unparseable results) |

## Prompt-injection hardening

The diff, PR description, and discussion are attacker-controlled text that ends up inside AI prompts. Agents are instructed to treat those sections strictly as data and to report (as a HIGH finding) any text that tries to steer the review, and the verification pass re-checks findings against the diff. This reduces but cannot eliminate the risk — for repositories where a malicious PR author is a realistic threat, keep a human approval requirement in branch protection alongside this tool.

---

## Releases

Every merge to `main` automatically creates a new patch release and publishes multi-platform binaries via goreleaser. Pin a specific version in your workflow to avoid unexpected changes.

---

## Development

```bash
make build    # build binary
make test     # go test -race ./...
make vet      # go vet ./...
make lint     # golangci-lint run
make clean    # remove built binary
```
