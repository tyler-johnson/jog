<div align="center">

# jog

**a memory for your working tree**

*Automatic snapshots of your uncommitted work — untracked files included —<br>
stored as ordinary git objects. No daemon, no new VCS, no workflow change.*

</div>

---

```console
$ rm -rf src/parser        # oops. uncommitted changes and untracked files, gone.

$ jog log                  # ...but every command boundary was snapshotted
c0ffee1  2 minutes ago   pre: git status
a1b2c3d  9 minutes ago   claude[b3f1a2c4]: Bash(go test ./...)
9e8d7c6  14 minutes ago  manual: before parser rewrite

$ jog restore --all        # worktree back to the newest snapshot
restored to c0ffee1 (2 minutes ago — pre: git status): 14 restored, 0 deleted
(undo: jog restore --all)
```

- [Why jog](#why-jog)
- [Install](#install)
- [Agents](#agents)
- [Editors](#editors)
- [Usage](#usage)
- [Recovery cookbook](#recovery-cookbook)
- [What jog will never touch](#what-jog-will-never-touch)
- [Why not jog](#why-not-jog)
- [Configuration](#configuration)
- [How it compares](#how-it-compares)

## Why jog

Pick the pitch that fits how you work:

- **A working-tree reflog.** Git has a safety net for refs; jog extends the
  same idea to your uncommitted changes. Every snapshot records *what command
  it ran ahead of* — the timeline is an operation log, not a smear of states.
- **The missing half of Claude Code's `/rewind`.** Claude's checkpoints
  restore Edit/Write changes but explicitly not bash-made changes, manual
  edits, or untracked files — and they expire. jog covers exactly that
  complement, at the same prompt/tool-call boundaries, with retention you
  control: `/rewind` the conversation, `jog restore --all` the world.
- **[jj]'s snapshot model wearing git's skin.** Snapshot at the start of
  every command — but the commands are the git you already type.

[jj]: https://github.com/jj-vcs/jj

### How it works

- Snapshots are plain git blobs/trees/commits in your repo's own object
  database, on refs under `refs/jog/<branch>` — invisible to branches, the
  index, `git log`, teammates, and remotes. Unchanged files cost zero bytes.
  There is no jog database.
- Snapshots are **command-triggered, causally before the command runs** — the
  only ordering that protects against `reset --hard` and `checkout -f`.
  (Editor save hooks are the one post-state exception: there the saved
  state *is* the checkpoint.) No daemon, no filesystem watcher, no git
  hooks to install or break.
- Four triggers: the shell alias (every git command you type), agent hooks
  (Claude Code, Codex, Copilot, Cursor, Gemini CLI, OpenCode — before
  every prompt and mutating tool call), editor save hooks (vim, emacs,
  VS Code, JetBrains, and more — every save becomes a checkpoint), and
  `jog` itself (deliberate checkpoints). A no-op snapshot costs a few
  tens of milliseconds.
- jog never reimplements a git verb, and never touches your index, HEAD,
  branches, or config. If jog vanished tomorrow, stock git reads every
  snapshot ([see below](#no-lock-in)).

> [!NOTE]
> jog is under active daily dogfood. The snapshot engine's behavior is
> lab-verified against git 2.50; every verified fact ships as a test.

## Install

**1. Install the binary:**

```sh
# install script (linux / macOS) — verifies checksums, lands in ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/tyler-johnson/jog/main/install.sh | sh

# Homebrew (macOS / Linux)
brew install tyler-johnson/tap/jog

# or with Go 1.26+
go install github.com/tyler-johnson/jog/cmd/jog@latest
```

```powershell
# Windows (PowerShell) — installs and adds jog to your user PATH
irm https://raw.githubusercontent.com/tyler-johnson/jog/main/install.ps1 | iex
```

Prebuilt binaries for linux/darwin/windows (amd64/arm64) are on the
[releases page](https://github.com/tyler-johnson/jog/releases). Script
installs update themselves later with `jog update` (brew and go installs
keep their own upgrade commands, and `jog update` says which). jog also
checks weekly in the background and mentions a new release once, after a
git command; `jog config updateCheck false` opts out.

**2. Add the alias** — this is how you "remember" to snapshot: you don't.
Muscle memory is the trigger; the compulsive `git status` tic becomes the
snapshot heartbeat.

```sh
# bash / zsh (~/.bashrc / ~/.zshrc)
alias git='jog git'

# fish
alias git 'jog git'
```

```powershell
# PowerShell (add to your profile — `notepad $PROFILE`)
function git { jog git @args }
```

Scripts, IDEs, and CI are untouched — they resolve `git` on PATH and get real
git. That's a feature: jog stays out of every code path that expects exact
git behavior.

**3. Wire up your agents and editors** (optional):

```sh
jog agents install        # hooks + skill for every agent client on this machine
jog editors install vim   # a post-save hook for your editor, one at a time
```

The [Agents](#agents) and [Editors](#editors) sections below have the
full picture — what's supported, where the wiring lands, and what each
surface does.

**4. Verify:** make any change in a repo, run `git status`, then `jog log`
— you should see a `pre: git status` entry.

## Agents

Agents are why jog exists twice over: they're the fastest writers your
worktree has ever had, and the first to delete something you still needed.
jog integrates with every supported client at two surfaces:

- **Hooks** — snapshot before every prompt and every mutating tool call,
  so each agent action has a boundary to roll back to.
- **A skill** — teaches the agent the recovery workflow itself: list
  versions with `jog log`, restore with `jog restore`, checkpoint with
  `jog -m` before risky work, and never declare uncommitted work gone
  without checking jog first.

| client | hooks | skill |
|---|---|---|
| Claude Code | `~/.claude/settings.json` — PreToolUse + UserPromptSubmit | `~/.claude/skills/jog/` |
| Codex | `~/.codex/hooks.json` — needs a one-time trust via `/hooks` | `~/.agents/skills/jog/` |
| Copilot CLI | `~/.copilot/settings.json` | `~/.copilot/skills/jog/` |
| Cursor (IDE + CLI) | `~/.cursor/hooks.json` | `~/.cursor/skills/jog/` |
| Gemini CLI | `~/.gemini/settings.json` | `~/.gemini/skills/jog/` |
| OpenCode | plugin at `~/.config/opencode/plugins/jog.js` | `~/.config/opencode/skills/jog/` |

The installed hook:

- snapshots the working tree before every prompt and every mutating
  tool call
- introduces jog to the agent once per session — one line saying
  snapshots are live and how to restore (on the clients that support
  injecting context: Claude Code, Codex, Gemini, OpenCode)
- can never block the agent: it always exits 0, and exits in
  milliseconds outside git repos

```sh
jog agents install     # both surfaces, every client found on this machine
jog agents list        # every supported client and what's installed
jog agents uninstall   # removes exactly what install wrote
```

Everything installs globally, so one wiring covers every repo — pass
`--project` to scope it to the current repo instead.

> [!NOTE]
> Install is additive: existing config fields are preserved, malformed
> JSON is never rewritten, and uninstall refuses to delete a skill file
> carrying local edits. The install output prints every path it touched.

## Editors

Agents get hooks; so do humans. `jog editors install <editor>` drops a
post-save hook into your editor, so every save inside a git repo becomes a
restorable snapshot — `vim: save src/main.go` in the timeline. This is the
one trigger that snapshots *after* the fact: the saved state is the
checkpoint (the pre-save state is your editor's own undo).

| editor | hook lands in | takes effect |
|---|---|---|
| `vim` | `~/.vim/plugin/jog.vim` | new sessions |
| `nvim` | `~/.config/nvim/plugin/jog.vim` | new sessions |
| `emacs` | `~/.emacs.d/jog.el` | after one `(load …)` line in your init file |
| `sublime` | `Packages/User/jog.py` (per-OS path) | immediately |
| `kakoune` | `~/.config/kak/` (autoload, or sourced from kakrc) | new sessions |
| `micro` | `~/.config/micro/plug/jog/jog.lua` | new sessions |
| `vscode` | `~/.vscode/extensions/` + `~/.vscode-server/extensions/` (whichever exist) | after a full restart (remote windows: reload) |
| `jetbrains` | `.idea/watcherTasks.xml` — per project | after a project reload; needs the File Watchers plugin |

```sh
jog editors list             # every supported editor and what's installed
jog editors install vim      # one editor at a time — each has its own gotchas
jog editors uninstall vim    # removes exactly what install wrote
```

install and uninstall take exactly one editor per run — the install output
is where each editor's how-it-works and caveats are taught. Everything
installs globally except `jetbrains`, whose hook can only live in a
project's `.idea` directory: re-run it in each project you want covered.

> [!NOTE]
> The wired hook (`jog editor-hook <editor>`) always exits 0 and prints
> nothing — a jog failure can never disturb a save — and it exits in
> milliseconds outside git repos. Uninstall refuses to delete a hook file
> carrying local edits. GUI editors get jog's absolute path baked in
> (desktop launches don't inherit your shell's PATH); re-run install if
> you move jog. For VS Code Remote-SSH, run the install on the remote
> machine — its extension host loads from `~/.vscode-server`, and the
> "Install in SSH" button copies your local machine's jog path.

## Usage

Two disjoint namespaces, one rule: **`jog git` is the only door to git.**

| command | what it does |
|---|---|
| `jog` | snapshot now, show the top of the timeline |
| `jog -m "msg"` | snapshot with a message (`manual: msg`) |
| `jog log [-p] [-n N] [--all] [--json] [--format=F] [path…]` | browse the timeline — scrub with a diff preview, `r` restores after a y/n confirm; piped it prints plainly: id, age, provenance, files changed (`-p`: patches, `-n`: newest N, `--all`: every branch interleaved, `--json`: machine-readable, `--format`: git log format). With paths, browsing and restoring are scoped to those paths |
| `jog since [T] [path…]` | what changed since a snapshot (default: your last command boundary; `-p`: patches) |
| `jog restore <path>… [--at T]` | restore files from a snapshot (worktree only) |
| `jog restore --all [--at T]` | restore the whole tree, including deleting files created since |
| `jog git <args>` | snapshot, then run the real git command — what the alias expands to |
| `jog trim [--dry-run] [--gone]` | drop snapshots older than `jog.keep` (default 90 days); `--gone` also drops deleted branches' chains; the previous tip stays at `refs/jog/@trash/<branch>` until the next trim |
| `jog config [key [value]]` | list jog's settings with values and meanings — or get and set them |
| `jog doctor [--fix]` | verify invariants, wiring, and liveness (`--fix` repairs the gc config) |
| `jog agents install` | hooks + skill for every agent client on this machine (`uninstall`, `list`; `[hooks\|skill]` and client names narrow it; `--project`: this repo) |
| `jog editors install <name>` | a post-save snapshot hook for one text editor (`uninstall`, `list`) |
| `jog update` | update jog to the latest release, sha256-verified (script/binary installs; brew and go installs are pointed at their own upgrade command) |
| `jog version` | print jog's version |

`--at` (and `since`'s target slot) accepts a snap id from `jog log` or a
time: `--at 30m`, `--at 1h`, `--at 2d`, `--at 1w` — plus anything git's
date syntax accepts (`--at yesterday`, `--at 2.hours.ago`). Asking for a
time older than the oldest snapshot falls back to the oldest, with a
warning.

A reading rule for the timeline: provenance records **what jog was running
ahead of**, never who made the changes. Manual edits swept up by an
agent-triggered snapshot are attributed to that trigger — jog can't know who
typed between boundaries, and refuses to guess.

`snaps` and `pick` are aliases of `jog log`, and `back` of `jog restore`
— older muscle memory keeps working.

`mcp` is reserved for a future release.
Anything else is an error — jog never guesses.

## Recovery cookbook

**An agent deleted a file three prompts ago.** `jog log src/config.yaml`
opens the browser scoped to the file — scrub to the version you want and
press `r`. Or entirely from the prompt:

```console
$ jog log src/config.yaml | head    # piped: the plain list
f3a9b12  8 minutes ago  claude[b3f1a2c4]: Bash(rm -rf src/old)

D	src/config.yaml
$ jog restore src/config.yaml --at 9m
restored src/config.yaml from 2c4d6e8 (9 minutes ago — claude[b3f1a2c4]: prompt "clean up the src tree")
```

**Roll everything back to before a refactor went sideways** — including
deleting the files it scattered:

```console
$ jog restore --all --at 25m
restored to 9e8d7c6 (26 minutes ago — manual: before parser rewrite): 11 restored, 3 deleted
(undo: jog restore --all)
```

Every restore snapshots first, so undo is itself undoable.

**What did I actually change in the last hour?**

```console
$ jog since 1h           # per-file summary since the snapshot nearest 1h ago
$ jog log -p             # full patches, in your pager
$ jog log src/parser/    # just one path's history
```

**Find the version of one file where it still worked**, scrubbing visually:

```console
$ jog log src/parser/lexer.go
```

**Script the timeline** — agents and scripts get structure without knowing
how snapshots map onto git refs:

```console
$ jog log --json -n 5           # newest five: sha, ISO time, provenance, chain, files
$ jog log --json src/ | jq -r '.[].provenance'
$ jog log --format='%h %cI %s'  # any git log format, one entry per line
```

**The timeline is getting long.** Drop everything older than 90 days
(configurable):

```console
$ jog trim --dry-run     # the plan, touching nothing
$ jog trim               # apply; the pre-trim tip stays at refs/jog/@trash/<branch>
$ jog trim --gone        # also drop chains whose branch no longer exists
```

Trim is the only jog command that discards snapshots, it never runs on its
own, and its last pre-trim state survives until the trim after next. A
chain whose snapshots have all aged out is removed whole, so deleted
branches' timelines eventually vanish on their own — `--gone` skips the
wait. Both trim and `jog doctor` report how much disk the snapshots hold;
the `maxSize` setting adds a total disk budget that trim enforces by
dropping oldest snapshots first, one snapshot leniently — the snapshot
that crosses the budget stays.

## What jog will never touch

1. Your **index** — byte-identical across any jog operation (jog stages into
   a private shadow index; all reads use `--no-optional-locks`).
2. Your **worktree** — written only by an explicit `jog restore`, which is
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
- Retention is manual: run `jog trim` when you want old snapshots dropped
  — nothing schedules it behind your back. Space cost is low either way
  (content-addressed, delta-compressed by repack); `jog doctor` shows the
  actual number, and `maxSize` can cap it.
- New files over 50 MiB are skipped (configurable, see below) and listed in
  the timeline entry.

## Configuration

Native `git config` keys — global or per-repo, no new file format.
`jog config` lists every setting with its current value and meaning;
`jog config <key> <value>` sets one (`--global`, `--unset`), with values
validated through git's own parsers:

| key | default | meaning |
|---|---|---|
| `jog.maxFileSize` | `50m` | skip new files larger than this (`0` disables the guard) |
| `jog.keep` | `90.days` | `jog trim` drops snapshots older than this (`never` keeps everything) |
| `jog.maxSize` | `0` (off) | total disk budget for snapshots — `jog trim` drops oldest first until the estimate fits (one snapshot lenient) |
| `jog.updateCheck` | `true` | weekly background release check, plus a one-line notice once per release after a git command (`false` disables both) |
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
  agent changes, short retention, not in git. jog's [editor hooks](#editors)
  put saves on the same timeline as everything else.
- **Claude Code checkpoints** — conversation + Edit/Write only; no bash
  changes, no untracked files, capped at 100 checkpoints / 30 days. jog is
  the other half.

## License

[MIT](LICENSE)
