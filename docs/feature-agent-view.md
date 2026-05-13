# Agent View

Agent View is the foreground dashboard for background agents. It is inspired by Claude Code's Agent View, but scoped to packetcode's existing job model: background agents remain `internal/jobs` jobs, and `/agents` is the interactive surface for inspecting and acting on them.

## User Surface

| Command | Behavior |
|---|---|
| `/agents` | Opens the grouped Agent View dashboard. |
| `/agents <id>` | Opens the full transcript for one background agent. |
| `/spawn <prompt>` | Starts a read-only background agent. |
| `/spawn --write <prompt>` | Starts a background agent that may request approval for writes, patches, and commands. |
| `/cancel <id\|all>` | Cancels one background agent or every active one. |

Agent View groups jobs by state, keeps the current selection stable as live updates arrive, and shows per-agent telemetry: provider/model, age, input/output tokens, estimated cost, and a compact status badge such as `approval`, `ready`, `seen`, or `injected`. The final column shows the most recent useful activity: prompt, assistant text, tool activity, approval wait, summary, or error. Keyboard controls are local to the dashboard:

| Key | Action |
|---|---|
| `Up` / `Down`, `k` / `j` | Move selection. |
| `p` | Peek at the selected result or current summary in the conversation. |
| `Enter` / `o` | Open the selected agent transcript. |
| `c` | Request cancellation. |
| `i` | Inject a terminal result into the next foreground turn. |
| `Esc` / `q` | Close Agent View. |

## Result Lifecycle

Completed background results are no longer silently injected into the next foreground turn. Terminal updates mark results as `seen`, which keeps them available in Agent View. The user explicitly decides whether to inject with `i`; injected results are appended to the foreground session as a user-role message:

```text
[Background job <id> result]
<summary>
```

This keeps the foreground model context truthful and avoids surprise context changes after long-running background work.

`DrainResults` is retained for older callers and marks drained results as `injected`. New UI flows should use `PendingResults`, `MarkResultSeen`, `MarkResultIgnored`, and `MarkResultInjected`.

## Live State

Job snapshots include monotonic `Seq` and `UpdatedAt` fields so the TUI can ignore stale asynchronous updates. Snapshots also carry `LastActivity`, `LastMessage`, `NeedsInput`, `NeedsApproval`, `AllowWrite`, and `ResultStatus`. Running job transcripts are read from the live sub-session when available, then from the persisted job session after completion.

## Current Scope

Implemented in this v1:

- `/agents` dashboard over existing jobs.
- Live job activity and needs-approval state.
- Full transcript open for selected agents.
- Peek, cancel, and explicit result injection actions.
- Persisted snapshot metadata and result status.

Deferred:

- Standalone `packetcode agents` CLI command.
- Worktree-per-agent isolation.
- Pinning, renaming, and grouping agents.
- Supervisor agents and DAG scheduling.
- Pull request status dots.
