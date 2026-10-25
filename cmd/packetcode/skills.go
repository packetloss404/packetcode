package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/packetcode/packetcode/internal/skills"
)

const skillsUsage = `usage: packetcode skills <command>

  list                       show every resolved skill and where it came from
  install <repo> [flags]     install skills from a git repository or local path
  remove <name> [flags]      delete an installed skill
  path                       print the skills directory for each scope

Install flags:
  --skill NAME     install only this skill; repeat for several
  --ref REF        branch or tag to clone
  --project        install into ./.packetcode/skills instead of the user scope
  --force          overwrite skills that are already installed

Examples:
  packetcode skills install naieum/snitchmarketplace
  packetcode skills install naieum/snitchmarketplace --skill snitch-docwriter
  packetcode skills install ./my-skill --project
  packetcode skills remove snitch-docwriter
`

func runSkillsCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, skillsUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return runSkillsList(args[1:], stdout, stderr)
	case "install":
		return runSkillsInstall(args[1:], stdout, stderr)
	case "remove", "uninstall":
		return runSkillsRemove(args[1:], stdout, stderr)
	case "path":
		fs := flag.NewFlagSet("skills path", flag.ContinueOnError)
		if err := parseCommandFlags(fs, args[1:], stdout, stderr); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() != 0 {
			fmt.Fprintf(stderr, "packetcode skills path: unexpected argument %q\n", fs.Arg(0))
			return 2
		}
		return runSkillsPath(stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, skillsUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "packetcode skills: unknown command %q\n\n%s", args[0], skillsUsage)
		return 2
	}
}

type skillListEntry struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Description string `json:"description"`
	Path        string `json:"path,omitempty"`
	Dir         string `json:"dir,omitempty"`
	Resources   int    `json:"resources"`
	// ResourcesTruncated reports that the listing stopped at
	// skills.MaxResourcesListed, so Resources is a floor rather than a count.
	// Dropping this made the JSON say "7 resources" about a directory holding
	// three hundred, which is not a rounding error but a wrong answer.
	ResourcesTruncated bool `json:"resources_truncated,omitempty"`
	BodyBytes          int  `json:"body_bytes"`
	// UserInvocable and ModelInvocable report who can reach the skill. Both
	// are emitted even when true: a consumer reading this to build a menu
	// needs the answer present, not inferred from an absent field.
	UserInvocable  bool `json:"user_invocable"`
	ModelInvocable bool `json:"model_invocable"`
	// Origin is the directory layout this was found under: "packetcode",
	// "claude" or "agents". Source says who wrote it; Origin says which
	// convention it was filed under.
	Origin string `json:"origin"`
	// Loaded is false for a repository skill filed under another agent's
	// layout that nobody has approved. Such a skill is listed and inert.
	Loaded bool `json:"loaded"`
}

func runSkillsList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("skills list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := parseCommandFlags(fs, args, stdout, stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "packetcode skills list: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "packetcode skills: %s\n", err)
		return 1
	}
	reg := skills.Load(cwd)

	entries := make([]skillListEntry, 0, len(reg.Skills()))
	for _, s := range reg.Skills() {
		list, truncated := reg.Resources(s.Name)
		entries = append(entries, skillListEntry{
			Name:               s.Name,
			Source:             s.Source,
			Description:        s.Description,
			Path:               s.Path,
			Dir:                s.Dir,
			Resources:          len(list),
			ResourcesTruncated: truncated,
			BodyBytes:          len(s.Body),
			UserInvocable:      s.UserInvocable(),
			ModelInvocable:     s.ModelInvocable(),
			Origin:             s.Origin,
			Loaded:             s.Enabled(),
		})
	}

	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{
			"skills":   entries,
			"errors":   reg.Errors(),
			"warnings": reg.Warnings(),
		}); err != nil {
			fmt.Fprintf(stderr, "packetcode skills: %s\n", err)
			return 1
		}
		if len(reg.Errors()) > 0 {
			return 1
		}
		return 0
	}

	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no skills resolved")
	}
	for _, e := range entries {
		// The leading slash is the difference between a name the model might
		// choose and a name the reader can type. A column of bare names says
		// nothing about which is which.
		name := e.Name
		// Loaded as well as UserInvocable: a pending skill is not typeable,
		// and printing "/deploy" beside "NOT LOADED" promises a command the
		// program will not run.
		if e.UserInvocable && e.Loaded {
			name = "/" + e.Name
		}
		// Clipped, not just padded: %-28s pads a short name but does nothing
		// to a long one, and one over-long skill directory name shifts every
		// following column on its row. Ecosystem skill names run long.
		fmt.Fprintf(stdout, "%-28s %-8s %5d B", truncateLine(name, 28), e.Source, e.BodyBytes)
		if e.Resources > 0 {
			fmt.Fprintf(stdout, "  %3d files", e.Resources)
			if e.ResourcesTruncated {
				fmt.Fprint(stdout, "+")
			}
		}
		if !e.ModelInvocable {
			fmt.Fprint(stdout, "  [not offered to the model]")
		}
		if !e.Loaded {
			// The loudest thing on the row. A skill that is present, listed,
			// and doing nothing is the one state a reader will otherwise
			// mistake for a working one.
			fmt.Fprintf(stdout, "  [NOT LOADED — found in .%s/skills/, needs /skills allow %s]",
				e.Origin, e.Name)
		}
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "  %s\n", truncateLine(e.Description, 110))
	}
	// Malformed skills are reported here rather than only inside a session,
	// because this is the command someone runs when a skill they installed did
	// not appear -- which is exactly when the reason needs to be visible.
	// Warnings first, and they never change the exit code. A shadowed skill or
	// a flag that did not parse is worth saying out loud; neither is a reason
	// to fail somebody's build, which is what Errors below does.
	if warns := reg.Warnings(); len(warns) > 0 {
		fmt.Fprintf(stdout, "\n%d warning(s):\n", len(warns))
		for _, w := range warns {
			fmt.Fprintf(stdout, "  %s\n", w)
		}
	}
	if errs := reg.Errors(); len(errs) > 0 {
		fmt.Fprintf(stdout, "\n%d skill(s) could not be loaded:\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(stdout, "  %s\n", e)
		}
		return 1
	}
	return 0
}

// truncateLine clips s to n runes.
//
// Runes, not bytes. Byte slicing splits a multi-byte character and emits a
// replacement glyph, and the descriptions this clips are ecosystem skill text
// full of em dashes and typographic apostrophes -- so the cut point landing
// mid-character is the common case, not the exotic one. internal/app's
// truncOneLine avoids this the same way.
func truncateLine(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "\u2026"
}

// parsePositionals parses flags that may appear after positional arguments.
//
// flag.Parse stops at the first non-flag argument, so "install REPO --force"
// would leave --force unparsed and the command would reject its own documented
// invocation. Re-parsing what follows each positional accepts flags on either
// side of it, which is the order people actually type.
func parsePositionals(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
}

func parseCommandPositionals(fs *flag.FlagSet, args []string, stdout, stderr io.Writer) (positional []string, err error) {
	err = routeCommandParse(fs, stdout, stderr, func() error {
		var parseErr error
		positional, parseErr = parsePositionals(fs, args)
		return parseErr
	})
	return positional, err
}

type repeatedString []string

func (r *repeatedString) String() string { return strings.Join(*r, ",") }
func (r *repeatedString) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*r = append(*r, part)
		}
	}
	return nil
}

func runSkillsInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("skills install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var names repeatedString
	fs.Var(&names, "skill", "install only this skill; repeat or comma-separate")
	ref := fs.String("ref", "", "branch or tag to clone")
	project := fs.Bool("project", false, "install into ./.packetcode/skills")
	force := fs.Bool("force", false, "overwrite skills that are already installed")
	positional, err := parseCommandPositionals(fs, args, stdout, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(positional) != 1 {
		fmt.Fprint(stderr, skillsUsage)
		return 2
	}

	opts := skills.InstallOptions{
		Repo:  positional[0],
		Ref:   *ref,
		Names: names,
		Scope: skills.ScopeUser,
		Force: *force,
	}
	if *project {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "packetcode skills: %s\n", err)
			return 1
		}
		opts.Scope = skills.ScopeProject
		opts.WorkingDir = cwd
	}

	result, err := skills.Install(opts, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "packetcode skills install: %s\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "\n%s\n", result.Dest)
	if n := len(result.Installed); n > 0 {
		fmt.Fprintf(stdout, "  installed %d: %s\n", n, strings.Join(result.Installed, ", "))
	}
	if n := len(result.Replaced); n > 0 {
		fmt.Fprintf(stdout, "  updated %d: %s\n", n, strings.Join(result.Replaced, ", "))
	}
	if n := len(result.Skipped); n > 0 {
		fmt.Fprintf(stdout, "  already installed, left alone (use --force to overwrite): %s\n", strings.Join(result.Skipped, ", "))
	}
	if len(result.Rejected) > 0 {
		fmt.Fprintf(stdout, "  refused %d:\n", len(result.Rejected))
		for _, r := range result.Rejected {
			fmt.Fprintf(stdout, "    %s\n", r)
		}
	}
	if len(result.Installed) == 0 && len(result.Replaced) == 0 {
		// Nothing changed. Exiting 0 here would tell a script the install
		// succeeded when the user's skills directory is exactly as it was.
		return 1
	}
	fmt.Fprintln(stdout, "\nRestart packetcode to pick them up.")
	return 0
}

func runSkillsRemove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("skills remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.Bool("project", false, "remove from ./.packetcode/skills")
	positional, err := parseCommandPositionals(fs, args, stdout, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(positional) != 1 {
		fmt.Fprint(stderr, skillsUsage)
		return 2
	}
	scope, workdir := skills.ScopeUser, ""
	if *project {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "packetcode skills: %s\n", err)
			return 1
		}
		scope, workdir = skills.ScopeProject, cwd
	}
	removed, err := skills.Remove(positional[0], scope, workdir)
	if err != nil {
		fmt.Fprintf(stderr, "packetcode skills remove: %s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed %s\n", removed)
	return 0
}

func runSkillsPath(stdout, stderr io.Writer) int {
	userDir, err := skills.UserSkillsDir()
	if err != nil {
		fmt.Fprintf(stderr, "packetcode skills: %s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "user     %s\n", userDir)
	if cwd, err := os.Getwd(); err == nil {
		fmt.Fprintf(stdout, "project  %s\n", skills.ProjectSkillsDir(cwd))
	}
	fmt.Fprintln(stdout, "builtin  embedded in the packetcode binary")
	return 0
}
