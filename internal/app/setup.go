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
	"sort"
	"strconv"
	"strings"
	"time"

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

func RunSetup(in io.Reader, out io.Writer, cfg *config.Config, factories FactoryMap) (*SetupResult, error) {
	reader := bufio.NewReader(in)

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "  ⚡ Welcome to packetcode")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "  No providers configured yet. Let's set one up.")
	fmt.Fprintln(out, "  (Configure additional providers later with Ctrl+P, then Ctrl+A on a provider row)")
	fmt.Fprintln(out, "")

	for {
		slug, err := promptProvider(reader, out, factories)
		if err != nil {
			return nil, err
		}

		key, err := promptKey(reader, out, cfg, slug)
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
	slugs := make([]string, 0, len(factories))
	for s, factory := range factories {
		if factory == nil {
			continue
		}
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
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

func promptKey(r *bufio.Reader, out io.Writer, cfg *config.Config, slug string) (string, error) {
	if !setupProviderRequiresKey(cfg, slug) {
		fmt.Fprintf(out, "  %s is keyless — no API key needed.\n", slug)
		return "", nil
	}
	fmt.Fprintf(out, "  %s API key: ", slug)
	raw, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", errors.New("empty API key")
	}
	return key, nil
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
	fmt.Fprintf(out, "  Available models (%d):\n", len(models))
	for i, m := range models {
		fmt.Fprintf(out, "    %d) %s", i+1, m.ID)
		if m.ContextWindow > 0 {
			fmt.Fprintf(out, "  (%d ctx)", m.ContextWindow)
		}
		fmt.Fprintln(out, "")
	}
	for {
		fmt.Fprint(out, "  Choice [1]: ")
		raw, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return models[0].ID, nil
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > len(models) {
			fmt.Fprintln(out, "  Please enter a number from the list.")
			continue
		}
		return models[n-1].ID, nil
	}
}
