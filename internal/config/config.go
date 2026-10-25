// Package config loads, persists, and exposes packetcode's user configuration.
//
// The on-disk format is TOML at ~/.packetcode/config.toml with 0600 perms.
// API keys may be overridden at runtime via env vars of the form
// PACKETCODE_<SLUG>_API_KEY (e.g. PACKETCODE_OPENAI_API_KEY).
package config

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/packetcode/packetcode/internal/atomicfile"
	"github.com/packetcode/packetcode/internal/compat"
)

type Config struct {
	// SchemaVersion is optional and normally absent. A config written before
	// versioning existed, or by hand, decodes as 0 and is treated as current
	// -- refusing those would be refusing nearly every config in existence to
	// guard against a case that has not happened yet.
	//
	// Its purpose is the other direction: a config touched by a newer build
	// tells this one so, and this one says which settings it ignored instead
	// of silently dropping them.
	SchemaVersion   int                        `toml:"schema_version,omitempty"`
	Default         DefaultConfig              `toml:"default"`
	Providers       map[string]ProviderConfig  `toml:"providers"`
	Behavior        BehaviorConfig             `toml:"behavior"`
	Permissions     PermissionConfig           `toml:"permissions"`
	MCP             map[string]MCPServerConfig `toml:"mcp"`
	StatusLine      StatusLineConfig           `toml:"statusline"`
	Hooks           HooksConfig                `toml:"hooks"`
	ACP             ACPConfig                  `toml:"acp"`
	PacketComputers PacketComputersConfig      `toml:"packet_computers"`
	Sugar           SugarConfig                `toml:"sugar"`
	Conduit         ConduitConfig              `toml:"conduit"`

	// dotEnv holds parsed .env values, attached by SetDotEnv. Unexported and
	// untagged: it is not configuration, it is one more place a key may come
	// from, and writing it back into config.toml would copy secrets out of the
	// file the user chose to keep them in.
	dotEnv         *DotEnv
	dotEnvProblems []string
	compatProblems []string
}

// ACPConfig controls the optional Agent Client Protocol stdio server. Enabled
// is a pointer so existing configurations remain enabled when absent.
type ACPConfig struct {
	Enabled         *bool `toml:"enabled,omitempty"`
	enabledOverride *bool
}

func (c ACPConfig) IsEnabled() bool {
	if c.enabledOverride != nil {
		return *c.enabledOverride
	}
	return c.Enabled == nil || *c.Enabled
}

// PacketComputersConfig controls PacketCode's optional local/SSH computer
// registry and remote execution surface. Enabled is a pointer so existing
// configurations remain enabled when the setting is absent.
type PacketComputersConfig struct {
	Enabled         *bool `toml:"enabled,omitempty"`
	enabledOverride *bool
}

func (c PacketComputersConfig) IsEnabled() bool {
	if c.enabledOverride != nil {
		return *c.enabledOverride
	}
	return c.Enabled == nil || *c.Enabled
}

// SugarConfig controls Packetcode's private cache/governor envelope. Sugar
// may enforce stricter workspace policy server-side; these values are requests,
// never a way for the client to weaken that policy.
type SugarConfig struct {
	Enabled         *bool  `toml:"enabled,omitempty"`
	CacheMode       string `toml:"cache_mode"`
	CacheRetention  string `toml:"cache_retention"`
	Privacy         string `toml:"privacy"`
	enabledOverride *bool
	// Environment overrides for the three envelope fields. Kept out of the
	// stored fields so a save cannot persist them; see ConduitConfig.
	cacheModeOverride      *string
	cacheRetentionOverride *string
	privacyOverride        *string
}

// EffectiveCacheMode is CacheMode with the environment override applied.
func (c SugarConfig) EffectiveCacheMode() string {
	if c.cacheModeOverride != nil {
		return *c.cacheModeOverride
	}
	return c.CacheMode
}

// EffectiveCacheRetention is CacheRetention with the environment override
// applied.
func (c SugarConfig) EffectiveCacheRetention() string {
	if c.cacheRetentionOverride != nil {
		return *c.cacheRetentionOverride
	}
	return c.CacheRetention
}

// EffectivePrivacy is Privacy with the environment override applied.
func (c SugarConfig) EffectivePrivacy() string {
	if c.privacyOverride != nil {
		return *c.privacyOverride
	}
	return c.Privacy
}

// SugarExplicitlyDisabled reports whether the user or environment selected a
// hard-off state. Explicit commands such as `sugar login` may activate the
// nil/automatic state, but must respect this state.
func (c *Config) SugarExplicitlyDisabled() bool {
	if c == nil {
		return false
	}
	if c.Sugar.enabledOverride != nil {
		return !*c.Sugar.enabledOverride
	}
	return c.Sugar.Enabled != nil && !*c.Sugar.Enabled
}

// SugarIsEnabled resolves Sugar's tri-state activation. Explicit true/false
// wins. When absent, existing Sugar users stay enabled, while a fresh install
// remains inert until `sugar login` deliberately activates it.
func (c *Config) SugarIsEnabled() bool {
	if c == nil {
		return false
	}
	if c.Sugar.enabledOverride != nil {
		return *c.Sugar.enabledOverride
	}
	if c.Sugar.Enabled != nil {
		return *c.Sugar.Enabled
	}
	if strings.EqualFold(strings.TrimSpace(c.Default.Provider), "sugar") {
		return true
	}
	provider, configured := c.Providers["sugar"]
	return configured && !provider.IsOpenAICompatible()
}

// SugarUsesCustomProvider reports the one supported built-in slug escape:
// explicit built-in disable plus an OpenAI-compatible [providers.sugar].
func (c *Config) SugarUsesCustomProvider() bool {
	if c == nil || !c.SugarExplicitlyDisabled() {
		return false
	}
	provider, configured := c.Providers["sugar"]
	return configured && provider.IsOpenAICompatible()
}

// ConduitConfig controls the optional, decision-only shadow runtime. It is
// disabled by default and never changes the live provider or model choice.
type ConduitConfig struct {
	ShadowEnabled   bool `toml:"shadow_enabled"`
	TimeoutMS       int  `toml:"timeout_ms"`
	CapsuleMaxBytes int  `toml:"capsule_max_bytes"`
	// shadowOverride carries PACKETCODE_CONDUIT_SHADOW. Unexported for the
	// same reason as enabledOverride: a save compares stored fields against
	// the file, and an environment variable exported for one experiment must
	// not become the stored setting the next time /provider or /effort runs.
	shadowOverride *bool
}

// ShadowIsEnabled resolves the Conduit shadow gate with the environment
// override applied.
func (c ConduitConfig) ShadowIsEnabled() bool {
	if c.shadowOverride != nil {
		return *c.shadowOverride
	}
	return c.ShadowEnabled
}

// ConduitIsEnabled resolves the child setting together with its Sugar parent
// without mutating either stored value.
// ConduitIsEnabled reports the Conduit shadow gate on its own terms.
//
// It deliberately does NOT also require Sugar. The config-level coupling it
// used to carry was redundant: conduitShadowState.start already refuses to do
// anything unless the live provider is the Sugar provider, so the protection
// is structural rather than a setting anyone can get wrong. Requiring both
// only meant a user who turned Sugar off silently lost a setting they had
// explicitly asked for, and got no way to describe "shadow on" independently.
func (c *Config) ConduitIsEnabled() bool {
	return c != nil && c.Conduit.ShadowIsEnabled()
}

// MCPServerConfig is the per-server entry for [mcp.<name>] in the user's
// config.toml. The map key is the server name; this struct holds the
// command, args, env, and lifecycle knobs.
//
// Enabled is a *bool so the absent / nil state means "default on" — set
// `enabled = false` explicitly to opt out.
type MCPServerConfig struct {
	Command    string            `toml:"command"`
	Args       []string          `toml:"args,omitempty"`
	Env        map[string]string `toml:"env,omitempty"`
	EnvFrom    []string          `toml:"env_from,omitempty"`
	Enabled    *bool             `toml:"enabled,omitempty"`
	TimeoutSec int               `toml:"timeout_sec,omitempty"`
}

// IsEnabled returns true when the user has not explicitly disabled the
// server. nil pointer → enabled.
func (c MCPServerConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

type DefaultConfig struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
}

type ProviderConfig struct {
	Type            string                `toml:"type,omitempty"`
	APIKey          string                `toml:"api_key"`
	APIKeyEnv       string                `toml:"api_key_env,omitempty"`
	APIKeyRequired  *bool                 `toml:"api_key_required,omitempty"`
	DefaultModel    string                `toml:"default_model"`
	ReasoningEffort string                `toml:"reasoning_effort,omitempty"`
	Host            string                `toml:"host,omitempty"` // Ollama only
	BaseURL         string                `toml:"base_url,omitempty"`
	DisplayName     string                `toml:"display_name,omitempty"`
	BrandColor      string                `toml:"brand_color,omitempty"`
	Headers         map[string]string     `toml:"headers,omitempty"`
	Models          []ProviderModelConfig `toml:"models,omitempty"`

	// Ollama-only tuning. All optional — omitted means packetcode's smart
	// defaults (auto-sized context, 30m keep-alive, the model's own default
	// temperature), so a stock local install needs none of these.
	NumCtx      int      `toml:"num_ctx,omitempty"`     // fixed context window; 0 = auto-size per request
	KeepAlive   string   `toml:"keep_alive,omitempty"`  // e.g. "30m", "-1" (pin), "0" (unload now)
	Temperature *float64 `toml:"temperature,omitempty"` // nil = leave to the model
}

// ProviderModelConfig is an optional static model entry for custom
// OpenAI-compatible providers whose /models endpoint is unavailable or
// incomplete.
type ProviderModelConfig struct {
	ID            string  `toml:"id"`
	DisplayName   string  `toml:"display_name,omitempty"`
	ContextWindow int     `toml:"context_window,omitempty"`
	SupportsTools *bool   `toml:"supports_tools,omitempty"`
	InputPer1M    float64 `toml:"input_per_1m,omitempty"`
	OutputPer1M   float64 `toml:"output_per_1m,omitempty"`
}

// IsOpenAICompatible reports whether this provider is a user-defined
// OpenAI-compatible endpoint.
func (c ProviderConfig) IsOpenAICompatible() bool {
	t := strings.ToLower(strings.TrimSpace(c.Type))
	return t == "openai_compatible" || t == "openai-compatible"
}

// IsKeylessProvider reports whether a built-in provider authenticates without
// an API key: ollama (a local server) and codex (a ChatGPT subscription whose
// OAuth tokens live in ~/.codex/auth.json). Central so the many key-prompt,
// setup, picker, and doctor call sites stay consistent.
func IsKeylessProvider(slug string) bool {
	return slug == "ollama" || slug == "codex"
}

// RequiresAPIKey reports whether packetcode should require a key before
// registering or validating this provider.
func (c ProviderConfig) RequiresAPIKey(slug string) bool {
	if IsKeylessProvider(slug) {
		return false
	}
	if isReservedHostedProvider(slug) {
		return true
	}
	if c.APIKeyRequired != nil {
		return *c.APIKeyRequired
	}
	return true
}

// ProviderRequiresAPIKey resolves key policy with feature-gate context. The
// built-in Sugar provider always requires its hosted credential, while an
// explicitly enabled custom provider reusing that slug honors
// api_key_required like every other OpenAI-compatible endpoint.
func (c *Config) ProviderRequiresAPIKey(slug string) bool {
	if c == nil {
		return !IsKeylessProvider(slug)
	}
	provider, configured := c.Providers[slug]
	if !configured {
		return !IsKeylessProvider(slug)
	}
	if slug == "sugar" && c.SugarUsesCustomProvider() {
		if provider.APIKeyRequired != nil {
			return *provider.APIKeyRequired
		}
		return true
	}
	return provider.RequiresAPIKey(slug)
}

func isReservedHostedProvider(slug string) bool {
	switch slug {
	case "sugar", "openai", "anthropic", "gemini", "minimax", "deepseek", "grok", "mistral", "openrouter":
		return true
	default:
		return false
	}
}

type BehaviorConfig struct {
	TrustMode            bool `toml:"trust_mode"`
	AutoCompactThreshold int  `toml:"auto_compact_threshold"`
	MaxInputRows         int  `toml:"max_input_rows"`

	// Provider request resilience. Total attempts (incl. the first) for a
	// streaming request on transient errors; 0 means use the default of 3.
	ProviderMaxRetries int `toml:"provider_max_retries"`

	// Abort a provider stream that goes silent for this many seconds; 0
	// means use the default of 60.
	ProviderStallTimeout int `toml:"provider_stall_timeout"`

	// Background agents (see docs/feature-background-agents.md).
	BackgroundMaxConcurrent   int    `toml:"background_max_concurrent"`
	BackgroundMaxDepth        int    `toml:"background_max_depth"`
	BackgroundMaxTotal        int    `toml:"background_max_total"`
	BackgroundDefaultProvider string `toml:"background_default_provider"`
	BackgroundDefaultModel    string `toml:"background_default_model"`
	BackgroundTokenBudget     int    `toml:"background_token_budget"`
	WorkflowTokenBudget       int    `toml:"workflow_token_budget"`

	// Loop detection aborts a run that keeps making the same tool call with
	// the same result. These exist because the detector infers "no progress"
	// from identical output, and a legitimate poll returning a constant value
	// looks identical -- so an operator must be able to loosen or disable it
	// without a rebuild. Zero means use the built-in defaults.
	LoopDetectionDisabled  bool `toml:"loop_detection_disabled"`
	LoopDetectionWindow    int  `toml:"loop_detection_window"`
	LoopDetectionThreshold int  `toml:"loop_detection_threshold"`

	// PostEditDiagnosticsDisabled suppresses the syntax diagnostics appended
	// to a successful write_file/patch_file result. On by default: an edit
	// that broke the file is something the model should learn from the tool
	// that made it, not two turns later. The switch exists because the
	// analysis is a heuristic appended to someone else's tool output, and an
	// operator who finds it noisy must be able to silence it without a
	// rebuild.
	PostEditDiagnosticsDisabled bool `toml:"post_edit_diagnostics_disabled"`

	// BackupRetentionDays bounds how long a previous session's undo backups
	// stay on disk. The undo stack is in-memory and reset per run, so a tree
	// from an exited session can never be undone again and is pure disk cost.
	// Zero uses the built-in default of fourteen days, like every other cap
	// here; BackupPruneDisabled is the switch for keeping everything.
	BackupRetentionDays int  `toml:"backup_retention_days"`
	BackupPruneDisabled bool `toml:"backup_prune_disabled"`
}

// PermissionConfig controls the approval policy applied to tool calls.
type PermissionConfig struct {
	// Profile names the active built-in or custom profile. Built-ins:
	// balanced/ask, safe/read_only, edit/accept_edits, full/trusted.
	Profile string `toml:"profile,omitempty"`
	// Profiles maps custom profile names to tool-action maps. Supported
	// actions are allow, ask, and deny. Keys are tool names, "default",
	// or "mcp" for all MCP tool aliases.
	Profiles map[string]PermissionProfile `toml:"profiles,omitempty"`
	// Rules are ordered explicit overrides. Later rules win when more
	// than one rule matches a tool call.
	Rules []PermissionRule `toml:"rules,omitempty"`

	// Legacy inline overrides from the early permissions draft. Keep
	// parsing them so existing local configs do not break.
	Default string            `toml:"default,omitempty"`
	Tools   map[string]string `toml:"tools,omitempty"`
}

type PermissionProfile map[string]string

type PermissionRule struct {
	Tool          string   `toml:"tool,omitempty"`
	Action        string   `toml:"action"`
	Command       string   `toml:"command,omitempty"`
	CommandPrefix []string `toml:"command_prefix,omitempty"`
	Reason        string   `toml:"reason,omitempty"`
}

// StatusLineConfig declares an optional command that renders the bottom
// status line. The command receives a JSON snapshot on stdin and packetcode
// renders its stdout when it exits successfully.
type StatusLineConfig struct {
	Command    string `toml:"command"`
	Enabled    *bool  `toml:"enabled,omitempty"`
	TimeoutSec int    `toml:"timeout_sec,omitempty"`
}

func (c StatusLineConfig) IsEnabled() bool {
	return c.Command != "" && (c.Enabled == nil || *c.Enabled)
}

// HooksConfig contains user-defined shell hooks. Use TOML arrays of tables:
// [[hooks.user_prompt_submit]], [[hooks.pre_tool_use]], [[hooks.post_tool_use]].
type HooksConfig struct {
	UserPromptSubmit []HookConfig `toml:"user_prompt_submit"`
	PreToolUse       []HookConfig `toml:"pre_tool_use"`
	PostToolUse      []HookConfig `toml:"post_tool_use"`
}

// HookConfig describes one shell command hook. Matcher applies only to
// tool hooks; empty or "*" matches every tool, otherwise it must equal the
// tool name.
type HookConfig struct {
	Command    string `toml:"command"`
	Matcher    string `toml:"matcher,omitempty"`
	Enabled    *bool  `toml:"enabled,omitempty"`
	TimeoutSec int    `toml:"timeout_sec,omitempty"`
}

func (c HookConfig) IsEnabled() bool {
	return c.Command != "" && (c.Enabled == nil || *c.Enabled)
}

// Load reads ~/.packetcode/config.toml and returns the parsed config.
// If the file does not exist, returns Default() — the caller can use
// IsFirstRun() to distinguish a fresh install from a returning user.
// Load reads config.toml from the conventional location.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	return LoadFrom(path)
}

// DotEnvProblems returns .env files that exist and could not be read.
func (c *Config) DotEnvProblems() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.dotEnvProblems...)
}

// CompatProblems returns what this build did not understand in the config
// file: settings from a newer schema, and keys it decoded nothing from.
//
// Reported rather than fatal, which is the one place the compatibility
// contract deliberately differs from the formats packetcode writes itself.
// config.toml is a file a person typed. Refusing to start because they also
// ran a newer build once, or because of a stray key, is a worse outcome than
// the misreading it would prevent -- and there is nothing here to corrupt,
// because saving edits this file in place (see save.go): a key no setting
// matched is left exactly where it was rather than re-encoded away. But
// ignoring a setting in silence is how someone spends an afternoon wondering
// why an option does nothing, so the ignoring is said out loud.
func (c *Config) CompatProblems() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.compatProblems...)
}

// LoadFrom reads config from an explicit path. Exposed for testing and
// for callers that want to point at a non-default file.
func LoadFrom(path string) (*Config, error) {
	data, err := atomicfile.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read config: %w", err)
		}
		data = nil
	}
	cfg := Default()
	if len(data) > 0 {
		// toml.Decode rather than toml.Unmarshal, for the MetaData: it is the
		// only way to learn which keys in the file this build decoded nothing
		// from. BurntSushi ignores unknown keys silently, which is the right
		// behaviour -- an older build must not fail on a setting it has never
		// heard of -- but silent is not the same as unreported.
		md, err := toml.Decode(string(data), cfg)
		if err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
		cfg.compatProblems = configCompatProblems(path, cfg.SchemaVersion, md.Undecoded())
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderConfig{}
	}
	if cfg.MCP == nil {
		cfg.MCP = map[string]MCPServerConfig{}
	}
	if cfg.Permissions.Tools == nil {
		cfg.Permissions.Tools = map[string]string{}
	}
	if cfg.Permissions.Profiles == nil {
		cfg.Permissions.Profiles = map[string]PermissionProfile{}
	}
	// .env is attached here, not in Load, because Load is not the only door:
	// `doctor` calls LoadFrom directly, and wiring this one level up left it
	// unable to see a key the TUI used happily. Every config that exists has
	// been through this function.
	cwd, _ := os.Getwd()
	dotEnv, dotEnvProblems := LoadDotEnv(cwd)
	cfg.SetDotEnv(dotEnv)
	cfg.dotEnvProblems = dotEnvProblems
	if err := applyIntegrationEnvironment(cfg); err != nil {
		return nil, err
	}
	if err := validateSugarAndConduit(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// configCompatProblems describes what this build did not understand.
//
// The two cases are reported together because they are one question from the
// user's chair -- "why did my setting do nothing?" -- and separately because
// the answers differ: a newer schema means upgrade, an unknown key usually
// means a typo.
func configCompatProblems(path string, schemaVersion int, undecoded []toml.Key) []string {
	var problems []string
	if schemaVersion > compat.ConfigVersion {
		problems = append(problems, fmt.Sprintf(
			"%s declares schema_version %d but this build understands %d; "+
				"settings added after %d are ignored",
			path, schemaVersion, compat.ConfigVersion, compat.ConfigVersion))
	}
	if len(undecoded) == 0 {
		return problems
	}
	keys := make([]string, 0, len(undecoded))
	for _, k := range undecoded {
		keys = append(keys, k.String())
	}
	sort.Strings(keys)
	// Bounded. A config written against a much newer build could name dozens,
	// and a startup warning that scrolls the terminal is one nobody reads.
	const maxNamed = 5
	shown, suffix := keys, ""
	if len(shown) > maxNamed {
		shown = shown[:maxNamed]
		suffix = fmt.Sprintf(" and %d more", len(keys)-maxNamed)
	}
	problems = append(problems, fmt.Sprintf(
		"%s: no setting matches %s%s; %s ignored (a newer build's option, or a typo)",
		path, strings.Join(shown, ", "), suffix,
		map[bool]string{true: "it was", false: "they were"}[len(keys) == 1]))
	return problems
}

// lookupBoolEnv reads a boolean feature-gate variable.
//
// Three outcomes, and the distinction between the first two is the point:
//   - unset, or set to only whitespace -> (nil, nil), meaning "no opinion",
//     so the configured value stands. An exported-but-empty variable is how
//     shells and .env files spell "not set", and treating it as an error
//     would fail startup over a blank line in someone's profile.
//   - a value Go can parse -> (&parsed, nil).
//   - anything else -> an error naming the variable. A misspelled gate must
//     never be silently read as "off"; that is the direction which quietly
//     disables a feature the user asked for.
func lookupBoolEnv(name string) (*bool, error) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return nil, nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseBool(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%s must be true or false", name)
	}
	return &parsed, nil
}

func applyIntegrationEnvironment(cfg *Config) error {
	for _, gate := range []struct {
		env    string
		target **bool
	}{
		{"PACKETCODE_ACP_ENABLED", &cfg.ACP.enabledOverride},
		{"PACKETCODE_PACKET_COMPUTERS_ENABLED", &cfg.PacketComputers.enabledOverride},
		{"PACKETCODE_SUGAR_ENABLED", &cfg.Sugar.enabledOverride},
	} {
		override, err := lookupBoolEnv(gate.env)
		if err != nil {
			return err
		}
		if override != nil {
			*gate.target = override
		}
	}
	// Read before the Sugar gate below: Conduit is no longer subordinate to
	// Sugar, so its variable must be honoured whether or not Sugar is on.
	shadow, err := lookupBoolEnv("PACKETCODE_CONDUIT_SHADOW")
	if err != nil {
		return err
	}
	if shadow != nil {
		cfg.Conduit.shadowOverride = shadow
	}
	if !cfg.SugarIsEnabled() {
		// Sugar owns the cache envelope and the privacy setting, so those stay
		// inert while it is off. The stored values are preserved rather than
		// cleared, so an unrelated config save cannot erase a preference the
		// user set while Sugar was enabled.
		return nil
	}
	if value, ok := os.LookupEnv("PACKETCODE_SUGAR_CACHE_MODE"); ok {
		trimmed := strings.TrimSpace(value)
		cfg.Sugar.cacheModeOverride = &trimmed
	}
	if value, ok := os.LookupEnv("PACKETCODE_SUGAR_CACHE_RETENTION"); ok {
		trimmed := strings.TrimSpace(value)
		cfg.Sugar.cacheRetentionOverride = &trimmed
	}
	if value, ok := os.LookupEnv("PACKETCODE_SUGAR_PRIVACY"); ok {
		trimmed := strings.TrimSpace(value)
		cfg.Sugar.privacyOverride = &trimmed
	}
	return nil
}

func validateSugarAndConduit(cfg *Config) error {
	// Conduit bounds are checked unconditionally: the setting is independent
	// of Sugar, so an out-of-range value must be reported even while Sugar is
	// off rather than lying dormant until someone enables it.
	if cfg.Conduit.TimeoutMS < 100 || cfg.Conduit.TimeoutMS > 30_000 {
		return fmt.Errorf("conduit.timeout_ms must be between 100 and 30000")
	}
	if cfg.Conduit.CapsuleMaxBytes < 2_048 || cfg.Conduit.CapsuleMaxBytes > 64*1024 {
		return fmt.Errorf("conduit.capsule_max_bytes must be between 2048 and 65536")
	}
	if !cfg.SugarIsEnabled() {
		return nil
	}
	switch cfg.Sugar.EffectiveCacheMode() {
	case "auto", "off":
	default:
		return fmt.Errorf("sugar.cache_mode must be auto or off")
	}
	switch cfg.Sugar.EffectiveCacheRetention() {
	case "provider_default", "5m", "30m", "1h":
	default:
		return fmt.Errorf("sugar.cache_retention must be provider_default, 5m, 30m, or 1h")
	}
	switch cfg.Sugar.EffectivePrivacy() {
	case "standard", "zdr_required":
	default:
		return fmt.Errorf("sugar.privacy must be standard or zdr_required")
	}
	return nil
}

// KeySource says where a provider key came from.
type KeySource string

const (
	// KeySourceNone means no key is set anywhere.
	KeySourceNone KeySource = "none"
	// KeySourceEnv is a real environment variable in this process.
	KeySourceEnv KeySource = "environment"
	// KeySourceDotEnv is a .env file. Which one is in KeyOrigin.Path.
	KeySourceDotEnv KeySource = "dotenv"
	// KeySourceConfig is config.toml.
	KeySourceConfig KeySource = "config"
)

// KeyOrigin describes where a provider key was resolved from, so `doctor` and
// the TUI can answer "which key am I actually using" without printing it.
type KeyOrigin struct {
	Source KeySource
	// Name is the environment variable name for env and dotenv sources.
	Name string
	// Path is the file for dotenv and config sources.
	Path string
}

// Describe renders an origin for a human, naming no secret.
func (o KeyOrigin) Describe() string {
	switch o.Source {
	case KeySourceEnv:
		return "environment variable " + o.Name
	case KeySourceDotEnv:
		return o.Name + " in " + o.Path
	case KeySourceConfig:
		return "config.toml"
	default:
		return "not set"
	}
}

// SetDotEnv attaches parsed .env values for key resolution.
//
// Held on the Config rather than read on demand so one process resolves keys
// from one snapshot: a .env edited mid-session must not make two lookups in
// the same turn disagree about which key is in use.
func (c *Config) SetDotEnv(d *DotEnv) {
	if c == nil {
		return
	}
	c.dotEnv = d
}

// GetProviderKey returns the API key for a provider.
//
// Precedence, strongest first: a real environment variable, then a .env file,
// then config.toml. A real environment variable wins because it is the one a
// person set deliberately for this run -- a stale .env silently overriding
// what someone just exported is the failure that makes dotenv loaders
// infuriating. Returns empty when nothing is set.
func (c *Config) GetProviderKey(slug string) string {
	key, _ := c.ProviderKeyWithOrigin(slug)
	return key
}

// ProviderKeyWithOrigin is GetProviderKey plus where the value came from.
func (c *Config) ProviderKeyWithOrigin(slug string) (string, KeyOrigin) {
	envKey := c.ProviderAPIKeyEnvName(slug)
	if v := os.Getenv(envKey); v != "" {
		return v, KeyOrigin{Source: KeySourceEnv, Name: envKey}
	}
	if c != nil {
		if v, from, ok := c.dotEnv.Lookup(envKey); ok && strings.TrimSpace(v) != "" {
			return v, KeyOrigin{Source: KeySourceDotEnv, Name: envKey, Path: from}
		}
	}
	if c != nil {
		if p, ok := c.Providers[slug]; ok && p.APIKey != "" {
			return p.APIKey, KeyOrigin{Source: KeySourceConfig}
		}
	}
	return "", KeyOrigin{Source: KeySourceNone, Name: envKey}
}

// ProviderAPIKeyEnvName returns the environment variable that overrides
// the configured API key for slug. Custom providers can set api_key_env;
// otherwise slugs are normalized to PACKETCODE_<SLUG>_API_KEY.
func (c *Config) ProviderAPIKeyEnvName(slug string) string {
	if c != nil {
		if p, ok := c.Providers[slug]; ok && strings.TrimSpace(p.APIKeyEnv) != "" {
			return strings.TrimSpace(p.APIKeyEnv)
		}
	}
	return DefaultProviderAPIKeyEnvName(slug)
}

// DefaultProviderAPIKeyEnvName normalizes provider slugs into shell-safe
// PACKETCODE_*_API_KEY variable names.
func DefaultProviderAPIKeyEnvName(slug string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(slug) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		name = "CUSTOM"
	}
	return fmt.Sprintf("PACKETCODE_%s_API_KEY", name)
}

// IsFirstRun reports whether the config file is missing on disk.
func IsFirstRun() bool {
	path, err := ConfigPath()
	if err != nil {
		return true
	}
	_, err = os.Stat(path)
	return os.IsNotExist(err)
}
