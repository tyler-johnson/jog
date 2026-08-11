package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tyler-johnson/jog/internal/selfupdate"
)

// Config is `jog config` — jog's settings, all of them, in one place.
// The values are plain git config under the jog.* namespace (`git
// config` reads and writes them identically); this command's real job is
// the listing: every key, its effective value, its default, and what it
// means, so neither people nor agents have to go find docs.
//
// Sets are validated through git's own parsers (--type=int /
// --type=expiry-date) against a throwaway config file before anything is
// written: the readers fall back to defaults on unparsable values, so a
// silently-accepted typo would mean a setting that looks set but isn't.

type configOption struct {
	key  string // full git config key
	def  string // raw default value, as git would print it
	kind string // "int" (git int, k/m/g suffixes ok), "expiry" (git expiry syntax), "bool", or "check" (expiry or bool)
	desc string // pre-wrapped description lines
}

// name is the setting's jog-facing spelling: the git key minus the jog.
// prefix. The prefix is storage detail; jog's own surface never needs it.
func (o configOption) name() string { return o.key[4:] }

var configOptions = []configOption{
	{
		key: "jog.maxFileSize", def: "52428800", kind: "int",
		desc: "Largest new file a snapshot will include, in bytes (52428800 = 50 MiB;\n" +
			"suffixes work: 100M, 1G). Bigger files are skipped and the timeline\n" +
			"lists them. 0 disables the guard.",
	},
	{
		key: "jog.keep", def: "90.days", kind: "expiry",
		desc: "How long snapshots live: `jog trim` drops everything older, and a\n" +
			"chain whose snapshots have all aged out is removed whole. Takes git\n" +
			"expiry syntax (30.days, 6.months, never — never keeps everything).",
	},
	{
		key: "jog.maxSize", def: "0", kind: "int",
		desc: "Total disk budget for snapshots: when they hold more than this,\n" +
			"`jog trim` drops oldest snapshots first — tightening the age cutoff\n" +
			"below keep — until the estimate fits, one snapshot leniently (the\n" +
			"snapshot that crosses the budget survives). 0 = no budget (the\n" +
			"default). Suffixes work: 500M, 2G.",
	},
	{
		key: "jog.updateCheck", def: "1.day", kind: "check",
		desc: "How often jog looks for new releases in the background. Takes git\n" +
			"expiry syntax (12.hours, 2.weeks), a number of seconds (3600), or a\n" +
			"bool: true means the default daily, false turns checking off\n" +
			"entirely — no updates, no notices, regardless of autoUpdate.",
	},
	{
		key: "jog.autoUpdate", def: "true", kind: "bool",
		desc: "Install new releases automatically in the background — the running\n" +
			"command finishes on the old version, the next one runs the new.\n" +
			"false prints a one-line notice after a git command instead, once\n" +
			"per release. Homebrew and source installs always get the notice\n" +
			"(their package manager owns the binary).",
	},
}

const configUsage = "jog: usage: jog config [<key> [<value>] | --unset <key>] [--global]"

func Config(args []string) int {
	global := false
	unset := false
	var rest []string
	for _, a := range args {
		switch a {
		case "--global":
			global = true
		case "--unset":
			unset = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintln(os.Stderr, configUsage)
				return 2
			}
			rest = append(rest, a)
		}
	}

	if len(rest) == 0 && !unset {
		return configList()
	}
	if len(rest) == 0 || len(rest) > 2 || (unset && len(rest) != 1) {
		fmt.Fprintln(os.Stderr, configUsage)
		return 2
	}

	opt, ok := findOption(rest[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "jog: unknown setting %q — `jog config` lists them all\n", rest[0])
		return 2
	}

	scope := []string{}
	if global {
		scope = append(scope, "--global")
	}
	switch {
	case unset:
		out, err := gitConfig(append(scope, "--unset", opt.key)...)
		if err != nil {
			// git exits non-zero when the key was not set; that outcome is
			// the goal state, not a failure — unless a wider scope still
			// carries a value this unset didn't reach.
			if out == "" {
				if v, gerr := gitConfig("--get", opt.key); gerr == nil {
					fmt.Printf("%s is not set here, but %s still applies from a wider scope — try --global\n", opt.name(), v)
				} else {
					fmt.Printf("%s is not set — the default (%s) applies\n", opt.name(), opt.def)
				}
				return 0
			}
			fmt.Fprintf(os.Stderr, "jog: %s\n", out)
			return 1
		}
		syncUpdateCheck(opt)
		if v, gerr := gitConfig("--get", opt.key); gerr == nil {
			fmt.Printf("%s unset here — %s still applies from a wider scope\n", opt.name(), v)
			return 0
		}
		fmt.Printf("%s unset — back to the default (%s)\n", opt.name(), opt.def)
		return 0
	case len(rest) == 1:
		if val, err := gitConfig("--get", opt.key); err == nil {
			fmt.Println(val)
		} else {
			fmt.Println(opt.def)
		}
		return 0
	default:
		value := rest[1]
		if err := validateValue(opt, value); err != nil {
			fmt.Fprintf(os.Stderr, "jog: %v\n", err)
			return 2
		}
		if out, err := gitConfig(append(scope, opt.key, value)...); err != nil {
			fmt.Fprintf(os.Stderr, "jog: %s\n", out)
			return 1
		}
		syncUpdateCheck(opt)
		where := "this repo"
		if global {
			where = "every repo"
		}
		fmt.Printf("%s = %s (%s)\n", opt.name(), value, where)
		return 0
	}
}

func configList() int {
	for i, opt := range configOptions {
		if i > 0 {
			fmt.Println()
		}
		value, note := opt.def, " (default)"
		if v, err := gitConfig("--get", opt.key); err == nil {
			value, note = v, ""
		}
		fmt.Println(styleTitle.Render(opt.name()) + "  " + value + styleDim.Render(note))
		for line := range strings.SplitSeq(opt.desc, "\n") {
			fmt.Println("  " + line)
		}
	}
	fmt.Println()
	fmt.Println("Set with:      " + styleDim.Render("jog config <key> <value> (--global: every repo)"))
	fmt.Println("Remove with:   " + styleDim.Render("jog config --unset <key>"))
	fmt.Println(styleDim.Render("Config is stored in git config under jog.<key>"))
	return 0
}

// findOption matches git-config-style: key names are case-insensitive,
// and the jog. prefix is optional.
func findOption(name string) (configOption, bool) {
	short := name
	if len(name) > 4 && strings.EqualFold(name[:4], "jog.") {
		short = name[4:]
	}
	for _, opt := range configOptions {
		if strings.EqualFold(opt.key[4:], short) {
			return opt, true
		}
	}
	return configOption{}, false
}

// validateValue runs the value through git's own parser for the option's
// type, against a throwaway config file — jog never second-guesses git's
// syntax, and nothing invalid ever reaches the real config.
func validateValue(opt configOption, value string) error {
	f := filepath.Join(os.TempDir(), fmt.Sprintf("jog-config-probe-%d", os.Getpid()))
	defer os.Remove(f)
	if _, err := gitConfig("--file", f, opt.key, value); err != nil {
		return fmt.Errorf("%q is not a valid value for %s", value, opt.name())
	}
	typeFlag := "--type=int"
	example := "a byte count like 52428800, 100M, or 0"
	switch opt.kind {
	case "expiry":
		typeFlag = "--type=expiry-date"
		example = "git expiry syntax like 3.days, 2.weeks, or never"
	case "bool":
		typeFlag = "--type=bool"
		example = "true or false"
	case "check":
		// Either a bool or an expiry duration: whichever git parser
		// accepts it wins.
		if _, err := gitConfig("--file", f, "--type=bool", "--get", opt.key); err == nil {
			return nil
		}
		typeFlag = "--type=expiry-date"
		example = "an interval like 12.hours, 2.weeks, or seconds (3600), or a bool"
	}
	if _, err := gitConfig("--file", f, typeFlag, "--get", opt.key); err != nil {
		return fmt.Errorf("%q is not a valid value for %s (want %s)", value, opt.name(), example)
	}
	return nil
}

// syncUpdateCheck pushes a changed check cadence into the update cache
// right away — the hot path decides staleness from the cached value, so
// without this a new cadence would only apply after the next check
// under the old one.
func syncUpdateCheck(opt configOption) {
	if opt.key == "jog.updateCheck" {
		selfupdate.SyncInterval()
	}
}

// gitConfig shells out to real `git config` with the given arguments.
// The combined output comes back trimmed; on error it carries git's
// message.
func gitConfig(args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"config"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
