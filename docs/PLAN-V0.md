# jog v0 — implementation plan

**Status:** planned, not started · **Last updated:** 2026-08-08 · **Parent:** [DESIGN.md](DESIGN.md) §11

v0 is the dogfood cut: everything needed to be *protected* and to *recover*,
nothing needed to be pretty. Ship when jog is snapshotting this repo and at
least one other real repo daily, via both the alias and Claude hooks.

## 1. Scope

**In:**

- Snapshot engine (DESIGN §4, verbatim recipe, every gotcha as code comment + test)
- `jog` (bare snapshot), `jog -m "msg"`
- `jog snaps [path] [-p]` — timeline read path
- `jog back <path> [--at T]`, `jog back --all --at T` — restore path
- `jog hook claude` — PreToolUse + UserPromptSubmit entry point
- git passthrough (`jog <anything-else>` → snapshot, then `exec git`)
- `~/.claude/settings.json` wiring + alias install docs (README)

**Out (v1/v2):** `since`, `pick`, `trim`/retention, `doctor`, `mcp`, brew tap,
prompt segment, Windows. Consequence of no `trim` in v0: loose objects
accumulate and reflog entries never expire — acceptable for the dogfood window;
`git gc` still runs manually if needed (snapshots survive it, verified).

## 2. Repo layout

```
docs/                  DESIGN.md, this plan
cmd/jog/main.go        thin: parse argv, dispatch, exit codes
internal/gitx/         git subprocess layer: repo discovery, run helpers,
                       --no-optional-locks reads, env plumbing
internal/snap/         the engine: shadow index, tree write, commit, CAS ref update
internal/provenance/   message building/parsing (`pre:` / `claude[sid]:` / `manual:`)
internal/cli/          snaps, back, passthrough, hook
internal/testrepo/     integration-test fixture: temp repos driving real git
```

- Go module path: placeholder `github.com/tyler-johnson/jog` until the
  name-collision sweep (DESIGN open question 1). That sweep gates *publishing*,
  not v0 — dogfood is local `go install`.
- Dependencies: stdlib only in v0. Bubble Tea and the MCP SDK arrive with
  their features (v1/v2).
- Toolchain: latest stable Go; `go test ./...` + `go vet` in a GitHub Actions
  workflow from M0 (integration tests need only `git` on the runner).

## 3. Work breakdown

Milestones are sequential; each lands with its tests. Dogfood begins at M4.

### M0 — scaffold (S) — ✅ done

- [x] `docs/` folder: DESIGN.md + this plan live there
- [x] `go mod init`, `cmd/jog` hello-world dispatch (reserved verbs stubbed
      with their milestone numbers), CI workflow (vet + test + build)
- [x] `internal/testrepo`: create temp repo, write files, run git, assert on
      index bytes / refs / trees. This fixture is the backbone of everything
      after it — build it first, not per-test. Host git config is masked
      (`GIT_CONFIG_GLOBAL=/dev/null`) so tests behave identically everywhere.

### M1 — git layer (S) — ✅ done

- [x] Repo discovery via `git rev-parse --absolute-git-dir` (worktree-aware:
      in a linked worktree this returns `.git/worktrees/<name>`, so the shadow
      index at `$GIT_DIR/jog/index` is naturally per-worktree — no sharing, no
      collisions; matches DESIGN §3 worktree story). Also detects bare repos
      (nothing to snapshot there).
- [x] Run helpers: `Run` (captured), `RunRead` (adds `--no-optional-locks`),
      `WithIndex` for `GIT_INDEX_FILE` shadow ops (absolute paths enforced —
      relative resolves inside the worktree, per the verified gotcha)
- [x] Not-a-repo detection distinguished from other failures (hooks need
      silent exit 0; user commands need a real error): `ErrNotARepo` sentinel
      vs `*GitError` with exit code + stderr

### M2 — snapshot engine (L, the core) — ✅ done

- [x] §4 recipe in Go: branch → ref resolution (incl. `@detached`), seed-once
      shadow index, `add -A` with `advice.addEmbeddedRepo=false`, `write-tree`,
      no-op tree compare, `commit-tree` with parent-1 = prev snap /
      parent-2 = HEAD, unborn-HEAD branch, `update-ref --create-reflog` CAS
      (empty old value on creation = "must not exist")
- [x] Fixed snapshot identity: author+committer `jog <jog@local>`. This is
      load-bearing, not cosmetic — see M5 (first-parent walk termination).
- [x] Sparse-checkout detection (`core.sparseCheckout`) → re-seed from HEAD
      each snapshot on those repos (accept cost, per verified gotcha)
- [x] `jog.maxFileSize` guard (default 50 MiB) — approach in §4 below;
      skipped list recorded in the commit message body
- [x] Shadow-index contention policy: concurrent snapshots serialize on git's
      own `<shadow>.lock`. On lock failure: one short retry (~50 ms), then
      **skip the snapshot** (never block, never fail the wrapped command; the
      concurrent winner captured a near-identical tree). CAS already protects
      the ref. Both contention paths report `Contended`, not an error.
- [x] Lazy per-repo gc config: on first creation of a `refs/jog/*` ref, set
      `gc.refs/jog/*.reflogExpire=never` + `.reflogExpireUnreachable=never`
      (decision D3 below)
- [x] Test matrix rows 1–14, 19, 20 (15–18 land with their milestones)

### M3 — CLI dispatch + passthrough (M)

- [ ] Reserved-verb table: v0 verbs live; `since`/`pick`/`trim`/`mcp`/`doctor`
      recognized but stubbed ("not in v0") — reserving them *now* means adding
      them later never changes passthrough semantics
- [ ] Bare `jog` / `jog -m "msg"` → snapshot with `manual:` provenance
- [ ] Passthrough: snapshot with `pre: git <args>` provenance, then
      `syscall.Exec` the real git (`exec.LookPath("git")`; alias-only install
      means no self-recursion risk) — real TTY, real exit code
- [ ] **Snapshot failure never blocks the command:** any engine error on the
      passthrough path warns on stderr and execs git anyway
- [ ] Outside a repo, passthrough still execs git cleanly (no snapshot, no noise)

### M4 — `jog hook claude` (M) → **dogfood starts**

- [ ] Parse hook JSON from stdin: `hook_event_name`, `session_id`, `cwd`,
      `tool_name` + `tool_input` (Bash `command`, Edit/Write `file_path`),
      UserPromptSubmit `prompt`
- [ ] Provenance: `claude[<sid[:8]>]: Bash(<cmd>)` / `Edit(<path>)` /
      `prompt "<text>"` — single line, truncated ~80 chars
- [ ] Iron rule: **always exit 0** — malformed JSON, not a repo (use `cwd`
      from the payload, not process cwd), engine error, anything. Diagnostics
      only behind `JOG_DEBUG=1`.
- [ ] Wire my own `~/.claude/settings.json` (DESIGN §6 snippet) + shell alias;
      begin daily dogfood on this repo

### M5 — `jog snaps` (M)

- [ ] `git log --first-parent refs/jog/<branch>` with a custom format: short
      id, reflog-style age, provenance, files-changed summary (`--name-status`
      vs parent 1); `-p` appends patches; optional path filter
- [ ] **Walk termination:** the oldest snapshot's parent 1 is a *real* HEAD
      commit (recipe: when `PREV` is empty, the only parent is HEAD), so a
      naive first-parent walk runs off the snapshot chain into real history.
      Stop at the first commit whose committer isn't `jog <jog@local>`.
- [ ] Default scope: current branch's chain (DESIGN open question 5 — decided
      for v0, see D5). No `--all` until v1.
- [ ] List files skipped by `maxFileSize` (recorded per D2)
- [ ] Bare `jog`: after snapshotting, print the top few timeline entries (D6)

### M6 — `jog back` (M)

- [ ] Resolve `--at T`: snap id (short sha) or git reflog time syntax against
      `refs/jog/<branch>@{…}`; default = newest snapshot. Past-oldest time
      queries warn + use oldest, exit 0 (verified git behavior, keep it).
- [ ] **Restore is itself snapshotted first** (`pre: jog back …`) — undo is
      undoable
- [ ] Single file: `git restore --source=<snap> --worktree -- <path>`
      (never `checkout <snap> -- path`; it stages — verified trap)
- [ ] `--all`: diff target snapshot against the just-taken pre-restore
      snapshot (`git diff --name-status <target> <fresh>`); restore
      modified/deleted paths, **delete** paths added since the target. Plain
      `restore --source` alone can't delete — this diff-driven pass is what
      makes `--all` honest. Never touches ignored files (they're in neither
      tree).
- [ ] Index byte-identical before/after (invariant test)

### M7 — docs + dogfood exit (S)

- [ ] README: what/why (borrow DESIGN's elevator pitches), `go install`,
      alias install per shell, settings.json snippet, recovery cookbook
      (three worked examples: deleted file, pre-refactor diff, full rollback),
      leak-vector note (`push --mirror` etc.), accepted gaps
- [ ] Exit checklist (§7 below)

## 4. Forced decisions (beyond DESIGN.md)

Things the design leaves open that v0 must pick. Recorded here so DESIGN.md
stays clean and future-us knows what was chosen and why.

- **D1 — snapshot commit identity.** Fixed `jog <jog@local>` author/committer,
  `GIT_{AUTHOR,COMMITTER}_DATE` = snapshot time. Gives `snaps` its walk
  terminator, makes jog commits machine-identifiable forever, and avoids
  leaking the user's identity into synthetic commits.
- **D2 — `maxFileSize` mechanism.** Pre-scan `git status --porcelain -z`
  (read-only, no-optional-locks), `stat` any added/modified paths over the
  threshold, and exclude them from the engine's `add -A` via
  `:(exclude)` pathspecs; warn on stderr and record the skipped list in the
  snapshot commit message body (so `snaps` can surface it with no side
  channel). Costs one status call per snapshot with a candidate — acceptable;
  revisit if it shows up in the perf budget.
- **D3 — when the gc config gets written.** v0 has no `init`/`doctor`, but
  without `gc.refs/jog/*.reflogExpire=never` a manual `git gc` could expire
  jog reflog entries. Chosen: write the two per-repo keys lazily when a
  `refs/jog/*` ref is first created, print a one-line notice on non-hook
  invocations (hooks stay silent). This bends DESIGN §9 invariant 4
  ("only at init with consent") — for dogfood-on-own-repos that consent is
  real; v1's `doctor`/init flow makes it explicit. Flagged in README.
- **D4 — engine errors on hook/passthrough paths never propagate.** Warn (or
  stay silent, for hooks) and let the user's action proceed. jog failing
  closed would violate its own reason to exist.
- **D5 — `snaps` scope.** Current-branch chain only (git-like), per the
  design's lean on open question 5. `--all` interleaving is v1 work with
  `pick`.
- **D6 — bare `jog` shows the timeline after snapshotting.** jj's no-arg
  default is `jj log` (after its implicit snapshot); bare `jog` mirrors that:
  snapshot first (unchanged from DESIGN §5), then print the last few `snaps`
  entries. Lands with M5, where the formatting exists. The full graph/forest
  view is v1 (`pick --all` territory).
- **D7 — no `st` verb, ever.** The `jj st` analog is `jog since` (v1:
  worktree vs last snapshot). `st` is one of the most common *user git
  aliases*; with `alias git=jog`, a jog-reserved `st` would intercept
  `git st` before git's alias machinery sees it — the exact collision the
  reserved-verb rule exists to prevent.

## 5. Test matrix

Integration tests against real git via `internal/testrepo`, one per verified
fact from DESIGN §4's table — the table *is* the spec:

| # | Test | Verifies |
|---|---|---|
| 1 | index bytes hash-identical across snapshot | invariant 1 |
| 2 | snapshot succeeds while `.git/index.lock` exists | shadow isolation |
| 3 | mid-merge: conflict markers captured, MERGE_HEAD + stage 1/2/3 intact | conflicted states are valid |
| 4 | unborn HEAD repo snapshots (no `read-tree HEAD`) | unborn branch of recipe |
| 5 | detached HEAD → `refs/jog/@detached` chain | ref resolution |
| 6 | sparse checkout: excluded files not silently dropped (re-seed path) | sparse gotcha |
| 7 | `.gitignore` + `info/exclude` respected; ignored never snapshotted | scope |
| 8 | embedded repo → gitlink, no advice warning | submodule gotcha |
| 9 | exec bit + symlink modes preserved | 100755/120000 |
| 10 | second identical snapshot: no new commit, ref unmoved | no-op fast path |
| 11 | stale-`old` CAS update-ref fails; winner's snapshot intact | concurrency |
| 12 | reflog exists on the ref; `@{1.minute.ago}` resolves | reflog creation |
| 13 | `git gc --prune=now`: all snapshots survive | gc protection |
| 14 | file > maxFileSize skipped + warned + listed | D2 |
| 15 | `back` (file + `--all`): index untouched; `--all` deletes post-snapshot files | restore semantics |
| 16 | `snaps` walk stops at real-history boundary | D1 terminator |
| 17 | passthrough propagates git's exit code; works outside a repo | shim contract |
| 18 | `hook claude`: exit 0 on garbage stdin / non-repo cwd / engine failure | iron rule |
| 19 | branch with `/` in name → valid `refs/jog/feature/x` chain | ref hygiene |
| 20 | perf smoke: warm no-op on generated 5k-file repo, report ms (non-gating, printed in CI log; budget: no-op ≤ 30 ms, snapshot ≤ 100 ms) | §4 budget |

## 6. Risks

- **Engine perf regressions are silent** — the ~30× re-seed cliff only shows
  up as sluggishness. Mitigation: test 20 prints timings in CI from day one.
- **Perf finding (M2, 2026-08-08):** warm no-op ≈ 51 ms on a 2k-file repo on
  the Pi 5, vs an 11.5 ms `git status` baseline — ratio 4.4× where the budget
  implies ~1.3×. Dominated by the D2 status pre-scan (a second full tree
  walk) plus ~9 subprocess spawns per snapshot. Consolidation candidates,
  deferred until after dogfood: merge the two `config` reads into one
  `--get-regexp` spawn; combine rev-parse calls; make the oversize pre-scan
  conditional or persisted. Architecture is right; this is spawn arithmetic.
- **Hook payload drift** — Claude Code hook JSON is external surface; parse
  defensively (unknown fields ignored, missing fields → generic provenance,
  never non-zero exit).
- **`back --all` is the one command that writes the worktree** — it gets the
  densest tests (15) and ships last, after weeks of snapshot-side dogfood.
- **Dogfooding with a broken engine could feel safe while capturing nothing.**
  Until `doctor` (v1): `snaps` is the liveness check — make its empty/stale
  states loud ("no snapshots on this branch yet").

## 7. v0 exit checklist

- [ ] All matrix tests green in CI; timings printed and within budget locally
- [ ] Alias + hooks live in my environment; snapshots accruing on ≥ 2 real
      repos for ≥ 1 week
- [ ] At least one *real* (unstaged) recovery performed via `jog back`
- [ ] `snaps` timeline readable and correctly scoped after a rebase and a
      branch switch on a dogfood repo
- [ ] README complete enough that a stranger could install and understand
      the leak vectors and accepted gaps
