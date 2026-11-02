package app

import (
	"fmt"
	"strings"

	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/ui/components/picker"
)

// providerKeyStatus says whether a key is set and where it came from.
//
// "key present" alone was fine when config.toml and an environment variable
// were the only two places a key could live. With .env there are three, and
// the question a person has when the wrong key is in use is not whether one
// exists but which one won -- so the row answers that. Never the key itself:
// this is a list rendered on screen.
func providerKeyStatus(cfg *config.Config, slug, fallback string) string {
	key, origin := cfg.ProviderKeyWithOrigin(slug)
	if key == "" {
		return fallback
	}
	switch origin.Source {
	case config.KeySourceEnv:
		return "key from " + origin.Name
	case config.KeySourceDotEnv:
		return "key from .env"
	default:
		return "key present"
	}
}

// providerItems builds the rows displayed by the provider picker. Each
// row's ID is the provider slug so SelectMsg.Item.ID plugs straight
// into applyProviderSwitch. The active provider's marker is a filled
// bullet tinted in its brand color. Names, connection details, and setup
// status occupy separate parts of the row so long names stay readable.
func providerItems(regs []provider.Provider, cfg *config.Config, activeSlug string) []picker.Item {
	out := make([]picker.Item, 0, len(regs))
	for _, p := range regs {
		slug := p.Slug()
		defModel := ""
		keyStatus := "(no key)"
		switch slug {
		case "ollama":
			keyStatus = "local"
		case "codex":
			keyStatus = "ChatGPT login"
		}
		if cfg != nil {
			if pc, ok := cfg.Providers[slug]; ok {
				if !cfg.ProviderRequiresAPIKey(slug) && !config.IsKeylessProvider(slug) {
					keyStatus = "keyless"
				}
				if pc.DefaultModel != "" {
					defModel = pc.DefaultModel
				}
				if cfg.ProviderRequiresAPIKey(slug) {
					keyStatus = providerKeyStatus(cfg, slug, keyStatus)
				}
			}
			// Env-var-only key case when the provider has no config entry.
			if keyStatus == "(no key)" && !config.IsKeylessProvider(slug) {
				keyStatus = providerKeyStatus(cfg, slug, keyStatus)
			}
		}
		details := []string{slug}
		status := ""
		if keyStatus == "(no key)" {
			status = "key needed"
			keyStatus = "Ctrl+A to set key"
		}
		details = append(details, keyStatus)
		if defModel != "" {
			details = append(details, defModel)
		}
		marker := ""
		if slug == activeSlug {
			marker = "●"
		}
		out = append(out, picker.Item{
			ID:     slug,
			Label:  p.Name(),
			Detail: strings.Join(details, " · "),
			Marker: marker,
			Color:  p.BrandColor(),
			Status: status,
			Extra:  p,
		})
	}
	return out
}

// modelItems builds the rows displayed by the model picker. The active
// model gets a bullet marker; everyone else gets empty. The ID gets its own
// line, with context, tool support, and pricing shown underneath.
func modelItems(ms []provider.Model, activeID string, prov provider.Provider) []picker.Item {
	out := make([]picker.Item, 0, len(ms))
	for _, m := range ms {
		tools := "no tools"
		if m.SupportsTools {
			tools = "tools"
		}
		pricing := "free"
		if m.InputPer1M > 0 || m.OutputPer1M > 0 {
			pricing = fmt.Sprintf("$%g/$%g per 1M in/out", m.InputPer1M, m.OutputPer1M)
		}
		detail := fmt.Sprintf("%s context · %s · %s", fmtContext(m.ContextWindow), tools, pricing)
		if m.DisplayName != "" && m.DisplayName != m.ID {
			detail += " · " + m.DisplayName
		}

		marker := ""
		if m.ID == activeID {
			marker = "●"
		}
		color := prov.BrandColor()
		out = append(out, picker.Item{
			ID:     m.ID,
			Label:  m.ID,
			Detail: detail,
			Marker: marker,
			Color:  color,
			Extra:  m,
		})
	}
	return out
}

// fmtContext renders a context-window size compactly: "128k", "1M", or
// the raw integer for small/unusual values. Zero means unknown — we
// emit "?" so the column stays occupied.
func fmtContext(n int) string {
	switch {
	case n <= 0:
		return "?"
	case n >= 1_000_000:
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%dM", n/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		if n%1_000 == 0 {
			return fmt.Sprintf("%dk", n/1_000)
		}
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
