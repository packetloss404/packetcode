# Testing and coverage limits

The advertised product is a terminal coding agent. `docs/packetcode-manual.html`
is its offline browser manual; this repository does not deploy a web application.
Its distribution target is GitHub Releases, built by `.github/workflows/release.yml`.

## Reproduce the checks

Run process-heavy Go checks sequentially. CI pins its toolchain in the workflow;
the module minimum is Go 1.26.0.

Use Go 1.26.10 or newer for validation and release builds. The vulnerability
scan found GO-2026-6809 in 1.26.9; the fixed patch is 1.26.10. The language
minimum is not a recommendation to build with an unpatched toolchain.

```text
go mod verify
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
```

`make smoke-e2e` drives the actual binary and a local stub provider with an
isolated home. CI runs it on Windows, macOS, and Linux. The Go suite also builds
and drives the CLI as a subprocess, so command dispatch, exit codes, stdout,
stderr, configuration, and saved state participate in the assertions.

For the HTML manual, install Node.js 20 or newer (CI uses Node.js 24), then run:

```text
npm ci
npx playwright install chromium
npm run test:browser
```

Linux CI uses `npx playwright install --with-deps chromium`. The lockfile pins
the test runner and its matching Chromium revision. Browser failures retain a
trace and report under `test-results/` and `playwright-report/`. Browser tests
open the real manual and exercise its section/local-file links, layout, offline behavior, Chromium
print CSS/PDF output, and print-mode reset. Native print dialogs remain a manual
check. These browser tests do not exercise the terminal agent.

The [POSIX PTY gate](tui-parity-harness.md) runs `make tui-golden-check` with
Python and `scripts/requirements-tui.txt`. It checks actual terminal output,
resize, styled cells, and terminal protocol safety. Native Windows/ConPTY
interaction remains a manual check.

## Advertised feature map

- **CLI and headless automation:** `cmd/packetcode` tests cover public command
  help, argument validation, JSON output, approvals, provider selection,
  session persistence/resume, error recovery, diagnostics, skills, ACP, and
  Sugar sign-in. Subprocess tests use local deterministic services rather than
  paid provider credentials.
- **Terminal interaction:** `internal/app` and `internal/ui` cover command
  dispatch, pickers, multiline input, completion, approvals, queue recovery,
  permissions, sessions, background views, loops, and rendering. The PTY suite
  separately checks terminal output and resize.
- **Tools and workspace boundaries:** `internal/tools`, `internal/computers`,
  `internal/permissions`, and `internal/session` cover reads, edits, backups,
  shell execution, root confinement, secret-file refusal, fetch limits, plans,
  and permission enforcement. Tests use temporary workspaces and local services.
- **Background agents and workflows:** `internal/jobs`, `internal/workflow`,
  and app integration tests cover spawning, concurrency limits, isolated Git
  worktrees, persistence, abandoned-job recovery, cancellation, result
  injection, verifiers, retry limits, and budgets.
- **Skills, MCP, hooks, and statusline:** their owning packages and app tests
  cover discovery, trust/temporary authority, local skill installation,
  subprocess protocol lifecycles, reconnect/cancellation, hook failures,
  custom statusline input, and theme loading.
- **Providers and usage:** `internal/provider/...`, `internal/agent`, and
  `internal/cost` cover protocol encoding, streaming completion/failure,
  authentication/configuration contracts, context accounting, loop detection,
  cache usage, and persisted cost attribution. Fixtures establish client
  behavior against controlled responses, not each upstream service's health.
- **Distribution:** CI checks six binary targets and a GoReleaser snapshot.
  Installer tests verify valid/invalid/missing signature branches. Tagged
  releases repeat platform preflight, PTY and browser checks before publication.

These groups make the feature claims traceable. Passing them is not a claim
that every possible feature combination or external environment was tested.

## External end-to-end checks

The following require their actual service, client, hardware, or credentials:

- Every hosted provider's current catalog, account entitlement, rate limits,
  billing, and live tool execution. `CODEX_LIVE=1 go test
  ./internal/provider/codex -run TestLive -v` is an opt-in live test using the
  local Codex login; ordinary tests skip it.
- A real Ollama daemon and local models; an SSH host with a trusted fingerprint,
  remote tools and worktrees; independently installed MCP servers; and an ACP
  editor client. Controlled protocol/server tests do not replace those checks.
- Sugar's member approval page, production device authorization, coding-task
  bridge, routing, and upstream compute. A local login fixture validates the
  client's request/poll/save sequence only.
- Native terminal behavior on the user's emulator, especially Windows ConPTY,
  clipboard integration, and terminal-specific shifted keys.
- Clean-machine release installation, upgrade/rollback, Apple notarization,
  and Windows certificate/reputation behavior. OS signing runs only when the
  repository has the required certificates; the release summary reports it.

Streamable HTTP MCP transport, daemon job resumption, managed-computer
provisioning, and an OS/container sandbox are not shipped features. Their
design documents and validators do not turn them into tested implementations.

## Publication gate

Inspect the CI run for the exact commit being released. Publish a new version
tag only after every required job succeeds; a previous green commit is not
evidence for a changed tree. Wait for the tagged release workflow as well, then
verify that all six archives, checksums, and checksum signatures were published.
The release workflow also attests build provenance. See [releases](releases.md)
for verification commands and the distinction between Sigstore and OS signing.
