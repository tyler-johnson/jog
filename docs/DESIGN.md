# jog — a memory for your working tree

**Status:** design approved, pre-implementation · **Last updated:** 2026-08-08

`jog` gives git users the one feature worth envying from Jujutsu — automatic
working-copy snapshots — without leaving git. No new VCS, no daemon, no changed
workflow. Every snapshot is ordinary git objects in the repo's own object
database, invisible to branches, index, teammates, and remotes.

Elevator pitches, pick per audience:

- *A working-tree reflog.* Git already has a command-triggered safety net for
  refs; jog extends the same idea to uncommitted changes.
- *The missing half of Claude Code's `/rewind`.* Claude's checkpoints restore
  conversation + Edit/Write changes but explicitly not bash-made changes,
  manual edits, or untracked files — and they expire (100 checkpoints / 30
  days). jog covers exactly that complement, at the same boundaries, with
  retention you control.
- *jj's snapshot model wearing git's skin.* Snapshot at the start of every
  command — but the commands are the git you already type.

---

## 1. Principles

1. **Never touch user state.** jog must never write the user's index, worktree
   (except explicit restore), HEAD, branches, or repo config. All reads use
   `--no-optional-locks` (plain `git status` opportunistically rewrites the
   index — lab-verified).
2. **Command-triggered, not watched.** No daemon, no filesystem watcher, no git
   hooks. Snapshots happen synchronously at command boundaries, causally
   *before* destructive operations. Deterministic and observable, like git.
3. **Shim, not porcelain.** jog never reimplements a git verb. Unknown
   subcommands snapshot, then `exec` the real git binary. Zero new semantics,
   zero drift, nothing to learn or unlearn.
4. **No lock-in.** Every snapshot must be fully recoverable with stock git
   alone. If jog vanishes, the data doesn't.
5. **Provenance over timestamps.** Every snapshot records *what command it ran
   ahead of*. The timeline is an op log, not an anonymous smear of states.

Non-goals: replacing git, syncing between machines (v2 option at most),
protecting against GUI-client discards (accepted gap), being a backup system
for ignored/build artifacts.

## 2. Architecture

One Go binary. Two write paths in, several read paths out:

```
  agent hooks ─────┐
  git alias (you) ─┼──►  jog snap engine ──► refs/jog/<branch> in .git
  manual `jog` ────┘                            │
                                                ├─► jog log / since / restore
                                                └─► jog mcp  (agent read access)
```

- **Language:** Go. Single static binary, brew-distributable, Bubble Tea for
  the `pick` TUI, official MCP Go SDK for `jog mcp`. GC pauses and binary
  startup (few ms) are irrelevant next to git subprocess spawns.
- **Git access:** shell out to the system `git` via `os/exec`. Never go-git —
  the verification below applies to real git; a reimplementation would void it.
  (go-git is at most a later optimization for the no-op check, behind the
  verified slow path.)
- **Config:** `git config jog.*` keys (global + per-repo), so configuration is
  native, discoverable, and needs no new file format. jog-private runtime state
  lives in `.git/jog/` (shadow index, nothing else of consequence).

## 3. Data model

### Storage

Snapshots are plain git blobs/trees/commits in the repo's own odb. Unchanged
files cost zero bytes (content-addressed reuse); repack deltas snapshot blobs
against each other and real history. There is **no jog database**.

### Refs

- One chain per branch: `refs/jog/<branch>`. Detached HEAD → `refs/jog/@detached`.
- Each snapshot commit has two parents:
  - **parent 1: previous snapshot on the chain** — the timeline. Order matters:
    this makes `git log --first-parent refs/jog/main` a clean timeline
    (verified; HEAD-first collapses it).
  - **parent 2: the HEAD commit at snapshot time** — the base edge. Records
    what commit you were on; ties the snapshot layer into the real DAG; keeps
    pre-rebase history reachable (reflog-grade protection, but queryable).
- Reflogs enabled via `git update-ref --create-reflog` (refs outside
  refs/heads|remotes get **no reflog by default** — verified). Reflog powers
  `@{20.minutes.ago}` time queries.
- Ref updates use the CAS form (`update-ref <ref> <new> <old>`) so concurrent
  triggers can't clobber each other (verified).
- gc protection: reachability from `refs/jog/*` survives `git gc --prune=now`
  (verified). Set `gc.refs/jog/*.reflogExpire=never` and
  `.reflogExpireUnreachable=never` per-repo on init — per-ref-pattern config is
  honored and wins over globals (verified). jog's own `trim` is the only thing
  that expires snapshots.

### Topology across git operations

- **Branch switch:** nothing migrates; next snapshot lands on the new branch's
  chain. Timelines are a forest, interleaved in time, structurally separate.
- **Rebase/pull:** chain continues; base edges (parent 2) show the transition.
  Old commits stay reachable via snapshot parents until trimmed.
- **Branch deletion:** `refs/jog/<branch>` survives; uncommitted work from
  deleted branches is recoverable until retention ages it out. Recreated
  same-name branches continue the chain; base edges disambiguate eras.
- **Worktrees:** git forbids the same branch in two worktrees, so branch-keyed
  refs never collide across worktrees. (Fallback if ever needed:
  `refs/worktree/jog/*` is natively per-worktree with working reflogs —
  verified.) This is a real edge over jj, which is incompatible with
  `git worktree`.

### Provenance

Commit message format: `<source>: <detail>`

```
pre: git rebase main                  # from the git alias, before your command
claude[b3f1]: Bash(rm -rf src/old)    # PreToolUse hook; [session-id prefix]
claude[b3f1]: prompt "refactor the…"  # UserPromptSubmit hook
manual: before surgery                # jog -m "before surgery"
```

## 4. Snapshot engine (lab-verified recipe)

Verified end-to-end on git 2.50.1. The engine is this sequence, encoded in Go
with each gotcha as a comment + test:

```sh
SNAPIDX="$GIT_DIR/jog/index"      # MUST be absolute; relative resolves inside the worktree
REF="refs/jog/$(git symbolic-ref --short HEAD 2>/dev/null || echo @detached)"

export GIT_INDEX_FILE="$SNAPIDX"
if git rev-parse --verify -q HEAD >/dev/null; then
    [ -f "$SNAPIDX" ] || git read-tree HEAD      # seed ONCE; re-seeding kills the stat cache (~30x slower)
    git -c advice.addEmbeddedRepo=false add -A   # respects .gitignore + info/exclude
    TREE=$(git write-tree)
    [ "$TREE" = "$(git rev-parse -q --verify "$REF^{tree}")" ] && exit 0   # no-op fast path
    PREV=$(git rev-parse -q --verify "$REF")
    SNAP=$(git commit-tree "$TREE" ${PREV:+-p "$PREV"} -p "$(git rev-parse HEAD)" -m "$PROVENANCE")
else
    git read-tree --empty; git add -A            # unborn HEAD: read-tree HEAD is fatal
    TREE=$(git write-tree); PREV=$(git rev-parse -q --verify "$REF")
    SNAP=$(git commit-tree "$TREE" ${PREV:+-p "$PREV"} -m "$PROVENANCE")
fi
unset GIT_INDEX_FILE
git update-ref --create-reflog -m "$PROVENANCE" "$REF" "$SNAP" ${PREV:+"$PREV"}   # CAS
```

Verified properties and gotchas the implementation must preserve:

| Fact (all reproduced in lab) | Consequence for jog |
|---|---|
| Real index stays byte-identical; shadow ops lock only `<shadow>.lock`, never `.git/index.lock` | safe to run during any concurrent git activity |
| `git status` (plain) rewrites the user's index opportunistically | all jog reads use `--no-optional-locks` |
| Works mid-merge: captures conflict markers, MERGE_HEAD intact, real index (stage 1/2/3) untouched | snapshot unconditionally; conflicted states are valid states |
| Persistent shadow index: warm re-snapshot ~27 ms even with 200 MB file in tree; re-seeding each run ~30× slower | seed once; never `read-tree HEAD` on the hot path |
| **Sparse checkout:** unseeded shadow index silently drops sparse-excluded files | if `core.sparseCheckout`, re-seed from HEAD each snapshot (accept cost) or on HEAD move |
| Embedded repos/submodules become gitlinks (mode 160000); inner files not captured | document; suppress warning with `advice.addEmbeddedRepo=false` |
| Exec bit and symlinks preserved (100755 / 120000) | nothing to do |
| Dangling commits (ref update skipped) are pruned by gc | update the ref immediately after commit-tree, always |
| Normal `push`/`clone` transfer nothing under custom ref namespaces; leaks only via `push --mirror`, `clone --mirror`, explicit `refs/*:refs/*`, `bundle --all` | private by default; document the leak vectors |
| New-file size: guard large blobs | `jog.maxFileSize` (default 50 MiB), skip + warn, list skipped in `jog log` |
| Loose objects accumulate (plumbing never triggers `gc --auto`) | `jog trim` runs `git gc --auto` afterward |

Performance budget: **no-op ≤ 30 ms; typical snapshot ≤ 100 ms** (reference:
`git status --porcelain` ≈ 20–25 ms warm on a 5k-file repo).

## 5. CLI

### Grammar: reserved verbs + git passthrough

First argument is matched against a short reserved list chosen to collide with
**no current git subcommand** (git owns `show`, `log`, `diff`, `restore`,
`prune`, `status` — jog must not):

```
jog                       # snapshot now        (also: jog -m "msg")
jog log [path]            # timeline browser: age, provenance, diffs (aliases: snaps, pick)
jog since [3h] [path]     # what changed vs the snapshot N ago
jog restore <path> [--at T]  # restore files      (T: git reflog time syntax or snap id)
jog restore --all --at T  # restore whole tree, including deletions (alias: back)
jog trim                  # apply retention policy + gc
jog hook claude|codex     # agent hook entry points (read hook JSON on stdin)
jog mcp                   # MCP server over the read paths
jog doctor                # verify invariants, config, liveness
```

**Anything else:** snapshot with provenance `pre: git <args>`, then
`exec git <args>` — real binary, real TTY (interactive `rebase -i`/`add -p`
work), real exit codes. jog reimplements nothing.

### The alias

```sh
alias git=jog
```

- This is the answer to "how do I remember to run it": you don't. Muscle
  memory is the trigger; the compulsive `git status` / `git diff` tic becomes
  the snapshot heartbeat (jj's model, git's skin).
- The wrapper snapshot is **causally before** destruction — the only mechanism
  that protects against `checkout -f` / `reset --hard`, since the
  reference-transaction hook provably fires after the worktree is clobbered
  (see §10).
- **Alias only — never a `git`-named binary/symlink on PATH.** Scripts, IDEs,
  and CI must hit real git. Their bypassing jog is a feature (jog stays out of
  code paths expecting exact git); coverage gap accepted (§9).

### Reading & restoring rules (verified)

- Restores use `git restore --source='refs/jog/<branch>@{…}' --worktree` —
  worktree-only, index untouched. Never `git checkout <snap> -- path` (it
  silently stages into the real index — verified trap).
- **Every restore snapshots first** → undo is itself undoable, jj-style.
- Never diff snapshot-vs-worktree with the one-commit form (`git diff <snap>`
  misreports untracked files as deleted — verified). `jog since` snapshots
  first, then diffs snapshot↔snapshot.
- Time syntax is git's own reflog syntax; `@{N.minutes.ago}` past the oldest
  entry falls back to oldest with a warning, exit 0 (verified).

## 6. Triggers

### Agent hooks (primary)

User-level `~/.claude/settings.json` — covers every repo, zero per-repo setup;
binary no-ops in ms outside git repos:

```json
{
  "hooks": {
    "PreToolUse": [{
      "matcher": "Bash|Edit|Write|NotebookEdit",
      "hooks": [{ "type": "command", "command": "jog hook claude" }]
    }],
    "UserPromptSubmit": [{
      "hooks": [{ "type": "command", "command": "jog hook claude" }]
    }]
  }
}
```

`jog hook claude` parses the hook JSON on stdin (tool name, command/file_path,
session_id) into provenance. Rules: always exit 0; never block; per-tool-call
frequency is fine (no-op path is ~25 ms; only tree-changing calls mint
objects). `UserPromptSubmit` aligns jog snapshots with Claude's own checkpoint
boundaries — `/rewind` the conversation, `jog restore --all` the world.

Codex uses the same event boundaries through `~/.codex/hooks.json`, with
`Bash|Edit|Write` matching shell commands and the `apply_patch` aliases, and
invokes `jog hook codex`. Non-managed Codex hooks must be reviewed with
`/hooks` before they run. At project scope Codex uses `.codex/hooks.json`;
its recovery skill is discovered from `.agents/skills/jog/SKILL.md`.

The other clients follow the same two boundaries in their own dialects, each
with its own `jog hook <client>` adapter (registry in internal/agents; one
declaration file per client). Copilot wires through its documented
Claude-compatible PascalCase mode in `~/.copilot/settings.json` — note its
`preToolUse` is fail-closed, so the exit-0 iron rule is load-bearing there.
Gemini wires `BeforeAgent`/`BeforeTool` in `~/.gemini/settings.json` (each
entry carries a `name` for its hook-trust fingerprinting; hook stdout must be
a single JSON document, so the session notice ships as `additionalContext`).
Cursor uses its own flat `~/.cursor/hooks.json` schema
(`beforeShellExecution`/`afterFileEdit`/`beforeSubmitPrompt`); its permission
events are answered with an explicit allow. OpenCode has no declarative
hooks, so install writes an embedded plugin
(`~/.config/opencode/plugins/jog.js`) that pipes `chat.message` and
`tool.execute.before` to `jog hook opencode` and forwards the notice into
the model's context.

### git alias (secondary — the human) and `jog` bare (deliberate checkpoints)

Covered in §5. The editor save hook shipped as `jog editors` (post-save
snapshots via `jog editor-hook <editor>` — the one post-state trigger, since
the saved state is the checkpoint). Other optional adapters (same engine, no
design change): zsh preexec one-liner, post-op beacon.

## 7. Retention

One age cutoff, configured via `git config jog.keep` (git expiry syntax),
default 90 days: `jog trim` drops everything older, tips included — a chain
whose snapshots have all aged out is removed whole, so deleted branches'
(and @detached) timelines eventually vanish on their own; `--gone` removes
dead chains immediately, and stale @trash refs for removed chains go on the
following run. (Originally a three-tier taper — all ≤ 24 h · hourly ≤ 7 d
· daily ≤ 90 d — simplified 2026-08-09; see open question 2.)

A size budget rides on top (2026-08-09): `jog.maxSize` (default off) makes
trim tighten the age cutoff — oldest snapshots first, one snapshot lenient
(the snapshot that crosses the budget survives, so even a 1-byte budget
keeps the newest) — until the projected survivor size fits. Both trim and doctor report jog-attributable
disk usage via `rev-list --disk-usage` (positives = refs or survivor *tree*
shas, negated against all non-jog refs; git ≥ 2.31, degrades gracefully).

Mechanism: `jog trim` rewrites the (private, synthetic, never-shared) chain
dropping expired snapshots, deletes corresponding reflog entries, then
`git gc --auto` reclaims. We never delete objects directly — we stop pointing
at them and let git collect. Trim is manual-only (PLAN-V1 D19): nothing
schedules it. jog sets `gc.refs/jog/*.reflogExpire=never` so git's own gc
never races the policy.

## 8. MCP server (`jog mcp`)

Read-mostly tools over the same internals: `list_snapshots`, `file_at`,
`diff_since`, `restore_file`. Lets the agent answer "what did I change in the
last hour," recover a file it deleted with bash three prompts ago, and diff
against pre-refactor state — snapshots as shared memory between dev and agent,
not just insurance. Official Go MCP SDK.

## 9. Safety invariants & accepted gaps

Invariants (each is a `jog doctor` check backed by a lab result):

1. User index byte-identical across any jog operation.
2. Worktree written only by explicit `jog restore`.
3. HEAD, branches, tags, remotes: never written.
4. Repo config written only at init (`gc.refs/jog/*` keys) with consent.
5. All read commands run `--no-optional-locks`.
6. Ref updates CAS-guarded; hook entry points always exit 0.
7. Crash at any point leaves at worst orphan objects (gc-sweepable) — jog only
   adds objects and moves its own refs.

Accepted gaps (documented, not defended):

- GUI/IDE git-panel discards (VS Code discard, Fork) — no trigger fires. VS
  Code trashes discarded files as partial comfort.
- Scripts/CI invoking real git — bypass by design.
- `push --mirror` / wide refspecs / `bundle --all` leak `refs/jog/*`.
- Solo terminal work without the alias installed.
- Ignored files are never snapshotted (by design).

## 10. Rejected alternatives (and the evidence)

- **jj in colocated passive mode** (cron/watchman firing `jj util snapshot`):
  works, has precedent, but: `refs/jj/keep/*` pollutes `git log --all`/GUIs;
  unbounded odb growth without `jj util gc`; **incompatible with git
  worktrees**; snapshot-mid-rebase can materialize conflict markers into the
  worktree. Fine as a stopgap; not the product.
- **Filesystem watcher / daemon**: event storms, debounce races (edits inside
  the debounce window before an `rm` are lost — watcher is only *temporally
  near* destruction, wrapper is *causally before* it), watchman lifecycle pain
  (jj's own trigger has open race bugs), daemon-liveness trust problem (dura).
  Engine is trigger-agnostic; a watcher can bolt on later if real losses argue
  for it.
- **git hooks**: `reference-transaction` at "prepared" already sees the
  post-clobber worktree on `checkout -f`/`reset --hard` (verified — uncommitted
  content gone by first hook invocation); never fires for `restore`,
  `checkout -- path`, `clean -fd`, `rm`; a non-zero exit (or broken script)
  **aborts the user's git command**; unguarded nested ref updates recurse
  unboundedly; husky/lefthook already own `.git/hooks` in real repos. Demoted
  to (unplanned) post-op beacon at most.
- **Porcelain (`jog commit` with opinions)**: that's rebuilding jj — new
  semantics, drift, team mismatch. Passthrough gives identical protection with
  none of it.
- **go-git / libgit2 engine**: voids the CLI-based verification; behavioral
  divergence around sparse/excludes/index details.

## 11. Implementation plan

- **v0 (dogfood ASAP):** Go binary — snap engine (§4 with all gotchas + tests),
  `jog` / `snaps` / `back` / `hook claude` / passthrough exec, settings.json
  wiring, alias docs. Dogfood on real repos immediately.
- **v1:** `since`, `pick` (Bubble Tea), `trim` + retention, `doctor`, brew tap.
- **v2:** `mcp`, prompt segment (`⚡3m` since last snapshot), optional backup
  remote push (`refs/jog/*` to a private remote — laptop-loss insurance),
  optional post-op beacon / preexec adapter.

## 12. Open questions

1. ~~Final name-collision sweep for `jog` (GitHub/brew/crates/npm) before
   publishing anything.~~ **Done 2026-08-08 — name is usable.** Everything jog
   will actually ship through is free: `tyler-johnson/jog` on GitHub, no
   Homebrew formula/cask, no Debian/AUR package, Go module path free. Taken
   elsewhere (not our channels): npm `jog` (TJ Holowaychuk's JSON logger,
   dead since 2012, ships a `jog` bin), crates.io `jog` (callum-oakley's task
   runner, **active** Feb 2026, ships a `jog` bin — the one live PATH-collision
   risk, for cargo users), PyPI `jog` (old logging lib). Exact-name GitHub
   repos are small/dormant (natethinks/jog ★472 shell script, tj/jog ★130,
   qiangyt/jog ★43); nothing in the git-tooling niche uses the name.
2. ~~Retention defaults — are 24h/7d/90d the right taper?~~ **Closed for v1
   2026-08-08 (PLAN-V1 D16)**, then **superseded 2026-08-09:** the taper's
   three tiers bought little over its simplest form and cost real
   explanation; retention is now one age cutoff — `jog.keep`, default
   90 days, git expiry syntax; age spares nothing (fully aged chains are
   removed whole), and the size budget is one snapshot lenient.
3. ~~`trim` chain-rewrite details.~~ **Closed 2026-08-08 (PLAN-V1 D17):**
   base edges preserved verbatim; parent 1 relinked; a new oldest survivor
   anchors to its own base edge; reflog replayed with original timestamps.
4. ~~Provenance for user manual edits swept up by an agent-triggered
   snapshot.~~ **Closed 2026-08-08 (PLAN-V1 D14):** no `sweep:` label —
   provenance records what jog ran ahead of, never inferred authorship;
   documented as a reading rule in the README.
5. ~~Whether `jog snaps` default scope is current branch (git-like).~~
   **Closed 2026-08-08:** yes — current branch by default (v0 D5),
   `--all` interleaved forest shipped in v1 M9 (PLAN-V1 D13).
6. Windows: `--no-optional-locks`, exec semantics (no execve — spawn+exit-code
   proxy), path handling. Not a v0 target.

---

## Appendix A — lab provenance

All "verified" claims trace to real experiments run 2026-08-07/08 on git 2.50.1
in scratch repos (shadow-index isolation, lock behavior, reflog creation rules,
gc/retention config precedence, push/clone/mirror hygiene, restore/checkout
index effects, one-arg diff untracked misreport, unborn-HEAD/mid-merge/sparse/
submodule/symlink edge cases, stat-cache timings, `reference-transaction`
firing matrix + ordering + re-entrancy + abort semantics, per-worktree ref
isolation). Re-verify against newer git versions before relying on: optional-
locks behavior, per-ref-pattern gc config, reference-transaction ordering.

## Appendix B — landscape (why this slot is open)

dura (dormant; hash-named branches killed recovery ergonomics) · gitwatch
(pollutes real branches) · git-wip (editor saves only) · git-branchless (op
undo, not edit capture; can't undo untracked changes) · VS Code/JetBrains local
history (editor-blind to CLI/agent changes; short retention) · Claude Code
checkpoints (no bash/manual/untracked; 100/30d limits) · Cursor (agent-only) ·
Cline (shadow-repo disk blowup) · GitButler (requires adopting client) ·
ShadowGit (commercial, closed; MCP-history idea worth stealing) · SafeSandbox
(new, no traction) · jj (the workflow tax this project exists to avoid).
Unserved combination jog targets: capture-everything + zero-workflow-change +
untracked/deleted files + per-file time travel + long configurable retention +
human-AND-agent coverage + git-native storage.
