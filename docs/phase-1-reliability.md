# Phase 1 — Agent Reliability

Phase 1 targets the gap between "the feature set is complete" and "the
agent is dependable on real, long-running, rate-limited work." Every
round is small, self-contained, and independently shippable, following
the round pattern in [`roadmap-deferred.md`](roadmap-deferred.md).

These items map to **v1 Stabilization** and **Agent and TUI Quality** in
[`../BACKLOG.md`](../BACKLOG.md). The larger Packet Computers / Packet
Control work is Phase 2 — it should sit on a dependable single-machine
agent, not race it.

Rounds are ordered by ROI-per-risk: provider resilience first (cheapest,
highest impact), the UI-touching streaming change last.

---

## Round 1 — Retry/backoff on transient provider errors

**Problem.** No provider adapter retries. A single `429`, `503`, or
dropped connection fails the whole turn. This is the most common
real-world failure and the highest-value fix.

**Design.** Add a shared retry helper in `internal/provider`
(`retry.go`) that wraps the *initial* request dispatch only — before the
SSE body is consumed (a mid-stream failure is not safely replayable).

- Classify retryable: HTTP `429`, `500`, `502`, `503`, `504`, and
  connection/`io` errors before first byte. Non-retryable: `4xx` other
  than 429, and any error after the stream has started emitting events.
- Exponential backoff with jitter; honor `Retry-After` when present.
- Bounded: default 3 attempts, configurable via `[behavior]`
  (`provider_max_retries`).
- Respect `ctx` — a cancelled turn aborts the retry loop immediately.

**Files.**
- New `internal/provider/retry.go` + `retry_test.go`.
- Wire into the request dispatch in `anthropic/client.go`,
  `openaicompat/client.go` (covers openai/minimax/openrouter/custom),
  `gemini/client.go`, `ollama/client.go`.
- `internal/config`: add `provider_max_retries` (default 3).

**Acceptance.** A stubbed server returning `429` then `200` produces one
successful turn with no user-visible error; a persistent `503` fails
after N attempts with a clear message; Ctrl+C during backoff cancels.

---

## Round 2 — Per-call stall timeout

**Problem.** Streaming relies only on the overall `ctx` deadline. A
provider that accepts the connection then stalls mid-stream hangs the
turn with no recovery.

**Design.** Each adapter has its own SSE read loop today (`parseSSE` in
`anthropic/anthropic.go`, `parseGeminiSSE` in `gemini/gemini.go`,
`parseOllamaStream` in `ollama/ollama.go`, and the openaicompat reader).
Add a shared stall-timeout helper (new `internal/provider/stream.go`)
that wraps the per-event read: reset a timer on each received event; if
no event arrives within the window, surface a retryable timeout error
(feeding Round 1's classifier when the stall is before first byte).
Distinct from `http.Client.Timeout` — note `openaicompat` deliberately
sets `Timeout: 0` to "rely on context", so a stall timeout must not
reintroduce a hard overall deadline.

**Files.**
- New `internal/provider/stream.go` + test; call it from each adapter's
  parse loop (`anthropic`, `gemini`, `ollama`, `openaicompat`).
- `internal/config`: `provider_stall_timeout` (default 60s).

**Acceptance.** A server that opens then goes silent triggers the stall
timeout within the window; a slow-but-steady stream is unaffected.

---

## Round 3 — `patch_file` whitespace-tolerant matching

**Status: Landed.** `patch_file` now falls back to a whitespace/line-ending
tolerant unique match when the exact match misses, still erroring on ambiguity.

**Problem.** `patch_file` requires an exact, unique string match
(`tools/patch_file.go`). A single trailing-space or CRLF/LF difference
fails the edit, which is a frequent agent failure mode.

**Design.** Keep exact match as the primary path. On a zero-count exact
match, fall back to a normalized match (collapse trailing whitespace,
normalize line endings) — but still require the normalized match to be
**unique**; ambiguity is an error, never a guess. Report in the result
which path matched so behavior stays auditable.

**Files.**
- `internal/tools/patch_file.go` + `patch_file_test.go`.

**Acceptance.** A patch whose `search` differs only by trailing
whitespace / line endings applies cleanly; a normalized match that hits
2+ sites still errors; exact-match behavior is unchanged when it works.

---

## Round 4 — `execute_command` output streaming

**Status: Landed.** `execute_command` now streams stdout/stderr to the
conversation incrementally as the process runs, while preserving the 100KB
bounded cap on the final result and the Ctrl+C / ctx cancellation path.

**Problem.** `execute_command` buffers up to 100KB and returns only on
exit (`tools/execute_command.go`). Long-running commands (builds, test
suites) show nothing until completion — poor feedback and hard to tell
a slow command from a hung one. This is the largest round (touches the
tool + the conversation UI).

**Design.** Stream stdout/stderr incrementally to the conversation pane
as the process runs, preserving the existing bounded-buffer cap for the
final tool result and the existing cancellation path
(`internal/procrun`). Throttle UI updates to avoid flooding the
renderer.

**Files.**
- `internal/tools/execute_command.go`, `internal/procrun`.
- `internal/ui/components/conversation` (incremental output block).
- `internal/agent` if the event channel needs a streaming tool-output
  event type.

**Acceptance.** `go test ./...` shows package results as they complete,
not all at once; the bounded cap and Ctrl+C cancellation still hold; no
visible render lag under high-output commands.

---

## Out of scope for Phase 1

Tracked in [`../BACKLOG.md`](../BACKLOG.md), deferred to keep this slice
tight: resumable jobs across restart, real-time sub-agent output
streaming, session resume picker UI, inline image rendering, and all
Packet Computers / Packet Control work.
