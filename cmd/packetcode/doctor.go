package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/permissions"
)

type doctorReport struct {
	SchemaVersion int           `json:"schema_version"`
	Status        string        `json:"status"`
	Version       string        `json:"version"`
	Commit        string        `json:"commit"`
	Platform      string        `json:"platform"`
	CWD           string        `json:"cwd"`
	Checks        []doctorCheck `json:"checks"`
}

type doctorCheck struct {
	ID      string `json:"id"`
	Section string `json:"section"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	Fix     string `json:"fix,omitempty"`
	Docs    string `json:"docs,omitempty"`
}

const (
	doctorOK   = "ok"
	doctorWarn = "warn"
	doctorFail = "fail"
	doctorSkip = "skip"
)

type doctorCheckFilter []string

func (f *doctorCheckFilter) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *doctorCheckFilter) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*f = append(*f, strings.ToLower(part))
		}
	}
	return nil
}

func runDoctorCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	var filter doctorCheckFilter
	fs.Var(&filter, "check", "limit diagnostics to a section or check id; repeat or comma-separate")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "packetcode doctor: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	report := buildDoctorReport()
	if filtered, err := filterDoctorReport(report, filter); err != nil {
		fmt.Fprintf(stderr, "packetcode doctor: %v\n", err)
		return 2
	} else {
		report = filtered
	}
	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(stderr, "packetcode doctor: write json: %v\n", err)
			return 1
		}
	} else {
		renderDoctorReport(stdout, report)
	}
	if report.Status == doctorFail {
		return 1
	}
	return 0
}

func filterDoctorReport(report doctorReport, filters []string) (doctorReport, error) {
	if len(filters) == 0 {
		return report, nil
	}
	seenFilters := map[string]bool{}
	for _, filter := range filters {
		seenFilters[filter] = false
	}
	out := report
	out.Checks = nil
	seenChecks := map[string]bool{}
	for _, check := range report.Checks {
		for filter := range seenFilters {
			if checkMatchesDoctorFilter(check, filter) {
				seenFilters[filter] = true
				if !seenChecks[check.ID] {
					out.Checks = append(out.Checks, check)
					seenChecks[check.ID] = true
				}
				break
			}
		}
	}
	for filter, ok := range seenFilters {
		if !ok {
			return doctorReport{}, fmt.Errorf("unknown doctor check %q", filter)
		}
	}
	out.Status = doctorOverallStatus(out.Checks)
	return out, nil
}

func checkMatchesDoctorFilter(check doctorCheck, filter string) bool {
	return check.Section == filter || check.ID == filter || strings.HasPrefix(check.ID, filter+".")
}

func buildDoctorReport() doctorReport {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	r := doctorReport{
		SchemaVersion: 1,
		Status:        doctorOK,
		Version:       version,
		Commit:        commit,
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		CWD:           cwd,
	}

	r.add("version.binary", "version", doctorOK, "packetcode "+version+" ("+commit+")", "go "+runtime.Version(), "", "")

	home, homeErr := config.HomeDir()
	if homeErr != nil {
		r.add("config.home", "config", doctorFail, "cannot resolve packetcode home", homeErr.Error(), "", "docs/configuration.md")
	} else {
		status, detail := writableDirStatus(home)
		r.add("config.home", "config", status, "packetcode home is writable", detail, "Fix filesystem permissions for "+home, "docs/configuration.md")
	}

	cfgPath, cfgPathErr := config.ConfigPath()
	var cfg *config.Config
	if cfgPathErr != nil {
		r.add("config.file", "config", doctorFail, "cannot resolve config path", cfgPathErr.Error(), "", "docs/configuration.md")
		cfg = config.Default()
	} else {
		loaded, loadErr := config.LoadFrom(cfgPath)
		switch {
		case loadErr != nil:
			r.add("config.file", "config", doctorFail, "config file is not readable TOML", cfgPath+": "+loadErr.Error(), "Fix or move "+cfgPath, "docs/configuration.md")
			cfg = config.Default()
		case fileExists(cfgPath):
			r.add("config.file", "config", doctorOK, "config file loaded", cfgPath, "", "docs/configuration.md")
			cfg = loaded
		default:
			r.add("config.file", "config", doctorWarn, "config file not found", cfgPath+" will be created by first-run setup", "Run packetcode to start setup", "docs/getting-started.md")
			cfg = loaded
		}
	}

	addProviderChecks(&r, cfg)
	addStateDirCheck(&r, "state.sessions", "sessions dir", config.SessionsDir)
	addStateDirCheck(&r, "state.backups", "backups dir", config.BackupsDir)
	addStateDirCheck(&r, "state.jobs", "jobs dir", config.JobsDir)
	addStateDirCheck(&r, "state.worktrees", "worktrees dir", config.WorktreesDir)
	addStateDirCheck(&r, "state.commands", "commands dir", config.UserCommandsDir)
	addPathCheck(&r, "state.cost_tally", "cost tally path", config.CostTallyPath)
	addPathCheck(&r, "state.theme", "theme path", config.ThemePath)
	addGitChecks(&r, cwd)
	addExecutableChecks(&r, cfg)
	addMCPChecks(&r, cfg, cwd)
	addPermissionChecks(&r, cfg)
	addAutomationChecks(&r, cfg)
	r.Status = doctorOverallStatus(r.Checks)
	return r
}

func (r *doctorReport) add(id, section, status, message, detail, fix, docs string) {
	if status == doctorOK {
		fix = ""
	}
	r.Checks = append(r.Checks, doctorCheck{
		ID:      id,
		Section: section,
		Status:  status,
		Message: message,
		Detail:  detail,
		Fix:     fix,
		Docs:    docs,
	})
}

func renderDoctorReport(w io.Writer, r doctorReport) {
	fmt.Fprintln(w, "Packetcode Doctor")
	fmt.Fprintf(w, "version: %s (%s)\n", r.Version, r.Commit)
	fmt.Fprintf(w, "platform: %s\n", r.Platform)
	fmt.Fprintf(w, "cwd: %s\n", r.CWD)
	fmt.Fprintf(w, "status: %s\n", r.Status)

	current := ""
	for _, c := range r.Checks {
		if c.Section != current {
			current = c.Section
			fmt.Fprintf(w, "\n%s\n", current)
		}
		fmt.Fprintf(w, "  %-4s %s", strings.ToUpper(c.Status), c.Message)
		if c.Detail != "" {
			fmt.Fprintf(w, " - %s", c.Detail)
		}
		fmt.Fprintln(w)
		if c.Fix != "" {
			fmt.Fprintf(w, "       Fix: %s\n", c.Fix)
		}
	}
}

func doctorOverallStatus(checks []doctorCheck) string {
	status := doctorOK
	for _, c := range checks {
		switch c.Status {
		case doctorFail:
			return doctorFail
		case doctorWarn:
			if status == doctorOK {
				status = doctorWarn
			}
		}
	}
	return status
}

func addStateDirCheck(r *doctorReport, id, message string, fn func() (string, error)) {
	dir, err := fn()
	if err != nil {
		r.add(id, "state", doctorFail, message+" unavailable", err.Error(), "", "docs/configuration.md")
		return
	}
	status, detail := writableDirStatus(dir)
	r.add(id, "state", status, message+" writable", detail, "Fix filesystem permissions for "+dir, "docs/configuration.md")
}

func addPathCheck(r *doctorReport, id, message string, fn func() (string, error)) {
	path, err := fn()
	if err != nil {
		r.add(id, "state", doctorFail, message+" unavailable", err.Error(), "", "docs/configuration.md")
		return
	}
	dir := filepath.Dir(path)
	status, detail := writableDirStatus(dir)
	r.add(id, "state", status, message+" parent writable", detail, "Fix filesystem permissions for "+dir, "docs/configuration.md")
}

func writableDirStatus(dir string) (string, string) {
	f, err := os.CreateTemp(dir, ".packetcode-doctor-*")
	if err != nil {
		return doctorFail, dir + ": " + err.Error()
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return doctorFail, dir + ": " + err.Error()
	}
	if err := os.Remove(name); err != nil {
		return doctorFail, dir + ": " + err.Error()
	}
	return doctorOK, dir
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func addProviderChecks(r *doctorReport, cfg *config.Config) {
	if cfg == nil {
		r.add("providers.config", "providers", doctorFail, "provider config unavailable", "", "", "docs/providers.md")
		return
	}
	addDefaultProviderChecks(r, cfg)

	reported := map[string]bool{}
	for _, slug := range builtInProviderSlugs() {
		source, ok := providerCredentialSource(cfg, slug)
		if !ok && slug != cfg.Default.Provider {
			continue
		}
		reported[slug] = true
		status := doctorOK
		message := slug + " credential configured"
		fix := ""
		if source == "missing" {
			status = doctorWarn
			message = slug + " credential missing"
			fix = "Open /provider add " + slug + " or set PACKETCODE_" + strings.ToUpper(slug) + "_API_KEY"
		}
		r.add("providers."+slug, "providers", status, message, source, fix, "docs/providers.md")
	}
	for slug, pc := range cfg.Providers {
		if isBuiltInProvider(slug) {
			if invalid := builtInProviderCustomFields(pc); len(invalid) > 0 {
				r.add("providers."+slug+".custom_fields", "providers", doctorFail, "built-in provider has custom-provider fields", strings.Join(invalid, ", "), "Remove custom-provider fields from [providers."+slug+"] or choose a new custom slug", "docs/providers.md")
			}
			continue
		}
		if reported[slug] {
			continue
		}
		if !pc.IsOpenAICompatible() {
			r.add("providers."+slug, "providers", doctorWarn, "unknown provider in config", slug, "Set type = \"openai_compatible\" or remove [providers."+slug+"]", "docs/providers.md")
			continue
		}
		reported[slug] = true
		if err := validateProviderBaseURL(pc.BaseURL); err != nil {
			r.add("providers."+slug, "providers", doctorFail, "custom provider base_url invalid", err.Error(), "Set [providers."+slug+"].base_url to an http(s) OpenAI-compatible /v1 endpoint", "docs/providers.md")
			continue
		}
		if warning := customProviderTransportWarning(pc.BaseURL); warning != "" {
			r.add("providers."+slug+".transport", "providers", doctorWarn, "custom provider uses insecure transport", warning, "Use https for hosted gateways; keep http endpoints local/private", "docs/security.md")
		}
		source, _ := providerCredentialSource(cfg, slug)
		status := doctorOK
		message := slug + " custom provider configured"
		fix := ""
		if source == "missing" {
			status = doctorWarn
			message = slug + " credential missing"
			fix = "Open /provider add " + slug + " or set " + cfg.ProviderAPIKeyEnvName(slug)
		}
		r.add("providers."+slug, "providers", status, message, source+"; base "+redactURLUserInfo(pc.BaseURL), fix, "docs/providers.md")
	}
	if len(reported) == 0 && strings.TrimSpace(cfg.Default.Provider) == "" {
		r.add("providers.none", "providers", doctorWarn, "no providers configured", "first-run setup will prompt for one", "Run packetcode to start setup", "docs/getting-started.md")
	}
}

func customProviderTransportWarning(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return ""
	}
	return redactURLUserInfo(raw)
}

func builtInProviderCustomFields(pc config.ProviderConfig) []string {
	var fields []string
	if strings.TrimSpace(pc.Type) != "" {
		fields = append(fields, "type")
	}
	if strings.TrimSpace(pc.BaseURL) != "" {
		fields = append(fields, "base_url")
	}
	if strings.TrimSpace(pc.DisplayName) != "" {
		fields = append(fields, "display_name")
	}
	if strings.TrimSpace(pc.BrandColor) != "" {
		fields = append(fields, "brand_color")
	}
	if len(pc.Headers) > 0 {
		fields = append(fields, "headers")
	}
	if len(pc.Models) > 0 {
		fields = append(fields, "models")
	}
	if pc.APIKeyRequired != nil {
		fields = append(fields, "api_key_required")
	}
	sort.Strings(fields)
	return fields
}

func addDefaultProviderChecks(r *doctorReport, cfg *config.Config) {
	active := strings.TrimSpace(cfg.Default.Provider)
	if active == "" {
		r.add("config.default_provider", "config", doctorWarn, "default provider missing", "first-run setup will prompt for one", "Run packetcode to start setup", "docs/configuration.md")
		return
	}
	pc, configured := cfg.Providers[active]
	if !isBuiltInProvider(active) && (!configured || !pc.IsOpenAICompatible()) {
		r.add("config.default_provider", "config", doctorFail, "default provider is unknown", active, "Set [default].provider to a configured provider slug", "docs/configuration.md")
		return
	}
	if configured && pc.IsOpenAICompatible() {
		if err := validateProviderBaseURL(pc.BaseURL); err != nil {
			r.add("config.default_provider", "config", doctorFail, "default provider base_url invalid", err.Error(), "Set [providers."+active+"].base_url to an http(s) OpenAI-compatible /v1 endpoint", "docs/providers.md")
			return
		}
	}
	if providerRequiresAPIKey(cfg, active) && cfg.GetProviderKey(active) == "" {
		r.add("config.default_provider", "config", doctorFail, "default provider has no API key", active, "Open /provider add "+active+" or set "+cfg.ProviderAPIKeyEnvName(active), "docs/providers.md")
		return
	}
	model := strings.TrimSpace(cfg.Default.Model)
	if model == "" {
		if pc, ok := cfg.Providers[active]; ok {
			model = strings.TrimSpace(pc.DefaultModel)
		}
	}
	if model == "" {
		r.add("config.default_model", "config", doctorFail, "default model missing", active, "Run /model or set [default].model in ~/.packetcode/config.toml", "docs/configuration.md")
		return
	}
	r.add("config.default_provider", "config", doctorOK, "default provider/model configured", active+" / "+model, "", "docs/configuration.md")
}

func providerCredentialSource(cfg *config.Config, slug string) (string, bool) {
	if slug == "ollama" {
		host := ollamaHost(cfg)
		if host == "" {
			host = "http://localhost:11434"
		}
		if _, ok := cfg.Providers[slug]; ok || os.Getenv("PACKETCODE_OLLAMA_HOST") != "" || cfg.Default.Provider == slug {
			return "keyless, host " + redactURLUserInfo(host), true
		}
		return "", false
	}
	pc, configured := cfg.Providers[slug]
	if configured && !pc.RequiresAPIKey(slug) {
		return "keyless", true
	}
	envKey := cfg.ProviderAPIKeyEnvName(slug)
	if os.Getenv(envKey) != "" {
		return "env:" + envKey, true
	}
	if configured && pc.APIKey != "" {
		return "config:~/.packetcode/config.toml", true
	}
	if configured || cfg.Default.Provider == slug {
		return "missing", true
	}
	return "", false
}

func validateProviderBaseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("base_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid base_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base_url must use http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("base_url must include a host")
	}
	return nil
}

func addGitChecks(r *doctorReport, cwd string) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		r.add("project.git", "project", doctorWarn, "git executable not found", "repo metadata and branch display will be unavailable", "Install git and ensure it is on PATH", "")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rootOut, err := exec.CommandContext(ctx, gitPath, "-C", cwd, "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(rootOut))
		if detail == "" {
			detail = err.Error()
		}
		if strings.Contains(strings.ToLower(detail), "dubious ownership") {
			safeDir := gitDubiousOwnershipPath(detail)
			if safeDir == "" {
				safeDir = filepath.ToSlash(cwd)
			}
			r.add("project.git.safe_directory", "project", doctorFail, "git rejects this repository as dubious ownership", detail, "Run: git config --global --add safe.directory "+shellQuote(safeDir), "")
			return
		}
		r.add("project.git", "project", doctorWarn, "not inside a usable git repository", detail, "", "")
		return
	}
	root := strings.TrimSpace(string(rootOut))
	branch := gitOutput(gitPath, root, "branch", "--show-current")
	if branch == "" {
		branch = "detached"
	}
	dirty := gitOutput(gitPath, root, "status", "--short")
	if strings.HasPrefix(dirty, "error:") {
		r.add("project.git.status", "project", doctorWarn, "git status failed", dirty, "", "")
		return
	}
	detail := root + " (" + branch + ")"
	if dirty != "" {
		r.add("project.git.dirty", "project", doctorWarn, "git repository has uncommitted changes", detail, "Commit or stash unrelated work before large changes", "")
		return
	}
	r.add("project.git", "project", doctorOK, "git repository detected", detail, "", "")
}

func gitOutput(gitPath, root string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	full := append([]string{"-C", root}, args...)
	out, err := exec.CommandContext(ctx, gitPath, full...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "error: " + msg
	}
	return strings.TrimSpace(string(out))
}

func gitDubiousOwnershipPath(detail string) string {
	const marker = "detected dubious ownership in repository at '"
	idx := strings.Index(detail, marker)
	if idx < 0 {
		return ""
	}
	rest := detail[idx+len(marker):]
	end := strings.Index(rest, "'")
	if end < 0 {
		return ""
	}
	return filepath.ToSlash(rest[:end])
}

func shellQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\r\n'\"") {
		return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
	}
	return s
}

func addExecutableChecks(r *doctorReport, cfg *config.Config) {
	addExecutableCheck(r, "tools.shell", shellExecutableName(), doctorFail, "execute_command shell")
	addExecutableCheck(r, "tools.rg", "rg", doctorWarn, "ripgrep search accelerator")
	if runtime.GOOS == "windows" {
		addExecutableCheck(r, "tools.taskkill", "taskkill", doctorWarn, "Windows process-tree fallback")
		if cfg != nil && (cfg.StatusLine.IsEnabled() || hooksConfigured(cfg)) {
			addExecutableCheck(r, "tools.powershell", "powershell", doctorFail, "hooks/statusline shell")
		}
	}
}

func addExecutableCheck(r *doctorReport, id, name, missingStatus, label string) {
	path, err := exec.LookPath(name)
	if err != nil {
		r.add(id, "tools", missingStatus, label+" not found", name+" is not on PATH", "Install "+name+" or adjust PATH", "")
		return
	}
	r.add(id, "tools", doctorOK, label+" available", path, "", "")
}

func shellExecutableName() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "sh"
}

func addMCPChecks(r *doctorReport, cfg *config.Config, cwd string) {
	if cfg == nil || len(cfg.MCP) == 0 {
		r.add("mcp.none", "mcp", doctorOK, "no MCP servers configured", "", "", "docs/mcp.md")
		return
	}
	names := make([]string, 0, len(cfg.MCP))
	for name := range cfg.MCP {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := cfg.MCP[name]
		id := "mcp." + name
		if _, err := config.MCPLogFileName(name); err != nil {
			r.add(id+".name", "mcp", doctorFail, "MCP server name is invalid", err.Error(), "Use only letters, digits, '_' or '-'", "docs/mcp.md")
			continue
		}
		timeout := entry.TimeoutSec
		if timeout <= 0 {
			timeout = defaultMCPTimeoutSec
		}
		if !entry.IsEnabled() {
			r.add(id, "mcp", doctorSkip, name+" disabled", fmt.Sprintf("timeout %ds", timeout), "", "docs/mcp.md")
			continue
		}
		if missing := missingEnvFrom(entry.EnvFrom); len(missing) > 0 {
			r.add(id+".env_from", "mcp", doctorWarn, name+" env_from variable missing", strings.Join(missing, ", "), "Export the variable or remove it from [mcp."+name+"].env_from", "docs/mcp.md")
		}
		command := strings.TrimSpace(entry.Command)
		if command == "" {
			r.add(id+".command", "mcp", doctorFail, name+" command missing", "enabled server has no command", "Set command or enabled = false under [mcp."+name+"]", "docs/mcp.md")
			continue
		}
		auth := mcpAuthSummary(entry.Env, entry.EnvFrom)
		resolved, err := resolveCommand(command, cwd)
		if err != nil {
			r.add(id+".command", "mcp", doctorFail, name+" command not runnable", err.Error()+"; "+auth, "Install "+command+" or disable [mcp."+name+"]", "docs/mcp.md")
			continue
		}
		detail := fmt.Sprintf("%s %s; timeout %ds; %s", resolved, strings.Join(redactCommandArgs(entry.Args), " "), timeout, auth)
		r.add(id, "mcp", doctorOK, name+" static config valid", strings.TrimSpace(detail), "", "docs/mcp.md")
	}
}

func redactURLUserInfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		if err == nil {
			u.RawQuery = redactURLQuery(u.RawQuery)
			return u.String()
		}
		return raw
	}
	if username := u.User.Username(); username != "" {
		u.User = url.UserPassword(username, "[REDACTED]")
	} else {
		u.User = url.User("[REDACTED]")
	}
	u.RawQuery = redactURLQuery(u.RawQuery)
	return u.String()
}

func redactURLQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "[REDACTED]"
	}
	for key := range values {
		if secretArgName(key) {
			values[key] = []string{"[REDACTED]"}
		}
	}
	return values.Encode()
}

func redactCommandArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, len(args))
	redactNext := false
	for i, arg := range args {
		if redactNext {
			out[i] = "[REDACTED]"
			redactNext = false
			continue
		}
		lower := strings.ToLower(arg)
		if key, value, ok := strings.Cut(arg, "="); ok && secretArgName(key) {
			out[i] = key + "=[REDACTED]"
			if value == "" {
				out[i] = key + "="
			}
			continue
		}
		out[i] = arg
		if secretArgName(lower) {
			redactNext = true
		}
	}
	return out
}

func secretArgName(arg string) bool {
	arg = strings.TrimLeft(strings.ToLower(strings.TrimSpace(arg)), "-/")
	arg = strings.ReplaceAll(arg, "_", "-")
	for _, marker := range []string{"api-key", "apikey", "token", "secret", "password", "passwd", "pwd", "bearer"} {
		if arg == marker || strings.HasSuffix(arg, "-"+marker) || strings.Contains(arg, marker) {
			return true
		}
	}
	return false
}

func resolveCommand(command, cwd string) (string, error) {
	if strings.ContainsAny(command, `/\`) {
		path := command
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("command %q not found: %w", command, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("command %q is a directory", command)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			return "", fmt.Errorf("command %q is not executable", command)
		}
		return path, nil
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("command %q not found in PATH", command)
	}
	return path, nil
}

func mcpAuthSummary(env map[string]string, envFrom []string) string {
	if len(env) == 0 && len(envFrom) == 0 {
		return "auth:none"
	}
	keys := make([]string, 0, len(env)+len(envFrom))
	for k := range env {
		keys = append(keys, k)
	}
	for _, k := range envFrom {
		k = strings.TrimSpace(k)
		if k != "" {
			label := "from:" + k
			if _, ok := os.LookupEnv(k); !ok {
				label += ":missing"
			}
			keys = append(keys, label)
		}
	}
	sort.Strings(keys)
	return "auth:env:" + strings.Join(keys, ",")
}

func missingEnvFrom(envFrom []string) []string {
	var missing []string
	seen := map[string]bool{}
	for _, name := range envFrom {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if _, ok := os.LookupEnv(name); !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func addAutomationChecks(r *doctorReport, cfg *config.Config) {
	if cfg == nil {
		return
	}
	if cfg.Behavior.TrustMode {
		r.add("approvals.trust_mode", "approvals", doctorWarn, "trust mode enabled", "write tools and shell commands auto-approve", "Set [behavior].trust_mode = false unless intentional", "docs/getting-started.md")
	} else {
		r.add("approvals.trust_mode", "approvals", doctorOK, "trust mode disabled", "approval prompts remain enabled", "", "docs/getting-started.md")
	}
	if cfg.StatusLine.IsEnabled() {
		r.add("automation.statusline", "automation", doctorOK, "statusline configured", "command present, not executed by doctor", "", "docs/hooks-and-statusline.md")
	}
	if hooksConfigured(cfg) {
		r.add("automation.hooks", "automation", doctorOK, "hooks configured", "commands present, not executed by doctor", "", "docs/hooks-and-statusline.md")
	}
}

func addPermissionChecks(r *doctorReport, cfg *config.Config) {
	if cfg == nil {
		return
	}
	policy, err := permissions.FromConfig(cfg)
	if err != nil {
		r.add("permissions.config", "permissions", doctorFail, "permission policy config invalid", err.Error(), "Use profile balanced, accept_edits, read_only, or bypass and actions ask, allow, or deny", "docs/configuration.md")
		return
	}
	r.add("permissions.profile", "permissions", doctorOK, "permission policy loaded", strings.Join(policy.SummaryLines(), "; "), "", "docs/configuration.md")
}

func hooksConfigured(cfg *config.Config) bool {
	return len(cfg.Hooks.UserPromptSubmit)+len(cfg.Hooks.PreToolUse)+len(cfg.Hooks.PostToolUse) > 0
}
