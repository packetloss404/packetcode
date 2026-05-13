# Real HTTP Cancellation on Ctrl+C — Round 5 Design Spec

> Historical design spec. This feature has landed; the "pre-round"
> section below describes the behavior before the cancellation work.

## Summary

Wire a real, lifecycle-scoped `context.Context` through `App.startTurn` so one Ctrl+C during streaming cleanly terminates the in-flight provider HTTP request, kills any running tool, and dismisses any pending approval prompt. Before this landed, Ctrl+C only stopped the spinner — the HTTP stream kept running until the provider closed the connection, so the user could still be billed for tokens they rejected. The visible UX delta: a friendlier `turn cancelled` system line in the conversation in place of an alarming `error: ...`.

## Pre-round behaviour

- `App.startTurn` builds `ctx := context.Background()` — never cancellable.
- `handleKey` `ctrl+c` only stops the spinner and sets `a.streaming = false`. No ctx cancel.
- Result: HTTP request keeps running, model keeps generating (billable), tool commands keep executing (60s default), approval modal sits visible.

## Target behaviour

- **First Ctrl+C while streaming** → cancel HTTP request, kill running tool, unblock pending approval (hide modal). Spinner stops. Conversation gets a dim `turn cancelled` line. App stays open.
- **Second Ctrl+C while idle** → quit (unchanged).
- Background `/spawn`'d jobs are NOT cascaded — they have their own ctx trees.

## Design

### `App.cancelTurn` lifecycle

New field on `App`:
```go
cancelTurn context.CancelFunc
```

Transitions (all from Update goroutine — single-writer, no mutex):

| Transition | Where | Action |
|---|---|---|
| Set | `startTurn` | `ctx, cancel := context.WithCancel(context.Background()); a.cancelTurn = cancel` |
| Clear (normal exit) | `agentDoneMsg` handler | `if a.cancelTurn != nil { a.cancelTurn(); a.cancelTurn = nil }` |
| Clear (error exit) | `EventError` case | `if a.cancelTurn != nil { a.cancelTurn(); a.cancelTurn = nil }` |
| Cancel (user) | `handleKey` `ctrl+c` while `a.streaming` | cancel + clear + `a.approval.Hide()` |

Deliberately **do not** clear in `EventDone` — that's the agent's "no more tool calls" signal, not the end of turn. `agentDoneMsg` (from channel close) is the canonical end.

### Ctrl+C handler replacement

```go
case "ctrl+c":
    if a.streaming {
        if a.cancelTurn != nil {
            a.cancelTurn()
            a.cancelTurn = nil
        }
        a.spinner.Stop()
        if a.approval.Visible() {
            a.approval.Hide()
        }
        // Do NOT clear a.streaming here — let agentDoneMsg clear it
        // once the goroutine has actually drained. Keeping it true
        // briefly means a second Ctrl+C is treated as "cancel no-op"
        // not "quit", giving us safe double-Ctrl+C during shutdown.
        return a, nil
    }
    return a, tea.Quit
```

State-machine property: first Ctrl+C cancels (clears `cancelTurn`); second Ctrl+C during shutdown is a no-op (`cancelTurn == nil`, `streaming == true`); third Ctrl+C after `agentDoneMsg` quits. No debouncing or timestamps needed.

### Cancellation UX in the conversation

Add private helper:

```go
func isCancellation(err error) bool {
    return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
```

Modify `handleAgentEvent` `EventError` branch:

```go
case agent.EventError:
    if isCancellation(ev.Error) {
        a.conversation.AppendSystem("turn cancelled")
    } else {
        a.conversation.AppendSystem("error: " + ev.Error.Error())
    }
    if a.cancelTurn != nil {
        a.cancelTurn()
        a.cancelTurn = nil
    }
```

Literal text: `"turn cancelled"` (lowercase, matches existing system-line register).

Styling reused from `theme.StyleSystemMessage` — the friendlier wording carries the meaning. No new theme token introduced.

#### Provider error wrapping

Agent wraps errors with `%w` (`fmt.Errorf("chat completion: %w", err)`), so `errors.Is(context.Canceled)` walks the chain. Implementer must verify no `%v` creeps into the cancellation path. Tool execution errors use `Sprintf("...%s", err)` which strips wrapping — acceptable because those flow through `EventToolCallExecuted` not `EventError`.

### Approval modal cleanup

Existing `uiApprover.Approve` already selects on `ctx.Done()`. Calling `a.cancelTurn()` unblocks it with `{Approved: false, Reason: "cancelled"}`. We add synchronous `a.approval.Hide()` to clean up the UI state; the next View renders without the modal.

### Background-job isolation

`jobs.Manager.Spawn` derives per-job ctx from a manager-level root, NOT from `agent.Run`'s ctx. Main-turn Ctrl+C does NOT cascade to spawned jobs. Users cancel those via `/cancel <id>` or `/cancel all`.

Implementer must verify by reading `internal/jobs/manager.go` Spawn call chain.

### Race conditions

- `agentDoneMsg` first, then Ctrl+C → falls into idle branch → quits. Correct.
- Ctrl+C first, `agentDoneMsg` arrives μs later → cancel + normal drain. Possible `turn cancelled` + nothing-extra. Acceptable.
- Double Ctrl+C during shutdown → first cancels, second is no-op (guard clauses), third (after done) quits. No debouncing needed — state machine handles it.

## Provider audit

All HTTP-backed providers use `http.NewRequestWithContext`. Body close on ctx cancel causes `scanner.Scan()` to return false; parser exits; `defer close(ch)` fires; agent's `for ev := range stream` exits.

### Defensive ctx.Err() guard

Add ONE `ctx.Err()` check per parser, at the top of the for loop, to bound cancellation latency regardless of provider TCP behaviour. Plumb ctx into parsers:

- `parseSSE(ctx context.Context, body io.ReadCloser, ch chan<- provider.StreamEvent)`
- `parseGeminiSSE(ctx context.Context, body io.ReadCloser, ch chan<- provider.StreamEvent)`
- `parseOllamaStream(ctx context.Context, body io.ReadCloser, ch chan<- provider.StreamEvent)`

Inside each, immediately after `for scanner.Scan() {`:

```go
if err := ctx.Err(); err != nil {
    ch <- provider.StreamEvent{Type: provider.EventError, Error: err}
    return
}
```

Emits `EventError` with the cancellation cause so the agent's path fires (instead of silent exit). Flows to App → `isCancellation` → `turn cancelled` rendering.

### Explicitly NOT doing

- No `select { case <-ctx.Done(): ... default: }` around `scanner.Scan()` — over-engineered.
- No per-chunk timeout — streaming can legitimately pause.
- No `Connection: close` headers — default behaviour is fine.

## File-by-file change list

### Bucket A — Implementation

| Path | Change |
|---|---|
| `internal/app/app.go` | Add `cancelTurn context.CancelFunc` field. Add `errors` import. Add `isCancellation(err error) bool`. In `startTurn`: `ctx, cancel := context.WithCancel(context.Background()); a.cancelTurn = cancel; stream := a.agent.Run(ctx, text)`. Modify `ctrl+c` case per spec (cancel, clear, hide approval, keep streaming=true). Modify `agent.EventError` in `handleAgentEvent` to use `isCancellation` and clear `cancelTurn`. Modify `agentDoneMsg` to clear `cancelTurn`. |
| `internal/provider/openaicompat/client.go` | Pass `ctx` into `parseSSE`. Per-iteration `ctx.Err()` guard at top of scanner loop, emitting `EventError` and returning on cancel. Update call site in `ChatCompletion`. |
| `internal/provider/gemini/gemini.go` | Same pattern — `parseGeminiSSE` gains ctx, adds guard, call site updated. |
| `internal/provider/ollama/ollama.go` | Same — `parseOllamaStream` gains ctx, guard, call site updated. |

OpenAI / MiniMax / OpenRouter wrapper packages: no changes (they delegate to `openaicompat.Client`).

### Bucket B — Tests

| Path | Change |
|---|---|
| `internal/provider/openaicompat/client_test.go` | **NEW.** `TestParseSSE_CancelStopsStream` — slow httptest server, ctx cancel after 100ms, assert channel closes <1s with `EventError(context.Canceled)`. |
| `internal/provider/gemini/gemini_test.go` | Add `TestGemini_ChatCompletion_CancellationStopsStream`. |
| `internal/provider/ollama/ollama_test.go` | Add `TestOllama_ChatCompletion_CancellationStopsStream`. |
| `internal/agent/agent_test.go` | Add `TestAgent_Run_CancelDuringChatCompletion` — mock provider hangs, cancel ctx, events channel closes within 200ms. |
| `internal/agent/agent_test.go` | Add `TestAgent_Run_CancelDuringApproval`. |
| `internal/tools/execute_command_test.go` | Add `TestExecuteCommand_ContextCancelKillsProcess` — `sleep 30` killed within 1s of cancel (Unix only; Windows skipped). |
| `internal/app/app_cancel_test.go` | **NEW.** Integration tests: `TestApp_CtrlC_DuringStream_CancelsTurn`, `TestApp_CtrlC_WhenIdle_Quits`, `TestApp_CtrlC_HidesApprovalModal`, `TestApp_CtrlC_RendersTurnCancelledLine`, `TestApp_DoubleCtrlC_DuringShutdown_DoesNotQuit`. |

### Bucket C — Docs

| Path | Change |
|---|---|
| `README.md` | Remove `Streaming-generation HTTP cancellation on Ctrl+C` line from the Next / Deferred list. |
| `CHANGELOG.md` | Added bullet under `[Unreleased]` describing cancellation; remove from Deferred. |
| `docs/roadmap-deferred.md` | Mark Round 5 as **Landed**. |
| `docs/feature-http-cancellation.md` | This spec (already persisted). |

## Tests (consolidated)

Provider-level: 3 tests (one per parser family).
Agent-level: 2 tests.
Tool-level: 1 test.
App-level: 5 tests.
Total: ~11 new tests.

## Out of scope (Round 6+)

- No status-bar `[cancelled]` flash.
- No new theme token for cancelled vs error system lines.
- No request-id telemetry.
- No partial-token striking of already-rendered deltas.
- No per-tool cancellation hotkey.
- No explicit `select` around `scanner.Scan()` in parsers (body-close + ctx.Err() guard is enough).
- No cascade to background `/spawn` jobs.
- No double-press debouncing (state machine handles it).
- No per-provider keep-alive tuning (defer until a real incident).
