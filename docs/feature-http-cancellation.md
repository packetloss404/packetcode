# Foreground Cancellation

Ctrl+C cancellation is lifecycle-scoped and propagates through the active foreground turn.

## Behavior

- First Ctrl+C during a turn cancels the provider request, retry/backoff wait, pending approval, or running tool process tree.
- The thinking spinner stops and the conversation shows `turn cancelled`, not a provider-error block.
- Queued foreground prompts are cleared so cancellation is predictable.
- Cancellation intent is recorded before draining buffered events, so a
  buffered success cannot restart a self-paced loop.
- A second Ctrl+C while the agent goroutine is still draining is ignored rather than exiting.
- Ctrl+C while idle clears a non-empty draft first; from an empty prompt it exits packetcode.
- Background agents and workflows have independent contexts; foreground cancellation does not cascade to them.

Provider stream parsers receive the turn context and close promptly on cancellation. `execute_command` uses process-tree cancellation so children do not continue silently after the UI settles.

## State Ownership

`App.startTurnResolved` creates the cancellable context only after the
auto-compaction guard. It sets loop ownership from the turn options, clearing
old ownership for ordinary prompts. The Bubble Tea update loop owns the cancel
function and streaming flag. Channel close (`agentDoneMsg`) is the canonical
turn boundary; it clears the operation, spinner, and cancellation handle.

On ordinary success the next queued prompt may start. On failure, pending
prompts are retained and paused; compaction/provider/save failures follow the
same rule. New prompts join the paused queue until `/queue resume` while idle
or `/queue clear`. This differs from explicit Ctrl+C, which clears pending
prompts. Display clearing and session switching do not resume paused work.

Regressions live in `internal/app/turn_recovery_test.go`, `app_cancel_test.go`,
and `slashcmd_loop_test.go`. Use [the developer guide](development.md) when
changing this state machine.

## Related Controls

- `/cancel <id|all>` cancels background jobs.
- `/workflows stop <id|all>` cascades through workflow children.
- `/loop stop <id|all>` stops future iterations and removes already queued
  iterations belonging to that loop, including skill invocations. It does not
  forcibly terminate unrelated foreground work.
