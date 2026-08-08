# jog v1 — implementation plan

**Status:** planned, not started · **Last updated:** 2026-08-08 · **Parent:** [DESIGN.md](DESIGN.md) §11 · **Predecessor:** [PLAN-V0.md](PLAN-V0.md)

v0 made jog *safe to rely on*: capture everything, recover anything, never
touch user state. v1 makes it *sustainable and legible*: the engine gets fast
enough to disappear, `since` answers "what changed", `doctor` proves the net
is really under you, `trim` keeps the object database from growing forever,
`pick` makes browsing pleasant, and a real release channel replaces
`go install` + hand-edited shell rc.

Numbering continues from v0 (milestones M8+, decisions D12+, matrix rows 21+)
so references across the two plans never collide.

## 0. Carry-over from v0

- The v0 exit checklist ([PLAN-V0.md](PLAN-V0.md) §7) still has clock-driven
  items open (≥ 1 week of accrual on ≥ 2 repos; README stranger test). v1
  development overlaps that window deliberately — the accrual clock needs no
  attention, and an organic rescue during v1 work counts double.
- **Perf debt is now scheduled, not deferred:** the v0 finding (warm no-op
  ≈ 51 ms on the Pi vs the ≤ 30 ms budget, ~4.4× the `git status` baseline)
  becomes M8, first in line — every v1 feature and every hook invocation sits
  on that hot path.
- Open questions inherited from DESIGN §12: retention defaults (Q2), trim
  rewrite details (Q3), sweep provenance (Q4), `snaps` `--all` scope (Q5).
  All are resolved by decisions below or during their milestone.

## 1. Scope

**In:**

- Engine perf consolidation to budget (no new features, spawn arithmetic)
- `jog since [T] [path…]` — "what changed" read path (the `jj st` analog, D7)
- `jog snaps --all` — the interleaved forest view (Q5, and the original
  wish behind D6's top-3 readout)
- `jog doctor` — invariant checks, liveness, config/wiring verification
- `jog trim` + tapering retention — the first feature that deletes anything
- `jog pick` — Bubble Tea scrubber over a file's versions (first dependency)
- Release channel: semver tags, cross-compiled binaries, Homebrew tap

**Out (v2, unchanged from DESIGN §11):** `mcp`, backup remote push, prompt
segment, post-op beacon / preexec adapters, Windows (Q6). `jog git` remains
pure passthrough forever (D10) — nothing in v1 adds flags or subcommands to it.

## 2. Dependencies & layout

- First non-stdlib dependencies arrive here, feature-scoped: Bubble Tea (+
  Lip Gloss) for `pick` only — engine, hooks, and every non-TUI verb stay
  stdlib-only so the hot path never pays for the TUI.
- New packages: `internal/retain` (taper policy: pure functions over
  timestamps, no git), `internal/tui` (pick). `since`/`doctor`/`trim` verbs
  live in `internal/cli` beside their siblings.
- Release plumbing: goreleaser config + a tag-triggered GitHub Actions
  workflow; tap repo `tyler-johnson/homebrew-tap`.

## 3. Work breakdown

Milestones are sequential; each lands with its tests. Order is
value-and-risk-driven: perf first (everything sits on it), reads before
writes, `doctor` before `trim` (the auditor exists before the first deleter),
`pick` after the data layer it browses, release last.

### M8 — engine perf to budget (M) — ✅ done 2026-08-08

- [x] Consolidated spawns: the warm hot path is now **5 spawns dirty / 3
      clean** (was 9). Mechanisms, each lab-verified first: (a) one batched
      tolerant `rev-parse` for toplevel + HEAD + HEAD tree + chain ref +
      chain tree — rev-parse answers queries in order and echoes the failing
      arg before exit 128, so the surviving line count identifies unborn
      HEAD / missing chain; (b) branch name read from `$GIT_DIR/HEAD`
      directly (documented stable format, `symbolic-ref` spawn as fallback —
      an optimization, never a semantic); (c) one `config -z --get-regexp`
      replacing both config reads, with git's int-suffix/bool syntax parsed
      engine-side; (d) the D2 status pre-scan now doubles as a **clean-tree
      fast path** — empty porcelain status means the would-be tree is
      exactly HEAD's tree, skipping the shadow index entirely.
- [x] **Gate met:** dirty-unchanged no-op 23.0 ms, clean no-op 8.4 ms vs
      6.5 ms status baseline (Pi 5, 5k-file repo); real-repo full invocation
      14–16 ms wall. Row 20/32 now locally gating (30 ms), advisory in CI.
- [x] No behavior change: rows 1–20 pass untouched.
- [x] **Finding (racy-clean):** entries whose mtime shares the index
      write's second are "racily clean" — git re-reads their contents on
      every status/add (~4×: 28 ms vs 7 ms, lab-verified; comparison is
      second-granularity). jog itself can never heal the user's index
      (invariant 1) but the user's own git commands do, so real repos live
      in the healed state; the perf fixture backdates mtimes to model it.
      Worth remembering when a repo's `since`/snapshot feels slow right
      after a mass file-generation step.

### M9 — `jog since` + `jog snaps --all` (M) — ✅ done 2026-08-08

- [x] `jog since [--at T | T] [-p] [--] [path…]`: snapshot first
      (jj-style), then diff *target snapshot ↔ fresh snapshot* — never the
      one-commit diff form (misreports untracked as deleted, verified trap;
      DESIGN §5). Default output `--compact-summary` (one table carrying
      both the name-status and diffstat roles: per-file churn plus
      (new)/(gone)/mode annotations); `-p` for patches; exec real
      `git diff` so pager/coloring apply. Prints a `since <id> (<age> —
      <provenance>)` header naming the resolved target.
- [x] `T` grammar identical to `back --at`, resolved via the shared
      `resolveTarget` (D1-identity-guarded), and resolved *before* the
      fresh snapshot, same rule as back. The first positional doubles as
      the target when it doesn't exist on disk (`jog since 3h` vs
      `jog since src/`; `--` forces paths). Bare `jog since` compares
      against the pre-invocation chain tip — "what changed since my last
      command boundary" (= `@{1}` whenever the fresh snapshot minted); an
      unchanged tree prints `no changes since <target>` instead of an
      empty diff (D12, refined).
- [x] **Fixed a latent v0 bug found by reuse:** `back --at @{N}` resolved
      through the commit-ish attempt first, which git reads as the *current
      branch's* reflog — a real commit the identity guard then rejected.
      `@{…}` targets now go straight to the chain ref.
- [x] `jog snaps --all`: one exec'd `git log --first-parent` over every
      `refs/jog/*` tip with per-chain boundaries excluded (`^<boundary>`
      each) and `%S` attributing entries to their chain — tips are spelled
      `jog/<branch>` so the label renders short (lab-verified: multi-tip
      first-parent walks each chain independently, interleaves by commit
      date, and honors multiple negations). The full version of D6's
      top-3 readout.
- [x] Closed Q4 (sweep provenance, D14): attribution stays with the
      triggering event; documented as a README reading rule. DESIGN §12
      Q4 + Q5 struck.
- [x] Rows 21–23 green; verified live on both real repos (since header +
      compact summary against hook-provenance targets; forest view shows
      `jog/main` and the deleted-branch chain `jog/drill/timeline`).

### M10 — `jog doctor` (M)

- [ ] Checks, each mapped to a DESIGN §9 invariant or a v0 lesson:
      repo discovery + bare detection; shadow index present/absent (absent
      is fine — reports "will seed"); chain refs + reflogs exist where
      expected; `gc.refs/jog/*` keys present; snapshot identity resolvable
      (D1); alias wired (`git` resolves to `jog git` in the *interactive*
      shell — detectable only heuristically, reported not asserted); Claude
      hook wiring in `~/.claude/settings.json`; last-snapshot age per chain
      (the liveness check `snaps`' loud empty state has been standing in
      for).
- [ ] Read-only by default; `doctor --fix` writes the gc config keys (and
      nothing else) with explicit consent — retiring the D3 bend: the lazy
      engine-side write stays as a safety net, `doctor` is the documented
      front door (D15).
- [ ] Exit codes: 0 healthy, 1 findings — scriptable, and the future brew
      caveat can say "run `jog doctor`".

### M11 — `jog trim` + retention (L, the core of v1)

The first feature that deletes anything. It gets the densest tests, a
dry-run, an insurance ref, and lands manual-only — automation follows
separately after dogfood.

- [ ] `internal/retain`: pure taper policy — given `(now, []snapshot times)`,
      return keep/drop. Defaults per DESIGN §7: everything ≤ 24 h, hourly
      ≤ 7 d, daily ≤ 90 d; config keys `jog.keepAll` / `jog.keepHourly` /
      `jog.keepDaily` (git-duration values). Property tests, no git (D16).
- [ ] Chain rewrite (closes Q3, D17): survivors are re-committed with tree,
      author/committer identity+dates, and message verbatim; parent 1
      relinked to the previous survivor; **parent 2 (base edge) preserved
      verbatim** — re-parenting would forge history the snapshot never saw
      and break era disambiguation. Reflog rebuilt to survivors only, so
      time queries stay truthful. CAS on the final ref update; a concurrent
      snapshot losing the race is the snapshot engine's normal contention
      path.
- [ ] Insurance: before the rewrite, the old chain head is saved at
      `refs/jog/@trash/<branch>` (no reflog, overwritten by the next trim) —
      one full undo window per chain, reclaimed automatically at the trim
      after next (D18). `doctor` reports trash refs and their age.
- [ ] `trim --dry-run` prints the keep/drop plan (counts + dropped-range
      summary per chain) and touches nothing. `trim` without flags applies
      to all chains, then runs `git gc --auto` (the plumbing-only engine
      never triggers it — DESIGN §4 table).
- [ ] **Not in this milestone:** piggybacking trim onto snapshots. Lands
      only after ≥ 2 weeks of manual-trim dogfood, as a separate small
      change: at most once per day, checked via a cheap timestamp file in
      `.git/jog/`, never on the hook path (hooks must stay worst-case-fast
      and silent) (D19).

### M12 — `jog pick` (M)

- [ ] `jog pick <path>`: Bubble Tea list of the file's versions across the
      current chain (snap id, age, provenance — same vocabulary as `snaps`),
      preview pane showing the diff between adjacent versions, enter →
      restore via the existing `back` machinery (which snapshots first, so
      picking is undoable for free). `q` leaves the worktree untouched.
- [ ] `pick --all`: same, across every chain (the forest, matching
      `snaps --all` scope).
- [ ] Scope discipline (D20): v1 pick is a *file-version scrubber*, not a
      repo browser — no tree navigation, no multi-file restore, no graph
      rendering. Those wait for evidence from dogfood.
- [ ] TUI layer stays thin: version-list assembly and diff extraction live
      in `internal/cli`/`internal/snap` as plain functions with plain tests;
      `internal/tui` only renders. TUI itself gets a smoke test at most.

### M13 — release channel (S/M)

- [ ] Tag `v0.1.0` at M8-complete (perf budget met = the engine is what the
      README promises), then tag per milestone; the version verb (already
      reading build info) starts showing real semver for `go install` users
      immediately (D21).
- [ ] goreleaser: linux/darwin × amd64/arm64 static binaries, checksums,
      GitHub Releases via tag-triggered workflow.
- [ ] Homebrew tap `tyler-johnson/homebrew-tap` with a formula pointing at
      the release binaries; caveats text prints the alias line and the
      Claude-hooks pointer (install steps 2–3 of the README, which can't be
      automated politely).
- [ ] README: install section gains brew as the first option; roadmap
      updated; `since`/`doctor`/`trim`/`pick` join the usage table and the
      recovery cookbook gains a trim/retention entry.

## 4. Forced decisions (beyond DESIGN.md)

- **D12 — `since` defaults.** Bare `jog since` compares against `@{1}` (the
  snapshot preceding the one the invocation itself just took): "what changed
  since my last command boundary" — the `jj st` analog. With `T`, the target
  grammar and D1-identity guard are exactly `back --at`'s; one resolver, one
  mental model, and `since → back` round-trips ("saw it in since, restored
  it with the same T").
- **D13 — forest rendering is still one exec'd `git log`.** `snaps --all`
  passes every chain tip plus per-chain `^boundary` exclusions and
  `--source` for chain attribution. No TUI, no custom graph — same
  pager/coloring-for-free rationale as v0's `snaps`, and the boundary
  discovery loop already exists per-chain.
- **D14 — no `sweep:` provenance (closes Q4).** Snapshots attribute to the
  *triggering event*, never to an inferred author. The engine cannot know
  who edited between boundaries; guessing would put wrong answers in the
  op log. Documented as a reading rule: provenance answers "what was jog
  running ahead of", not "who made these changes".
- **D15 — `doctor` is read-only; `--fix` writes only the gc keys.** Retires
  the D3 bend by giving the config write an explicit, consented front door,
  while the lazy engine-side write remains as a safety net for repos that
  never run doctor. DESIGN §9 invariant 4 is restored to the letter.
- **D16 — retention policy is a pure function** in `internal/retain`:
  `(now, times) → keep/drop`, no git, property-tested (monotonicity: adding
  snapshots never drops a previously-kept one at the same `now`; boundary
  behavior at bucket edges). The 24h/7d/90d defaults from DESIGN §7 stand
  until dogfood argues otherwise (closes Q2 for v1); config via
  `jog.keepAll`/`jog.keepHourly`/`jog.keepDaily`.
- **D17 — trim rewrite preserves base edges verbatim (closes Q3).**
  Survivors are re-created byte-for-byte except parent 1 (relinked to the
  previous survivor). Trees, dates, messages, and parent 2 are untouched:
  the base edge is a *record* of where HEAD was, and rewriting records is
  forgery. Consequence: pre-rebase real commits reachable only through
  *dropped* snapshots' base edges become collectable — retention genuinely
  ends the reflog-grade protection window, which is its job.
- **D18 — trash ref insurance.** Each trim saves the pre-rewrite chain head
  at `refs/jog/@trash/<branch>` (plain ref, no reflog, clobbered by the
  next trim). One-deep undo for the one operation that discards data, at
  the cost of delaying gc of dropped snapshots by exactly one trim cycle.
  `@trash` joins `@detached` in the reserved non-branch namespace (`/` in
  branch names can't collide with `@`-prefixed segments).
- **D19 — trim automation is a separate, later change.** Manual `jog trim`
  dogfoods first; the piggyback (≤ once/day, timestamp-gated, never on the
  hook path) lands only after ≥ 2 weeks without incident. The tool that
  deletes must earn autonomy the same way the tool that captures did.
- **D20 — pick is a file scrubber, not a repo browser.** List + preview +
  restore for one path (or `--all` chains), nothing else in v1. Every
  richer TUI idea (tree view, multi-select restore, graph) waits for a
  dogfood-backed reason.
- **D21 — versioning starts at `v0.1.0`, not `v1.0.0`.** The "v0/v1" in
  these plans are milestone eras, not semver. Public semver stays 0.x until
  trim has months of dogfood — the deletion feature is the maturity
  gate for 1.0.

## 5. Test matrix additions

Rows continue from v0 (1–20). Same discipline: integration tests against
real git via `internal/testrepo`; retention policy additionally gets pure
property tests.

| # | Test | Verifies |
|---|---|---|
| 21 | `since`: untracked file reported as added, never deleted; output = target↔fresh snapshot diff | one-arg-diff trap stays fenced (DESIGN §5) |
| 22 | `since` bare vs `--at` grammar: `@{1}` default; `HEAD` rejected by identity guard | D12 |
| 23 | `snaps --all`: entries from two chains interleave with correct chain attribution; per-chain boundaries hold | D13 |
| 24 | retain policy: property tests (taper correctness on synthetic timelines, bucket-edge behavior, monotonicity) | D16 |
| 25 | trim rewrite: survivors' trees, dates, messages, base edges byte-identical; parent-1 relinked; dropped snapshots unreachable and gc-collected | D17 |
| 26 | trim reflog rebuild: time queries resolve to survivors; past-oldest still warns + falls back | truthful timeline after trim |
| 27 | trim safety: `--dry-run` writes nothing (refs, reflogs, odb all untouched); trash ref holds pre-trim head; second trim clobbers it | D18 |
| 28 | trim vs concurrent snapshot: CAS loser retries/skips per v0 contention policy; no lost snapshot, no corrupted chain | concurrency |
| 29 | trim never drops snapshots ≤ `jog.keepAll`; user index byte-identical across trim | invariant 1 under the deleter |
| 30 | doctor: healthy repo exits 0; dead chain / missing gc keys / stale accrual each reported, exit 1; `--fix` writes only the two gc keys | D15 |
| 31 | pick data layer: version list for a path matches `snaps <path>`; adjacent-version diff extraction correct | D20 (TUI excluded) |
| 32 | perf gate: warm no-op ≤ 30 ms on generated repo (locally gating post-M8; advisory in CI) | M8 budget |

## 6. Risks

- **Trim is jog's first self-inflicted data-loss vector.** A rewrite bug
  doesn't crash — it silently thins the safety net, discovered only at the
  next rescue attempt. Defense in depth: pure-function policy (row 24),
  byte-level rewrite verification (row 25), dry-run (row 27), trash ref
  (D18), manual-only until dogfooded (D19), and 0.x semver until it has
  months of history (D21).
- **Perf consolidation can bend invariants invisibly.** Merging spawns
  changes *when* the engine reads state; a reordering could reintroduce the
  exact races v0's matrix fenced. Rows 1–20 unchanged are the gate — any
  consolidation that needs a test amended is treated as a semantics change,
  not an optimization.
- **`--source`/multi-tip log rendering is less traveled than single-ref
  log.** Verify the interleave + attribution on real git 2.50.1 before
  building on it (lab-first, like every §4 claim); fall back to a jog-side
  merge of per-chain walks if git's rendering disappoints.
- **First dependencies.** Bubble Tea lands TUI-only; if it ever creeps
  toward the engine or hook path, that's a review flag, not a convenience.
- **Trash-ref leak surface.** `refs/jog/@trash/*` rides the same
  `push --mirror` leak vectors as the rest of `refs/jog/*` — no new class
  of leak, but README's leak-vector list must mention it holds *deleted*
  history one cycle longer.
- **Brew tap maintenance.** A tap is a second repo that can rot. goreleaser
  automates the formula bump; the release workflow is the only writer.

## 7. v1 exit checklist

- [ ] All matrix rows 1–32 green; no-op ≤ 30 ms sustained on the Pi
- [ ] v0 exit checklist fully closed (accrual week + README stranger test)
- [ ] `doctor` green on ≥ 2 real repos; at least one real misconfiguration
      caught by it during dogfood (manufacture one if none occurs naturally)
- [ ] Manual `trim` run on ≥ 2 real repos for ≥ 2 weeks: object counts
      shrink, every post-trim `back`/`since`/`pick` still correct, zero
      "needed a snapshot trim dropped" events → only then does the
      piggyback land (D19)
- [ ] `pick` used in anger for at least one real recovery or comparison
- [ ] `brew install tyler-johnson/tap/jog` on a clean machine → alias +
      hooks → first snapshot, using only README + caveats text
- [ ] Tagged release with binaries for linux/darwin × amd64/arm64
