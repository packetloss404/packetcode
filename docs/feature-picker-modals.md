# Provider and Model Pickers

Provider and model selection use the shared `picker` component.

## Open and Use

- `Ctrl+P`, `/provider`, or `/providers`: provider picker.
- `/model` or `/models`: active-provider model picker. `Alt+M` also works when the terminal reports Alt distinctly. `Ctrl+M` is intentionally unbound under Bubble Tea v1 because it aliases Enter.
- Type to filter; matching is case-insensitive and normalized.
- Up/Down, Ctrl+N/P, and Ctrl+J/K move.
- PageUp/PageDown move by half a page.
- Enter selects; Esc closes; Ctrl+U clears the filter.
- In the provider picker, Ctrl+A opens API-key entry for the focused provider.
- In the model picker, Ctrl+R refreshes the catalog immediately.

Selecting a provider persists its default model. Model discovery runs asynchronously and shows loading, empty, and error/retry states without blocking the Bubble Tea update loop. Providers with incomplete/unavailable model-list endpoints may return curated fallback models.

The model catalog refreshes every time the model picker opens and once a minute after each completed request while it remains open. Cached choices stay visible and selectable during refresh, with a `refreshing…` indicator. Updates preserve the search query and focused choice. If refresh fails, the picker labels the cached list, offers Ctrl+R to retry, and retries automatically at the next interval. Closing or selecting cancels an in-flight request and stops periodic refresh; older replies cannot replace a newer picker's catalog.

API providers query their model-list endpoints. Codex re-reads the Codex CLI's `models_cache.json` on each refresh, so changes to that file are picked up without restarting Packetcode; it uses a bundled catalog if the CLI cache is unavailable. Packetcode does not update the Codex CLI cache itself.

Keyless providers (`codex` and `ollama`) never open an API-key prompt. Custom OpenAI-compatible providers participate through the same registry.

Names and model IDs use a full row, with connection details or model context, tool support, and pricing underneath. The active choice is labeled `current`; providers needing setup show `key needed`. The picker opens focused on the current choice, including after asynchronous model discovery. Search results show match counts, keep the panel height steady, and retain the end of long queries. Short terminals use compact rows, and long lists show the visible range.

Credential-free previews are available with `packetcode --tui-fixture=provider-picker` and `packetcode --tui-fixture=model-picker`.

## Focus Rules

The picker owns keyboard input while visible and sits above the transcript/input. Approval has higher urgency and cannot be stacked with a new picker; provider/model switching is rejected while a turn is active.

Implementation: `internal/ui/components/picker`, `internal/app/provider_switch.go`, and App picker handlers/tests.
