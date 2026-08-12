// Package setup implements `jog install` and `jog uninstall` — the
// guided walk through jog's three wiring surfaces: the shell (the git
// alias and the preexec hook), agent hooks, and editor save hooks.
//
// install asks every question first, then runs the wiring in one quiet
// batch and summarizes exactly what changed — no installer output
// between questions. It owns no mechanics: every yes routes to the same
// installer the standalone command uses (`jog shell`, `jog agents`,
// `jog editors`), so idempotence and edge handling are identical
// whichever door the user came in.
//
// Questions need no TTY: they are plain stdin lines, so piped answers
// and `--yes` (take every default) work in scripts. In a terminal the
// agents and editors questions upgrade to an inline checkbox picker
// over the detected names; piped, they stay plain lines. EOF (or
// cancelling a picker) stops the remaining questions; whatever was
// already answered yes still runs, because every step is additive and
// idempotent.
package setup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/tyler-johnson/jog/internal/agents"
	"github.com/tyler-johnson/jog/internal/editors"
	"github.com/tyler-johnson/jog/internal/install"
	"github.com/tyler-johnson/jog/internal/selfupdate"
	"github.com/tyler-johnson/jog/internal/shell"
	"github.com/tyler-johnson/jog/internal/tui"
)

const installUsage = "jog: usage: jog install [--yes] [--[no-]alias] [--[no-]preexec] [--[no-]agents [<names>]] [--[no-]editors [<names>]]"
const uninstallUsage = "jog: usage: jog uninstall [--yes]"

// installFlags carries each question's pre-answer from the command
// line: nil means ask (or take the default under --yes). A flagged
// question is never asked, so any subset of the wizard can be scripted
// — which is also how a coding agent runs it for someone. --agents and
// --editors optionally take names (comma- or space-separated); named
// clients install even when not auto-detected, same as the standalone
// `jog agents install <name>`.
type installFlags struct {
	yes                             bool
	alias, preexec, agents, editors *bool
	agentNames, editorNames         []string
}

func parseInstall(args []string) (installFlags, bool) {
	var f installFlags
	set := func(p **bool, v bool) bool {
		if *p != nil && **p != v {
			return false // --x and --no-x together is a contradiction
		}
		*p = &v
		return true
	}
	names := func(p **bool, into *[]string, v string) bool {
		if !set(p, true) {
			return false
		}
		for _, n := range strings.Split(v, ",") {
			if n = strings.TrimSpace(n); n != "" {
				*into = append(*into, n)
			}
		}
		return true
	}
	for i := 0; i < len(args); i++ {
		a, ok := args[i], true
		if v, has := strings.CutPrefix(a, "--agents="); has {
			ok = names(&f.agents, &f.agentNames, v)
		} else if v, has := strings.CutPrefix(a, "--editors="); has {
			ok = names(&f.editors, &f.editorNames, v)
		} else {
			switch a {
			case "--yes", "-y":
				f.yes = true
			case "--alias":
				ok = set(&f.alias, true)
			case "--no-alias":
				ok = set(&f.alias, false)
			case "--preexec":
				ok = set(&f.preexec, true)
			case "--no-preexec":
				ok = set(&f.preexec, false)
			case "--agents", "--editors":
				p, into := &f.agents, &f.agentNames
				if a == "--editors" {
					p, into = &f.editors, &f.editorNames
				}
				// Bare = yes for everything detected; following bare
				// words are names, so `--agents claude codex` reads
				// naturally.
				got := false
				for ok && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					got, ok = true, names(p, into, args[i])
				}
				if !got && ok {
					ok = set(p, true)
				}
			case "--no-agents":
				ok = set(&f.agents, false)
			case "--no-editors":
				ok = set(&f.editors, false)
			default:
				ok = false
			}
		}
		if !ok {
			fmt.Fprintln(os.Stderr, installUsage)
			return f, false
		}
	}
	return f, true
}

// unknownName returns the first name not present in known — flag-named
// clients are checked against the registry before anything runs.
func unknownName(names, known []string) string {
	set := map[string]bool{}
	for _, k := range known {
		set[k] = true
	}
	for _, n := range names {
		if !set[n] {
			return n
		}
	}
	return ""
}

// asker answers questions: interactively from stdin, or with the
// default under --yes. ok=false means stdin closed — stop asking.
type asker func(prompt string, def bool) (answer, ok bool)

func newAsker(yes bool) asker {
	if yes {
		return func(_ string, def bool) (bool, bool) { return def, true }
	}
	r := bufio.NewReader(os.Stdin)
	return func(prompt string, def bool) (bool, bool) { return readAnswer(r, prompt, def) }
}

func parseYes(args []string, usage string) (yes, ok bool) {
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		default:
			fmt.Fprintln(os.Stderr, usage)
			return false, false
		}
	}
	return yes, true
}

// supportedNames lists every agent client and editor jog knows, for
// validating flag-named entries before anything runs.
func supportedNames() (agentNames, editorNames []string) {
	for _, st := range agents.Statuses() {
		agentNames = append(agentNames, st.Name)
	}
	for _, st := range editors.Statuses() {
		editorNames = append(editorNames, st.Name)
	}
	return
}

// checkAll turns detected names into checkbox items, all pre-checked —
// the picker's default matches the plain questions' default yes.
func checkAll(names []string) []tui.CheckItem {
	items := make([]tui.CheckItem, len(names))
	for i, n := range names {
		items[i] = tui.CheckItem{Name: n, Checked: true}
	}
	return items
}

// wiredRow is one summary line — a surface that was installed or
// removed, and where.
type wiredRow struct{ name, surface, where string }

// printWired prints the summary block both wizards share: a header,
// then one green-check row per surface that actually changed.
func printWired(header string, rows []wiredRow) {
	fmt.Println(header)
	for _, r := range rows {
		fmt.Printf("  %s %-10s %-7s %s\n", install.StyleGood.Render("✓"), r.name, r.surface, r.where)
	}
}

// Install is `jog install`: every question first, then the wiring in
// one quiet batch, then a summary of exactly what was installed.
func Install(args []string) int {
	f, ok := parseInstall(args)
	if !ok {
		return 2
	}
	allAgents, allEditors := supportedNames()
	if n := unknownName(f.agentNames, allAgents); n != "" {
		fmt.Fprintf(os.Stderr, "jog: unknown agent client %q (supported: %s)\n", n, strings.Join(allAgents, ", "))
		return 2
	}
	if n := unknownName(f.editorNames, allEditors); n != "" {
		fmt.Fprintf(os.Stderr, "jog: unknown editor %q (supported: %s)\n", n, strings.Join(allEditors, ", "))
		return 2
	}
	// spoke tracks whether the question phase put anything on screen —
	// prompts or notes — so the summary only sets itself apart with a
	// blank line when there is something to be apart from.
	spoke := false
	baseAsk := newAsker(f.yes)
	ask := func(prompt string, def bool) (bool, bool) {
		spoke = spoke || !f.yes
		return baseAsk(prompt, def)
	}
	// answered resolves one question: the command-line flag wins, then
	// the asker — a real prompt, or the default under --yes.
	answered := func(flag *bool, prompt string, def bool) (bool, bool) {
		if flag != nil {
			return *flag, true
		}
		return ask(prompt, def)
	}
	note := func(n string) {
		spoke = true
		fmt.Println(install.StyleDim.Render("· " + n))
	}

	fmt.Println(install.StyleTitle.Render("jog install") + install.StyleDim.Render(" — the shell wiring, agent hooks, and editor hooks"))
	fmt.Println()

	// Phase one: the questions. Nothing runs yet — the answers become a
	// plan, so a later question never scrolls an earlier step's output
	// away.
	type shellChoice struct {
		name           string
		alias, preexec bool
	}
	var shellPlan []shellChoice
	var agentPlan, editorPlan []string
	aborted := false
	// In a terminal, the agents and editors questions become inline
	// checkbox pickers over the detected names. Piped input keeps every
	// question a plain stdin line, so scripts and tests drive the wizard
	// the same as ever; --yes never prompts at all.
	interactive := !f.yes && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))

	// The shell: two questions for the login shell — the alias, then the
	// preexec hook (where the shell has one). Other shells with an rc
	// file on disk are offered as follow-ups and get the same surfaces
	// the user chose here.
	login, haveLogin := shell.Login()
	if !haveLogin {
		note("cannot tell your shell from $SHELL — `jog shell install <name>` wires the alias by hand")
	}
	var aliasWant, preexecWant bool
	for _, st := range shell.Statuses() {
		if st.Name != login {
			continue
		}
		aliasNew, preexecNew := false, false
		if st.Installed || st.ByHand {
			note(st.Name + " already has the alias (" + st.RC + ")")
			aliasWant = true
		} else if answer, ok := answered(f.alias, "add `"+st.Line+"` to "+st.RC+"?", true); !ok {
			aborted = true
		} else {
			aliasWant, aliasNew = answer, answer
		}
		// Default No: hooking every command is a bigger ask than an
		// alias — it is wired only on an explicit yes, so --yes stays
		// alias-only.
		if !aborted && st.Preexec != "" {
			if st.PreexecInstalled || st.PreexecByHand {
				note(st.Name + " already has the preexec hook (" + st.RC + ")")
				preexecWant = true
			} else if answer, ok := answered(f.preexec, "also snapshot before every command, not just git? (preexec hook in "+st.RC+")", false); !ok {
				aborted = true
			} else {
				preexecWant, preexecNew = answer, answer
			}
		}
		if aliasNew || preexecNew {
			shellPlan = append(shellPlan, shellChoice{st.Name, aliasNew, preexecNew})
		}
	}
	for _, st := range shell.Statuses() {
		if aborted || st.Name == login || !st.RCExists {
			continue
		}
		wantAlias := aliasWant && !st.Installed && !st.ByHand
		wantPreexec := preexecWant && st.Preexec != "" && !st.PreexecInstalled && !st.PreexecByHand
		if !wantAlias && !wantPreexec {
			continue
		}
		prompt := "also found " + st.RC + " — add the alias there too?"
		if !wantAlias {
			prompt = "also found " + st.RC + " — add the preexec hook there too?"
		}
		answer, ok := ask(prompt, false)
		if !ok {
			aborted = true
			break
		}
		if answer {
			shellPlan = append(shellPlan, shellChoice{st.Name, wantAlias, wantPreexec})
		}
	}

	// Agents: one question covers every detected client — a checkbox per
	// client in a terminal, a single yes-covers-all line piped.
	if !aborted {
		var detected []string
		for _, st := range agents.Statuses() {
			if st.Detected {
				detected = append(detected, st.Name)
			}
		}
		switch {
		case f.agents != nil:
			switch {
			case !*f.agents:
			case len(f.agentNames) > 0:
				agentPlan = f.agentNames
			case len(detected) > 0:
				agentPlan = detected
			default:
				note("no agent clients detected — `jog agents list` shows the supported ones")
			}
		case len(detected) == 0:
			note("no agent clients detected — `jog agents list` shows the supported ones")
		case interactive:
			spoke = true
			chosen, cancelled, err := tui.RunCheckboxes("install hooks + skill for detected agents?", checkAll(detected))
			if err != nil || cancelled {
				aborted = true
			} else {
				agentPlan = chosen
			}
		default:
			if answer, ok := ask("install hooks + skill for detected agents ("+strings.Join(detected, ", ")+")?", true); !ok {
				aborted = true
			} else if answer {
				agentPlan = detected
			}
		}
	}

	// Editors: a checkbox per detected editor in a terminal, one plain
	// question each piped — either way each editor is its own opt-in.
	if !aborted {
		var detected []string
		for _, st := range editors.Statuses() {
			if st.Detected && st.Location == "" {
				detected = append(detected, st.Name)
			}
		}
		switch {
		case f.editors != nil:
			switch {
			case !*f.editors:
			case len(f.editorNames) > 0:
				editorPlan = f.editorNames
			case len(detected) > 0:
				editorPlan = detected
			default:
				note("no editors detected — `jog editors list` shows the supported ones")
			}
		case len(detected) == 0:
			note("no editors detected — `jog editors list` shows the supported ones")
		case interactive:
			spoke = true
			chosen, cancelled, err := tui.RunCheckboxes("install save hooks for detected editors?", checkAll(detected))
			if err != nil || cancelled {
				aborted = true
			} else {
				editorPlan = chosen
			}
		default:
			for _, name := range detected {
				answer, ok := ask(name+" detected — install its save hook?", true)
				if !ok {
					aborted = true
					break
				}
				if answer {
					editorPlan = append(editorPlan, name)
				}
			}
		}
	}

	// Phase two: run the plan. Answers already given are honored even
	// when a later question hit EOF — every yes still runs. The summary
	// lists only what actually changed; already-wired surfaces stay
	// quiet.
	var rows []wiredRow
	var notes []string
	code := 0
	fail := func(name, surface string, err error) {
		fmt.Fprintf(os.Stderr, "jog: %s %s: %v\n", name, surface, err)
		code = 1
	}

	shellWired, bashPS0 := false, false
	for _, c := range shellPlan {
		src := ""
		for _, w := range shell.Install(c.name, c.alias, c.preexec) {
			if w.Err != nil {
				fail(c.name, w.Surface, w.Err)
				continue
			}
			if !w.Changed {
				continue
			}
			rows = append(rows, wiredRow{c.name, w.Surface, w.Where})
			shellWired = true
			src = w.Source
			if c.name == "bash" && w.Surface == "preexec" {
				bashPS0 = true
			}
		}
		if src != "" {
			notes = append(notes, "takes effect in new "+c.name+" sessions — or `"+src+"` now")
		}
	}
	if bashPS0 {
		notes = append(notes, "the bash preexec line uses PS0 (bash 4.4+) — older bash ignores it harmlessly")
	}
	if shellWired {
		notes = append(notes, "scripts, IDEs, and CI still get real git — they resolve it on PATH")
	}
	for _, w := range agents.InstallClients(agentPlan) {
		if w.Err != nil {
			fail(w.Client, w.Surface, w.Err)
			continue
		}
		if w.Changed {
			rows = append(rows, wiredRow{w.Client, w.Surface, w.Where})
		}
	}
	for _, name := range editorPlan {
		where, changed, editorNotes, err := editors.Install(name)
		if err != nil {
			fail(name, "hook", err)
			continue
		}
		if changed {
			rows = append(rows, wiredRow{name, "hook", where})
			for _, n := range editorNotes {
				notes = append(notes, name+": "+n)
			}
		}
	}
	if len(rows) > 0 {
		if _, err := exec.LookPath("jog"); err != nil {
			notes = append(notes, "jog is not on PATH — the wiring will not work until it is")
		}
	}

	if spoke {
		fmt.Println()
	}
	switch {
	case len(rows) > 0:
		printWired("installed:", rows)
		if len(notes) > 0 {
			fmt.Println()
			for _, n := range notes {
				note(n)
			}
		}
	case len(shellPlan) > 0 || len(agentPlan) > 0 || len(editorPlan) > 0:
		note("everything chosen is already wired — nothing to change")
	default:
		note("nothing installed")
	}
	fmt.Println()
	if aborted {
		note("stopped early — everything already done stays; re-run `jog install` to continue")
	}
	fmt.Println("`jog doctor` verifies the wiring; a `git status` in any repo takes the first snapshot.")
	return code
}

// Uninstall is `jog uninstall`: show what is wired, confirm once, then
// run each surface's own uninstaller.
func Uninstall(args []string) int {
	yes, ok := parseYes(args, uninstallUsage)
	if !ok {
		return 2
	}

	var shells, preexecShells, sweepShells, agentNames, editorNames []string
	for _, st := range shell.Statuses() {
		if st.Installed {
			shells = append(shells, st.Name)
		}
		if st.PreexecInstalled {
			preexecShells = append(preexecShells, st.Name)
		}
		if st.Installed || st.PreexecInstalled {
			sweepShells = append(sweepShells, st.Name)
		}
	}
	for _, st := range agents.Statuses() {
		if st.HooksLocation != "" || st.SkillLocation != "" {
			agentNames = append(agentNames, st.Name)
		}
	}
	for _, st := range editors.Statuses() {
		if st.Location != "" {
			editorNames = append(editorNames, st.Name)
		}
	}

	fmt.Println(install.StyleTitle.Render("jog uninstall") + install.StyleDim.Render(" — remove jog's wiring"))
	fmt.Println()
	if len(sweepShells)+len(agentNames)+len(editorNames) == 0 {
		fmt.Println("nothing is wired — no alias, preexec hook, agent hooks, or editor hooks found.")
		binaryHint()
		return 0
	}
	row := func(kind string, names []string) {
		if len(names) > 0 {
			fmt.Printf("  %-8s %s\n", kind, strings.Join(names, ", "))
		}
	}
	fmt.Println("currently wired:")
	row("alias", shells)
	row("preexec", preexecShells)
	row("agents", agentNames)
	row("editors", editorNames)
	fmt.Println()

	if !yes {
		confirmed, ok := readAnswer(bufio.NewReader(os.Stdin), "remove all of it?", false)
		if !ok || !confirmed {
			fmt.Println(install.StyleDim.Render("nothing removed"))
			return 0
		}
		fmt.Println()
	}

	// Run the removals in one quiet batch, mirroring install: the
	// summary lists exactly what came out, and surfaces that turned out
	// to have nothing to remove stay silent.
	var rows []wiredRow
	code := 0
	fail := func(name, surface string, err error) {
		fmt.Fprintf(os.Stderr, "jog: %s %s: %v\n", name, surface, err)
		code = 1
	}
	for _, name := range sweepShells {
		for _, w := range shell.Uninstall(name, true, true) {
			if w.Err != nil {
				fail(name, w.Surface, w.Err)
				continue
			}
			if w.Changed {
				rows = append(rows, wiredRow{name, w.Surface, w.Where})
			}
		}
	}
	for _, w := range agents.UninstallClients(agentNames) {
		if w.Err != nil {
			fail(w.Client, w.Surface, w.Err)
			continue
		}
		if w.Changed {
			rows = append(rows, wiredRow{w.Client, w.Surface, w.Where})
		}
	}
	for _, name := range editorNames {
		where, changed, err := editors.Uninstall(name)
		if err != nil {
			fail(name, "hook", err)
			continue
		}
		if changed {
			rows = append(rows, wiredRow{name, "hook", where})
		}
	}

	if len(rows) > 0 {
		printWired("removed:", rows)
	} else {
		fmt.Println(install.StyleDim.Render("· nothing removed"))
	}
	fmt.Println()
	fmt.Println("snapshots are untouched — they live in each repo's refs/jog/*; `jog trim` prunes them.")
	binaryHint()
	return code
}

// binaryHint names the channel-appropriate way to remove the binary
// itself — the one thing uninstall deliberately does not do.
func binaryHint() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if selfupdate.IsBrewInstall(exe) {
		fmt.Println(install.StyleDim.Render("the binary itself: `brew uninstall jog`"))
		return
	}
	fmt.Println(install.StyleDim.Render("the binary itself: rm " + install.TildePath(exe)))
}
