// Package theme exposes the Terminal Noir design tokens from the design
// system as Lip Gloss colors and pre-built styles.
//
// Every UI component should reference tokens from this package rather
// than hardcoding hex values, so that a future ~/.packetcode/theme.toml
// override can swap in user themes without touching component code.
package theme

import "github.com/charmbracelet/lipgloss"

// ────────────────────────────────────────────────────────────────────────────
// Color tokens — Terminal Noir
// ────────────────────────────────────────────────────────────────────────────

var (
	// Base.
	BaseBg            = lipgloss.Color("#0F0F0F")
	BaseSurface       = lipgloss.Color("#1A1A2E")
	BaseSurfaceBright = lipgloss.Color("#232340")
	BaseBorder        = lipgloss.Color("#2A2A3D")
	BaseBorderBright  = lipgloss.Color("#3D3D5C")

	// Text.
	TextPrimary   = lipgloss.Color("#E1E1E8")
	TextSecondary = lipgloss.Color("#8888A0")
	TextDim       = lipgloss.Color("#4A4A60")
	TextInverse   = lipgloss.Color("#0F0F0F")

	// Accent. Electric cyan — distinctive, terminal-native, NOT purple.
	AccentPrimary    = lipgloss.Color("#00D9FF")
	AccentPrimaryDim = lipgloss.Color("#0891B2")
	AccentSecondary  = lipgloss.Color("#FF6B6B")

	// Semantic.
	Success = lipgloss.Color("#4ADE80")
	Warning = lipgloss.Color("#FBBF24")
	Error   = lipgloss.Color("#F87171")
	Info    = lipgloss.Color("#60A5FA")
)

// providerColors maps a provider slug to its brand colour. Populated
// with the built-in providers; the theme loader merges user
// overrides (and additional slugs) into this map at startup.
//
// OpenRouter was originally #9B59B6 (purple); switched to rose (#EC4899)
// to keep the palette purple-free.
var providerColors = map[string]lipgloss.Color{
	"openai":     lipgloss.Color("#10A37F"),
	"anthropic":  lipgloss.Color("#D97757"),
	"gemini":     lipgloss.Color("#4285F4"),
	"minimax":    lipgloss.Color("#FF8C00"),
	"openrouter": lipgloss.Color("#EC4899"),
	"ollama":     lipgloss.Color("#E1E1E8"),
}

// ProviderColor returns the brand color for a provider slug. Falls back
// to TextPrimary for unknown slugs (defensive — every registered
// provider should be in the map).
func ProviderColor(slug string) lipgloss.Color {
	if c, ok := providerColors[slug]; ok {
		return c
	}
	return TextPrimary
}

// ────────────────────────────────────────────────────────────────────────────
// Pre-built styles — every component composes these so the design system
// is the single source of truth for visual decisions.
// ────────────────────────────────────────────────────────────────────────────

var (
	StylePrimary   = lipgloss.NewStyle().Foreground(TextPrimary)
	StyleSecondary = lipgloss.NewStyle().Foreground(TextSecondary)
	StyleDim       = lipgloss.NewStyle().Foreground(TextDim)
	StyleAccent    = lipgloss.NewStyle().Foreground(AccentPrimary).Bold(true)
	StyleAccentDim = lipgloss.NewStyle().Foreground(AccentPrimaryDim)

	StyleSuccess = lipgloss.NewStyle().Foreground(Success)
	StyleWarning = lipgloss.NewStyle().Foreground(Warning)
	StyleError   = lipgloss.NewStyle().Foreground(Error)
	StyleInfo    = lipgloss.NewStyle().Foreground(Info)

	StyleTopBar = lipgloss.NewStyle().
			Background(BaseSurface).
			Foreground(TextPrimary).
			Padding(0, 1).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(BaseBorder)

	StyleUserMessage = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(AccentPrimary).
				Padding(0, 1)

	StyleAgentMessage = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(BaseBorder).
				Padding(0, 1)

	StyleSystemMessage = lipgloss.NewStyle().
				Foreground(TextSecondary).
				Italic(true).
				Padding(0, 2)

	StyleApprovalPrompt = lipgloss.NewStyle().
				Border(lipgloss.ThickBorder()).
				BorderForeground(Warning).
				Padding(0, 1)

	StyleToolCall = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(BaseBorder).
			Background(BaseSurface).
			Padding(0, 1)

	StyleInputIdle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(BaseBorder).
			Padding(0, 1)

	StyleInputFocused = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(AccentPrimary).
				Padding(0, 1)

	StyleDiffAdded   = lipgloss.NewStyle().Foreground(Success)
	StyleDiffRemoved = lipgloss.NewStyle().Foreground(Error)
	StyleDiffHunk    = lipgloss.NewStyle().Foreground(TextSecondary)
)

// LabelBadge renders text as an UPPERCASE bold label in the given color.
// Used for "You", "packetcode (model)", and tool-name headers.
func LabelBadge(text string, color lipgloss.Color) string {
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(text)
}
