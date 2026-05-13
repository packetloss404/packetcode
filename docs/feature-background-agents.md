# Background / Parallel Agents — Design Spec

## Summary

Background agents let the user (via `/spawn`) or the main agent (via a `spawn_agent` tool call) launch independent agent loops that run concurrently with the foreground conversation. Each job is a fully isolated mini-`Agent` with its own session, cost tally, backup stack, and provider/model selection; a new `internal/jobs` package owns lifecycle, concurrency limits, cancellation, and result fan-in. Results surface in Agent View and can be explicitly injected into the foreground conversation when the user chooses.

## User stories

1. **Parallel research while coding.** User is mid-conversation iterating on a refactor. They type `/spawn --provider gemini --model gemini-2.5-flash 'find every call site of UserService.Authenticate and summarise auth patterns'` and keep working. Three minutes later a system message appears: `[job:7f3a — done, 12s, $0.003] 14 call sites in 8 files; two patterns…`.
2. **Agent-initiated parallelism.** User asks "audit the test suite for missing edge cases." Main agent calls `spawn_agent` three times — one per package — then waits on the results, summarises, and reports back without burning the user's context window with raw test output.
3. **Cheap background scout, expensive foreground edit.** User keeps Claude Sonnet on the main conversation but every `/spawn` defaults to Gemini Flash for cost. Configured via `[behavior].background_default_provider`.
4. **Cancellation.** User realises a spawned job has gone off the rails: `/cancel 7f3a` (or `/cancel all`) terminates it; the result message reads `[job:7f3a — cancelled by user]`.
5. **Inspection.** `/agents` opens Agent View for live grouped status and result actions; `/agents 7f3a` or `/jobs 7f3a` opens a transcript modal showing the spawned agent's full conversation including tool calls.

## Architecture

### Data model

New package `internal/jobs/` introduces:

```go
// internal/jobs/job.go
package jobs

type State int

const (
    StateQueued    State = iota // accepted, not yet running (concurrency limit)
    StateRunning                // worker goroutine started, agent loop active
    StateCompleted              // agent emitted EventDone
    StateFailed                 // EventError or panic
    StateCancelled              // ctx cancelled by user or shutdown
)

type Job struct {
    ID            string        // 8-char short id, also the subsession UUID prefix
    SessionID     string        // full UUID of the job's underlying session.Session
    ParentJobID   string        // "" when spawned from the main session
    Prompt        string        // initial user message
    Provider      string        // slug; may differ from main session
    Model         string        // model id under that provider
    State         State
    CreatedAt     time.Time
    StartedAt     time.Time
    FinishedAt    time.Time
    Summary       string        // short result summary surfaced into main convo
    Error         string        // populated on StateFailed
    InputTokens   int
    OutputTokens  int
    CostUSD       float64
    Depth         int           // 0 for main-spawned, parent.Depth+1 otherwise
    Transcript    []provider.Message // snapshot taken when state becomes terminal
}

type Snapshot struct {  // safe-to-copy projection used by UI
    ID, ParentJobID, Prompt, Provider, Model, Summary, Error string
    State        State
    CreatedAt, StartedAt, FinishedAt time.Time
    Tokens       struct{ Input, Output int }
    CostUSD      float64
    Depth        int
}
```

`JobManager` is the public surface:

```go
// internal/jobs/manager.go

type Config struct {
    Registry        *provider.Registry
    Tools           *tools.Registry           // main registry; Manager wraps it for jobs
    MainSessions    *session.Manager          // for cross-checks; jobs use sub-managers
    SessionsDir     string                    // ~/.packetcode/sessions
    BackupsDir      string                    // ~/.packetcode/backups
    JobsDir         string                    // ~/.packetcode/jobs (transcript snapshots)
    CostTracker     *cost.Tracker             // shared; jobs key by their own session id
    PricingFor      cost.PricingFunc
    SystemPromptFor func(parentDepth int) string  // builds job's system prompt
    MaxConcurrent   int                       // default 4
    MaxDepth        int                       // default 2 (main → spawn → spawn)
    MaxTotal        int                       // safety cap, default 32 lifetime per app run
    DefaultProvider string                    // optional override of main provider
    DefaultModel    string
    Approver        agent.Approver            // delegated to for restricted tools, see Approval Policy
    OnUpdate        func(Snapshot)            // async fan-out to UI; Manager calls in goroutine
}

type Manager struct {
    cfg    Config
    mu     sync.RWMutex
    jobs   map[string]*Job          // keyed by short ID
    queue  chan *Job                // bounded by MaxConcurrent
    cancel map[string]context.CancelFunc
    sem    chan struct{}            // concurrency limiter, len == MaxConcurrent
    closed bool
}

func NewManager(cfg Config) *Manager

// Spawn enqueues a new job. Returns the assigned ID immediately; the
// worker goroutine is started by the manager.
type SpawnRequest struct {
    Prompt       string
    ParentJobID  string  // "" for main-spawned
    ParentDepth  int     // 0 for main-spawned
    Provider     string  // "" → use default
    Model        string  // "" → use default
    SystemPrompt string  // "" → use SystemPromptFor
    AllowWrite   bool    // opt-in to destructive tools (--write or allow_write=true)
}
type SpawnError struct{ Code string; Reason string } // "limit_reached", "depth_exceeded", "unknown_provider"

func (m *Manager) Spawn(req SpawnRequest) (Snapshot, *SpawnError)

// Get returns a snapshot. Found=false when ID is unknown.
func (m *Manager) Get(id string) (Snapshot, bool)

// List returns snapshots sorted newest-first.
func (m *Manager) List() []Snapshot

// Transcript returns the full message history captured at terminal state,
// or the live-so-far history if still running.
func (m *Manager) Transcript(id string) ([]provider.Message, bool)

// Cancel signals a single job. No-op for unknown id or terminal state.
func (m *Manager) Cancel(id string) bool

// CancelAll cancels every running/queued job. Used on shutdown and /cancel all.
func (m *Manager) CancelAll() int

// PendingResults drains completed jobs whose results haven't yet been
// surfaced to the main conversation. Returns up to n entries.
type Result struct {
    JobID, Provider, Model, Summary string
    State              State
    DurationMS         int64
    InputTokens, OutputTokens int
    CostUSD            float64
}
func (m *Manager) DrainResults(n int) []Result

// WaitForJob blocks until the job reaches a terminal state or timeout
// elapses. Used by the spawn_agent tool's wait=true mode.
func (m *Manager) WaitForJob(id string, timeout time.Duration) (Result, bool)

// Shutdown cancels all jobs and waits up to the given timeout.
func (m *Manager) Shutdown(timeout time.Duration) error

// ActiveCount returns the number of jobs in StateQueued or StateRunning.
func (m *Manager) ActiveCount() int
```

### Lifecycle diagram

```
        Spawn()                               worker goroutine
  ┌──────────────────┐    sem<-{}    ┌────────────────────────────┐
  │   StateQueued    │──────────────▶│        StateRunning         │
  └──────────────────┘  (sem token   └─────┬───────┬──────────────┘
            │            available)        │       │
            │                               │       │ ctx.Done()
            │ Cancel()                      │       ▼
            ▼                               │  StateCancelled
       StateCancelled                       │
                                            │ EventDone
                                            ▼
                                       StateCompleted
                                            │
                                            │ EventError / panic
                                            ▼
                                        StateFailed
```

Valid transitions: `Queued → Running | Cancelled`; `Running → Completed | Failed | Cancelled`. Terminal states never transition further. Each transition fans out a `Snapshot` via `OnUpdate(snap)` so the UI gets push notifications without polling.

### Goroutine model

- `Manager.Spawn` allocates the `Job`, persists a `queued.json`, and spawns a worker goroutine: `go m.runJob(job)`.
- `runJob` blocks on `m.sem <- struct{}{}` to honour `MaxConcurrent`. While blocked the job is `StateQueued`. Acquisition flips to `StateRunning`.
- The worker creates a private `session.Manager` (one-shot, in-memory + persisted under `sessions/<sub-uuid>.json`), a private `session.BackupManager` keyed by the sub-session id, and a per-job `tools.Registry` cloned from the main one but with the job's BackupManager wired into `write_file`/`patch_file` (see Isolation).
- It builds an `agent.Agent` with the per-job dependencies, calls `agent.Run(jobCtx, prompt)`, and consumes the event channel. Each `EventTextDelta` is appended to an in-memory transcript; tool events feed the live transcript surfaced by `/jobs <id>`. `EventUsageUpdate` updates `Job.InputTokens/OutputTokens` and reports to the shared `cost.Tracker` keyed by the sub-session id (so totals naturally aggregate).
- On `EventDone` → `StateCompleted`. On `EventError` → `StateFailed`. The worker computes a `Summary` (last assistant text trimmed to ~280 chars) and pushes a `Result` to an internal results queue.
- On any terminal state the worker releases the semaphore (`<-m.sem`), persists the final snapshot to `~/.packetcode/jobs/<short-id>.json`, and fires the final `OnUpdate`.
- `Manager` holds a `context.CancelFunc` per running job (derived from a manager-level base context). `Cancel(id)` calls that func, which propagates to the agent's HTTP request and any in-flight `execute_command` (since `agent.Run` already plumbs ctx through).

### Isolation

| Concern | Main session | Background job |
|---|---|---|
| `provider.Message` history | `Manager.current` in main `session.Manager` | A fresh `*session.Session` in a per-job `session.Manager`; sub-uuid derived as `mainID+"-job-"+shortID`; persisted under `sessions/`. |
| Cost tracking | Existing `cost.Tracker` keyed by main session id | Same shared `*cost.Tracker`, keyed by the **sub-session id**. `Tracker.Breakdown()` already returns per-session entries — the UI sums them. No tracker code change. |
| `BackupManager` (undo) | `session.NewBackupManager(backupsDir, mainID)` | `session.NewBackupManager(backupsDir, subID)`. The job's backups live in `backups/<subID>/`. The main `/undo` only sees its own stack, so a background write does not pollute foreground undo history. |
| Provider/model | Whatever `provider.Registry.Active()` returns | Resolved at spawn time from `SpawnRequest.Provider/Model` → `cfg.DefaultProvider/Model` → `Registry.Active()`. The job binds its (provider, model) at spawn — main session hot-switches do **not** retroactively switch a running job's model. We thread the bound (provider, model) into a per-job mini-`provider.Registry` that overrides `Active()`. |
| Tool registry | Main `*tools.Registry` | Per-job clone built from `cfg.Tools.All()` with replacements for `write_file`/`patch_file` (job-local backup manager) and an `execute_command` wrapped to honour the per-job `cwd` lock. `spawn_agent` is included only when `Job.Depth < cfg.MaxDepth`. |
| System prompt | `app.systemPrompt` | `cfg.SystemPromptFor(parentDepth)` returns a hardened prompt: same base but with an appended block "You are a background sub-agent. Be concise. Do not ask the user clarifying questions — make reasonable assumptions and act. Your final assistant message becomes your delivered result." Plus, when restricted, "You may only call read-only tools." |
| Approver | `uiApprover` (channel-based) | Per the policy below — defaults to `agent.AutoApprove()` against a pre-filtered tool subset, no UI prompt. |

We do **not** make the existing `session.Manager`/`cost.Tracker`/`BackupManager` "multi-session aware" with internal id-keyed maps. Why: their current single-session API is what the rest of the app expects, and sharing one mutable manager across goroutines invites lock contention and accidental leakage. Instead we instantiate one of each per job — cheap, isolated, idiomatic Go.

The one exception is `cost.Tracker`, which is already multi-session-aware by construction (its `Sessions map[string]SessionCost` is keyed by id). Sharing it is correct and gives us free aggregation.

## API surface

### `spawn_agent` tool (autonomous delegation)

Schema:

```json
{
  "type": "object",
  "properties": {
    "prompt":   { "type": "string", "description": "The task for the background agent. Be specific and self-contained — the spawned agent has no shared memory with you." },
    "provider": { "type": "string", "description": "Optional provider slug override (openai, anthropic, gemini, minimax, openrouter, ollama). Defaults to the parent's provider." },
    "model":    { "type": "string", "description": "Optional model id override. Defaults to the parent's model." },
    "wait":     { "type": "boolean", "description": "If true, this tool call blocks until the spawned job completes and returns its result inline. If false (default), returns the job id immediately and the result surfaces later." },
    "allow_write": { "type": "boolean", "description": "If true, opt the child into destructive tools. This is independent of wait and may be unavailable from read-only parent jobs." }
  },
  "required": ["prompt"]
}
```

Semantics:
- `RequiresApproval()` returns **true**. Spawning is a meaningful action — the user should consent to a parallel agent at least the first time. Trust mode auto-approves.
- The tool's `Execute` calls `JobManager.Spawn(...)` and:
  - **wait=false** (default): returns immediately. `ToolResult.Content = "Spawned job 7f3a (gemini/gemini-2.5-flash). Result will appear when the job completes."`. Metadata carries `job_id`.
  - **wait=true**: calls `JobManager.WaitForJob(id, timeout)` and blocks (default 5 min). Returns the spawned agent's final summary as the tool result content. Waiting does not grant write access; callers must request `allow_write=true`, and read-only parent jobs cannot escalate.
- Constructor: `tools.NewSpawnAgentTool(spawner JobSpawner, parentJobID string, parentDepth int) tools.Tool`. `JobSpawner` is a tiny interface defined in `internal/tools` to avoid an import cycle: `Spawn(SpawnRequest) (Snapshot, *SpawnError)`, `WaitForJob(id, timeout) (Result, bool)`. `*jobs.Manager` satisfies it.
- The tool is **registered conditionally** in the per-job tool registry only when the new job's depth is less than `MaxDepth-1`.

Example LLM-issued call:
```json
{
  "name": "spawn_agent",
  "arguments": "{\"prompt\":\"Read everything under internal/provider/openai/ and summarise the streaming-event handling in 5 bullet points.\",\"wait\":true,\"provider\":\"gemini\",\"model\":\"gemini-2.5-flash\"}"
}
```

### Slash commands (explicit user delegation)

Both `/spawn` and `spawn_agent` are supported. `/spawn` is the first concrete user of the slash-command parsing path and is implemented in `internal/app/slashcmd.go`.

| Command | Effect |
|---|---|
| `/spawn [--provider <slug>] [--model <id>] [--write] <prompt...>` | Calls `JobManager.Spawn`. Echoes `[job:7f3a queued — gemini/gemini-2.5-flash] <prompt>` into the conversation as a system message. |
| `/agents` | Opens Agent View, a grouped dashboard with peek, open transcript, cancel, and explicit result injection actions. |
| `/agents <id>` | Opens the transcript modal for one background agent. |
| `/jobs` | Renders an inline ASCII table: `id  state  prov/model  age  tok in/out  prompt-snippet`. Newest first. |
| `/jobs <id>` | Opens the transcript modal (`internal/ui/components/jobs`). Esc closes. Shows the spawned agent's full message history including tool calls. Read-only. |
| `/cancel <id>` | `JobManager.Cancel(id)`. Replies with `[job:<id> — cancellation requested]` or `[job:<id> not found]`. |
| `/cancel all` | `JobManager.CancelAll()`. Replies with `[cancelled n jobs]`. |

`/spawn` argument parsing is intentionally simple flag-then-prompt: anything after the last flag value is the prompt, joined by spaces.

## Approval policy

**Decision: Restricted tool set + main-session approval for any destructive call.**

A background job's tool registry is built in two tiers:

1. **Always available, never approval-gated:** `read_file`, `search_codebase`, `list_directory`, `spawn_agent` (gated by depth, not approval).
2. **Available only when explicitly opted in via spawn flag `--write` or tool argument `allow_write=true`:** `write_file`, `patch_file`, `execute_command`. When opted in, these tools route their approval through the **main session's `uiApprover`**, prefixing the approval prompt header with `(from job:<id>)`.

Why this combination:

- **Pure auto-approve is dangerous.** A misbehaving sub-agent could silently `rm -rf` something. Background ≠ trusted.
- **Inheriting the main approval flow for everything is too noisy.** The point of background jobs is they don't interrupt you. Read-only tools have no destructive effect, so we exempt them.
- **Restricting to read-only by default with explicit opt-in gives users control.**
- **Surfacing destructive prompts through the main approver** keeps the user in control without inventing a separate approval UI. The main `uiApprover` is already a single-slot channel-based queue, which naturally serialises prompts.
- **`wait=true` is not a write grant:** waiting only changes result delivery. Destructive tools require `allow_write=true`, and read-only background parents cannot use that flag to escalate a child.

Implementation: the per-job `Approver` is a small adapter:
```go
// internal/jobs/approver.go
type jobApprover struct {
    parent      agent.Approver  // the main uiApprover
    jobID       string
    allowWrite  bool            // from --write or allow_write=true
}
func (j *jobApprover) Approve(ctx context.Context, req agent.ApprovalRequest) agent.ApprovalDecision {
    if !j.allowWrite {
        return agent.ApprovalDecision{Approved: false, Reason: "background job is read-only"}
    }
    annotated := req
    annotated.ToolCall.Name = fmt.Sprintf("[job:%s] %s", j.jobID, req.ToolCall.Name)
    return j.parent.Approve(ctx, annotated)
}
```

## Concurrency, recursion, conflict handling

- **Concurrency cap:** default `MaxConcurrent = 4`. Configurable as `background_max_concurrent`.
- **Lifetime cap:** `MaxTotal = 32` jobs per app run. Configured as `background_max_total`.
- **Recursion depth:** default `MaxDepth = 2` (main → spawn → spawn). Configurable as `background_max_depth`. Past max depth, `spawn_agent` is not registered in the sub-agent's tool registry.
- **Fan-out per parent:** soft-capped via system-prompt addendum; hard-capped only by global `MaxConcurrent`.
- **File-write conflicts:** the `Manager` keeps a `map[string]*sync.Mutex` of locked absolute paths. Wrapped `write_file` and `patch_file` acquire the mutex before touching the path; conflicts serialise. Last write wins, but writes are atomic.
- **CWD conflicts for `execute_command`:** no isolation in v1. Concurrent execs allowed; kernel handles process isolation.
- **Cancellation propagation:**
  - `Cancel(id)` calls the per-job `cancelFunc`, derived from `context.WithCancel(managerCtx)`.
  - The agent loop already plumbs ctx through to `provider.ChatCompletion` and `tool.Execute`.
  - `execute_command` releases within ~1s via `exec.CommandContext`'s SIGKILL.
  - If a worker fails to terminate within `cancelGraceTimeout = 10s`, the manager logs a warning, marks the job `StateCancelled` anyway, and lets the goroutine leak.
- **Shutdown:** App's `Quit` path calls `JobManager.Shutdown(5s)`. Persisted `jobs/<id>.json` files left in `StateRunning` are rewritten as `StateCancelled` with reason `app shutdown`.

## UI changes

### Top bar counter

Extend `topbar.Model` with `SetJobs(active int)`. Renders as a new droppable segment:

```
⚙ 2 jobs
```

Hidden when `active == 0`. Position: between cost and duration in the priority list. On a narrow terminal it's the second-to-last to drop, ahead of `costSeg`.

`App.refreshTopBar` calls `topbar.SetJobs(jobsManager.ActiveCount())` once per tick (already 15s), and additionally on every `OnUpdate` callback.

### `/jobs` panel

Two presentations:

- `/jobs` — inline system message rendered as monospace ASCII table:

  ```
  ID    STATE      PROV/MODEL              AGE   TOK(IN/OUT)   PROMPT
  7f3a  running    gemini/2.5-flash        00:42  840/210       Find all call sites of UserService.Authenticate…
  9b21  done       openai/gpt-4.1          02:13  1.2K/450      Audit the test suite for missing edge cases…
  c4dd  cancelled  ollama/qwen2.5-coder    01:05  120/0         Try writing a Rust port of the…
  ```

- `/jobs <id>` — full transcript modal. New component `internal/ui/components/jobs/jobs.go` exposes a `Model` with `Show(snap, transcript)`, `Hide()`, `Visible()`, `View()`, `Update()`. Esc / `q` closes.

### Completion notifications

**Hybrid:** inline message + top-bar counter decrement.

When `Manager.OnUpdate` fires with a job entering a terminal state, the App appends a system message:

```
[job:7f3a — done · 12s · gemini/2.5-flash · $0.0031]
14 call sites in 8 files; auth pattern A used in 11/14 (legacy), pattern B in the rest. Detail: /jobs 7f3a
```

For `StateFailed`: `[job:7f3a — failed · 8s] error: rate limited (429); retry in 30s`.
For `StateCancelled`: `[job:7f3a — cancelled · 4s]`.

**Result delivery decision:** Inline system message plus explicit injection from Agent View. Terminal updates mark results as `seen`; pressing `i` in `/agents` marks the result `injected` and appends a `provider.Message{Role: RoleUser, Content: "[Background job 7f3a result]\n<summary>"}` to the foreground session. We use `RoleUser` because (a) some providers don't allow multi-system, (b) the existing system prompt is already in slot 0, (c) this content is conversational input the user is explicitly handing the model.

## Persistence

**Decision: Persisted, but not resumable in v1.**

- During a job's lifetime: the underlying `session.Session` is auto-saved by the existing `session.Manager.AddMessage` flow. So the *transcript* is always on disk under `~/.packetcode/sessions/<sub-uuid>.json`.
- Job *metadata* (state, prompt, summary, timing, parent linkage) is written to `~/.packetcode/jobs/<short-id>.json` on every state transition. Atomic temp-file rename.
- On app start, `JobManager` reads `~/.packetcode/jobs/`, finds any files in `StateRunning` or `StateQueued` (orphans from a crash), and rewrites them to `StateCancelled` with reason `previous app exit`. They appear in `/jobs` history but are not resumed.
- On clean shutdown, in-flight jobs are cancelled and persisted with `StateCancelled` reason `app shutdown`.

The `jobs/` dir uses 0700 perms via `config.EnsureDir`. New helper `config.JobsDir() (string, error)` mirrors `BackupsDir()`.

## Cost attribution

Background job tokens **aggregate into the same `cost.Tracker`** because the tracker is already keyed by session id, and background jobs have their own session ids. Therefore:

- `Tracker.TotalCost()` naturally includes background work.
- New API: `Tracker.SessionCostsForIDs(ids []string) float64` is a tiny helper for the `/jobs` panel.
- The `/jobs <id>` modal shows per-job cost using existing `Tracker.SessionCost(subID)`.

## File-by-file change list

### Bucket A — Backend

| Path | What changes |
|---|---|
| **NEW** `internal/jobs/job.go` | `State` constants, `Job`, `Snapshot`. State `String()` for logging. |
| **NEW** `internal/jobs/manager.go` | `Manager`, `Config`, `SpawnRequest`, `SpawnError`, `Result`. Methods listed above. Owns: `jobs map[string]*Job`, `sem chan struct{}`, `cancel map[string]context.CancelFunc`, `pathLocks map[string]*sync.Mutex`, `results []Result`, `subscribers []func(Snapshot)`. |
| **NEW** `internal/jobs/worker.go` | `(m *Manager).runJob(j *Job)`. Acquires sem, builds per-job `session.Manager`, per-job `BackupManager`, per-job tool registry via `m.buildJobToolRegistry(parentDepth, allowWrite)`, builds `agent.Agent`, runs the loop, drains events, writes Snapshot updates. Catches panics → `StateFailed`. |
| **NEW** `internal/jobs/registry.go` | `(m *Manager).buildJobToolRegistry(parentDepth int, allowWrite bool, jobID string)`. Iterates `cfg.Tools.All()` and re-registers using job-local `BackupManager`. Adds `spawn_agent` when `parentDepth < cfg.MaxDepth-1`. Drops destructive tools when `!allowWrite`. |
| **NEW** `internal/jobs/approver.go` | `jobApprover{parent agent.Approver, jobID string, allowWrite bool}` implementing `agent.Approver`. |
| **NEW** `internal/jobs/persistence.go` | `loadOrphaned(jobsDir)` + `saveSnapshot(jobsDir string, j *Job)`. Atomic temp-file rename. |
| **NEW** `internal/jobs/manager_test.go`, `worker_test.go`, `registry_test.go`, `approver_test.go`, `persistence_test.go` | See Tests section. |
| `internal/cost/tally.go` | Add `func (t *Tracker) SessionCostsForIDs(ids []string) float64`. *(Optional.)* |
| `internal/config/config.go` | Add to `BehaviorConfig`: `BackgroundMaxConcurrent`, `BackgroundMaxDepth`, `BackgroundMaxTotal`, `BackgroundDefaultProvider`, `BackgroundDefaultModel`. |
| `internal/config/defaults.go` | Defaults: `4`, `2`, `32`, `""`, `""`. |
| `internal/config/paths.go` | Add `JobsDir() (string, error)` returning `~/.packetcode/jobs/`. |

`agent.Agent`, `session.Manager`, `session.BackupManager`, `tools.Registry`, `cost.Tracker`, `provider.Registry` get **no breaking changes**.

### Bucket B — Tool + integration

| Path | What changes |
|---|---|
| **NEW** `internal/tools/spawn_agent.go` | `SpawnAgentTool` struct holding `Spawner JobSpawner`, `ParentJobID string`, `ParentDepth int`. Schema per spec above. `RequiresApproval()` returns `true`. `Execute` parses params, calls `Spawner.Spawn`. If `wait=true`, blocks on `Spawner.WaitForJob(id, timeout)`. |
| **NEW** `internal/tools/spawn_agent_test.go` | Tests 16-19. |
| **NEW** `internal/tools/job_spawner.go` | Defines the `JobSpawner` interface with `Spawn` and `WaitForJob` methods. Avoids an import cycle. |
| **NEW** `internal/app/slashcmd.go` | `func ParseSlashCommand(text string) (cmd string, args []string, ok bool)`. Recognises `/spawn`, `/jobs`, `/cancel`. `func parseSpawnFlags(args []string) (provider, model string, allowWrite bool, prompt string, err error)`. |
| **NEW** `internal/app/slashcmd_test.go` | Tests 20-22. |
| `internal/app/app.go` | Add `jobs *jobs.Manager` to `App` and `Jobs *jobs.Manager` to `Deps`. Wire `OnUpdate` callback through `tea.Program.Send`. In `Update`, handle job snapshots and Agent View messages. Terminal snapshots mark results as `seen`; `/agents` actions can peek, open, cancel, or explicitly inject a selected result. In the input.SubmitMsg path, intercept `/spawn`, `/agents`, `/jobs`, `/cancel` slash commands before agent.Run. |
| `cmd/packetcode/main.go` | After constructing `tracker`, build `jobs.Manager`, register `tools.NewSpawnAgentTool(mgr, "", 0)` into the **main** `toolReg`. Pass `Jobs: mgr` into `app.Deps`. Defer `mgr.Shutdown(5*time.Second)`. |

### Bucket C — TUI + docs

| Path | What changes |
|---|---|
| `internal/ui/components/topbar/topbar.go` | Add `activeJobs int` field, `SetJobs(n int)` setter. Render `⚙ N jobs` segment when `n > 0`. Insert in `droppable` slice between `costSeg` and `durSeg`. |
| `internal/ui/components/topbar/topbar_test.go` | Test 24. |
| **NEW** `internal/ui/components/jobs/jobs.go` | Modal-style component. `Model` with `viewport.Model`, `visible bool`, `snap jobs.Snapshot`, `messages []provider.Message`. `Show(snap, msgs)`, `Hide()`, `Visible()`, `View()`, `Update(msg)`. |
| **NEW** `internal/ui/components/jobs/jobs_test.go` | Tests 25-26. |
| `internal/app/app.go` | Wire the jobs modal: when `/jobs <id>` is parsed, look up snapshot + transcript, call `jobsPanel.Show(...)`. Add `jobsPanel jobs.Model`. The `View()` overlay slot stacks: approval > jobsPanel > spinner. |
| `internal/app/keymap.go` | Add entries for `/spawn`, `/agents`, `/jobs`, `/cancel`, and modal close keys. |
| `README.md` | Document `/spawn`, `/jobs`, `/cancel`, and the `[behavior]` block keys in the current user-facing sections. |
| `CHANGELOG.md` | Under `[Unreleased] → Added`: bullet for **Background agents**. Move "Background / parallel agents" out of `Deferred to a future release`. |

## Tests

### Bucket A — backend tests

1. `TestManager_SpawnAndComplete` — spawn against an in-process fake `provider.Provider` that emits `EventTextDelta("hello") → EventDone`; assert state transitions Queued→Running→Completed and that `OnUpdate` fires for each.
2. `TestManager_ConcurrencyLimit` — spawn 6 jobs with `MaxConcurrent=2`; assert at most 2 running at any time.
3. `TestManager_DepthLimit` — spawn with `ParentDepth = MaxDepth-1`; assert the per-job tool registry does not contain `spawn_agent`.
4. `TestManager_LifetimeLimit` — `MaxTotal=3`, attempt 4 spawns; 4th returns `SpawnError{Code: "limit_reached"}`.
5. `TestManager_Cancel` — spawn a job whose fake provider streams forever; `Cancel(id)`; assert state becomes `StateCancelled` within 2s.
6. `TestManager_CancelAll` — spawn 5 jobs, `CancelAll`, assert all reach `StateCancelled`, returned count == 5.
7. `TestManager_Shutdown_Persists` — spawn 2 running jobs, `Shutdown(2s)`, assert `~/.packetcode/jobs/<id>.json` files exist with `StateCancelled`.
8. `TestManager_OrphanRecovery` — pre-write a `StateRunning` file, `NewManager`, assert it's rewritten as `StateCancelled` with reason `previous app exit`.
9. `TestManager_DrainResults` — spawn 3 completing jobs, `DrainResults(10)` returns 3 entries; subsequent call returns 0.
10. `TestManager_Isolation_Backups` — spawn a job that calls `write_file`; assert the parent's `BackupManager` stack is unchanged and the job's stack has 1 entry.
11. `TestManager_PathLock_Serialises` — two concurrent jobs both `write_file` to the same path; assert no data interleaving, no deadlock.
12. `TestManager_CostAggregation` — spawn 2 jobs reporting usage; assert `cost.Tracker.TotalCost()` includes both sub-session ids.
13. `TestJobApprover_ReadOnlyRejects` — `allowWrite=false`; approval request returns `Approved=false`.
14. `TestJobApprover_AnnotatesJobID` — `allowWrite=true`; assert the parent approver receives a request whose `ToolCall.Name` is prefixed with `[job:<id>]`.
15. `TestSnapshotMatchesJob` — round-trip `Job → Snapshot` is consistent, no shared mutable state.

### Bucket B — tool + integration tests

16. `TestSpawnAgentTool_NoWait` — `wait=false`; tool returns immediately with job id in metadata.
17. `TestSpawnAgentTool_Wait_Completed` — `wait=true`; tool blocks until fake spawner's `WaitForJob` returns; result content is the summary.
18. `TestSpawnAgentTool_Wait_Cancelled` — parent ctx cancelled mid-wait; tool returns an `IsError` result.
19. `TestSpawnAgentTool_Schema` — schema parses, required fields present.
20. `TestParseSlashCommand_Spawn` — variants: `/spawn hello`, `/spawn --provider gemini --model g-flash hello there`, `/spawn --write hello`, malformed flags.
21. `TestParseSlashCommand_Jobs` — `/jobs`, `/jobs 7f3a`.
22. `TestParseSlashCommand_Cancel` — `/cancel 7f3a`, `/cancel all`, `/cancel` (missing arg).
23. `TestApp_SpawnInjectsResultIntoNextTurn` — integration: spawn a job that completes, start a new turn, assert the agent's input messages include the result as a `RoleUser` entry.

### Bucket C — TUI tests

24. `TestTopbar_JobsSegment_HiddenWhenZero` and `TestTopbar_JobsSegment_ShownWhenActive`.
25. `TestJobsPanel_RendersTranscript` — show with a fake snapshot + 5 messages; view contains each author/content.
26. `TestJobsPanel_EscClosesPanel`.

### End-to-end smoke

27. `TestE2E_SpawnAgentToolViaSlashCommand` — boot a minimal `App` with a fake provider, simulate typing `/spawn 'hi'`, assert the conversation pane gets the queued echo, the topbar shows `⚙ 1 jobs`, and after the fake provider's stream completes, the conversation gets the result message and topbar drops to `0`.

## Out of scope (v1 of this feature)

- **Resumable jobs across restart.** Persisted but not re-launched.
- **Per-job worktree isolation.** Path-lock serialisation is the v1 conflict story.
- **Streaming sub-agent output into the main conversation in real time.** v1 surfaces only the final summary plus a transcript view on demand.
- **Cross-job dependencies / DAG scheduling.** Each spawn is independent.
- **Per-tool trust setting.** Trust is still all-or-nothing via `--trust`.
- **Sub-agent → user messages.** Sub-agents can't ask the user clarifying questions.
- **Sub-agent observability beyond the transcript.** No spans, no metrics export.
- **MCP-spawned sub-agents.**
