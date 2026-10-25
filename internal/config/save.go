package config

// Persisting configuration.
//
// Saving is surgical: packetcode works out which individual settings it means
// to change and rewrites only those, in place. Everything else in
// config.toml -- comments, key order, spacing, and every key this build does
// not recognise -- comes out byte-identical.
//
// The alternative, and what this replaced, was to re-encode the whole struct.
// That deleted the user's comments on every save, and worse: an older build
// that saved a config written by a newer one silently dropped every setting it
// had no field for. `sugar login`, /provider, /effort and the API-key picker
// all save, so one keystroke was enough to lose settings permanently.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/packetcode/packetcode/internal/atomicfile"
)

// Save writes the config to ~/.packetcode/config.toml with 0600 perms,
// changing only the settings that differ from what the file already says.
func (c *Config) Save() error {
	path, err := ConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	return c.SaveTo(path)
}

// SaveTo writes the config to an explicit path. Same semantics as Save.
//
// The settings written are those where this Config disagrees with the file on
// disk, which means it persists any in-memory change since the load -- the
// behaviour every existing caller relies on. Prefer Update for new code: it
// persists only what its own mutation touched, so an unrelated field someone
// adjusted elsewhere is not swept along with it.
func (c *Config) SaveTo(path string) error {
	existing, err := readConfigFile(path)
	if err != nil {
		return err
	}
	var edits []tomlEdit
	if strings.TrimSpace(existing) != "" {
		stored := Default()
		// Decoded into Default() so absent keys carry the same values they
		// would after a load; otherwise every unset key would look like a
		// change and get written out.
		if _, err := toml.Decode(existing, stored); err != nil {
			return fmt.Errorf("parse config %s before saving: %w", path, err)
		}
		before, err := configSettings(stored)
		if err != nil {
			return err
		}
		after, err := configSettings(c)
		if err != nil {
			return err
		}
		edits = diffSettings(before, after)
	}
	return c.writeSettings(path, existing, edits)
}

// Update applies a mutation and persists exactly the settings it changed.
//
// This is the shape callers want: they set a key or a model, and nothing else
// in the file moves -- not their comments, not a setting a newer build wrote,
// and not an unrelated field some other part of the process happened to touch
// on the same Config.
func (c *Config) Update(mutate func(*Config)) error {
	path, err := ConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	return c.UpdateIn(path, mutate)
}

// UpdateIn is Update against an explicit path.
func (c *Config) UpdateIn(path string, mutate func(*Config)) error {
	if mutate == nil {
		return nil
	}
	before, err := configSettings(c)
	if err != nil {
		return err
	}
	mutate(c)
	after, err := configSettings(c)
	if err != nil {
		return err
	}
	existing, err := readConfigFile(path)
	if err != nil {
		return err
	}
	return c.writeSettings(path, existing, diffSettings(before, after))
}

// writeSettings patches existing with edits and replaces the file atomically.
func (c *Config) writeSettings(path, existing string, edits []tomlEdit) error {
	// A file that does not exist yet, or holds only whitespace, has nothing to
	// preserve. Encoding the whole struct there gives a first run a complete,
	// readable config instead of the two or three keys setup happened to set.
	if strings.TrimSpace(existing) == "" {
		var buf strings.Builder
		if err := toml.NewEncoder(&buf).Encode(c); err != nil {
			return fmt.Errorf("encode config: %w", err)
		}
		return writeConfigFile(path, buf.String())
	}
	if len(edits) == 0 {
		// Nothing to change, so nothing is written. The file keeps its mtime
		// and its bytes.
		return nil
	}
	patched, err := patchTOML(existing, edits)
	if err != nil {
		return fmt.Errorf("update config %s: %w", path, err)
	}
	// The patcher edits text, so a mistake in it is a corrupted config -- a
	// worse outcome than the rewriting it exists to prevent. Reparse and
	// compare before committing: the result must differ from the original in
	// exactly the settings that were asked for and in nothing else.
	if err := verifyTOMLPatch(existing, patched, edits); err != nil {
		return fmt.Errorf("refusing to write config %s: %w", path, err)
	}
	return writeConfigFile(path, patched)
}

func readConfigFile(path string) (string, error) {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	data, err := atomicfile.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read config: %w", err)
	}
	return string(data), nil
}

func writeConfigFile(path, contents string) error {
	// Synced before the rename, like sessions and job records: without it a
	// crash can leave a correctly named, empty config.toml.
	if err := atomicfile.Write(path, []byte(contents), 0o600, ".config.*.toml.tmp"); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// tomlLeaf is one setting: the path that addresses it and its decoded value.
type tomlLeaf struct {
	path  []string
	value any
}

// configSettings flattens a Config into the leaf settings it would occupy in
// a TOML file.
//
// It goes through the encoder rather than reflecting over the struct so that
// `omitempty`, field names, and value types are decided in exactly one place:
// whatever the encoder would have written is what the diff compares.
func configSettings(c *Config) (map[string]tomlLeaf, error) {
	var buf strings.Builder
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	var generic map[string]any
	if _, err := toml.Decode(buf.String(), &generic); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return flattenTOML(generic), nil
}

func flattenTOML(document map[string]any) map[string]tomlLeaf {
	leaves := map[string]tomlLeaf{}
	var walk func(prefix []string, table map[string]any)
	walk = func(prefix []string, table map[string]any) {
		for key, value := range table {
			path := append(append([]string{}, prefix...), key)
			if child, ok := value.(map[string]any); ok {
				// Only leaves are settings. A table contributes nothing of its
				// own, so an empty one is neither a change to make nor a loss
				// to guard against.
				walk(path, child)
				continue
			}
			leaves[tomlPathKey(path)] = tomlLeaf{path: path, value: normalizeTOMLValue(value)}
		}
	}
	walk(nil, document)
	return leaves
}

// normalizeTOMLValue erases a distinction the decoder makes but TOML does not:
// an array of tables written as [[table]] blocks decodes as []map[string]any
// while the same array written inline decodes as []any. Left alone, that makes
// a value look changed when only its spelling differs.
func normalizeTOMLValue(value any) any {
	switch v := value.(type) {
	case []map[string]any:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, normalizeTOMLValue(item))
		}
		return items
	case []any:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, normalizeTOMLValue(item))
		}
		return items
	case map[string]any:
		table := make(map[string]any, len(v))
		for key, item := range v {
			table[key] = normalizeTOMLValue(item)
		}
		return table
	default:
		return value
	}
}

// diffSettings is the list of edits that turns before into after. Sorted so a
// save is deterministic and so keys landing in the same new table stay
// together.
func diffSettings(before, after map[string]tomlLeaf) []tomlEdit {
	var edits []tomlEdit
	for key, leaf := range after {
		if old, ok := before[key]; ok && reflect.DeepEqual(old.value, leaf.value) {
			continue
		}
		edits = append(edits, tomlEdit{path: leaf.path, value: leaf.value})
	}
	for key, leaf := range before {
		if _, ok := after[key]; !ok {
			edits = append(edits, tomlEdit{path: leaf.path, remove: true})
		}
	}
	sort.Slice(edits, func(i, j int) bool {
		return tomlPathKey(edits[i].path) < tomlPathKey(edits[j].path)
	})
	return edits
}

// verifyTOMLPatch confirms the patched document still parses and differs from
// the original in exactly the intended settings: every other key survives with
// the same value, no key appears that nobody asked for, and every edit landed.
func verifyTOMLPatch(before, after string, edits []tomlEdit) error {
	var beforeDoc, afterDoc map[string]any
	if _, err := toml.Decode(before, &beforeDoc); err != nil {
		return fmt.Errorf("the file no longer parses: %w", err)
	}
	if _, err := toml.Decode(after, &afterDoc); err != nil {
		return fmt.Errorf("the result would not parse: %w", err)
	}
	beforeLeaves := flattenTOML(beforeDoc)
	afterLeaves := flattenTOML(afterDoc)
	intended := make(map[string]tomlEdit, len(edits))
	for _, edit := range edits {
		intended[tomlPathKey(edit.path)] = edit
	}

	for key, want := range beforeLeaves {
		got, present := afterLeaves[key]
		edit, edited := intended[key]
		switch {
		case !edited:
			if !present {
				return fmt.Errorf("it would drop %s, which nothing asked to change", renderTOMLKeyPath(want.path))
			}
			if !reflect.DeepEqual(want.value, got.value) {
				return fmt.Errorf("it would change %s, which nothing asked to change", renderTOMLKeyPath(want.path))
			}
		case edit.remove:
			if present {
				return fmt.Errorf("%s was not cleared", edit.name())
			}
		default:
			if !present || !reflect.DeepEqual(edit.value, got.value) {
				return fmt.Errorf("%s did not survive the write", edit.name())
			}
		}
	}
	for key, got := range afterLeaves {
		if _, existed := beforeLeaves[key]; existed {
			continue
		}
		edit, edited := intended[key]
		if !edited || edit.remove {
			return fmt.Errorf("it would add %s, which nothing asked for", renderTOMLKeyPath(got.path))
		}
		if !reflect.DeepEqual(edit.value, got.value) {
			return fmt.Errorf("%s did not survive the write", edit.name())
		}
	}
	for key, edit := range intended {
		if _, present := afterLeaves[key]; !present && !edit.remove {
			return fmt.Errorf("%s was not written", edit.name())
		}
	}
	return nil
}

// The named settings below are the migration target for the four places that
// call Save today. Each says what it is changing, so the file it writes to
// changes by exactly that much -- no round trip, and no dependence on what
// else happened to the in-memory Config first.

// SetProviderKey records an API key for the named provider and persists it,
// leaving every other setting in the file alone.
func (c *Config) SetProviderKey(slug, apiKey string) error {
	return c.Update(providerKeyEdit(slug, apiKey))
}

func (c *Config) setProviderKeyIn(path, slug, apiKey string) error {
	return c.UpdateIn(path, providerKeyEdit(slug, apiKey))
}

func providerKeyEdit(slug, apiKey string) func(*Config) {
	return func(cfg *Config) {
		cfg.setProvider(slug, func(p *ProviderConfig) { p.APIKey = apiKey })
	}
}

// SetProviderReasoningEffort records the reasoning effort for a provider and
// persists it. An empty effort clears the setting rather than storing a blank.
func (c *Config) SetProviderReasoningEffort(slug, effort string) error {
	return c.Update(providerEffortEdit(slug, effort))
}

func (c *Config) setProviderReasoningEffortIn(path, slug, effort string) error {
	return c.UpdateIn(path, providerEffortEdit(slug, effort))
}

func providerEffortEdit(slug, effort string) func(*Config) {
	return func(cfg *Config) {
		cfg.setProvider(slug, func(p *ProviderConfig) { p.ReasoningEffort = effort })
	}
}

// SetActiveModel records the provider/model pair packetcode should open on,
// and the provider's own default model so switching back lands here again.
func (c *Config) SetActiveModel(slug, modelID string) error {
	return c.Update(activeModelEdit(slug, modelID))
}

func (c *Config) setActiveModelIn(path, slug, modelID string) error {
	return c.UpdateIn(path, activeModelEdit(slug, modelID))
}

func activeModelEdit(slug, modelID string) func(*Config) {
	return func(cfg *Config) {
		cfg.setProvider(slug, func(p *ProviderConfig) { p.DefaultModel = modelID })
		cfg.Default.Provider = slug
		cfg.Default.Model = modelID
	}
}

func (c *Config) setProvider(slug string, mutate func(*ProviderConfig)) {
	if c.Providers == nil {
		c.Providers = map[string]ProviderConfig{}
	}
	provider := c.Providers[slug]
	mutate(&provider)
	c.Providers[slug] = provider
}
