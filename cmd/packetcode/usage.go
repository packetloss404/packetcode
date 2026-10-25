package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// The subcommand table, and the reason it is a table.
//
// packetcode had four subcommands and `--help` listed none of them: the flag
// package prints flags, and nothing had ever told it there was anything else.
// doctor, skills, acp and sugar were reachable only by reading main.go, which
// is a poor way to find out that a diagnostic command exists.
//
// Fixing the text alone would have fixed today's symptom. The actual defect is
// that dispatch and help were two lists maintained by hand, and adding to one
// while forgetting the other is the easiest mistake available -- it is how this
// happened. One table, read by both, is what stops it happening again.

// subcommand is one `packetcode <name>` verb.
type subcommand struct {
	name string
	// args is the argument shape shown in help, empty when there are none.
	args string
	// summary is one line, lowercase, no trailing period -- it sits in a
	// column beside its siblings.
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
}

func subcommands() []subcommand {
	return []subcommand{
		{
			name:    "run",
			args:    "[flags] <prompt...>",
			summary: "run one non-interactive agent turn and print the final response",
			run:     func(a []string, o, e io.Writer) int { return runRunCommand(a, o, e) },
		},
		{
			name:    "doctor",
			args:    "[--json] [--check SECTION]",
			summary: "check configuration, providers, MCP servers and stored state",
			run:     func(a []string, o, e io.Writer) int { return runDoctorCommand(a, o, e) },
		},
		{
			name:    "skills",
			args:    "<list|install|remove|path>",
			summary: "manage Agent Skills, including installing them from a git repository",
			run:     func(a []string, o, e io.Writer) int { return runSkillsCommand(a, o, e) },
		},
		{
			name:    "acp",
			args:    "[flags]",
			summary: "serve the Agent Client Protocol on stdio, for editor integration",
			run:     func(a []string, o, e io.Writer) int { return runACPCommand(a, os.Stdin, o, e) },
		},
		{
			name:    "sugar",
			args:    "login [--server URL]",
			summary: "authenticate with a Sugar server",
			run:     runSugarCommand,
		},
	}
}

// runSugarCommand keeps sugar's own nested verb where it belongs, rather than
// spreading a second level of dispatch through the table.
func runSugarCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(stdout, "usage: packetcode sugar login [--server URL] [--name NAME] [--no-browser]")
		return 0
	}
	if len(args) >= 1 && args[0] == "login" {
		return runSugarLoginCommand(args[1:], stdout, stderr)
	}
	fmt.Fprintln(stderr, "usage: packetcode sugar login [--server URL] [--name NAME] [--no-browser]")
	return 2
}

// parseCommandFlags keeps requested help on stdout with a successful exit,
// while flag errors and their usage stay on stderr. Capture parser output so
// its destination follows the actual parse result, including help after flags.
func parseCommandFlags(fs *flag.FlagSet, args []string, stdout, stderr io.Writer) error {
	return routeCommandParse(fs, stdout, stderr, func() error { return fs.Parse(args) })
}

func routeCommandParse(fs *flag.FlagSet, stdout, stderr io.Writer, parse func() error) error {
	var output bytes.Buffer
	previous := fs.Output()
	fs.SetOutput(&output)
	err := parse()
	fs.SetOutput(previous)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = io.Copy(stdout, &output)
	} else {
		_, _ = io.Copy(stderr, &output)
	}
	return err
}

// cliFlags is the top-level flag set, declared in one place so help and main
// cannot disagree about what exists — the same reason subcommands() is a table.
type cliFlags struct {
	version        *bool
	provider       *string
	model          *string
	resume         *string
	trust          *bool
	permissionMode *string
	computer       *string
	tuiFixture     *string
}

// registerFlags declares the top-level flags on fs.
//
// A function rather than inline in main so that anything needing the flag set —
// help, and the test that checks help lists it — can build a real one without
// running main.
func registerFlags(fs *flag.FlagSet) *cliFlags {
	return &cliFlags{
		version:        fs.Bool("version", false, "print version and exit"),
		provider:       fs.String("provider", "", "override default provider for this session"),
		model:          fs.String("model", "", "override default model for this session"),
		resume:         fs.String("resume", "", "resume a saved session by ID"),
		trust:          fs.Bool("trust", false, "auto-approve all tool actions for this session"),
		permissionMode: fs.String("permission-mode", "", "override permission profile for this session (ask, accept-edits, auto, read-only, bypass)"),
		computer:       fs.String("computer", "", "use a registered SSH computer as the active workspace"),
		tuiFixture:     fs.String("tui-fixture", "", "render a deterministic TUI lifecycle fixture (development/testing)"),
	}
}

// printUsage writes the top-level help for fs.
//
// The flag set is a parameter rather than flag.CommandLine because the flags
// are registered by main, and reaching for the global made this silently print
// an empty Flags section anywhere main had not run -- including its own test,
// and including the first version of this function, which was called before
// the flags were declared and looked complete while listing nothing.
func printUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `packetcode — a keyboard-first terminal coding agent.

Usage:
  packetcode [flags]            start the interactive session in the current directory
  packetcode <command> [args]   run a command and exit

Commands:
`)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range subcommands() {
		invocation := c.name
		if c.args != "" {
			invocation += " " + c.args
		}
		fmt.Fprintf(tw, "  %s\t%s\n", invocation, c.summary)
	}
	_ = tw.Flush()

	fmt.Fprint(w, "\nFlags:\n")
	// The flag package writes its defaults to its own configured output, so
	// point it at ours for the duration rather than assuming they match.
	previous := fs.Output()
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(previous)

	fmt.Fprint(w, `
Run "packetcode <command> --help" for a command's own flags.
`)
}

// wantsTopLevelHelp reports whether args ask for the help above, as opposed to
// a subcommand's own.
//
// Only the FIRST argument counts. `packetcode doctor --help` must reach doctor
// and print doctor's flags; answering it here would make the top-level help
// actively misleading, since it would appear to be the help for whatever the
// user actually typed.
func wantsTopLevelHelp(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch strings.TrimSpace(args[0]) {
	case "help", "-h", "-help", "--help":
		return true
	}
	return false
}

// rejectTopLevelFlagsBeforeSubcommand prevents a successfully parsed global
// flag from being silently discarded when a command owns its own FlagSet.
// Besides being surprising for provider/model flags, that is unsafe for a
// permission flag: an explicit read-only request must not fall back to a more
// permissive saved profile merely because it appeared before the command.
func rejectTopLevelFlagsBeforeSubcommand(fs *flag.FlagSet, args []string, w io.Writer) bool {
	if fs == nil || len(args) == 0 {
		return false
	}
	command := ""
	for _, candidate := range subcommands() {
		if args[0] == candidate.name {
			command = candidate.name
			break
		}
	}
	if command == "" {
		return false
	}
	var names []string
	fs.Visit(func(f *flag.Flag) {
		names = append(names, "--"+f.Name)
	})
	if len(names) == 0 {
		return false
	}
	fmt.Fprintf(w, "packetcode %s: top-level flag(s) %s cannot precede a subcommand; use 'packetcode %s --help' and pass only supported command flags after %s\n",
		command, strings.Join(names, ", "), command, command)
	return true
}
