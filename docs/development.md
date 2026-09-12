# Developing Packetcode

Updated 2026-09-11. Start with [the maintainer handoff](../HANDOFF.md),
[the changelog](../CHANGELOG.md), and `git status --short --branch`. The
[backlog](../BACKLOG.md) records remaining work; historical audits are evidence,
not instructions to reapply old patches.

## Code and regression map

| Area | Implementation | Focused checks |
| --- | --- | --- |
| Foreground turns and queue | `internal/app/app.go`, `slashcmd_queue.go`, `slashcmd_compact.go`, `slashcmd_loop.go` | `turn_recovery_test.go`, `app_cancel_test.go`, `slashcmd_loop_test.go` |
| Approval display and remembered scope | `internal/ui/components/approval`, `internal/app/approval_remember.go` | `clarity_test.go`, `approval_remember_test.go`, `approval_queue_test.go` |
| Temporary skill authority | `internal/app/skillgrant.go` | `skillgrant_lifecycle_test.go` |
| Background recovery and persistence | `internal/jobs/manager.go`, `resubmit.go`, `internal/app/job_recovery.go` | `resubmit_recovery_test.go`, `persistence_failure_test.go`, `recovery_guidance_test.go` |
| MCP transport and reconnection | `internal/mcp`, `internal/app/slashcmd_mcp.go` | `manager_lifecycle_test.go`, `write_cancellation_test.go`, `slashcmd_mcp_test.go` |
| Headless runtime and failures | `cmd/packetcode/runtime.go`, `run_command.go` | `run_recovery_test.go`, `run_integration_test.go` |

Test filenames are relative to the owning package. See
[maintenance](maintenance.md) for package commands and recovery procedures.

## Preserve these contracts

- The Bubble Tea update loop owns foreground state. Drain a cancelled turn
  before starting another. Record cancellation immediately, since provider
  success may already be buffered.
- A failed turn or compaction retains and pauses pending prompts. New prompts
  join that queue until explicit `/queue resume`, or `/queue clear` discards
  them. Display clearing and session switching do not resume the queue.
- Claim loop ownership only when the agent turn actually begins, after the
  compaction guard. Ordinary turns clear old ownership; stopping a loop removes
  its queued iterations without dropping unrelated prompts.
- Approval rendering must describe the actual rule: exact command text across
  working directories, or all arguments of one tool, for this session. Escape
  decoded display text without changing the approved arguments or preview input.
- Keep session policy separate from temporary skill grants. Running background
  jobs keep their captured policy; foreground changes do not broaden it.
- Persist a new job before publishing or launching it. Keep failed snapshots
  pending, and report unresolved saves at shutdown. Reserve a recovered job
  during resubmission so concurrent requests cannot launch two successors;
  release the reservation on validation or storage failure.
- Reconnection and session recovery must not repeat tool actions automatically.
  Preserve terminal records, worktrees, and partial JSON failure output. Plain
  headless stdout contains only successful final output; diagnostics use stderr.
- Construct MCP pipe wrappers before reader/reaper goroutines start. Do not
  replace live transport fields in test fixtures.

## Validate a change

Reproduce the failure in the owning package. Add a regression that exercises
observable behavior, then run the relevant package before the integrated checks:

```text
go mod verify
go vet ./...
go test ./...
go test -race -count=1 ./...
golangci-lint run ./...
```

Run these sequentially. For asynchronous tests use `internal/testwait`; prove
concurrency with barriers instead of elapsed-time ceilings. A timeout needs
investigation, not a removed assertion. The module floor is Go 1.26.0; CI pins
are defined in `.github/workflows/ci.yml` and `.github/workflows/release.yml`.

For TUI changes, follow [the PTY harness](tui-parity-harness.md): inspect both
72×24 and 100×30 captures, update intentional text/style changes, and run
`make tui-golden-check`. Use Linux, macOS, or WSL with
`scripts/requirements-tui.txt`. Commit reviewed goldens, not raw ANSI captures,
credentials, local state, or generated binaries.

## Keep documentation aligned

Update the focused `docs/feature-*.md` contract and user reference first, then
the README, changelog, and handoff when behavior or maintenance priorities change.
Keep `docs/manual.md`, `docs/advanced-guide.md`, `docs/cheat-sheet.md`, and
`docs/packetcode-manual.html` consistent with command help. Preserve dated audit
findings as history and add current status above them. For documentation-only
changes, check relative links and command syntax against the source; runtime
tests are unnecessary unless code or fixtures also change.
