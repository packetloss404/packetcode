# Provider / Model Selector Modals — Round 2 Design Spec

## Summary

Replace the unimplemented Ctrl+P / Ctrl+M keybindings with live modal pickers that mirror the behaviour of `/provider <slug>` and `/model <id>` exactly. Introduce one generic `picker` component — a filter-as-you-type list modal — that Round 3's slash-command autocomplete popup and Round 6's theme picker can reuse. Extract the provider-switch and model-switch side effects into two private App helpers (`applyProviderSwitch`, `applyModelSwitch`) so the slash handlers and the picker dispatch through a single code path, and add an in-memory model-list cache to `provider.Registry` so repeat Ctrl+M opens don't refetch.

## User stories

1. **Switch provider without leaving the flow.** Mid-conversation the user hits `Ctrl+P`. The provider modal opens showing configured and available providers with brand-coloured dots, the current default model, and a `key present` / `(no key)` indicator. They arrow down to `gemini` and hit Enter. The modal closes, the top bar flips to `gemini · gemini-2.5-flash`, and `switched provider: gemini (gemini-2.5-flash)` appears in the conversation. Next turn goes to Gemini.

2. **Pick a model on a slow network.** After switching to OpenRouter the user hits `Ctrl+M`. The modal opens immediately with `loading models…`. Two seconds later the list populates — 200 models — ordered as the provider returned them. The user types `claude 3.7` to narrow to four rows, arrows to `anthropic/claude-3.7-sonnet`, hits Enter. Top bar updates, `switched model: openrouter/anthropic/claude-3.7-sonnet` prints.

3. **Ambient Ctrl+M on a provider that just failed.** The user hits `Ctrl+M` but the provider is unreachable. The modal shows `error: Get "https://…": lookup …: no such host`. A footer hint reads `Esc close · r retry`. The user hits `r`; the loader re-runs; on success the modal populates without having to close and re-open.

## Component design

### `picker.Model` (generic)

Lives at `internal/ui/components/picker/picker.go`. Presentation-only, same lifecycle shape as `approval` and `jobs`:

```go
package picker

type Item struct {
    ID     string
    Label  string
    Detail string
    Marker string          // single-rune visual marker (e.g. "●" for active)
    Color  lipgloss.Color  // optional tint for Marker + Label prefix
    Extra  any             // caller-owned payload
}

type Loader func(ctx context.Context) ([]Item, error)

type SelectMsg struct {
    PickerID string
    Item     Item
}

type CloseMsg struct{ PickerID string }

type Model struct {
    id       string
    visible  bool
    title    string
    filter   string
    items    []Item
    filtered []int
    cursor   int
    width    int
    height   int
    loading  bool
    loadErr  error
    loader   Loader
}

func New(id, title string) Model
func (m *Model) Open(loader Loader) tea.Cmd
func (m *Model) SetItems(items []Item)
func (m *Model) Hide()
func (m Model)  Visible() bool
func (m *Model) Resize(w, h int)
func (m Model)  Update(msg tea.Msg) (Model, tea.Cmd)
func (m Model)  View() string
func (m *Model) SetActive(id string)
```

Internal:

```go
type itemsLoadedMsg struct {
    pickerID string
    items    []Item
    err      error
}
```

### Update semantics (only when `m.visible == true`)

| Key | Action |
|---|---|
| `up`, `ctrl+p`, `ctrl+k` | Cursor up 1 (stays at 0) |
| `down`, `ctrl+n`, `ctrl+j` | Cursor down 1 (stays at len-1) |
| `pgup` | Up by height/2 |
| `pgdown` | Down by height/2 |
| `home` | Cursor to 0 |
| `end` | Cursor to len-1 |
| `enter` | Emit `SelectMsg{PickerID, Item}`, `Hide()` |
| `esc` | Emit `CloseMsg{PickerID}`, `Hide()` |
| `backspace` | Drop last rune from filter |
| `ctrl+u` | Clear filter, cursor=0 |
| `r` | Error state only: re-fire loader |
| printable rune (no modifier) | Append to filter |

Note: `ctrl+m` is `tea.KeyEnter` on many terminals — folds into Enter. `ctrl+p` inside the picker is cursor-up; the global handler refuses to stack a second picker anyway.

Filter matching: case-insensitive substring on normalized haystack (ID + " " + Label + " " + Detail, whitespace collapsed to dashes). Multiple filter tokens are joined with dashes (so `"gpt 5.5"` becomes `"gpt-5.5"` substring match).

### View layout

```
╭─ Select provider ──────────────────────────────────╮
│ > gpt                                              │
│                                                    │
│   ● openai       OpenAI           gpt-5.5    key present │
│ ▶   gemini       Google Gemini    gemini…    key present │
│     minimax      MiniMax          abab6…     no key│
│     openrouter   OpenRouter       auto       key present │
│     ollama       Ollama           qwen2.5    local │
│                                                    │
│  ↑/↓ move · ⏎ select · Esc close                   │
╰────────────────────────────────────────────────────╯
```

- Outer: `lipgloss.RoundedBorder()` with `theme.AccentPrimary` foreground, `Padding(0, 1)`.
- Width: `clamp(terminal_width/2, 52, 96)`.
- Height: `clamp(3 + len(filtered) + 1, 8, terminal_height/2)`. Loading/error states: height 8.
- Title: accent-coloured, left-aligned.
- Filter line: `"> " + filter + "▌"` (static cursor block).
- Rows: three columns — marker (width 4: `"▶ " + marker + " "` cursor row, else `"  " + marker + " "`), label (left-aligned, width = min(24, body/3)), detail (right-aligned, ellipsis-truncated).
- Cursor row: `Background(theme.BaseSurfaceBright)`.
- Footer: `↑/↓ move · ⏎ select · Esc close` in `theme.StyleDim`. Appends `· filtering N/M` when filter active.
- Error state: `error: <msg>` in `theme.StyleError` + `r retry · Esc close`.
- Loading state: centered `loading…` in `theme.StyleDim`.

Scrolling: direct-render window (no viewport) with `scrollOffset` kept so cursor is always visible.

## App integration

### Focus precedence

Updated: **approval > picker > jobsPanel > spinner**.

Both `Update()` routing and `View()` overlay cascade adjusted.

### Keybindings

`handleKey` adds above `ctrl+c` / `ctrl+l`:

```go
case "ctrl+p":
    if a.approval.Visible() || a.picker.Visible() {
        return a, nil
    }
    return a, a.openProviderPicker()
case "ctrl+m":
    if a.approval.Visible() || a.picker.Visible() {
        return a, nil
    }
    return a, a.openModelPicker()
```

Note: Ctrl+P/M DO open over the jobs panel (picker's higher precedence hides jobs visually; closing restores it).

`keymap.go` updates:

```go
GlobalKeys = []KeyHelp{
    {"Ctrl+P", "Open provider picker"},
    {"Ctrl+M", "Open model picker"},
    {"Ctrl+C", "Cancel current generation; press twice to exit"},
    {"Ctrl+L", "Clear screen (keep session)"},
    {"Esc", "Close jobs modal / approval / picker"},
}

PickerKeys = []KeyHelp{
    {"↑/↓", "Move cursor"},
    {"Ctrl+N/P", "Move cursor (down/up)"},
    {"PgUp/PgDn", "Move a half page"},
    {"Enter", "Select"},
    {"Esc", "Close"},
    {"Ctrl+U", "Clear filter"},
    {"r", "Retry (error state)"},
    {"type", "Filter items"},
}
```

`slashcmd_help.go` renders `PickerKeys` in the help output.

### Async model loading

Provider picker: synchronous (`Registry.List()`).

Model picker: show `loading…` immediately, dispatch loader as `tea.Cmd`, populate on `itemsLoadedMsg`. Cache successful loads via `Registry.SetCachedModels`.

Registry additions (`internal/provider/registry.go`):

```go
cachedModels map[string][]Model  // new field
mu           sync.RWMutex        // already present

func (r *Registry) CachedModels(slug string) ([]Model, bool)
func (r *Registry) SetCachedModels(slug string, ms []Model)
func (r *Registry) InvalidateCachedModels(slug string)
```

**Do not** invalidate on `SetActive` — model lists rarely change mid-session; a manual refresh is cheap.

### Shared switch helpers

`internal/app/provider_switch.go` (new):

```go
// applyProviderSwitch mirrors /provider <slug>'s full side-effect
// chain: resolve the model (config default → cached ListModels →
// fresh ListModels[0] → error), call Registry.SetActive, refresh
// the top bar, and append a "switched provider" system message.
func (a *App) applyProviderSwitch(slug string) error {
    prov, ok := a.deps.Registry.Get(slug)
    if !ok {
        return fmt.Errorf("unknown provider %q", slug)
    }
    modelID := ""
    if a.deps.Config != nil {
        if pc, ok := a.deps.Config.Providers[slug]; ok {
            modelID = pc.DefaultModel
        }
    }
    if modelID == "" {
        if cached, ok := a.deps.Registry.CachedModels(slug); ok && len(cached) > 0 {
            modelID = cached[0].ID
        } else {
            ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
            models, err := prov.ListModels(ctx)
            cancel()
            if err == nil && len(models) > 0 {
                a.deps.Registry.SetCachedModels(slug, models)
                modelID = models[0].ID
            }
        }
    }
    if modelID == "" {
        return fmt.Errorf("%s has no default model; run /model <id> after switching", slug)
    }
    if err := a.deps.Registry.SetActive(slug, modelID); err != nil {
        return err
    }
    a.refreshTopBar()
    a.conversation.AppendSystem(fmt.Sprintf("switched provider: %s (%s)", slug, modelID))
    return nil
}

func (a *App) applyModelSwitch(modelID string) error {
    prov, _ := a.deps.Registry.Active()
    if prov == nil {
        return fmt.Errorf("no active provider")
    }
    if err := a.deps.Registry.SetActive(prov.Slug(), modelID); err != nil {
        return err
    }
    a.refreshTopBar()
    a.conversation.AppendSystem(fmt.Sprintf("switched model: %s/%s", prov.Slug(), modelID))
    return nil
}
```

Refactor `slashcmd_provider.go`:
- `handleProviderCommand(args)` with 1 arg → `a.applyProviderSwitch(args[0])`, wrap error with `"provider: "` prefix.
- `handleModelCommand(args)` with 1 arg → `a.applyModelSwitch(args[0])`, wrap error with `"model: "` prefix.
- In both list branches (0 args), after a successful `prov.ListModels`, call `a.deps.Registry.SetCachedModels(prov.Slug(), models)` to warm the cache.

### Item builders

`internal/app/picker_items.go` (new):

```go
func providerItems(regs []provider.Provider, cfg *config.Config, activeSlug string) []picker.Item
func modelItems(ms []provider.Model, activeID string, prov provider.Provider) []picker.Item
func fmtContext(n int) string // "128k", "1M", or raw
```

Provider row: `Label="slug — Name"`, `Detail="<default-model> · <key-status>"`, `Marker="●"` if active, `Color=prov.BrandColor()`.
Model row: `Label="id (DisplayName)"` or just `id`, `Detail="<ctx> · <tools> · <pricing>"`, `Marker="●"` if active.

Key status: `"key present"` / `"(no key)"` / `"local"` (Ollama).

### `openProviderPicker` / `openModelPicker`

```go
func (a *App) openProviderPicker() tea.Cmd {
    provs := a.deps.Registry.List()
    if len(provs) == 0 {
        a.conversation.AppendSystem("provider picker: no providers configured")
        return nil
    }
    activeSlug := ""
    if p, _ := a.deps.Registry.Active(); p != nil {
        activeSlug = p.Slug()
    }
    a.picker = picker.New("provider", "Select provider")
    a.picker.Resize(a.width, a.height)
    a.picker.SetItems(providerItems(provs, a.deps.Config, activeSlug))
    a.picker.SetActive(activeSlug)
    return a.picker.Open(nil)
}

func (a *App) openModelPicker() tea.Cmd {
    prov, active := a.deps.Registry.Active()
    if prov == nil {
        a.conversation.AppendSystem("model picker: no active provider")
        return nil
    }
    a.picker = picker.New("model", fmt.Sprintf("Select model — %s", prov.Name()))
    a.picker.Resize(a.width, a.height)
    a.picker.SetActive(active)
    if cached, ok := a.deps.Registry.CachedModels(prov.Slug()); ok {
        a.picker.SetItems(modelItems(cached, active, prov))
        return a.picker.Open(nil)
    }
    loader := func(ctx context.Context) ([]picker.Item, error) {
        ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
        defer cancel()
        models, err := prov.ListModels(ctx)
        if err != nil {
            return nil, err
        }
        a.deps.Registry.SetCachedModels(prov.Slug(), models)
        return modelItems(models, active, prov), nil
    }
    return a.picker.Open(loader)
}
```

`Update` additions:

```go
case picker.SelectMsg:
    switch msg.PickerID {
    case "provider":
        if err := a.applyProviderSwitch(msg.Item.ID); err != nil {
            a.conversation.AppendSystem("provider: " + err.Error())
        }
    case "model":
        if err := a.applyModelSwitch(msg.Item.ID); err != nil {
            a.conversation.AppendSystem("model: " + err.Error())
        }
    }
    return a, nil
case picker.CloseMsg:
    return a, nil
```

`Open(loader)` semantics:
- `loader == nil` → `visible=true`, `loading=false`, preloaded items via `SetItems`; return nil.
- Else → `visible=true`, `loading=true`; returned `tea.Cmd` runs `loader(context.Background())` and emits `itemsLoadedMsg{pickerID: m.id, items, err}`.

## File-by-file change list

### Bucket A — Implementation

| Path | What changes |
|---|---|
| `internal/ui/components/picker/picker.go` | **NEW.** Generic modal with types + Model + methods. ~400 LOC. |
| `internal/ui/components/picker/filter.go` | **NEW.** Pure `matches` / `normalize` helpers. |
| `internal/provider/registry.go` | Add `cachedModels map[string][]Model` field + `CachedModels` / `SetCachedModels` / `InvalidateCachedModels`. Thread-safe via existing `mu`. Initialize in `NewRegistry`. |
| `internal/app/app.go` | Add `picker picker.Model` field; construct placeholder in `New`; add `ctrl+p` / `ctrl+m` to `handleKey`; add picker branch in `Update`'s precedence switch; add `picker.SelectMsg` + `picker.CloseMsg` cases; add picker to overlay cascade in `View`; call `a.picker.Resize(w, h)` from `resize`. |
| `internal/app/provider_switch.go` | **NEW.** `applyProviderSwitch(slug) error`, `applyModelSwitch(modelID) error`. |
| `internal/app/picker_items.go` | **NEW.** `providerItems`, `modelItems`, `fmtContext`. |
| `internal/app/slashcmd_provider.go` | Refactor switch branches to call shared helpers; warm cache after list calls. |
| `internal/app/keymap.go` | Update `GlobalKeys` (Ctrl+P, Ctrl+M). Add `PickerKeys`. |
| `internal/app/slashcmd_help.go` | Render `PickerKeys` section. |

### Bucket B — Tests + docs + commit

| Path | What changes |
|---|---|
| `internal/ui/components/picker/picker_test.go` | **NEW.** Component unit tests (enumerated below). |
| `internal/ui/components/picker/filter_test.go` | **NEW.** Matcher tests. |
| `internal/provider/registry_test.go` | Add cache tests. |
| `internal/app/app_picker_test.go` | **NEW.** Integration tests for Ctrl+P / Ctrl+M. |
| `internal/app/app_slashcmd_test.go` | Add `TestApp_ProviderSwitch_UsesCacheWhenPresent` regression + helper-delegation checks. |
| `README.md` | Strike provider/model modals from Next; add Keyboard subsection. |
| `CHANGELOG.md` | Added bullet for picker modals; removed from Deferred. |
| `docs/roadmap-deferred.md` | Mark Round 2 landed. |

## Tests

### `internal/ui/components/picker/filter_test.go`
- `TestMatches_EmptyFilter` — every non-empty item matches.
- `TestMatches_CaseInsensitiveSubstring` — `"GPT"` matches `"gpt-5.5"`.
- `TestMatches_WhitespaceToDash` — `"gpt 5.5"` matches `"gpt-5.5"`.
- `TestMatches_MultipleTokens` — `"claude sonnet"` → `"claude-sonnet"` substring.
- `TestMatches_NoMatch` — `"foo"` against `"gpt-5.5"` false.
- `TestMatches_SearchesAllFields` — matches Label / ID / Detail.

### `internal/ui/components/picker/picker_test.go`
- `TestPicker_NewHidden`.
- `TestPicker_OpenSyncPopulates`.
- `TestPicker_OpenAsyncShowsLoading`.
- `TestPicker_ItemsLoadedMsgPopulates`.
- `TestPicker_ItemsLoadedError`.
- `TestPicker_ArrowsMoveCursor`.
- `TestPicker_CtrlNPAliases`.
- `TestPicker_EnterEmitsSelectMsg`.
- `TestPicker_EscEmitsCloseMsg`.
- `TestPicker_FilterNarrowsList`.
- `TestPicker_FilterClampsCursor`.
- `TestPicker_BackspaceEditsFilter`.
- `TestPicker_CtrlUClearsFilter`.
- `TestPicker_SetActiveSeedsCursor`.
- `TestPicker_MarkerAppearsOnActive`.
- `TestPicker_EmptyFilteredShowsNoMatches`.
- `TestPicker_RetryOnErrorState`.
- `TestPicker_ResizeRecomputesWidth`.

### `internal/provider/registry_test.go` additions
- `TestRegistry_CacheRoundtrip`.
- `TestRegistry_CacheMiss`.
- `TestRegistry_CacheInvalidate`.
- `TestRegistry_CacheConcurrent`.

### `internal/app/app_picker_test.go`
- `TestApp_CtrlP_OpensProviderPicker`.
- `TestApp_CtrlP_PopulatedFromRegistry`.
- `TestApp_CtrlP_ActiveMarkedAndCursored`.
- `TestApp_CtrlP_SelectAppliesSwitch`.
- `TestApp_CtrlP_EscLeavesActiveUnchanged`.
- `TestApp_CtrlP_NoProvidersConfigured`.
- `TestApp_CtrlM_OpensModelPicker`.
- `TestApp_CtrlM_CachedListNoNetwork`.
- `TestApp_CtrlM_FreshLoadFromListModels`.
- `TestApp_CtrlM_SelectAppliesModelSwitch`.
- `TestApp_CtrlM_CachePopulatedAfterLoad`.
- `TestApp_CtrlM_ListError`.
- `TestApp_CtrlM_RetryFiresAgain`.
- `TestApp_CtrlP_RefusesToStackOverApproval`.
- `TestApp_CtrlM_RefusesToStackOverApproval`.
- `TestApp_CtrlP_OpensOverJobsPanel`.
- `TestApp_ApplyProviderSwitch_UsesCacheFirst`.
- `TestApp_ApplyProviderSwitch_Delegates`.
- `TestApp_ApplyModelSwitch_ErrorsWhenNoProvider`.
- `TestApp_SlashProviderStillWorksUnchanged` (regression).
- `TestApp_SlashModelStillWorksUnchanged` (regression).

## Out of scope (Round 3+)

- Slash-command autocomplete popup (Round 3) — will reuse `picker.matches`.
- Fuzzy matching.
- Cross-provider model switching from the model picker.
- Mouse support.
- Keyboard remap via theme.toml.
- Persisted model-list cache across restarts.
- Prefetching model lists on startup via `InitializeAll`.
- Double Ctrl+P / Ctrl+M to close (Esc is the only dismiss).
