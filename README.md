<div align="center">

# jog

**a memory for your working tree**

*Automatic snapshots of your uncommitted work — untracked files included —<br>
stored as ordinary git objects. No daemon, no new VCS, no workflow change.*

</div>

---

```console
$ rm -rf src/parser        # oops. uncommitted changes and untracked files, gone.

$ jog snaps                # ...but every command boundary was snapshotted
c0ffee1  2 minutes ago   pre: git status
a1b2c3d  9 minutes ago   claude[b3f1a2c4]: Bash(go test ./...)
9e8d7c6  14 minutes ago  manual: before parser rewrite

$ jog back --all           # worktree back to the newest snapshot
restored to c0ffee1 (2 minutes ago — pre: git status): 14 restored, 0 deleted
(undo: jog back --all)
```

## Why jog

Pick the pitch that fits how you work:

- **A working-tree reflog.** Git has a safety net for refs; jog extends the
  same idea to your uncommitted changes. Every snapshot records *what command
  it ran ahead of* — the timeline is an operation log, not a smear of states.
- **The missing half of Claude Code's `/rewind`.** Claude's checkpoints
  restore Edit/Write changes but explicitly not bash-made changes, manual
  edits, or untracked files — and they expire. jog covers exactly that
  complement, at the same prompt/tool-call boundaries, with retention you
  control: `/rewind` the conversation, `jog back --all` the world.
- **[jj]'s snapshot model wearing git's skin.** Snapshot at the start of
  every command — but the commands are the git you already type.

[jj]: https://github.com/jj-vcs/jj

### How it works

- Snapshots are plain git blobs/trees/commits in your repo's own object
  database, on refs under `refs/jog/<branch>` — invisible to branches, the
  index, `git log`, teammates, and remotes. Unchanged files cost zero bytes.
  There is no jog database.
- Snapshots are **command-triggered, causally before the command runs** — the
  only ordering that protects against `reset --hard` and `checkout -f`. No
  daemon, no filesystem watcher, no git hooks to install or break.
- Three triggers: the shell alias (every git command you type), Claude Code
  hooks (every prompt and tool call), and `jog` itself (deliberate
  checkpoints). A no-op snapshot costs a few tens of milliseconds.
- jog never reimplements a git verb, and never touches your index, HEAD,
  branches, or config. If jog vanished tomorrow, stock git reads every
  snapshot ([see below](#no-lock-in)).

> [!NOTE]
> jog is under active daily dogfood. The snapshot engine's behavior is
> lab-verified against git 2.50; every verified fact ships as a test.

## Install

**1. Install the binary:**

```sh
# Homebrew (macOS / Linux)
brew install tyler-johnson/tap/jog

# or with Go 1.26+
go install github.com/tyler-johnson/jog/cmd/jog@latest
```

Prebuilt binaries for linux/darwin (amd64/arm64) are on the
[releases page](https://github.com/tyler-johnson/jog/releases).

**2. Add the alias** — this is how you "remember" to snapshot: you don't.
Muscle memory is the trigger; the compulsive `git status` tic becomes the
snapshot heartbeat.

```sh
# bash / zsh (~/.bashrc / ~/.zshrc)
alias git='jog git'

# fish
alias git 'jog git'
```

Scripts, IDEs, and CI are untouched — they resolve `git` on PATH and get real
git. That's a feature: jog stays out of every code path that expects exact
git behavior.

**3. Wire up Claude Code** (optional):

```sh
jog hook claude install    # hooks: snapshot before every prompt and tool call
jog skill claude install   # skill: teach agents the recovery workflow
```

The hooks land in user-level `~/.claude/settings.json` — one wiring covers
every repo; the hook exits in milliseconds outside git repos and never
blocks a tool call. Install writes exactly this (paste it yourself if you
prefer):

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

(If jog isn't on PATH for non-interactive shells, install writes the
absolute path instead — and tells you so.)

`--project` scopes either command to the current repo: hooks go to the
personal `.claude/settings.local.json` (a committed hook command would
break for teammates without jog), the skill to the committable
`.claude/skills/`. `uninstall` removes exactly what install wrote and
touches nothing else.

Once the hooks are wired, jog introduces itself to Claude once per session
— a single line of context saying snapshots are live and how to restore —
so the agent knows the safety net exists exactly when it's active. The
skill (`~/.claude/skills/jog/SKILL.md`) goes further and teaches the full
recovery workflow.

**4. Verify:** make any change in a repo, run `git status`, then `jog snaps`
— you should see a `pre: git status` entry.

## Usage

Two disjoint namespaces, one rule: **`jog git` is the only door to git.**

| command | what it does |
|---|---|
| `jog` | snapshot now, show the top of the timeline |
| `jog -m "msg"` | snapshot with a message (`manual: msg`) |
| `jog snaps [-p] [--all] [path…]` | the timeline: id, age, provenance, files changed (`-p`: patches, `--all`: every branch, interleaved) |
| `jog since [T] [path…]` | what changed since a snapshot (default: your last command boundary; `-p`: patches) |
| `jog back <path>… [--at T]` | restore files from a snapshot (worktree only) |
| `jog back --all [--at T]` | restore the whole tree, including deleting files created since |
| `jog git <args>` | snapshot, then run the real git command — what the alias expands to |
| `jog pick [--all] <path>` | scrub through a file's versions — list, preview, enter to restore (`q` leaves everything untouched) |
| `jog trim [--dry-run]` | apply the retention taper; the previous tip stays at `refs/jog/@trash/<branch>` until the next trim |
| `jog doctor [--fix]` | verify invariants, wiring, and liveness (`--fix` repairs the gc config) |
| `jog hook claude install` | wire the Claude Code hooks (`uninstall` removes it; `--project`: this repo only) |
| `jog skill claude install` | install the Claude Code skill that teaches agents the recovery workflow (`uninstall`, `--print`; `--project`: this repo) |
| `jog version` | print jog's version |

`--at` (and `since`'s target slot) accepts a snap id from `jog snaps` or a
time: `--at 30m`, `--at 1h`, `--at 2d`, `--at 1w` — plus anything git's
date syntax accepts (`--at yesterday`, `--at 2.hours.ago`). Asking for a
time older than the oldest snapshot falls back to the oldest, with a
warning.

A reading rule for the timeline: provenance records **what jog was running
ahead of**, never who made the changes. Manual edits swept up by an
agent-triggered snapshot are attributed to that trigger — jog can't know who
typed between boundaries, and refuses to guess.

`mcp` is reserved for a future release.
Anything else is an error — jog never guesses.

## Recovery cookbook

**An agent deleted a file three prompts ago.** Find it, get it back:

```console
$ jog snaps src/config.yaml
f3a9b12  8 minutes ago  claude[b3f1a2c4]: Bash(rm -rf src/old)
D       src/config.yaml
$ jog back src/config.yaml --at 9m
restored src/config.yaml from 2c4d6e8 (9 minutes ago — claude[b3f1a2c4]: prompt "clean up the src tree")
```

**Roll everything back to before a refactor went sideways** — including
deleting the files it scattered:

```console
$ jog back --all --at 25m
restored to 9e8d7c6 (26 minutes ago — manual: before parser rewrite): 11 restored, 3 deleted
(undo: jog back --all)
```

Every restore snapshots first, so undo is itself undoable.

**What did I actually change in the last hour?**

```console
$ jog since 1h           # per-file summary since the snapshot nearest 1h ago
$ jog snaps -p           # full patches, in your pager
$ jog snaps src/parser/  # just one path's history
```

**Find the version of one file where it still worked**, scrubbing visually:

```console
$ jog pick src/parser/lexer.go
```

**The timeline is getting long.** Apply the retention taper — everything
kept ≤ 24 h, hourly ≤ 7 d, daily ≤ 90 d:

```console
$ jog trim --dry-run     # the plan, touching nothing
$ jog trim               # apply; the pre-trim tip stays at refs/jog/@trash/<branch>
```

Trim is the only jog command that discards snapshots, it never runs on its
own, and its last pre-trim state survives until the trim after next.

## What jog will never touch

1. Your **index** — byte-identical across any jog operation (jog stages into
   a private shadow index; all reads use `--no-optional-locks`).
2. Your **worktree** — written only by an explicit `jog back`, which is
   itself snapshotted first.
3. **HEAD, branches, tags, remotes** — never written.
4. **Repo config** — two `gc.refs/jog/*` keys are set once, on first
   snapshot, so git's gc never expires jog's history; announced when it
   happens.

Crash at any point leaves at worst orphan objects for git's gc to sweep —
jog only adds objects and moves its own refs, with compare-and-swap updates
so concurrent triggers can't clobber each other.

### No lock-in

Every snapshot is recoverable with stock git alone:

```sh
git log refs/jog/main                                   # browse the chain
git show 'refs/jog/main@{2.hours.ago}:src/main.go'      # read one file
git restore --source refs/jog/main --worktree -- path   # restore one file
```

## Why not jog

Honesty section, in the ripgrep tradition. jog does **not** protect against:

- **GUI discards.** VS Code's git panel, Fork, etc. call git via their own
  bindings — no trigger fires. (VS Code moves discarded files to the trash,
  which is some comfort.)
- **Scripts and CI running real git.** They bypass the alias *by design*.
- **Ignored files.** Never snapshotted, by design — jog is not a backup
  system for `node_modules` or build artifacts.
- **Solo terminal work without the alias installed.** No trigger, no snapshot.

Also worth knowing:

- `refs/jog/*` stays private through normal `push`/`fetch`/`clone`, but
  **leaks** via `push --mirror`, `clone --mirror`, explicit `refs/*:refs/*`
  refspecs, and `git bundle --all`. Your snapshots contain your scratch work
  — know your mirror scripts. (That includes `refs/jog/@trash/*`, which
  holds snapshots the last `jog trim` dropped, one cycle longer.)
- Retention is manual: run `jog trim` when you want the taper applied —
  nothing schedules it behind your back. Space cost is low either way
  (content-addressed, delta-compressed by repack).
- New files over 50 MiB are skipped (configurable, see below) and listed in
  the timeline entry.

## Configuration

Native `git config` keys — global or per-repo, no new file format:

| key | default | meaning |
|---|---|---|
| `jog.maxFileSize` | `50m` | skip new files larger than this (`0` disables the guard) |
| `jog.keepAll` | `24.hours` | `jog trim` keeps every snapshot younger than this |
| `jog.keepHourly` | `7.days` | …then one per hour up to this age |
| `jog.keepDaily` | `90.days` | …then one per day up to this age; older ones are dropped (`never` disables a tier) |
| `gc.refs/jog/*.reflogExpire` | `never` | set by jog on first snapshot; keeps gc off jog's reflogs |
| `gc.refs/jog/*.reflogExpireUnreachable` | `never` | same |

## How it compares

- **[jj]** — the inspiration for snapshot-on-every-command, but it's a new
  VCS with a workflow tax, and it's incompatible with `git worktree`. jog is
  git, plus a memory.
- **[dura](https://github.com/tkellogg/dura)** — background daemon, hash-named
  branches that made recovery painful; dormant.
- **[gitwatch](https://github.com/gitwatch/gitwatch)** — commits your real
  branch on a watcher; pollutes history.
- **Editor local history** (VS Code, JetBrains) — blind to terminal and
  agent changes, short retention, not in git.
- **Claude Code checkpoints** — conversation + Edit/Write only; no bash
  changes, no untracked files, capped at 100 checkpoints / 30 days. jog is
  the other half.

## Roadmap

- **shipped:** snapshot engine, `snaps` (+ `--all` forest view), `since`,
  `back`, `pick`, `trim` + tapering retention, `doctor`, Claude Code
  integration (hooks, agent skill, once-per-session notice), brew tap.
- **next:** automatic trim (piggybacked, at most daily — after the manual
  command has earned trust), MCP server (agents query their own snapshot
  history), optional encrypted backup push of `refs/jog/*` to a private
  remote, shell prompt segment.

## License

[MIT](LICENSE)
