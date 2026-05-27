// Setup is the first-run flow: walk the user through choosing a
// provider, entering an API key, validating it, and picking a model.
//
// We use a deliberately lo-fi line-based prompt rather than a Bubble Tea
// modal because the alternative (a full TUI flow before the main TUI) is
// a lot of code for an interaction the user does once. stdin/stdout via
// bufio is plenty for "type your key here".
package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	term "github.com/charmbracelet/x/term"

	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/provider"
)

// SetupResult is what RunSetup hands back to main(). The mutated config
// has already been saved; main() typically reloads it before constructing
// the App so the in-memory copy matches disk.
type SetupResult struct {
	Slug  string
	Model string
}

// RunSetup walks the user through configuring at least one provider.
// `factories` maps each known provider slug to a constructor that takes
// an API key and returns a provider.Provider. ollama's factory ignores
// the key argument.
type ProviderFactory func(apiKey string) provider.Provider

// FactoryMap covers every provider packetcode knows about.
type FactoryMap map[string]ProviderFactory

const setupModelListLimit = 20

func RunSetup(in io.Reader, out io.Writer, cfg *config.Config, factories FactoryMap) (*SetupResult, error) {
	reader := bufio.NewReader(in)

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "  ⚡ Welcome to packetcode")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "  No providers configured yet. Let's set one up.")
	fmt.Fprintln(out, "  (Configure additional providers later with Ctrl+P, Ctrl+A, or /provider add <slug>)")
	fmt.Fprintln(out, "")

	for {
		slug, err := promptProvider(reader, out, factories)
		if err != nil {
			return nil, err
		}

		key, err := promptKey(reader, out, cfg, slug, in)
		if err != nil {
			return nil, err
		}

		fmt.Fprintf(out, "  Validating key... ")
		factory := factories[slug]
		if factory == nil {
			return nil, fmt.Errorf("provider %q is not available in this build", slug)
		}
		prov := factory(key)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		validateErr := prov.ValidateKey(ctx, key)
		cancel()
		if validateErr != nil {
			fmt.Fprintln(out, "✗")
			fmt.Fprintf(out, "  %s\n", validateErr)
			fmt.Fprintln(out, "  Try again? (y/N)")
			ans, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(ans)) != "y" {
				return nil, fmt.Errorf("setup cancelled")
			}
			continue
		}
		fmt.Fprintln(out, "✓")

		fmt.Fprintln(out, "  Loading models...")
		ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
		models, err := prov.ListModels(ctx)
		cancel()
		if err != nil {
			fmt.Fprintf(out, "  Could not list models: %s\n", err)
			return nil, err
		}
		if len(models) == 0 {
			return nil, errors.New("provider returned no models")
		}

		modelID, err := promptModel(reader, out, models)
		if err != nil {
			return nil, err
		}

		// Persist.
		if cfg.Providers == nil {
			cfg.Providers = map[string]config.ProviderConfig{}
		}
		pc := cfg.Providers[slug]
		pc.APIKey = key
		pc.DefaultModel = modelID
		cfg.Providers[slug] = pc
		cfg.Default.Provider = slug
		cfg.Default.Model = modelID
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("save config: %w", err)
		}

		fmt.Fprintf(out, "\n  ✓ Saved. Active: %s / %s\n\n", slug, modelID)
		return &SetupResult{Slug: slug, Model: modelID}, nil
	}
}

func promptProvider(r *bufio.Reader, out io.Writer, factories FactoryMap) (string, error) {
	slugs := setupProviderSlugs(factories)
	if len(slugs) == 0 {
		return "", errors.New("no providers available")
	}

	fmt.Fprintln(out, "  Available providers:")
	for i, s := range slugs {
		fmt.Fprintf(out, "    %d) %s\n", i+1, s)
	}
	for {
		fmt.Fprint(out, "  Choice [1]: ")
		raw, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return slugs[0], nil
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > len(slugs) {
			fmt.Fprintln(out, "  Please enter a number from the list.")
			continue
		}
		return slugs[n-1], nil
	}
}

func setupProviderSlugs(factories FactoryMap) []string {
	slugs := make([]string, 0, len(factories))
	seen := make(map[string]struct{}, len(factories))
	for _, slug := range provider.DisplayOrder() {
		factory := factories[slug]
		if factory == nil {
			continue
		}
		slugs = append(slugs, slug)
		seen[slug] = struct{}{}
	}
	var extras []string
	for slug, factory := range factories {
		if factory == nil {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		extras = append(extras, slug)
	}
	sort.Strings(extras)
	return append(slugs, extras...)
}

func promptKey(r *bufio.Reader, out io.Writer, cfg *config.Config, slug string, rawIn ...io.Reader) (string, error) {
	if !setupProviderRequiresKey(cfg, slug) {
		fmt.Fprintf(out, "  %s is keyless — no API key needed.\n", slug)
		return "", nil
	}
	for {
		fmt.Fprintf(out, "  %s API key: ", slug)
		raw, err := readSetupSecret(r, out, rawIn...)
		if err != nil {
			return "", err
		}
		key := strings.TrimSpace(raw)
		if key == "" {
			fmt.Fprintln(out, "  API key cannot be empty.")
			continue
		}
		return key, nil
	}
}

func readSetupSecret(r *bufio.Reader, out io.Writer, rawIn ...io.Reader) (string, error) {
	if len(rawIn) > 0 {
		if f, ok := rawIn[0].(*os.File); ok && term.IsTerminal(f.Fd()) {
			data, err := term.ReadPassword(f.Fd())
			fmt.Fprintln(out, "")
			if err != nil {
				return "", err
			}
			return string(data), nil
		}
	}
	return r.ReadString('\n')
}

func setupProviderRequiresKey(cfg *config.Config, slug string) bool {
	if cfg == nil {
		return slug != "ollama"
	}
	if pc, ok := cfg.Providers[slug]; ok {
		return pc.RequiresAPIKey(slug)
	}
	return slug != "ollama"
}

func promptModel(r *bufio.Reader, out io.Writer, models []provider.Model) (string, error) {
	shown := models
	for {
		renderSetupModels(out, shown, len(models))
		fmt.Fprint(out, "  Choice, model id, or filter [1]: ")
		raw, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return shown[0].ID, nil
		}
		n, err := strconv.Atoi(raw)
		if err == nil {
			if n >= 1 && n <= visibleSetupModelCount(shown) {
				return shown[n-1].ID, nil
			}
			fmt.Fprintln(out, "  Please enter a number from the visible list.")
			continue
		}
		for _, m := range models {
			if m.ID == raw {
				return m.ID, nil
			}
		}
		filtered := filterSetupModels(models, raw)
		if len(filtered) == 0 {
			fmt.Fprintf(out, "  No models match %q.\n", raw)
			continue
		}
		shown = filtered
	}
}

func visibleSetupModelCount(models []provider.Model) int {
	if len(models) > setupModelListLimit {
		return setupModelListLimit
	}
	return len(models)
}

func renderSetupModels(out io.Writer, models []provider.Model, total int) {
	limit := len(models)
	if limit > setupModelListLimit {
		limit = setupModelListLimit
	}
	fmt.Fprintf(out, "  Available models (%d", len(models))
	if len(models) != total {
		fmt.Fprintf(out, " of %d", total)
	}
	fmt.Fprintln(out, "):")
	for i := 0; i < limit; i++ {
		m := models[i]
		fmt.Fprintf(out, "    %d) %s", i+1, m.ID)
		if m.ContextWindow > 0 {
			fmt.Fprintf(out, "  (%d ctx)", m.ContextWindow)
		}
		fmt.Fprintln(out, "")
	}
	if len(models) > limit {
		fmt.Fprintf(out, "    ... %d more; type a model id or filter text\n", len(models)-limit)
	}
}

func filterSetupModels(models []provider.Model, query string) []provider.Model {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return models
	}
	out := make([]provider.Model, 0)
	for _, m := range models {
		if strings.Contains(strings.ToLower(m.ID), query) ||
			strings.Contains(strings.ToLower(m.DisplayName), query) {
			out = append(out, m)
		}
	}
	return out
}
