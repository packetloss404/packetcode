# Changelog

All notable packetcode changes are recorded here. The project is pre-1.0; `Unreleased` describes the current `main` branch.

## [Unreleased]

### Fixed

- Failed foreground turns and compaction pause pending prompts for explicit
  `/queue resume`. Ctrl+C cannot restart a loop from buffered success, and
  stopping a loop removes its queued iterations.
- Approval prompts explain exact-command or whole-tool scope for the current
  session, how to revoke rules, and which shell will run. Decoded command/path
  text cannot inject terminal controls into the approval display.
- Failed headless runs include saved-session recovery guidance; displayed
  errors and MCP startup reports strip terminal control sequences.
- Job recovery uses complete IDs and actionable errors. Concurrent resubmit
  requests cannot launch duplicate successors, and recovery descriptions
  distinguish abandoned work from jobs cancelled before they started.
- Stopped or disabled MCP servers show state-specific recovery instructions;
  reconnection never claims to repeat the failed tool call.
- Skill grants apply when their foreground turn starts, survive session-policy
  changes without losing user decisions, and cannot leak from queued or
  background skill loads. Trust toggles and permission resets no longer restore
  expired grants.
- Command-prefix permissions handle CMD expansion and executable aliases
  conservatively, and explicit option tokens in deny prefixes are preserved.
- Code-intelligence tools refuse dotenv secret files, including symbol lookup
  and discovered source files, using the same policy as native file reads.
- MCP restart and shutdown coordinate ownership of replacement processes.
  Blocked protocol writes honor cancellation and abort partial transports.
- Anthropic and Responses streams require their explicit completion markers;
  an early EOF no longer turns partial text or tool proposals into success.
- Completed ACP prompts and workflow drivers release their child contexts.
- Jobs reject new work when its initial record cannot be saved. Failed writes
  remain pending for retry, and shutdown reports unresolved persistence errors
  and still waits for workers on repeated calls.
- Workflow and ACP tests scale asynchronous waits; MCP parallel-startup tests
  prove overlapping handshakes with a barrier rather than an elapsed-time limit.
- Release signing summaries report whether certificates are configured without
  writing certificate contents into the GitHub Actions job summary.
- Loading multiple skills, or reloading the same skill, in one turn now releases
  all skill permission grants when the turn ends. Teardown preserves the policy
  from before the first grant and the current permission profile.
- ACP prompt cleanup now clears the active flag only once. A completed prompt
  can no longer clear a subsequent prompt's flag after sending its response,
  which could admit overlapping prompts or cause cancellation to be ignored.

### Changed

- `allowed-tools` narrowed to particular commands is now honoured for the
  shell tool instead of refused. `Bash(gh:*)` becomes a command-prefix rule on
  `execute_command` and `execute_command(git status)` an exact-command rule, so
  a skill published for the wider ecosystem pre-approves what its author wrote
  rather than nothing. The refusal claimed packetcode "does not support"
  narrowing a grant to particular arguments, which was not true —
  `permissions.Policy` has had `WithCommandPrefixRule` and `WithCommandRule`
  all along; the skill loader simply never reached them. Three of the skills in
  one real `~/.agents/skills` use this form and every one of them printed a
  warning on every startup while granting nothing.
  The scope is also the only reason a foreign tool name is translated. A bare
  `Bash` still matches no registered tool and grants nothing, because turning
  it into `execute_command` would hand a ported skill the whole shell on a
  guess; `Bash(gh:*)` is not that guess, and the most it can produce is
  permission to run `gh …` — strictly less than the bare-name grant that is
  refused. Every containment around the feature is unchanged: a project skill's
  list is still refused outright, the grant still converts ask to allow and
  never lifts a deny floor, it is still torn down when the turn ends, and a
  prefix rule still refuses to authorise a compound program, so `gh pr list &&
  rm -rf .` asks. A scope packetcode has no rule shape for — `Read(src/**)` is
  a path glob — is still refused and reported, now saying where narrowing does
  work instead of denying it exists.

## [0.6.0] - 2026-09-05

### Added

- Approval prompts are a queue bound to identity, not a single slot showing
  whichever request arrived first. The old `uiApprover` held one `active`
  envelope and returned nothing while it was live, and the App refused to
  raise a new prompt while a modal was up. Three defects followed. A
  background job whose approval was on screen when the job was cancelled left
  the modal visible forever: nothing noticed the envelope's context had died,
  so every other job's request sat in the queue and the foreground input
  stayed blocked on a prompt nobody was waiting for — and answering it with
  "don't ask again" installed a permanent session allow rule on behalf of a
  dead job. Ctrl+C during a foreground turn resolved whatever approval
  happened to be visible, including a background job's. And several waiting
  jobs could only ever be seen one at a time.
  Envelopes now carry an id, an origin, and a job id, and every decision path
  — the keypress, Ctrl+C, a live permission-mode change — resolves a specific
  id. Three independent gates make it impossible for a decision to land on an
  envelope other than the one displayed: nothing is promoted while a live
  request is on screen, the id round-trips through the UI and is compared on
  the way back, and the approver re-checks it under its own lock. Abandonment
  is noticed both ways, by an explicit notify for same-frame response and by
  the top-bar tick as a backstop for embedders that never receive the notify.
  Ordering is foreground before background, then arrival: a foreground
  approval is what stands between the user and their own turn finishing, while
  a background job keeps running either way. "Don't ask again" now installs a
  rule only when the decision actually reached a waiting caller.
  `Snapshot.AwaitingApproval`/`AwaitingAnswer`/`Blocked` separate "waiting for
  you to approve a tool" from "waiting for you to answer something", which
  Agent View and the workflow view now render distinctly — the groundwork the
  backlog names for letting a background agent ask a question. The snapshot
  policy binding is unchanged and now also applies to queued envelopes, so a
  later deny revokes a waiting background request without ever showing it,
  while a later broadening still cannot approve one.
- Oversized tool output no longer means lost tool output. Every tool result
  converges on one point in the agent loop, and only `execute_command` had a
  byte cap there; `search_codebase` and `list_directory` capped item counts,
  and MCP results were uncapped entirely, so a server returning megabytes
  landed whole in the transcript. The 64 KiB the model saw was pure
  truncation: the rest was unreachable.
  A new `internal/toolout` store sits at that chokepoint. Output over the
  limit is written to disk and the model receives a head-and-tail excerpt —
  head alone loses the verdict of a failing test run or a compiler dump —
  cut on rune and line boundaries, stating how many bytes were withheld and
  the exact call that retrieves them, so the model learns a handle exists from
  the result itself. `read_tool_output` pages through the remainder.
  The handle is an opaque random token resolved through an in-process map, not
  a path. That is the whole security argument: the model chooses this
  argument, so a handle that were a path — or that were concatenated into one
  — would hand it an arbitrary file read straight through root confinement. No
  path is ever derived from model-supplied text, and every miss, whether
  invented, malformed, pruned, evicted, or belonging to another session, is
  one indistinguishable "no longer retained" result, so nothing can be probed.
  Misses degrade rather than erroring, because a stale handle must never stall
  a turn. One store per session, per the todo-store precedent, so a background
  job can neither read nor evict what the foreground captured; bounded by a
  per-session budget with oldest-first eviction, removed when the session ends
  and pruned at startup so a killed process cannot orphan spill files. Small
  results are untouched, byte for byte. The context estimator counts the
  excerpt rather than the retained original, so spilling cannot trigger
  premature compaction.

- Workflow verifiers can now read the work they judge. A step's verifier ran
  read-only, and a read-only job is rooted at the project tree — but the work
  agent's changes live in an isolated worktree outside it, so every
  `read_file` and `execute_command` the verifier tried was refused as outside
  the project root. It could see only the work agent's own summary and
  artifact previews, which means a `pass` verdict attested to the work agent's
  self-report and never to a diff that compiles or a test that passes. That is
  the one thing a fail-closed contract must not do.
  `jobs.SpawnRequest` gained `VerifyWorktreeOf`, naming the job whose worktree
  becomes the verifier's root. It is a job id and not a path on purpose: the
  Manager answers it from the worktrees it created itself, so no caller — and
  no model — can nominate a root. It is refused outright alongside
  `AllowWrite`, because a verifier that could write would be able to fix the
  code it is judging; the workspace must match, since a worktree on a Packet
  Computer is not reachable from a local job; a local root is re-validated
  against the worktrees directory both at spawn and immediately before the
  tools are pointed at it, so a queued job cannot be redirected by a directory
  swapped for a symlink in between; and an id with no recorded worktree keeps
  the ordinary root rather than failing, because a read-only work step never
  had a worktree to begin with. Parallel work agents produce several
  worktrees with no single candidate, so that case stays unrooted rather than
  picking one arbitrarily. The verifier's prompt now says it is looking at the
  candidate worktree, so it does not mistake the change for the user's tree.
- `write_file` and `patch_file` now tell the model when an edit broke the
  file. A successful write reported success and nothing else, so the model
  learned it had broken the build a turn or two later, indirectly, or not at
  all. Diagnostics for the new contents are appended to the result.
  Deliberately syntax-only, via `go/parser` on the bytes already in hand:
  nothing shells out, and there is no opt-in to make it. Running `go build`
  behind a write would execute an unapproved command on every edit, which is
  exactly what `execute_command`'s permission gate exists to prevent — and
  the only importer that would resolve real types invokes the `go` command
  itself, so type checking is the same shell-out under another name.
  Diagnostics the file already had are not reported, because blaming the model
  for pre-existing breakage sends it editing code nobody asked it to touch;
  the pre-edit set is diffed against the post-edit set as a multiset keyed on
  message text rather than position, since an edit shifts every line below it.
  Bounded so an edit never becomes slow or noisy: the edited file only, never
  its package, files over 256 KB skipped, a 250 ms budget after which the
  diagnostics are dropped rather than delaying the result, and at most ten
  items or 2 KB. Non-Go files add nothing at all — not a "no checker
  available" line, nothing. The write still reports success and `IsError`
  stays false: a diagnostic is not a failed write. The
  `behavior.post_edit_diagnostics_disabled` config key turns it off.
- Saving `config.toml` no longer rewrites the file. `Save` re-encoded the
  whole struct, which silently dropped every key this build did not
  understand — run an older build once and a newer build's settings were gone
  — and replaced the comments, key order, and spacing of a file people edit by
  hand. The published compatibility contract asserted that packetcode never
  rewrites this file, which was not true: setting an API key or changing
  `/effort` rewrote all of it.
  Saving is now surgical. The in-memory config is diffed against the file on
  disk through the encoder, so the comparison can never disagree with the
  schema, and only the differing leaves are patched in place by byte offset.
  Unknown keys, unknown tables, and a newer `schema_version` all survive
  untouched — which is what finally makes saving safe on a file a newer build
  wrote. Comments, ordering, trailing comments, CRLF, and a missing final
  newline are preserved; a save that changes nothing writes nothing at all.
  Every patch is re-parsed and compared leaf by leaf before it replaces the
  file: the result must differ in exactly the intended settings and nothing
  else, or the write is refused and the file is left alone. Constructs with no
  single unambiguous target are refused by name rather than approximated —
  keys inside `[[array of tables]]`, a path naming a table rather than a leaf,
  and a key assigned twice. `Update`/`UpdateIn` and named setters
  (`SetProviderKey`, `SetActiveModel`, `SetProviderReasoningEffort`) express a
  change as a mutation, so a caller cannot persist unrelated in-memory drift.

- A reproducible development harness now compares headless `run` with ACP using
  alternating fresh-session pairs, an isolated temporary home, identical
  provider/model/permission inputs, wall and ACP phase timing, persisted usage,
  work counts, approval counts, and output hashes without storing response text
  or credentials. The first controlled result matched two provider calls, one
  tool call, input/output tokens, exact output, and zero approvals in all six
  samples. Median wall time was 4.152 s for `run` and 4.027 s for ACP; live
  provider variance exceeded that 3% difference, and ACP setup was about 79 ms.
  The result does not justify engine changes or a public phase-timing schema.
- `packetcode run` executes one headless agent turn for scripts, CI,
  benchmarks, and other agents without requiring an ACP client. It shares
  runtime construction with the TUI and ACP paths and supports provider/model,
  permission-mode, saved-session resume, and JSON overrides. Approval requests
  fail closed with exit 3; cancellation exits 130. Plain stdout contains only
  the sanitized final response. JSON is one versioned document with outcome,
  session/provider/model identity, output, total elapsed milliseconds, and
  per-run input/output/cache usage. No stdin prompt, trust/computer shortcut,
  or ephemeral-session mode is implied.
- Cached-input telemetry now reaches sessions, `/cost`, background jobs, and
  native/custom statusline snapshots. OpenAI-compatible providers parse
  `prompt_tokens_details.cached_tokens`, Gemini parses
  `cachedContentTokenCount`, and Anthropic preserves both cache creation and
  read counts across message deltas. Cached counts are subsets of input, not
  extra tokens; provider tests pin that contract to prevent double-counting.
- The agent loop detects repeated no-progress tool calls before the existing
  25-iteration ceiling. It signs the executed tool name, arguments, and result
  over a bounded sliding window, so changing output still counts as progress;
  foreground, background, and ACP runs share the configurable guard.
- A native bounded `fetch` tool for HTTP(S) evidence. It enforces response,
  header, timeout, and redirect limits; disables ambient proxies; validates the
  actual post-DNS address on every connection and redirect; refuses private and
  loopback targets by default; and wraps sanitized content in an explicit
  untrusted-evidence boundary. It is intentionally not a general download or
  agentic web-search surface.
- `todo_write` gives each foreground, ACP, and background session its own
  validated, bounded plan. The conversation renders the same compact block the
  model receives, and background plans persist with job evidence and appear in
  Agent View as completion counts plus the current item.
- Process-tree teardown now produces structured evidence: the mechanism used,
  whether termination was confirmed, and any surviving PIDs. Local command
  results surface it directly. POSIX release sweeps the process group and
  Windows uses Job Objects; SSH teardown remains explicitly unconfirmed instead
  of implying that a detached remote descendant stopped.
- ACP sessions expose provider/model overrides, saved-session list/load/rename,
  usage, permission-mode controls, configured MCP tools, project file search,
  Markdown prompt-command discovery/expansion, and explicit session close
  through versioned `_packetcode/*` extensions. Skills are available to the ACP
  agent through the same index/tool path as other runtimes; adding user-
  invocable skills to the command catalogue remains separate backlog work.
- A published compatibility contract: [docs/compatibility.md](docs/compatibility.md),
  backed by `internal/compat` and a test that fails when the document and the
  code disagree. One rule: an older build must never silently misread a newer
  file. Go's decoders discard fields they do not know, so a build that reads a
  newer file and writes it back has not misread it — it has destroyed what it
  could not see.
- Sessions now refuse to load, list, or save over a session written by a newer
  build. They carried a `format_version` and enforced nothing, so a newer
  session loaded looking normal and the next message wrote it back with every
  unknown field stripped — permanent loss, in a file nobody touched, with no
  error anywhere.
- `config.toml` accepts an optional `schema_version`, and packetcode now names
  the settings it did not understand — a newer schema, or a key no setting
  matches — at startup and as a `config.compatibility` check in `doctor`.
  Unrecognised keys were previously ignored in silence, which is how someone
  spends an afternoon wondering why an option does nothing. Config reports
  rather than refuses: it is a file a person typed, packetcode never rewrites
  it, and refusing to start over it would be the worse failure.

- Release artifacts are signed, attested, and checked before they ship.
  `checksums.txt` now carries a Sigstore signature made with the release
  workflow's OIDC token — no key to store or rotate. That was the missing half
  of the existing check: both installers verified an archive *against* a
  checksum file that nothing established as ours, so anyone who could serve a
  modified archive could serve the matching checksums beside it. `install.sh`
  and `install.ps1` verify the signature when `cosign` is present, say so
  plainly when it is not, and refuse outright when a signature is present and
  invalid. `REQUIRE_SIGNATURE=1` / `-RequireSignature` makes an unverifiable
  download an error.
  Archives also carry `LICENSE`, `README.md` and `CHANGELOG.md`; builds are
  reproducible (`mod_timestamp` from the commit, not the clock); and every
  release is attested with SLSA build provenance (`gh attestation verify`).
  macOS Developer ID signing with notarization, and Windows Authenticode, are
  wired and gated on their certificates being configured — the release states
  in its summary which ran, and `REQUIRE_SIGNING=1` turns a skip into a failure
  once the certificates exist. See docs/releases.md.
- CI now builds a full release snapshot on every push and asserts it: six
  archives, checksums that match, the binary and licence inside each, and the
  built Linux binary actually running. `goreleaser check` validated the config
  file and none of that, so the pipeline was previously first exercised by
  tagging — the worst moment to find a problem, because the version number is
  already spent.
- Skills now say what packetcode did not do with them. Two ecosystem syntaxes
  reach the model as literal text: `` !`gh pr diff` `` dynamic command
  injection, and positional `$1`/`$2` placeholders. Neither errors and neither
  is visibly empty, so both read as filled slots — the model answers about a
  diff it never saw. A note beneath the skill block now states that nothing ran,
  or that a placeholder is unfilled and the user's words follow. Executing and
  substituting are still refused; only the silence is fixed.
  The notes sit outside the block, where a body cannot write: its own `<skill`
  markers are defanged, so a repository cannot forge one. Placeholder detection
  runs on bodies only and skips fenced and inline code — a resource file is
  never expanded with arguments, and its `$1` is shell or SQL syntax. That
  scoping came from measurement: the first version annotated 23 real files and
  every one was a false positive.
- `allowed-tools` in skill frontmatter pre-approves those tools for the turn
  that invokes the skill, so it does not stop to ask for each. Honoured only for
  builtin and user-scope skills, never a repository's; converts "ask" to "allow"
  and never lifts an explicit deny; released when the turn ends; and a name that
  is not a packetcode tool grants nothing and is reported rather than guessed at.
- `${CLAUDE_SKILL_DIR}` in a skill body or resource expands to that skill's own
  directory when it reaches the model, so a skill can point at the files bundled
  beside it. Left literal it did not fail — it directed the model at a path that
  does not exist. Builtin skills are embedded and have no directory, so the
  variable is left alone for them; `${CLAUDE_PLUGIN_ROOT}` is not substituted,
  because packetcode has no plugin bundles for it to name.
- Agent Skills: invocation flags, `/<skill-name>`, and foreign discovery.
  `disable-model-invocation` and `user-invocable` decide who may load a skill,
  spelled as Claude Code spells them and defaulting the same way, so a skill
  written for that ecosystem behaves the same here. Both are enforced at the
  system-prompt index *and* at the `skill` tool: omitting a skill from the index
  stops the model being told it exists and does not stop the model naming one
  the user just mentioned. A flag value that does not parse keeps its default
  and is reported, because for both flags the default is the permissive answer
  and a typo must not silently grant what the author refused.
- A user-invocable skill registers as a slash command, so `/<skill-name>`
  expands its body into the turn as text — the mechanism Claude Code uses, not
  a routed tool call. The transcript shows the verb you typed while the model
  receives the framed body, because a skill body runs to 64KB and pasting one
  into the conversation pane buries the exchange under a document nobody wrote.
  Arguments reach the turn either way; `$ARGUMENTS` is honoured only for a body
  its user wrote, since a repository body must not choose where a user's words
  land. Precedence is builtin > `commands/<name>.md` > skill, and every
  displacement is reported rather than leaving an author with a file that
  silently does nothing.
- Skills are discovered from six directories: `.packetcode/skills`,
  `.claude/skills` and `.agents/skills`, at both user and project scope, with
  the native layout winning within a scope — so skills already installed for
  another agent are found where they are. A repository's foreign-layout skills
  are the exception: discovered, listed, and inert until `/skills allow <name>`,
  because that is the one directory you acquire by cloning, and a skill's
  description reaches the system prompt while its name would become a command
  you can type. Approval is per skill, per workspace, and bound to the body
  approved, so a repository that rewrites a skill afterwards is asked again
  rather than inheriting the answer.
- Skill scope precedence is now user > project > builtin, inverted from
  project-first. Only one direction of that choice has a victim: if the
  repository won, cloning a hostile one would replace the `deploy` skill you
  wrote for yourself with one you have never read, invoked by the same name and
  the same habit. Claude Code orders its scopes the same way, so published
  skills are written expecting it. Displacements are reported through a new
  warnings channel, kept apart from load errors so an ordinary override does not
  fail `packetcode skills list`.
- Provider keys can come from a `.env` file: `~/.packetcode/.env` for every
  project and `<project>/.env` for one. Precedence is a real environment
  variable, then the project file, then the user file, then `config.toml` — the
  environment wins because it is what someone set deliberately for this run.
  Values are never injected into the process environment: packetcode runs shell
  commands on the model's behalf, and a file that exists to hold API keys is the
  last thing an arbitrary subprocess should inherit. With three possible
  sources, `/provider` and `doctor` now report which one is in force, naming the
  variable or the file and never the key.
- Explicit `abandoned` terminal state for background jobs (PCMP10), so a job
  whose outcome was never observed is no longer reported as a cancellation
  somebody chose. Carries an `AbandonCause` of `app-exit`, `transport-lost`, or
  `unknown`, surfaced in Agent View, `/jobs`, the terminal conversation line,
  and the sub-agent result handed to the parent model. A cancel is now recorded
  durably *before* the job's context is cancelled, which is what makes a
  deliberate stop distinguishable from a loss at all — a context alone carries
  no cause. A job left running by an unclean exit reconciles as abandoned; one
  left queued still reconciles as cancelled, because it provably never started.
  packetcode still does not resume jobs across a restart, and `/jobs resubmit`
  continues to start a new job rather than claiming the old one continued.
- Unreadable job records are now reported instead of silently disappearing: a
  bounded stderr warning at startup and a `state.jobs.records` check in
  `doctor`, both via the new read-only `jobs.InspectRecords`.

- Foreground SSH Packet Computer workspaces: `/computers register|ssh|remove`,
  mandatory SHA256 host-key pinning, SSH-agent or identity-file
  authentication, one process-lifetime SSH/SFTP connection, root-confined
  remote read/list/search/write/patch/command tools, `--computer <name>`, and
  session-to-computer binding that refuses cross-machine resume.
- Process-lifetime remote background agents and workflows: active remote
  sessions inherit their computer; local sessions use `/spawn --computer` or
  `/workflows run --computer`; jobs persist immutable endpoint/root identity,
  own independent SSH/SFTP connections, apply restrictive computer policy,
  and require isolated remote Git worktrees before enabling writes. Durable
  reconnect remains PCMP9; remote `/undo`, code intelligence, hooks, and
  heartbeat remain deferred.
- Approved `packetcode-mcp-http-trust-v1` design gate plus a fail-closed,
  transport-independent validator for exact origins/ports, separately allowed
  network address classes, bounded bodyless same-origin GET/HEAD redirects,
  disabled ambient proxies, system-root TLS, atomically bound target-only
  environment credentials, per-call
  approval, bounded response/event/header/output sizes, bounded timeouts, and
  manual reconnect. Remote output has a credential-bound, labelled, capped
  untrusted tool boundary with exact-value, partial percent, JSON-escape, and
  base64 redaction tests; the
  Streamable HTTP transport itself remains disabled (PCH5).
- `/permissions reset` revokes remembered/manually added session rules and
  restores the startup permission policy.
- Permission transitions now fail closed: Plan mode cannot be weakened by
  allow/ask rules, explicit denies remain floors, queued approvals advance,
  and a running background job's snapshot-bound prompt cannot be silently
  broadened by foreground trust changes. Remembered background approvals are
  recorded against the real tool name; leaving Bypass preserves session rules.
- Versioned workflow schema and `/workflows validate <name>`, plus explicit
  read-only step verifiers, fail-closed structured verdicts, hard retry caps,
  verifier-feedback retries, and agent/token accounting across every attempt
  (PCH3).
- `/jobs resubmit [id]` re-runs a background job abandoned by a previous app
  exit. It starts a **new** job from the saved prompt and never claims the old
  process resumed: the abandoned job keeps its cancelled state, reason, and
  evidence, and the two records link both ways. Allowed once per job; oversize
  saved prompts are refused rather than truncated (PCH4).
- Packet Computers registry (Milestone A): versioned
  `~/.packetcode/computers/registry.json` with conservative policy defaults,
  plus read-only `/computers` and `/computers status <name>`. Registry-only —
  there is no daemon, nothing connects, and a stored status is never presented
  as a live probe (PCMP1/PCMP2).
- OpenAI Codex ChatGPT-subscription provider backed by the official Codex CLI OAuth store and Responses API, including catalog-driven reasoning effort/summary behavior.
- DeepSeek, Grok (xAI), and Mistral built-in providers, plus refreshed OpenAI, Anthropic, MiniMax M3, Ollama, and Codex model metadata.
- First-class native Ollama support with zero-config `localhost:11434`, remote-host overrides, bounded automatic `num_ctx`, `/api/show` capability discovery, keep-alive, model pull/status/PS commands, warmup, timing telemetry, and Apple Silicon memory recommendations.
- Native statusline enabled by default, plus a Claude Code-compatible custom statusline JSON snapshot.
- `@` file mentions with git-aware fuzzy completion and bounded project-root expansion.
- Live reasoning-summary rendering for providers/models that expose it.
- Prompt history recall with draft restoration and multiline-aware Up/Down behavior.
- Permission modes and footer: Manual, Accept Edits, Auto, Plan, and explicit Bypass Permissions.
- `/plan`, `/loop`, and `/workflows`; workflows support ordered phases, single-agent steps, parallel fan-out, template bindings, cancellation, live inspection, and aggregate token boundaries.
- Versioned structured stop decisions for self-paced `/loop`, retaining the
  legacy sentinel and hard 25-iteration cap.
- Per-server `/mcp restart <name>` recovery with safe dynamic tool-adapter
  replacement.
- Full-screen Agent workspace with grouped state, direct task entry, transcripts, peek, cancellation, explicit injection, and compact artifact manifests.
- Per-job and per-workflow token budgets.
- Deterministic credential-free TUI lifecycle fixtures and a PTY/cell capture harness for packetcode/Claude comparisons at fixed terminal sizes.
- Reviewed credential-free TUI text-and-style cell goldens at 72×24 and
  100×30, a post-SIGWINCH live-resize fixture, raw terminal-protocol safety
  checks, pinned PTY tooling, and CI/release gates. Supported terminal geometry
  and platform evidence are now documented explicitly.
- Runtime `/effort` control for Codex models, including catalog-advertised levels, persistent configuration, and a compact footer indicator.
- A practical user manual, advanced operator guide, printable terminal cheat sheet, and self-contained offline HTML5 manual.
- A root-level maintainer handoff covering architecture, state and security boundaries, verification, interaction caveats, and prioritized next work.

### Changed

- Asynchronous tests wait on a scaled deadline (`internal/testwait`) instead of
  a fixed one, so a loaded machine no longer fails tests that pass in isolation
  — the one thing a test must never do. A wait that takes longer than its
  baseline logs that the machine was slow rather than failing. Development-only;
  no shipped behaviour changes.
- Removed the unused `agent.ToolDecider` seam and its `uiApprover.DecideTool`
  implementation. Nothing ever consulted it: the commit that added it also gave
  the agent its own permission-policy consult, which is the one that runs. No
  behaviour changes, and the security property it appeared to provide — a deny
  rule blocking a tool that never prompts — is now covered by a test.
- Reworked the TUI toward Claude Code's flow: flat conversation blocks, `❯` submitted prompts, understated horizontal input rules, Claude-style thinking/tool markers, numbered approval choices, message spacing, full-screen Agent/Workflow views, and exact mode footers.
- Shift+Tab now changes permission mode during an active turn. The new policy applies to later tool actions and can resolve an approval already waiting; an already-running command is left alone.
- The default system prompt favors concise, terminal-friendly answers and avoids unnecessary scaffolding.
- Context estimation now includes the system prompt, transcript structure, tool schemas, and pending input using a conservative source-code-oriented estimate.
- Automatic compaction runs before an over-threshold turn, preserves complete recent tool exchanges, records summarization usage, and updates live occupancy independently from cumulative billing.
- Older oversized tool results are compacted only in the model-facing request; full content remains in persisted sessions and the UI.
- Code-intelligence result defaults and hard caps are smaller to reduce context growth.
- Anthropic requests use ephemeral cache breakpoints for stable system and tool-schema prefixes and account for cache tokens explicitly.
- High-frequency background-job activity snapshots are coalesced while queued/running/terminal transitions remain synchronous and recovery-safe.
- Documentation was consolidated around current behavior; shipped `Round N` design specs are now concise implementation/user guides.
- The built-in agent prompt now encourages parallel fan-out for independent work, while Agent View separates list shortcuts from its explicit `n` task composer.
- The model picker uses `Alt+M` (or `/model`) instead of the unsafe Bubble Tea
  v1 `Ctrl+M`/Enter alias. Global picker and clear shortcuts no longer mutate
  content beneath a visible modal or workspace.

### Fixed

- `TestStallGuard_TickKeepsAlive` and `TestStallGuard_ConcurrentTicks` blamed
  the stall guard for working. Both assert a negative — that the guard does not
  fire while Ticks keep arriving — which holds only while the ticking goroutine
  is scheduled inside the guard's window. The windows were 30ms and 40ms, so a
  loaded machine could starve a ticker past one, at which point the guard fired
  and the test reported a bug in code that had just done its job. Reproduced at
  3 failures in a batch of 25 under load. The windows now scale with
  `testwait.Factor` (the multiplier without `Timeout`'s five-second floor,
  which would turn a millisecond-scale test into a ten-second one), and the
  tests now measure the widest gap between their own Ticks: a cancellation is a
  failure only when every Tick was demonstrably inside the window, and is
  otherwise reported as the machine being slow. Verified both ways — with Tick
  stubbed out both tests fail and name the gap, and under an impossibly narrow
  window they skip rather than lie. 900 executions under six concurrent suite
  runs are clean.
- **Job records could silently disappear on Windows.** Windows opens deny by
  default, and Go's `os.ReadFile` asks for `FILE_SHARE_READ|WRITE` but not
  `FILE_SHARE_DELETE`. So while any reader holds a job record open, the rename
  that publishes a new version of it fails with `ERROR_ACCESS_DENIED`, and
  while that rename is in flight a reader fails with
  `ERROR_SHARING_VIOLATION`. Both are "try again in a moment", which is what
  POSIX does implicitly; both were being treated as permanent. Measured on one
  contended path: 809 of 2000 renames and 857 concurrent reads failed. The
  consequences were real — a terminal job state whose write failed is
  discarded silently by every `_ = m.savePersistedSnapshot...` call site, and a
  record whose read failed is reported as *malformed* by `decodeRecordFile` and
  dropped from the reload entirely. `atomicfile` now waits such a collision out
  (ten attempts, 10ms apart, Windows only; the loops compile to a single pass
  everywhere else) and exposes `atomicfile.ReadFile` for the read half, which
  the job record readers use. A regression test contends a reader and a writer
  on one path and fails without the retry.
- `TestResubmit_SpawnsNewJobAndLinksBothWays` was flaky on Windows CI as a
  result of the above, plus a mistake of its own: it waited for `Manager.Get`
  to report a terminal state and then read the record off disk, but
  `markTerminalCause` flips the in-memory state under the manager lock and
  persists only after releasing it. Reading inside that window found the
  successor still `running`, which sent the loader down its reconcile-and-
  rewrite path against a file the manager was writing at the same instant. It
  now waits for the record itself, and asserts the loader reported nothing
  unreadable — the discarded `unreadable` return is why the failure only ever
  said "map does not contain <id>".
- `TestRunUserPromptSubmit_CollectsStdout` failed on `test (windows-latest)`
  about two runs in three and never on a developer machine. The cause was
  measured rather than guessed: on four GitHub `windows-latest` runners the
  first `powershell -Command "exit 0"` in a job took 4.33-4.87s while every
  later one took 0.16-0.19s, and a bare `cmd.exe` CreateProcess at the same
  instant took 15-38ms. The machine was not busy, the stdin plumbing cost
  nothing (170ms with no stdin, 175ms with it attached, 180ms running the full
  hook script) and `internal/hooks` added nothing (184ms with the tree-cancel
  wiring, 194ms end to end through `Runner`). It was Windows PowerShell's own
  start-up, paid once per machine, and it landed entirely on whichever test
  spawned first -- whose 5s budget sat a few hundred milliseconds above a 4.6s
  constant. `internal/hooks` now pays that cost in `TestMain`, before any
  test's budget is running, and the budgets themselves scale through
  `internal/testwait` like every other deadline in the suite. `pwsh` was
  measured as an alternative and is slower warm (265-285ms), so the tests still
  run the interpreter production uses. The same trap was latent in
  `internal/statusline` and `internal/jobs`, which spawn the same interpreter
  and were surviving only because `internal/hooks` happened to run first; their
  budgets scale now too.
- Bugfix pass, 2026-09-03. Six read-only reviewers swept the packages and
  the confirmed findings were fixed with regression tests:
  - **Permissions.** A session or skill allow rule for `execute_command`
    returned before the deny-floor check ran, so `sh -c 'git push'` was
    allowed outright under a configured `git push` deny once any allow rule
    existed. Escalation now runs on every allow, whichever path produced it.
    Deny prefixes also compared raw words, so `"git" push`, `gi""t push`, and
    `git -C . push` were clean misses; quotes are now seen through, an
    unresolvable word or an option between the prefix words escalates when
    the denied word is still ahead, and the scripting interpreters and
    Windows shells count as indirection like `sh -c`.
  - **Approval prompt.** Tool arguments were rendered without the terminal
    sanitizer the conversation already uses, so an escape sequence inside a
    proposed command could erase the part of the line being approved.
  - **Cost.** `cost.Tracker` still billed cached input at the full rate after
    sessions and jobs were corrected, so `/cost` and the statusline disagreed
    with the session total by ~6x on cache-heavy runs. It now shares
    `provider.EstimateCost`, with provider-specific cache rates plumbed to the
    tally and to background jobs.
  - **Providers.** Anthropic never read `stop_reason`, so a reply cut off by
    `max_tokens` was persisted as complete and a truncated tool call surfaced
    as "arguments are invalid JSON"; it is now an error naming the cause, as
    it is for the other adapters. Gemini `SAFETY`/`RECITATION`/`MAX_TOKENS`
    finishes and prompt-level blocks ended as a silent empty turn; they are
    errors too. The one bare channel send left in the Responses parser goes
    through `StreamSink`. `/compact` no longer sends an empty summary request
    when the cut lands inside the first tool group.
  - **Config.** `PACKETCODE_CONDUIT_SHADOW` and the three
    `PACKETCODE_SUGAR_*` envelope variables were written into the stored
    fields, so the next `/provider`, `/effort`, or key save made a one-off
    environment override permanent in `config.toml`. They are held as
    overrides now, like the enabled flags already were.
  - **Jobs.** A write job launched from a repository sub-directory got the
    worktree top level as its root while a read-only sibling kept the
    sub-directory, so relative paths in the prompt resolved differently by
    mode. `Spawn` now counts its worker before releasing the lock `Shutdown`
    checks, closing a WaitGroup ordering race that could start a worker
    nothing waited for.
  - **Persistence.** `config.toml`, job sub-session transcripts, and the
    computers registry are written through `atomicfile` with fsync, like
    sessions and job records. The registry also keeps rows this build cannot
    read and writes them back, instead of deleting a newer build's record the
    first time an older build saves.
  - **Local shell on Windows.** os/exec re-quoted the `cmd /C` argument, so
    `echo "hi"` printed `\"hi\"` and the PowerShell invocation the tool
    describes ran nothing; cmd.exe now receives the line verbatim via
    `/S /C`. A command that exits 0 but leaves a background child holding the
    pipe is reported with its real exit status rather than `-1` and a
    "WaitDelay expired" error.
  - **Tools.** `read_file` on an empty file is no longer an error. The
    Go-fallback search no longer follows file symlinks out of the root. An
    unterminated tag in `fetch` HTML extraction was rescanned from every
    later `<`, quadratic to ~48 s of CPU at the body cap; it now ends the
    scan.
  - **TUI.** A self-paced `/loop` restarted immediately after Ctrl+C or a
    provider error; it now stops. An interval loop no longer queues another
    body on every tick while the previous one is still streaming. `/clear`
    sized the fresh conversation at width 0, wrapping at 78 columns. Releasing
    a skill grant restores the pre-grant rules at the profile in force now, so
    a mid-turn switch to plan mode is not reverted underneath the plan flag.
  - **MCP and run.** `/mcp restart` gave up when closing the old process
    returned an error, which is exactly the hung server a restart is for.
    `packetcode run` no longer turns a slow MCP shutdown into exit 1 with the
    answer withheld, prints flag-error usage to stderr, and reports an unknown
    `--provider` as unknown rather than "no model is configured".
- `packetcode --help` now lists all commands. It printed flags and nothing
  else, so the four pre-existing commands — `doctor`, `skills`, `acp` and
  `sugar` — were reachable only by reading the source — a diagnostic command
  nobody can find is not much of a
  diagnostic. `help`, `-h` and `--help` now exit 0 rather than 2, since asking
  for help is not an error.
  Dispatch and help read one table instead of two hand-maintained lists, which
  is how they came to disagree; a command can no longer exist in one and be
  missing from the other.
- Cost estimates no longer bill cached input at the full input rate. Every
  provider serves cached tokens at a fraction of fresh ones and reports how
  many; packetcode recorded that number faithfully and then multiplied the
  whole cache-inclusive input count by the standard price. Measured over a
  six-task benchmark where 93% of input came from cache, the displayed figure
  was roughly **6x the real bill** — a tool that is cheap reporting itself as
  expensive. The counts were always there; only the arithmetic was wrong.
  Fixed in `internal/session` and `internal/jobs`, which carried independent
  copies of the same formula, through one shared `provider.EstimateCost`.
  Cache reads default to a tenth of the input rate and writes to par;
  Anthropic states its own, since it charges a premium for cache writes.


- Session and job records are fsynced before the rename that publishes them.
  The rename alone is atomic for a reader, but it could reach the disk ahead of
  the bytes it names, so a crash could leave a correctly-named, empty file —
  the conversation, for a session, and the state a restart reconciles from, for
  a job. Measured cost is about 130µs on a small session file.
- A session file that cannot be read is now reported by `/sessions` and
  `/resume` instead of silently missing from the list, where it was
  indistinguishable from a session that never existed.
- `session.Manager.New` and `Load` return a copy rather than the manager's live
  session, matching `Current`. Callers could previously mutate mutex-guarded
  state from outside the mutex, and the change would have persisted.
- A finished background job whose only output was artifacts reported none. The
  job line returned early when there was no summary, error or worktree, which
  skipped the artifacts line below it — losing both the description of what the
  job produced and the `/agents <id>` pointer for reading it.
- Provider stream parsers can no longer be stranded by a consumer that stops
  reading. Every event now goes through a sink bound to the turn's context, so
  a send cannot block indefinitely on a full channel; previously a cancelled
  turn whose consumer never drained again left the parser goroutine holding the
  response body and the stall guard for the life of the process. Applies to the
  OpenAI-compatible, Responses, Anthropic, Gemini, and Ollama parsers.
- An MCP server that died with a non-zero exit status could be reported as
  `exited: EOF`. A child's stdout closes before it is reaped, so the reader
  usually won the race and recorded EOF as the cause; the reaper corrected it a
  moment later, but anything asking right after seeing the server was dead got
  the wrong answer. The reader now records only that the server exited, and the
  reaper supplies the status. `/mcp` and the MCP report wait for it.
- A self-paced `/loop` started while a turn was streaming now runs. The
  streaming guard sat before the line claiming loop ownership, so the loop
  registered, appeared in `/loop list` forever, and did nothing — and slash
  commands dispatch during a stream, so typing `/loop <prompt>` mid-turn hit
  this every time. Ownership now travels with the queued turn and is claimed
  when that turn starts. The queued body also carried no iteration instruction,
  leaving the model with no way to declare the work finished; the turn is built
  once now, so the queued and immediate paths cannot differ.
- OpenAI models that refuse function tools on `/v1/chat/completions` are routed
  to `/v1/responses` instead. Selecting `gpt-5.6-sol` previously failed every
  turn with a 400, because packetcode sends tools on every turn and that model
  only accepts them on the other endpoint. Routing is per request, so switching
  models mid-session takes the endpoint with it. The `-pro` family is no longer
  hidden from the catalog either: it was excluded because chat-completions was
  the only endpoint spoken here, and hiding a model packetcode can drive leaves
  capability on the floor.
- The Responses API's own error message reaches the user. An error nested under
  `error` in the SSE frame was reduced to `codex stream error`, so a message
  naming the exact fix — with the URL — was replaced by a phrase that says
  nothing. Fallback text also names the backend the user configured rather than
  always saying `codex`.
- YAML block scalars in skill frontmatter parse. `description: >-` with the text
  on following indented lines is how most published skills write a description;
  the line-based parser read the value as the literal `">-"`, which passed every
  validation and produced a meaningless index entry. Continuation lines are now
  folded in and consumed, so a colon inside the prose is prose rather than a key
  that sets a flag its author never wrote.
- Skill directories that resolve to the same place are scanned once. Running
  packetcode from a home directory that has skills in it reported every
  malformed skill twice, made every skill shadow itself, and — worse — labelled
  the user's own skills untrusted repository content on the project pass.
- An abandoned background agent is no longer reported as a success. Two
  workflow gates, `spawn_agent`, `collect_agent_results`, and Agent View's
  grouping all tested for known failure states, so any terminal state they did
  not enumerate fell through as passing — a workflow step whose agent was lost
  would advance the run, and a lost sub-agent was handed to the parent model as
  a completed one. They now test `State.IsSuccess()` instead.
- A cancelled job no longer discards the error that actually stopped it. The
  terminal-state precedence dropped `lastErr` whenever the context was also
  cancelled, so a dead transport was recorded with no error text at all.
- Deny permission rules no longer fail open on shell metacharacters. A
  `command_prefix` rule refuses to match anything but a single simple command,
  so that a prefix can never *authorize* a larger shell program — but deny-floor
  rules were evaluated through that same predicate, where "cannot prove it
  matches" became "does not match". A rule denying `git push` held against
  `git push origin main` and fell through on `git push origin main; :`,
  `true && git push origin main`, and `sh -c 'git push origin main'`. Deny
  matching now evaluates each simple command inside a compound command
  separately, ignores redirection targets, tolerates leading `NAME=value`
  words, and escalates to an approval prompt when a stage hands its arguments
  to an interpreter or a script it cannot see through. Escalation only
  tightens: an existing `ask` or `deny` is never loosened, and a compound
  command provably unrelated to the rule still runs without a prompt.
- Background jobs are scoped to the project that created them. The jobs
  directory is shared across every project on the machine, so starting
  packetcode in one project rewrote another project's queued and running jobs
  as abandoned, made them eligible for `/jobs resubmit` — which could launch a
  duplicate of a job that was still running, worktree and all — and left the
  two instances overwriting each other's records. Job records now carry the
  project root that created them, and another root's live jobs are left
  strictly alone. Records written before this change carry no root and are
  still recovered.
- Job records are versioned and no longer disappear when unreadable. A corrupt
  or future-versioned job file was skipped silently, so abandoned work was
  reported as nothing at all. Records now carry `format_version`; unreadable
  ones are collected and exposed rather than dropped, an unrecognised state is
  reported instead of being flattened to `failed`, and a record written by a
  newer packetcode is never overwritten by an older one.
- MiniMax interleaved-thinking models (M2.x/M3) keep their reasoning chain
  across tool calls. Their thinking arrives inline in `content` wrapped in
  `<think>` blocks; it was rendered as ordinary assistant text and then
  discarded on every tool-calling turn, which MiniMax's tool-use guide warns
  degrades multi-turn tool use. Reasoning is now split out of the transcript
  into the reasoning stream, persisted on the assistant message, and replayed
  on the next request with the tags preserved exactly. Content sharing a frame
  with a tool call is no longer dropped on these providers. Other
  OpenAI-compatible backends are unaffected: a literal `<think>` in prose stays
  visible, and stored reasoning is never sent to a provider that did not
  produce it.
- Context gauges now show current context occupancy instead of cumulative session input, keeping the native and custom statuslines aligned and allowing occupancy to drop after compaction.
- Foreground permission-policy swaps are synchronized with the running agent; background job policy startup reads are synchronized as well.
- Workflow cancellation closes spawn/register races, cancels sibling fan-out jobs on failure, drains terminal states with bounded waits, and reports malformed workflow files instead of silently falling back.
- Provider/tool cancellation stops the generic thinking spinner on first visible progress and distinguishes cancellation from provider errors.
- Job persistence flushes pending snapshots on shutdown.
- Symlink-escape scans and file-as-parent write errors behave consistently on macOS.
- MCP JSON-RPC request IDs correctly handle every `int64` value.
- Prompt editing now respects the caret for file mentions, supports portable multiline bindings, honors `max_input_rows`, keeps history navigation at visual-row boundaries, and clears a draft before Ctrl+C exits.
- Terminal-originated text is sanitized before rendering, preventing tool output from enabling mouse modes, writing the clipboard, or corrupting the TUI; split UTF-8 and progress-line output remain intact.
- Statuslines stay on one row at narrow widths, background transcripts refresh without losing scroll position, and reasoning activity is visible while an agent is thinking.
- Built-in statusline segments are recomposed after a terminal resize instead
  of retaining a wide-layout selection and clipping it at the new width.
- Git branch refreshes no longer block the Bubble Tea event loop, and approval prompts wake the UI on demand instead of forcing continuous idle redraws.
- Queued prompts preserve leading indentation and trailing newlines exactly.
  Secret-entry prompts own the terminal geometry instead of rendering above an
  extra composer/status footer, and inactive composers render blurred beneath
  overlays.

## [0.5.1] - 2026-05-30

### Added

- Retry with backoff and jitter for transient provider failures, honoring `Retry-After` and cancellation.
- Per-call provider stall timeout.
- Whitespace- and line-ending-tolerant unique matching for `patch_file`.
- Incremental `execute_command` stdout/stderr rendering with bounded final output.

## [0.5.0] - 2026-05-29

### Added

- Multi-provider agent loop, native project tools, approvals, sessions, cost tracking, undo, context compaction, and git-aware status.
- Bubble Tea TUI with terminal scrollback, provider/model pickers, slash completion, queued prompts, custom prompt commands, hooks, statusline, and themes.
- Background agents, Agent View, isolated write worktrees, transcripts, cancellation, and explicit result injection.
- MCP stdio tools registered as provider-safe aliases.

### Changed

- Provider/model completion opens interactive pickers rather than requiring guessed identifiers.
- Topbar/statusline include operation state, elapsed time, queue depth, context, and job telemetry.
- Approval and transcript views include clearer source, runtime, and worktree context.

### Fixed

- Custom statusline refreshes are throttled.
- MCP diagnostics display the actual provider-safe aliases.
- Process startup/cancellation tests are reliable across supported operating systems.
