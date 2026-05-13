# Packet Computers and Packet Control

Last reviewed: 2026-05-13

This document captures the Packet Computers and Packet Control product ideas for PacketADE/packetcode, the external research behind them, and a staged implementation plan that fits the current codebase.

## Executive Summary

Packet Computers and Packet Control both make sense for PacketADE, but they should be built in different layers.

Packet Computers should be the durable place where agents work: local machines, SSH machines, and eventually managed cloud machines that keep project state, dependencies, services, jobs, transcripts, logs, and approvals across time.

Packet Control should be the evidence layer: a workflow for proving that a change works by capturing terminal, browser, and later desktop evidence, then reporting a verdict against the original claim.

The recommended order:

1. Build Packet Computers as BYO local/SSH computers first.
2. Add persistent jobs and remote tool execution through a backend abstraction.
3. Build Packet Control as terminal-first verification and QA.
4. Add browser evidence through Playwright or agent-browser.
5. Defer managed cloud computers, desktop control, and polished demo-video composition until the evidence and remote-machine contracts are stable.

## Research Notes

### Factory Droid Computers

Factory's Droid Computers are persistent workstations for agents. Their BYOM model is especially relevant: users can register their own Linux, macOS, or Windows machines, and Factory routes traffic without exposing public ports.

Relevant ideas for PacketADE:

- A computer is not a chat session. It is a durable execution environment.
- Users should be able to register their own machines before PacketADE attempts managed cloud machines.
- Remote development access, including SSH-style workflows, is a major part of the product surface.
- Persistent machine state is useful because agents need installed dependencies, running services, repo state, and logs.

Sources:

- [Factory Droid Computers](https://docs.factory.ai/cli/features/droid-computers)
- [Factory Bring Your Own Machine](https://docs.factory.ai/cli/features/droid-computers-byom)
- [Factory Desktop](https://factory.ai/news/factory-desktop)
- [Factory Droid Computers launch](https://factory.ai/news/droid-computers)

### Factory Droid Control and Automated QA

Factory's Droid Control points toward an evidence-driven control layer: automate terminals, browsers, and desktop apps for QA and validation. The valuable product primitive is not only recording a video. It is the contract:

1. State what needs to be proven.
2. Execute the real workflow.
3. Capture evidence.
4. Judge the evidence against the claim.
5. Return a verdict with artifacts.

Relevant ideas for PacketADE:

- Packet Control should start as verification and QA, not as a video generator.
- Artifacts need to be first-class: logs, terminal snapshots, screenshots, traces, manifests, and verdicts.
- Terminal and browser control are realistic early targets. Desktop control has a much higher security and platform burden.

Sources:

- [Factory Droid Control](https://docs.factory.ai/cli/features/droid-control)
- [Factory Automated QA](https://docs.factory.ai/guides/skills/automated-qa)
- [Factory plugins](https://github.com/Factory-AI/factory-plugins)

### OpenAI Codex

Codex points toward isolated, configurable environments, explicit approvals, security-first defaults, and network controls. The most relevant lesson is that agent execution environments should have clear boundaries and reviewable outputs.

Relevant ideas for PacketADE:

- Environment setup and project-specific docs matter as much as model quality.
- Internet access and privileged execution need explicit policy.
- Approvals should be visible and auditable.
- Remote environments should not silently inherit all local credentials.
- Users need transparency into what the agent did, not just the final answer.

Sources:

- [Codex cloud overview](https://platform.openai.com/docs/codex/overview)
- [Codex cloud environments](https://developers.openai.com/codex/cloud/environments)
- [Codex remote connections](https://developers.openai.com/codex/remote-connections)
- [Agent internet access](https://platform.openai.com/docs/codex/agent-network)
- [Running Codex safely at OpenAI](https://openai.com/index/running-codex-safely/)

### Computer-Use Systems

OpenAI and Anthropic computer-use documentation reinforces that UI and desktop control should be treated as a high-risk capability. The controlled app or webpage can contain prompt-injection content, and authenticated sessions can expose sensitive data.

Relevant ideas for PacketADE:

- Treat UI content as untrusted evidence, not instructions.
- Use fresh browser profiles by default.
- Require target-level permissions for browser and desktop control.
- Preserve artifacts so users can inspect the agent's actions.
- Start with narrow terminal/browser flows before broad desktop automation.

Sources:

- [OpenAI Computer Use](https://platform.openai.com/docs/guides/tools-computer-use)
- [Anthropic Computer Use](https://docs.anthropic.com/en/docs/build-with-claude/computer-use)

### Browser and Terminal Evidence

Playwright and asciinema are good references for the evidence side. Playwright traces are structured and inspectable. Asciinema is lightweight and well-suited to terminal recordings.

Relevant ideas for PacketADE:

- Browser evidence should include traces, screenshots, console summaries, network summaries, and accessibility or DOM snapshots.
- Terminal evidence should include command output, exit codes, text snapshots, and optional recordings.
- Artifact manifests should be stable even when optional tools are missing.

Sources:

- [Playwright Trace Viewer](https://playwright.dev/docs/trace-viewer)
- [Playwright screenshots](https://playwright.dev/docs/screenshots)
- [asciinema docs](https://docs.asciinema.org/)
- [Remotion renderMedia](https://www.remotion.dev/docs/renderer/render-media)
- [agent-browser](https://agent-browser.io/)
- [agent-browser Chrome/CDP docs](https://agent-browser.dev/engines/chrome)

## Current Packetcode Fit

Packetcode already has much of the spine needed for both ideas:

- Provider-neutral agent loop.
- Tool registry with approval-aware tools.
- Background jobs and Agent View.
- Session persistence and transcript viewing.
- MCP integration.
- Statusline and hooks.
- File backup stack and undo.
- Provider/model registry.
- Cost and token tracking.

Important gaps:

- No durable machine registry.
- No daemon or remote execution transport.
- No persistent job reconnect/reconcile flow after PacketADE restarts.
- No per-job worktree model for serious parallel editing.
- No native evidence manifest/artifact system.
- No browser/terminal capture abstraction.
- No target-level policy for computer control.

## Packet Computers

### Product Definition

A Packet Computer is a durable machine that PacketADE can delegate work to.

It can be:

- `local`: the current machine running packetcode.
- `ssh`: a user-owned remote machine reached over SSH.
- `managed`: a future Packet-provisioned cloud machine.

A computer owns:

- Project roots.
- Installed dependencies.
- Running dev servers.
- Background jobs.
- Logs.
- Agent transcripts.
- File changes.
- Optional per-job worktrees.
- Policy for network, shell, filesystem, and approvals.

### User Surface

Suggested slash commands:

```text
/computers
/computers register <name>
/computers ssh <name>
/computers status <name>
/computers remove <name>
/spawn --computer <name> <prompt>
/agents --computer <name>
```

Suggested Agent View changes:

- Group agents by computer.
- Show computer state: online, offline, reconnecting, busy, blocked.
- Show per-computer job counts.
- Show daemon version and last heartbeat.
- Open a computer detail view with active jobs, logs, terminals, and project roots.

### Architecture

Add a new package:

```text
internal/computers/
  registry.go       # load/save computer records
  computer.go       # Computer, Capabilities, Policy
  daemon.go         # local daemon server
  client.go         # RPC client
  transport.go      # local loopback and SSH-forwarded transport
  runner.go         # job runner adapter
  policy.go         # trust and permission rules
```

Computer registry lives under:

```text
~/.packetcode/computers/
  registry.json
  <computer-id>.json
```

Computer record:

```json
{
  "id": "pc_...",
  "name": "workstation",
  "kind": "ssh",
  "status": "online",
  "last_seen": "2026-05-13T00:00:00Z",
  "daemon_version": "v0.1.0",
  "os": "windows",
  "arch": "amd64",
  "project_roots": ["D:/projects"],
  "capabilities": {
    "shell": true,
    "filesystem": true,
    "jobs": true,
    "terminals": false,
    "browser": false
  },
  "policy": {
    "network": "ask",
    "write": "ask",
    "shell": "ask",
    "approval_mode": "explicit"
  }
}
```

### Daemon

Add:

```text
packetcode daemon --listen 127.0.0.1:<port>
```

The daemon should expose a small RPC surface:

- `ping`
- `capabilities`
- `project.list`
- `job.spawn`
- `job.cancel`
- `job.status`
- `job.transcript`
- `tool.execute`
- `fs.read`
- `fs.write`
- `terminal.open` later
- `browser.run` later

V1 transport should be local loopback or SSH-forwarded loopback. Do not expose a public daemon port.

### Runner Integration

Avoid sprinkling remote conditionals into every tool. Introduce a backend abstraction:

```go
type RuntimeBackend interface {
    ResolvePath(root, path string) (string, error)
    ReadFile(ctx context.Context, path string) ([]byte, error)
    WriteFile(ctx context.Context, path string, data []byte) error
    Execute(ctx context.Context, command ExecuteRequest) (ExecuteResult, error)
}
```

Then provide:

- `LocalBackend`: current behavior.
- `ComputerBackend`: forwards to the daemon.

Jobs should gain:

```go
ComputerID string
WorkingDir string
```

The jobs manager should resolve `ComputerID` into a backend and pass it into the job's tool registry.

### Implementation Phases

#### Phase 1: BYO Local/SSH MVP

Goal: PacketADE can register a machine and spawn an agent job there.

Scope:

- `internal/computers` registry.
- `packetcode daemon`.
- Local loopback client.
- SSH-forwarded client.
- `/computers` list and status.
- `/spawn --computer <name>`.
- Agent View grouped by computer.
- Preserve existing approval semantics.

Out of scope:

- Managed cloud.
- Snapshots.
- Desktop control.
- Browser control.
- Public relay.

#### Phase 2: Persistent Jobs

Goal: Jobs survive PacketADE restart.

Scope:

- Daemon keeps job state.
- PacketADE reconnects and reconciles jobs.
- Agent View restores active and completed jobs.
- Transcripts stream after reconnect.
- Cancellation works across reconnect.

#### Phase 3: Project Workspaces

Goal: Parallel agents stop fighting over the same root.

Scope:

- Per-job git worktrees or branch directories.
- Conflict summary.
- Merge/apply workflow.
- Clear ownership in Agent View.

#### Phase 4: Process Persistence

Goal: Agents can own long-running dev servers and terminals.

Scope:

- Named terminals.
- Dev-server logs.
- Health checks.
- Restart policy.
- Attach/detach UI.

#### Phase 5: Snapshots and Checkpoints

Goal: Users can roll back machine/project state.

Scope:

- Git/file checkpoints first.
- Optional filesystem snapshots later.
- Managed VM/container snapshots only after managed computers exist.

#### Phase 6: Managed Computers

Goal: Packet can provision computers for users.

Scope:

- Cloud VM/container provisioning.
- Auto sleep/wake.
- Credential bootstrap.
- Network policy.
- Billing/quotas.
- Fleet updates.

This is deliberately late. Managed cloud computers are an operations product, not just a feature.

## Packet Control

### Product Definition

Packet Control is a first-class workflow for proving a claim with real captured evidence.

Examples:

```text
/verify "Esc cancels streaming without exiting the app"
/qa "run packetcode and confirm /help lists /transcript"
/demo "show the provider picker and model picker"
/control runs
/control open <id>
```

Control runs should produce:

- A plan.
- Steps.
- Captured artifacts.
- A verdict.
- A concise report.

### Core Model

```text
ControlRun
  id
  intent: verify | qa | demo
  target: terminal | browser | desktop
  claim
  plan
  steps
  artifacts
  verdict: confirmed | refuted | pass | fail | blocked | inconclusive
  created_at
  completed_at
```

Artifact directory:

```text
~/.packetcode/control/<run-id>/
  manifest.json
  report.md
  terminal.log
  stdout.txt
  stderr.txt
  screenshots/
  traces/
  recordings/
```

Manifest sketch:

```json
{
  "id": "ctrl_...",
  "intent": "verify",
  "target": "terminal",
  "claim": "Esc cancels streaming without exiting the app",
  "verdict": "confirmed",
  "steps": [
    {
      "id": "step_1",
      "action": "run command",
      "command": "go test ./internal/app -run TestApp_CtrlC",
      "exit_code": 0,
      "artifacts": ["stdout.txt"]
    }
  ],
  "artifacts": [
    {
      "path": "stdout.txt",
      "type": "text",
      "description": "go test output"
    }
  ]
}
```

### Architecture

Add:

```text
internal/control/
  run.go
  manager.go
  manifest.go
  report.go
  artifacts.go
  planner.go

internal/control/drivers/
  terminal/
  browser/
  desktop/
```

Drivers:

- `terminal`: command execution, output capture, terminal snapshots, optional asciinema.
- `browser`: Playwright or agent-browser, screenshots, traces, console/network summaries.
- `desktop`: deferred, explicit permission only.

### User Surface

Slash commands:

```text
/verify <claim>
/qa <command> -- <expected behavior>
/demo <scenario>
/control runs
/control open <id>
/control cancel <id>
```

Agent View or Control View:

- Show active control runs.
- Show current step.
- Show artifact count.
- Show verdict.
- Open report.
- Inject report into current conversation.

### Implementation Phases

#### Phase 1: Control Manifest

Goal: Stable evidence format before heavy automation.

Scope:

- `internal/control` types.
- Manifest writer/reader.
- Report renderer.
- Artifact directory layout.
- Tests for manifest stability.

#### Phase 2: Terminal Verify MVP

Goal: Prove CLI claims with terminal evidence.

Scope:

- `/verify <claim>`.
- `/qa <command> -- <expected behavior>`.
- Background execution through jobs.
- Capture command, exit code, stdout, stderr, timestamps.
- Final report in conversation.
- Human approval before execution.

Out of scope:

- Browser control.
- Desktop control.
- Video composition.

#### Phase 3: Browser QA

Goal: Prove browser/app behavior.

Scope:

- Playwright-backed driver.
- Fresh browser profile by default.
- Screenshots.
- Traces.
- DOM/accessibility snapshots.
- Console/network summaries.
- Target allowlist policy.

#### Phase 4: Agent View Integration

Goal: Make control runs inspectable like agents.

Scope:

- Control run rows.
- Step progress.
- Artifact preview/open.
- Verdict badges.
- Inject result into chat.

#### Phase 5: Demo Composition

Goal: Turn evidence into sharable demos.

Scope:

- Raw terminal/browser recordings.
- Simple report plus assets.
- Remotion-style polished videos later.

#### Phase 6: Desktop Control

Goal: Carefully add real desktop automation.

Scope:

- Explicit target permissions.
- Fresh/isolated profiles where possible.
- No silent access to authenticated sessions.
- Strong prompt-injection boundaries.
- Artifact-first audit trail.

## Shared Policy Model

Packet Computers and Packet Control should use the same policy language.

Suggested policy axes:

```text
filesystem: read-only | ask-write | allow-write
shell: ask | allow-safe | allow-all
network: off | ask | allowlist | unrestricted
browser: off | fresh-profile | ask-profile | current-profile
desktop: off | ask
secrets: deny | ask | allow-named
approval: explicit | trust-workspace | trust-computer
```

Defaults should be conservative:

- Local foreground chat keeps current behavior.
- Packet Computers default to explicit approval.
- Remote computers do not inherit local secrets.
- Packet Control browser runs use fresh profiles.
- Desktop control is off by default.
- Network access is off, ask, or allowlisted by default for remote/managed computers.

## Recommended First Milestones

### Milestone A: Packet Computers Design PR

Deliver:

- `internal/computers` data model.
- Registry load/save.
- `/computers` read-only list.
- Config support for local/SSH records.
- No daemon yet.

### Milestone B: Local Computer Daemon

Deliver:

- `packetcode daemon`.
- Loopback RPC.
- Heartbeat/status.
- Local shell and filesystem backend.
- `/computers status`.

### Milestone C: Spawn on Computer

Deliver:

- `ComputerID` on jobs.
- Backend abstraction for tools.
- `/spawn --computer <name>`.
- Agent View grouped by computer.

### Milestone D: Packet Control Manifest

Deliver:

- `internal/control` manifest/artifact/report.
- `/control runs`.
- No automation yet.

### Milestone E: Terminal Verify

Deliver:

- `/verify`.
- `/qa`.
- Terminal output artifacts.
- Verdict report.
- Background job integration.

## Risks

### Security

Persistent computers accumulate credentials, repo state, logs, and shell history. They need explicit trust boundaries, credential policy, and audit trails.

Mitigations:

- SSH/loopback only in v1.
- No public daemon listener.
- Explicit approvals.
- Per-computer policy.
- Network policy.
- Redaction for logs and reports.

### Prompt Injection

Browser and desktop control can expose the agent to malicious UI text.

Mitigations:

- Treat controlled UI content as evidence, not instructions.
- Keep system/developer instructions separate from observed page/app text.
- Use fresh browser profiles.
- Require explicit user approval for sensitive targets.

### State Drift

Persistent machines can become stale or dirty.

Mitigations:

- Health checks.
- Setup scripts.
- Project status summaries.
- Worktrees/checkpoints.
- Visible dirty-state warnings.

### Parallel Edit Conflicts

Multiple agents editing one tree can conflict.

Mitigations:

- Per-job worktrees.
- Path locks remain useful but should not be the only protection.
- Merge/apply review workflow.

### Product Confusion

Users need clear distinctions:

- Session: the conversation.
- Agent/job: a delegated task.
- Computer: the durable machine where work runs.
- Control run: an evidence workflow that proves or disproves a claim.

## Recommendation

Build both, but do not build both as giant features.

Packet Computers should start with BYO local/SSH machines and persistent job execution. That makes PacketADE more useful quickly without taking on managed cloud operations.

Packet Control should start with terminal verification and QA manifests. That gives PacketADE a trust-and-truth layer: users can ask not just "did you change it?" but "prove it works."

The strategic connection is strong:

- Packet Computers provide durable places for work.
- Packet Control proves the work is real.
- Agent View shows who is doing what.
- Sessions and transcripts preserve why it happened.

