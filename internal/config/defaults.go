package config

// Default returns a fresh Config populated with safe defaults.
// First-run setup mutates this and saves it; until a provider is configured,
// Default.Provider and Default.Model are empty strings.
func Default() *Config {
	return &Config{
		Default: DefaultConfig{
			Provider: "",
			Model:    "",
		},
		Providers: map[string]ProviderConfig{},
		MCP:       map[string]MCPServerConfig{},
		StatusLine: StatusLineConfig{
			Command:    "",
			TimeoutSec: 2,
		},
		Behavior: BehaviorConfig{
			TrustMode:            false,
			AutoCompactThreshold: 80,
			MaxInputRows:         10,

			BackgroundMaxConcurrent:   4,
			BackgroundMaxDepth:        2,
			BackgroundMaxTotal:        32,
			BackgroundDefaultProvider: "",
			BackgroundDefaultModel:    "",
		},
		Permissions: PermissionConfig{
			Profile:  "balanced",
			Profiles: map[string]PermissionProfile{},
			Rules:    nil,
			Tools:    map[string]string{},
		},
	}
}
