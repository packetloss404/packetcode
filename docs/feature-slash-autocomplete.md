# Slash-Command Autocomplete — Round 3 Design Spec

## Summary

When the input buffer starts with `/` and the user has not yet typed a space after the verb, render a small borderless floating popup **above** the input bar listing the matching slash commands from `keymap.SlashCommands`. Typing narrows the list (two-tier prefix-first sort), arrow keys / Ctrl+N/P / Ctrl+J/K navigate, Tab always accepts the highlighted row, Enter accepts when the buffer is still a bare verb (otherwise submits normally), and Esc dismisses. The popup lives in a **new layout slot** between overlay and input — not in the overlay cascade — so it disappears whenever any modal (approval / picker / jobs) is up because the input is unreachable then. A new bespoke `autocomplete.Model` component handles presentation + cursor + filtering; the filter helpers (`Normalize`, `Matches`) come from `internal/ui/components/picker/filter.go` via direct import.

## User stories

1. **Discover commands.** Type `/` → popup opens with all 12 commands, first row highlighted. Arrow down + Tab to fill the input with `/cancel `.

2. **Fast-type.** Type `/sp` → only `/spawn` remains. Hit Enter (buffer is still a bare verb with a selection) → input becomes `/spawn `, popup closes.

3. **Past the verb.** Type `/spawn write a test` → popup closed once the space landed. Enter submits normally.

4. **Escape.** Type `/pro`, hit Esc. Popup closes, `/pro` stays in input.

5. **No-match fall-through.** Type `/xyz` (0 rows). Enter falls through to the input's submit path; `/xyz` goes to the LLM as a normal message since `ParseSlashCommand` returns `ok=false`.

## Component design

### `autocomplete.Model` (bespoke)

Lives at `internal/ui/components/autocomplete/autocomplete.go`. ~150 LOC. Does NOT reuse `picker.Model` (different geometry and lifecycle).

```go
type Entry struct {
    Verb  string  // "spawn" (no slash)
    Usage string  // "/spawn <prompt>" (display label from keymap.SlashCommands)
    Desc  string  // "Spawn a background agent"
}

type SelectMsg struct{ Verb string }

type Model struct {
    entries  []Entry
    filter   string
    filtered []int
    cursor   int
    visible  bool
    width    int
}

func New(entries []Entry) Model
func (m *Model) Open(filter string)
func (m *Model) Close()
func (m Model)  Visible() bool
func (m *Model) SetFilter(filter string)
func (m Model)  Filter() string
func (m Model)  Count() int
func (m Model)  SelectedVerb() string
func (m *Model) SetWidth(w int)
func (m Model)  Update(msg tea.Msg) (Model, tea.Cmd)
func (m Model)  View() string
```

### Filter — two-tier prefix-first sort

Inside `SetFilter`:

1. `needle := picker.Normalize(strings.TrimPrefix(filter, "/"))`.
2. For each entry, compute `verbNorm := picker.Normalize(e.Verb)` and `haystackNorm := picker.Normalize(e.Verb + " " + e.Desc)`.
3. Bucket: **tier 1** = `strings.HasPrefix(verbNorm, needle)`; **tier 2** = not tier 1 but `strings.Contains(haystackNorm, needle)`.
4. Sort each tier alphabetically by `Verb`. Concatenate. Reset `cursor = 0`.

Empty filter → all entries in original `keymap.SlashCommands` order, cursor 0.

### Update (navigation only)

```go
switch km.String() {
case "up", "ctrl+p", "ctrl+k":   if cursor > 0 { cursor-- }
case "down", "ctrl+n", "ctrl+j": if cursor < len(filtered)-1 { cursor++ }
}
```

Tab / Enter / Esc are handled at the App layer (they coordinate with the input buffer).

### View

- Frame: `lipgloss.RoundedBorder()` with `BorderForeground(theme.BaseBorder)` (dimmer than picker — autocomplete is a helper, not modal). `Padding(0, 1)`.
- Width: `clamp(40, w-4, 60)`.
- Height: border(2) + `min(Count(), 6)` rows + optional "+N more" footer when overflow.
- Row format: marker gutter (3 cols — `"▶ "` on cursor row, `"  "` else), Usage left-aligned in `min(22, innerW/2)` column, one space, Desc right-truncated.
- Usage: `theme.StyleAccent` (cyan). Desc: `theme.StyleSecondary`. Cursor row: `Background(theme.BaseSurfaceBright)`.
- Empty filtered list → `View() == ""` (popup silently disappears).

### Entry derivation

App-side helper `buildAutocompleteEntries()` reads `keymap.SlashCommands`:
- `Usage` = `.Key` verbatim (`"/spawn <prompt>"`).
- `Desc` = `.Desc` verbatim.
- `Verb` = first whitespace-delimited token of `Key`, with leading `/` stripped.

**Dedup by verb** (keep first): `SlashCommands` lists pairs such as `/agents` and `/agents <id>`, or `/jobs` and `/jobs <id>`; collapse each pair to the bare verb (`Usage = "/agents"` / `"/jobs"`). The exact entry count follows `internal/app/keymap.go`.

## App integration

### Struct

New field on `App`:

```go
autocomplete autocomplete.Model
```

Constructed in `New`:

```go
app.autocomplete = autocomplete.New(buildAutocompleteEntries())
```

### Opening / closing rules — `refreshAutocomplete()`

Called at the END of `handleKey` (after input processes its keystroke) and at the END of the input branch of the generic fallback in `Update`. Also called at the start of the `input.SubmitMsg` handler to close the popup on submit.

```go
func (a *App) refreshAutocomplete() {
    if a.approval.Visible() || a.picker.Visible() || a.jobsPanel.Visible() {
        a.autocomplete.Close()
        return
    }
    text := a.input.Value()
    if !strings.HasPrefix(text, "/") {
        a.autocomplete.Close()
        return
    }
    if strings.ContainsAny(text, " \t\n") {
        a.autocomplete.Close()
        return
    }
    filter := strings.TrimPrefix(text, "/")
    a.autocomplete.SetWidth(a.width)
    if a.autocomplete.Visible() {
        a.autocomplete.SetFilter(filter)
    } else {
        a.autocomplete.Open(filter)
    }
}
```

### Keybindings

In `handleKey`, inserted BEFORE the modal-visible guards:

```go
if a.autocomplete.Visible() {
    switch msg.String() {
    case "esc":
        a.autocomplete.Close()
        return a, nil
    case "tab":
        if verb := a.autocomplete.SelectedVerb(); verb != "" {
            a.acceptAutocomplete(verb)
        }
        return a, nil
    case "enter":
        verb := a.autocomplete.SelectedVerb()
        text := a.input.Value()
        bufferIsBareVerb := strings.HasPrefix(text, "/") &&
            !strings.ContainsAny(text, " \t\n")
        if verb != "" && bufferIsBareVerb {
            a.acceptAutocomplete(verb)
            return a, nil
        }
        // fall through to input's SubmitMsg path
    case "up", "down", "ctrl+n", "ctrl+p", "ctrl+k", "ctrl+j":
        var cmd tea.Cmd
        a.autocomplete, cmd = a.autocomplete.Update(msg)
        return a, cmd
    }
}
```

After the modal guards and input.Update, always call `a.refreshAutocomplete()`.

### `acceptAutocomplete`

```go
func (a *App) acceptAutocomplete(verb string) {
    a.input.SetValue("/" + verb + " ")
    a.autocomplete.Close()
}
```

Requires new method on `input.Model`:

```go
// SetValue replaces the textarea contents and moves the caret to the end.
func (m *Model) SetValue(s string) {
    m.ta.SetValue(s)
    m.ta.CursorEnd()
}
```

### Layout slot

Extend `layout.Frame`:

```go
// Frame stacks: body / [overlay] / [aboveInput] / input / status.
// aboveInput is for anchored-to-input widgets (autocomplete popup); it
// sits below overlay so a modal covers it but above input so it feels
// attached.
func Frame(body, overlay, aboveInput, input, status string) string
```

`App.View` computes `aboveInput` from the popup and includes its height when sizing the body viewport.

### Modal precedence

Approval > Picker > JobsPanel > Autocomplete. Autocomplete is NOT in the overlay cascade — it's in a separate layout slot — and `refreshAutocomplete` closes it whenever any modal is visible.

## File-by-file change list

### Bucket A — Implementation

| Path | Changes |
|---|---|
| `internal/ui/components/autocomplete/autocomplete.go` | **NEW.** Full component (~150 LOC). |
| `internal/ui/components/picker/filter.go` | Add exported `Normalize(string) string` + `Matches(Item, string) bool` wrappers over existing lowercase helpers. |
| `internal/ui/components/input/input.go` | Add `SetValue(s string)` that delegates to `ta.SetValue` + `ta.CursorEnd`. |
| `internal/ui/layout/layout.go` | Extend `Frame` signature to include `aboveInput` slot. |
| `internal/app/autocomplete_entries.go` | **NEW.** `buildAutocompleteEntries()` from `SlashCommands`, dedup by verb. |
| `internal/app/app.go` | Add `autocomplete` field, `refreshAutocomplete`, `acceptAutocomplete`, key handling in `handleKey`, `Update` routing, `View` rendering with the new `aboveInput` slot, `resize` calls `SetWidth`. Also close popup at start of `input.SubmitMsg` handler. |
| `internal/app/keymap.go` | Add `AutocompleteKeys` slice. Extend `GlobalKeys.Esc` description. |
| `internal/app/slashcmd_help.go` | Render `AutocompleteKeys` section between Input and Picker. |

### Bucket B — Tests + docs + commit

| Path | Changes |
|---|---|
| `internal/ui/components/autocomplete/autocomplete_test.go` | **NEW.** ~20 component tests (enumerated below). |
| `internal/ui/components/picker/filter_test.go` | Smoke tests for exported wrappers. |
| `internal/ui/layout/layout_test.go` | Update existing to 5-arg signature; add aboveInput-slot tests. |
| `internal/ui/components/input/input_test.go` | **NEW.** `TestInput_SetValueReplacesBufferAndMovesCursorToEnd`. |
| `internal/app/app_autocomplete_test.go` | **NEW.** ~20 integration tests. |
| `internal/app/slashcmd_test.go` | Add `TestParseSlashCommand_TrailingSpaceAfterVerb`. |
| `README.md` | Autocomplete subsection under Keyboard. |
| `CHANGELOG.md` | Added bullet; remove from Deferred. |
| `docs/roadmap-deferred.md` | Mark Round 3 landed. |

## Tests

### `autocomplete_test.go`
- `TestAutocomplete_NewHidden`, `_OpenEmptyFilterShowsAllEntries`, `_OpenWithFilterNarrows`, `_PrefixMatchesBeatSubstring`, `_AlphabeticalWithinTier`, `_NoMatchesHidesView`, `_CloseHides`, `_SetFilterResetsCursor`, `_UpDownCursor`, `_CtrlNCtrlP`, `_CtrlJCtrlK`, `_UpdateIgnoredWhenHidden`, `_UpdateIgnoresNonKeyMsgs`, `_UpdateDoesNotHandleTabEnterEsc`, `_SelectedVerbEmptyWhenNoMatches`, `_SelectedVerbMatchesCursor`, `_ViewContainsUsageAndDesc`, `_ViewShowsCursorRowHighlight`, `_ViewMoreFooterWhenOverflow`, `_WidthClamp`, `_SetFilterStripsLeadingSlash`.

### `picker/filter_test.go` additions
- `TestNormalize_Exported`, `TestMatches_Exported`.

### `layout_test.go` updates
- Update existing to 5-arg sig.
- `TestFrame_AboveInputBetweenOverlayAndInput`, `TestFrame_OmitsAboveInputWhenEmpty`.

### `input_test.go` (new)
- `TestInput_SetValueReplacesBufferAndMovesCursorToEnd`.

### `app_autocomplete_test.go`
- `TestApp_Autocomplete_OpensOnSlash`, `_NarrowsAsUserTypes`, `_ClosesOnSpaceAfterVerb`, `_ClosesOnBackspacePastSlash`, `_StaysClosedOnNonSlashInput`, `_EmptyFilterShowsAllEntriesInOrder`, `_TabAccepts`, `_TabWithNoMatchesIsNoOp`, `_EnterAcceptsWhenBufferIsBareVerb`, `_EnterSubmitsWhenBufferHasArgs`, `_EnterFallsThroughOnNoMatches`, `_EscClosesPopupButKeepsText`, `_ArrowsMoveCursor`, `_CtrlNPNavigate`, `_ClosesWhenApprovalOpens`, `_ClosesWhenPickerOpens`, `_ReopensAfterPickerCloses`, `_TabDoesNotInsertTabCharacter`, `_TabFallsThroughToInputWhenHidden`, `_SlashHelpStillFires`, `_SelectedVerbAfterFilterDoesNotMatch`, `_PopupDoesNotRenderWhenHidden`, `_EntriesDedupedFromKeymap`.

### `slashcmd_test.go`
- `TestParseSlashCommand_TrailingSpaceAfterVerb` — `ParseSlashCommand("/spawn ")` returns `("spawn", []{}, true)`.

## Out of scope (Round 4+)

- Mouse support.
- Fuzzy matching (prefix + substring is enough).
- Caret-aware mid-buffer completion.
- Flag autocomplete (`/spawn --<tab>` → `--provider`).
- Subcommand autocomplete (`/sessions resume`).
- Tab-cycles-on-repeated-tab.
- MRU floating to top.
- PgUp/PgDn/Home/End in the popup.
- Keyboard remap via theme.toml.
