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

### M10 — `jog doctor` (M) — ✅ done 2026-08-08

- [x] Checks shipped, each mapped to a DESIGN §9 invariant or a v0 lesson:
      repo discovery + bare detection (bare/non-repo degrade to global-only
      wiring checks); per-chain tip age listing (the liveness check `snaps`'
      loud empty state was standing in for); chain-tip D1 identity ("moved
      by something other than jog"); reflog presence per chain (`git reflog
      exists`); `gc.refs/jog/*` keys == `never`; shadow index
      present/absent (info); effective `jog.maxFileSize` (info); Claude
      hook wiring in `~/.claude/settings.json` (deterministic JSON parse);
      alias presence in shell rc files (heuristic, reported never
      asserted). "Neither alias nor hooks wired" is the one trigger-level
      finding — a silent engine feels safe while capturing nothing.
- [x] Read-only by default; `doctor --fix` writes the two gc keys and
      nothing else (asserted byte-level in the row-30 test) — D15, retiring
      the D3 bend; the lazy engine-side write stays as the safety net.
- [x] Exit codes: 0 healthy, 1 findings. Row 30 green; verified live on
      both real repos (all-ok), outside a repo (global-only), and against
      manufactured findings (dead engine, stripped gc keys, foreign tip,
      unwired triggers).

### M11 — `jog trim` + retention (L, the core of v1) — ✅ done 2026-08-08

The first feature that deletes anything. It got the densest tests (rows
24–29), a dry-run, an insurance ref, and landed manual-only — automation
follows separately after dogfood (D19). Dry-run verified on both real
repos (all snapshots inside keep-all; correctly nothing to drop).

- [x] `internal/retain`: pure taper policy, no git (D16). Defaults per
      DESIGN §7 (24 h / hourly 7 d / daily 90 d), newest-per-bucket,
      epoch-UTC-aligned buckets. Config `jog.keepAll`/`keepHourly`/
      `keepDaily` in **git's own expiry syntax** (`3.days`, `never` —
      parsed by git via `--type=expiry-date`, lab-verified; `never` turns a
      tier off). Property tests: taper counts, newest-per-bucket,
      idempotence at fixed now, no-resurrection as now advances, bucket
      edges. (Plan originally said "monotonicity under insertion" — that
      property belongs to oldest-per-bucket; the properties that actually
      matter for an append-only timeline are the two above.)
- [x] Chain rewrite (closes Q3, D17): survivors re-committed with tree,
      dates, and message verbatim; parent 1 relinked; **base edge
      untouched**; a new oldest survivor anchors to its own base edge so
      walks still terminate on a real commit it sat on. Original shas are
      preserved below the first drop. Reflog **replayed with original
      timestamps** — `update-ref` honors `GIT_COMMITTER_DATE` in reflog
      entries (lab-verified), so `@{time}` stays truthful. Tip always
      survives. Contention: tip verified before anything moves, and the
      ref swap starts with a CAS-guarded delete — a concurrent mint wins,
      the chain is skipped untouched (row 28, deterministic test).
- [x] Insurance (D18): pre-trim tip saved at `refs/jog/@trash/<branch>`
      before any write; clobbered by the next trim. `doctor` reports trash
      refs; `snaps --all` and trim itself exclude them from chain
      enumeration. README leak-vector list mentions trash holds dropped
      snapshots one cycle longer.
- [x] `trim --dry-run` prints the plan and provably touches nothing (ref,
      reflog, no trash ref — row 27). Apply reports per chain and runs
      `git gc --auto --quiet` once at the end. Trim snapshots first like
      every jog command; its boundary snapshot lands in the keep-all tier.
- [x] **Still not in this milestone** (D19): piggybacked automation —
      manual-only until ≥ 2 weeks of dogfood; the checklist item below
      carries it.

### M12 — `jog pick` (M) — ✅ done 2026-08-08 (interactive drill pending)

- [x] `jog pick <path>`: Bubble Tea list of the file's versions (snap id,
      age, provenance — `snaps` vocabulary), preview pane, enter → restore
      via the existing `back` machinery (pre-restore snapshot + undo hint
      for free), `q` backs out untouched. `--all` spans every chain via
      the same ranges `snaps --all` uses, so the views always agree.
      Without a TTY the version list prints plainly (pipeable, and the
      e2e-testable face of the data layer).
- [x] **Preview gotcha:** snapshots are two-parent commits, so plain
      `git show` renders a combined (`--cc`) diff against both parents —
      which also *hides* files that match either parent.
      `log --first-parent -p` diffs against the previous snapshot only.
- [x] Scope discipline held (D20): file scrubber only. Dependency surface:
      Bubble Tea alone (list + reverse-video cursor hand-rolled; no
      lipgloss/bubbles), confined to `internal/tui`; engine and every
      other verb remain stdlib-only.
- [x] Row 31: data layer (`fileVersions`, first-parent preview) tested in
      package cli; e2e covers non-TTY listing, path filtering, empty and
      usage cases. TUI boot smoke-tested under a pty (alt-screen enter +
      first render confirmed; the fake pty can't answer terminal queries,
      so the interactive loop is verified by hand instead).
- [x] Interactive run verified on a real terminal (Tyler, 2026-08-08,
      scrubbing the README) — TUI confirmed working end to end. An *organic*
      rescue with it still counts extra; the exit-checklist item carries
      that.

### M13 — release channel (S/M) — ✅ done 2026-08-08

- [x] Tagged `v1.0.0` (D21 as amended; first cut went out as v0.1.0 and
      was re-released the same day at Tyler's call). Verified end-to-end:
      workflow-built archives for all four platforms, checksums match, the
      released linux_arm64 binary runs on the Pi, and both the release
      binary and `go install …@v1.0.0` print a clean `jog version v1.0.0`.
      (First cut printed `v0.1.0+dirty` — goreleaser's untracked `dist/`
      makes Go's VCS stamping report a dirty tree; fixed by gitignoring
      `dist/` and re-cutting the tag.)
- [x] goreleaser v2 config + tag-triggered workflow (`release.yml`,
      GITHUB_TOKEN only).
- [x] Homebrew tap `tyler-johnson/homebrew-tap` published with the 1.0.0
      formula (per-platform release binaries, caveats printing the alias
      line + hooks pointer + `jog doctor`). **Formula bumps are manual for
      now:** goreleaser's tap automation needs a cross-repo push token the
      workflow doesn't have — wire `HOMEBREW_TAP_GITHUB_TOKEN` + a `brews`
      section when release cadence justifies it.
- [x] README: brew + releases-page install, `since`/`pick`/`trim`/`doctor`
      in the usage table, cookbook entries for since/pick/trim, roadmap
      flipped to shipped/next, status callout updated to 0.x.
- [x] `brew install tyler-johnson/tap/jog` on a clean machine — verified
      by Tyler, 2026-08-09 (formula at 1.1.0 by then).

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
- **D21 — versioning starts at `v1.0.0`** *(amended 2026-08-08 — Tyler's
  call, superseding the original 0.x plan)*: the v1 feature set ships as
  1.0.0; the milestone eras and semver line up. The original caution (trim
  is young) is carried by D19's manual-only rule and the insurance ref
  instead of a 0.x version number.

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

- [x] All matrix rows 1–32 green; no-op ≤ 30 ms on the Pi (23.0 ms
      dirty-unchanged, 8.4 ms clean — 2026-08-08; the gate is now a local
      test failure if it regresses)
- [ ] v0 exit checklist fully closed (accrual week + README stranger test)
- [ ] `doctor` green on ≥ 2 real repos *(green on both since 2026-08-08)*;
      at least one real misconfiguration caught during dogfood (manufacture
      one if none occurs naturally) *(2026-08-09: on the fresh brew
      machine, doctor correctly flagged everything unwired pre-setup —
      field evidence it works, but a pre-setup machine isn't a
      misconfiguration; leaving open for a real config-level catch)*
- [ ] Manual `trim` run on ≥ 2 real repos for ≥ 2 weeks: object counts
      shrink, every post-trim `back`/`since`/`pick` still correct, zero
      "needed a snapshot trim dropped" events → only then does the
      piggyback land (D19). *(Clock starts when the first chains age past
      24 h — dry-runs verified correct on both repos day one.)*
- [ ] `pick` used in anger for at least one real recovery or comparison
      *(TUI itself verified interactively 2026-08-08 — what remains is an
      organic use, not a functionality check)*
- [x] `brew install tyler-johnson/tap/jog` on a clean machine → alias +
      hooks → first snapshot, using only README + caveats text
      *(verified by Tyler, 2026-08-09)*
- [x] Tagged release with binaries for linux/darwin × amd64/arm64
      (v1.0.0, 2026-08-08; released arm64 binary + `go install @v1.0.0`
      both verified)
