# Sugar Provider

Packetcode's built-in `sugar` provider connects the existing local Go agent to the Sugar service.

## Contract

- Authentication: a private `sgr_...` bearer key.
- Base URL: the Sugar deployment's `/api/v1` path.
- Model discovery: authenticated `GET /models` on every provider initialization.
- Default model: `sugar/conduit`.
- Direct models: every additional `sugar/*` ID returned by the live catalog.
- Completion transport: OpenAI-compatible `POST /chat/completions` with SSE.
- Local tools, permissions, sessions, worktrees, background agents, and MCP remain owned by Packetcode.
- Compute routing and upstream usage metering remain owned by Sugar and Runpod.

## Feature gate

Sugar uses a tri-state gate. With `enabled` absent, a fresh non-Sugar install is
auto-inactive and does no Sugar initialization; an existing configured/default
Sugar provider remains active for compatibility. `packetcode sugar login`
explicitly activates the automatic state. To hard-disable the complete surface:

```toml
[sugar]
enabled = false
```

`PACKETCODE_SUGAR_ENABLED=false` is the environment equivalent. Disabled mode
removes built-in Sugar from provider registration, blocks `packetcode sugar login`,
suppresses `sugar_cache` metadata, and forces Conduit shadow off. It makes no
Sugar or Conduit network calls, does not remove saved credentials, and does not
affect non-Sugar providers. Re-enable it to use an existing configuration.
The gate preserves saved credentials and provider configuration.
An explicitly disabled built-in also permits advanced users to reuse the
`sugar` slug for an independent `type = "openai_compatible"` provider; that
custom provider does not activate Sugar cache or Conduit features.

## Login

```text
packetcode sugar login
```

Both flags are optional. On a machine that has never connected to a Sugar
service, the command asks for the service URL and offers the hosted deployment
as the default; the answer is persisted, so it is asked once. `--server
https://your-sugar-service.example` skips the question, and
`PACKETCODE_SUGAR_BASE_URL` overrides both. The API key is named after the
machine's hostname unless `--name your-name` says otherwise, so a member's key
list identifies which computer each key belongs to. Sugar accepts names of 2-80
characters; Packetcode checks the length before sending and reports a rejected
client (bad name, or a `client_id` the service does not register) as
`invalid_client` with the fix.

The command requests a short-lived device code, opens Sugar's approval page, and shows the same code in the terminal. A signed-in member reviews the named device and approves it; Packetcode polls at Sugar's required interval until it receives a one-time, member-owned API key. It then validates the live model list, persists the provider with user-only config permissions, and activates `sugar/conduit`. Use `--no-browser` to open the printed URL manually.

The short user code and opaque device code expire after ten minutes. Sugar stores only purpose-separated HMACs, creates the `sgr_live` key in the same transaction that consumes an approved request, and never sends the key to the browser.

Remote Sugar URLs must use HTTPS. Plain HTTP is accepted only for localhost development.

## Live model selection

Use `/model` in Packetcode. The picker is populated from Sugar at runtime, so the service can add, remove, or advance models without a new CLI build. Choosing `sugar/conduit` restores task-aware routing; choosing another returned ID pins that Sugar lane.

## Process-scoped coding tasks

When Sugar starts Packetcode for a coding task, it can set `SUGAR_TASK_API_ROOT`
to the HTTP API root printed by its local task bridge. The built-in provider
uses that root for this process while keeping the saved endpoint and sign-in.
Only `http://127.0.0.1:<port>/task/<UUID-v4>/api/v1` is accepted, with a port
above 3050 and no credentials, query, fragment, or encoded path. An invalid
override fails model discovery and completion rather than silently bypassing
the task bridge. Do not set this variable for an ordinary hosted connection.

The endpoint validation has automated tests. A real Sugar task bridge, its
upstream routing, and the member's browser approval remain external integration
checks; a local device-login fixture does not prove those services work.

## Phase 2

The cross-platform desktop application for Windows, macOS, and Linux should reuse this exact provider contract. It needs no separate compute integration: authenticate with Sugar, fetch `/models`, send chat requests, and render Sugar usage/routing metadata while keeping project tools local.
