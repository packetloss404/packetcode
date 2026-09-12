# Operational runbooks

Started at the 2026-09-05 security-audit merge (`e0ce868`); recovery and approval
guidance updated 2026-09-11. Original line references identify the audit's
evidence and may have moved. Check the current implementation and
[developer guide](development.md) before changing behavior.

packetcode runs as your user on your machine. There is no server to restart, no
service to fail over, and no shared state: every runbook below is something one
operator does to one workstation.

**Conventions.** Commands are bash. Where PowerShell differs materially the
PowerShell form follows. `$PC` means the packetcode data home: `~/.packetcode`,
or whatever absolute path `PACKETCODE_HOME` names
(`internal/config/paths.go:20-32`). Find it with:

```bash
packetcode doctor --json | grep -m1 effective_home
```

**Start here for almost anything.** `doctor` executes no hooks, no MCP servers,
and no provider requests (`cmd/packetcode/doctor.go:984-987`), so it is always
safe to run:

```bash
packetcode doctor
packetcode doctor --json
packetcode doctor --check config,providers,mcp
```

It exits 1 if any check failed, 0 otherwise (`doctor.go:110-113`).

---

## R1. A provider setting seems to do nothing

**Symptom.** You set something in `config.toml` and packetcode behaves as if you
had not.

**Check.** Since the audit merge, packetcode names these at startup and in
doctor (`internal/config/validate.go`, `doctor.go` check `config.validation`):

```bash
packetcode doctor --check config
```

Three causes, distinguished by the message:

| Message | Cause |
| --- | --- |
| `no setting matches <key>` | typo, or a setting from a newer build (`config.go:520-549`) |
| `declares schema_version N but this build understands 1` | you ran a newer packetcode against this config |
| `will fail or do nothing` | the setting parsed but names something absent, such as an unset `env_from` variable |

**Fix.** Correct the named key. packetcode never rewrites `config.toml`
wholesale; saving edits only the settings it means to change, in place
(`internal/config/save.go:1-15`), so unknown keys and comments survive.

**Verify.** `packetcode doctor --check config` reports
`every config setting is understood by this build` and `config settings validate`.

---

## R2. Set, rotate, or remove a provider API key

**Precedence, strongest first** (`internal/config/config.go:707-736`): a real
environment variable, then `.env`, then `config.toml`. A stale `.env` cannot
override what you just exported.

**Set for one session:**

```bash
export PACKETCODE_OPENAI_API_KEY=sk-...
packetcode
```

**Set durably without putting it in config.toml:** add it to `$PC/.env` (applies
everywhere) or `<project>/.env` (that project only). packetcode never exports
these into subprocesses, hooks, or MCP servers (`internal/config/dotenv.go:19-26`),
and since the audit merge the model cannot read them either
(`internal/tools/secretfiles.go`).

**Set through the UI:** `Ctrl+P`, select the provider, `Ctrl+A`, paste. It is
validated against the provider before it is saved
(`internal/app/provider_key.go:110-116`).

**Confirm which one is in force without printing it:**

```bash
packetcode doctor --check providers
```

It reports `env:<VAR>`, `dotenv:<path>`, or `config:<path>`
(`doctor.go:592-636`).

**Rotate.** Set the new value in the same place, then start packetcode. Nothing
caches a key across process starts.

**Remove.** Delete the `api_key` line from `config.toml`, or unset the variable.
`doctor` will then report `credential missing` for that provider.

---

## R3. A key may have been exposed

**Assume exposure if** the key was in a `?key=` URL in a log from a build before
the audit merge (fixed for Gemini in `dd71133`), was pasted into a prompt, or
appeared in a session transcript.

**Do, in order:**

1. Revoke and reissue at the provider. Nothing local can undo exposure.
2. Find local copies:

```bash
grep -rl "sk-" "$PC"/sessions "$PC"/config.toml "$PC"/.env 2>/dev/null
grep -rn "<key-prefix>" "$PC" 2>/dev/null
```

3. Sessions hold full tool output (`internal/session/session.go:682-711`), so a
   transcript that read a secret still holds it. Delete the affected session:

```bash
packetcode   # then: /sessions   to find the id
rm "$PC/sessions/<id>.json"
rm -rf "$PC/backups/<id>"
```

   Or from inside: `/sessions delete <id> --yes`.
4. Check the diagnostic log if it was enabled. It never records keys, bodies, or
   query strings by construction (`internal/diaglog/diaglog.go:120-152`), but
   delete it anyway if you are unsure: `rm "$PACKETCODE_LOG_FILE"`.
5. Set the new key per R2.

---

## R4. Provider requests are failing

**Check.** Turn on the diagnostic log and reproduce (R11). Every provider HTTP
attempt is recorded with method, redacted URL, attempt number, and either the
status or the transport error (`internal/provider/retry.go`, event
`provider.http`).

```bash
export PACKETCODE_LOG_FILE="$HOME/.packetcode/logs/pc.jsonl"
packetcode run --permission-mode read-only "say ok"
grep provider.http "$PACKETCODE_LOG_FILE" | tail -5
```

| Status | Meaning | Action |
| --- | --- | --- |
| 401 / 403 | credential wrong or revoked | R2 |
| 429 | rate limited | packetcode already retries with backoff and honours `Retry-After` (`retry.go:88-100,164-186`); raise `provider_max_retries` if it gives up too early |
| 5xx | provider side | retried automatically; if persistent, switch provider with `/provider` |
| transport error | DNS, TLS, proxy, firewall | test the base URL with `curl -sS -o /dev/null -w '%{http_code}\n' <base_url>/models` |

**Stream goes silent mid-turn.** The stall guard aborts after
`provider_stall_timeout` seconds, default 60 (`cmd/packetcode/runtime.go:311-322`).
Raise it under `[behavior]` if a slow model is being cut off.

---

## R5. Codex subscription authentication broken

**Symptom.** `codex` provider reports no access token, or every request 401s.

**Check.**

```bash
packetcode doctor --check providers
ls -l "${CODEX_HOME:-$HOME/.codex}/auth.json"
```

packetcode reads, and refreshes in place, the official Codex CLI store
(`internal/provider/codexauth/codexauth.go:84-93,198-242`). It never runs a
login flow of its own.

**Fix.**

```bash
codex login          # choose "Sign in with ChatGPT", not API key mode
```

An `auth.json` holding only `OPENAI_API_KEY` is reported as such
(`codexauth.go:115-121`); use the `openai` provider for key-based billing
instead.

**Verify.** With the log on, a refresh emits `codex.token_refresh` with the
store path and whether the refresh token rotated. It never records a token.

---

## R6. An MCP server will not start, or died

**Check.**

```bash
packetcode doctor --check mcp     # static config only: command resolvable, timeout, auth summary
```

Then in the TUI:

```text
/mcp                 table: state, tool count, pid, command
/mcp status <name>   last error, server name/version, timeout
/mcp logs <name>     redacted tail of $PC/mcp-<name>.log
```

**Common causes.**

| Symptom | Cause | Fix |
| --- | --- | --- |
| `command not runnable` | not on PATH | install it, or use an absolute `command` |
| `initialize timeout` | server slow to start | raise `timeout_sec` under `[mcp.<name>]` |
| `server does not expose tools` | server has no tools capability | packetcode only consumes tools (`docs/feature-mcp.md`) |
| `env_from names X, which is not set` | missing secret | export it before starting packetcode |
| starts, then `exited` | server crashed | `/mcp logs <name>` |

**Recover without restarting packetcode:**

```text
/mcp restart <name>
```

That replaces the process and its tool adapters using the configuration loaded
at startup (`internal/mcp/manager.go:153-194`). **Configuration changes still
need a full restart** — the manager keeps its startup config deliberately.

**Note.** MCP servers are trusted local programs, not sandboxed. They inherit a
small environment allowlist plus your explicit `env`/`env_from`
(`internal/mcp/process.go:96-127`).

---

## R7. A background job is stuck, or was abandoned

**Inspect.**

```text
/jobs                list
/jobs <id>           live transcript
/agents              full-screen view, grouped by state
```

**Cancel:** `/cancel <id>` or `/cancel all`.

**After an unclean exit.** A job that was *running* recovers as `abandoned` with
a cause; one that was only *queued* recovers as `cancelled`, because it provably
never started (`internal/jobs/persistence.go`). packetcode does not resume jobs
across a restart, by decision, not by omission
(`docs/packet-computers-loop.md:30-46`).

**Re-run one:**

```text
/jobs resubmit        list candidates
/jobs resubmit <id>   start a NEW job from the saved prompt
```

The original keeps its state, reason, artifacts, and worktree; the two records
link both ways. Allowed once per job; a saved prompt over 32 KiB is refused
rather than truncated.

**Records this build cannot read** are reported rather than silently dropped:

```bash
packetcode doctor --check state.jobs.records
```

That means a record written by a newer packetcode. The files are untouched;
upgrade, or move the named files out of `$PC/jobs/`.

---

## R8. A write agent left a worktree

packetcode never merges or deletes these (`docs/manual.md:437`).

**Find it.** The path is printed in `/jobs <id>` and Agent View, and is
`$PC/worktrees/<repo-key>/<job-id>` on branch `packetcode-job-<job-id>`, based
on the committed `HEAD` at spawn time.

```bash
git -C <worktree-path> status --short
git -C <worktree-path> diff
git -C <worktree-path> log --oneline -5
```

**Take the work.** If the agent committed:

```bash
git cherry-pick <commit>
```

If it left uncommitted edits, review and commit them in the worktree, or copy
the diff across by hand.

**Remove it when done:**

```bash
git worktree remove <worktree-path>
git branch -D packetcode-job-<job-id>
```

Use `git worktree remove --force` only after checking the diff: it discards
uncommitted work.

**Reclaim space:**

```bash
git worktree list
du -sh "$PC/worktrees"/*
```

---

## R9. Disk usage under the data home

Nothing here is pruned automatically except spilled tool output.

| Path | Grows with | Safe to delete |
| --- | --- | --- |
| `$PC/sessions/` | every turn | yes, loses transcripts |
| `$PC/backups/<session>/` | every native write | yes, loses `/undo` history for that session |
| `$PC/jobs/` | every background job | yes, loses job records |
| `$PC/worktrees/` | every write job | **check the diff first** (R8) |
| `$PC/tool-output/` | oversized tool results | yes; pruned after 24h anyway (`internal/toolout/store.go:44,159-181`) |
| `$PC/mcp-*.log` | MCP stderr, append-only | yes |
| `$PC/cost-tally.json` | usage records | use `/cost reset --yes` |

```bash
du -sh "$PC"/* | sort -h
```

**Known gap.** Backups are never pruned and `BackupManager.Cleanup` has no
production caller (audit item H-08). Delete `$PC/backups/*` for sessions you no
longer need.

---

## R10. `/undo` did not restore what you expected

`/undo` restores the most recent backup taken by `write_file` or `patch_file`
(`internal/session/backup.go`). It is **not** general undo:

- The stack is in memory and resets when packetcode restarts (`backup.go:19-21`).
- Deletions and renames made by `execute_command` are not backed up at all and
  are unrecoverable from packetcode.
- Remote (SSH) writes have no backups (`internal/tools/write_file.go:59-61`).

**Use git as the real safety net.** Commit before a broad change; for anything
destructive prefer `/plan` first.

---

## R11. Turn on the diagnostic log

Off unless `PACKETCODE_LOG_FILE` names an **absolute** path
(`internal/diaglog/diaglog.go:88-90`).

```bash
export PACKETCODE_LOG_FILE="$HOME/.packetcode/logs/packetcode.jsonl"
packetcode
```

```powershell
$env:PACKETCODE_LOG_FILE = "$env:USERPROFILE\.packetcode\logs\packetcode.jsonl"
packetcode
```

One JSON object per line, appended, opened requesting mode 0600 (best-effort on
Windows, where inherited ACLs govern, as for `config.toml`).

| Event | Answers |
| --- | --- |
| `startup` | which build ran |
| `provider.http` | which endpoint, which attempt, what status |
| `codex.token_refresh` | when auth was refreshed and whether the token rotated |
| `policy.decision`, `approval` | what the policy decided per tool call and what was answered |
| `acp.session_new`, `acp.session_load`, `acp.session_close`, `acp.permission` | what an ACP client did |
| `mcp.spawn` | which servers started, with how many tools |
| `fetch` | every URL the model reached |
| `ssh.connect` | which computer, which stage failed |
| `hook.run` | which hook ran, how it exited |

Never recorded: bodies, headers, tool arguments, prompts, keys, tokens, URL
query strings.

**Read it:**

```bash
grep -E '"level":"WARN"' "$PACKETCODE_LOG_FILE" | tail -20
python -c "import json,sys;[print(json.loads(l)['msg']) for l in open(sys.argv[1])]" "$PACKETCODE_LOG_FILE"
```

**Rotate it yourself.** Nothing in packetcode rotates or prunes it.

---

## R12. A tool was denied, or allowed, unexpectedly

**Inspect the live policy:**

```text
/permissions
/permissions explain execute_command
/permissions profiles
```

**Rules of the system** (`internal/permissions/policy.go`):

- An explicit `deny` is a floor. It is checked before every other rule
  (`policy.go:288-302`) and a later `allow` never lifts it.
- A deny rule that *cannot be proven* not to match escalates an allow to a
  prompt rather than falling through (`policy.go:304-316`). Interpreters
  (`sh -c`, `sudo`, `python`) and script paths trigger this.
- Among non-deny matches, the later rule wins.
- The `read_only`/Plan profile denies every non-read-only tool regardless.
- `bypass` allows everything except explicit denies.

**Undo session changes:**

```text
/permissions reset      revoke remembered rules, restore the startup policy
/trust off              leave bypass, keep session rules
```

**Note.** "Allow exact command this session" matches the **exact command
string** in any working directory, never a command family. Other tools offer
"Allow this tool this session", covering all its arguments and paths
(`internal/app/approval_remember.go`). Running jobs keep their captured policy.

---

## R13. An SSH Packet Computer will not connect

**Check the record:**

```text
/computers
/computers status <name>
```

Status is a stored record, never a live probe: there is no heartbeat
(`internal/computers/computer.go:34-44`).

**Requirements, all mandatory** (`internal/computers/ssh_backend.go:53-144`):
a SHA256 host-key fingerprint, an SSH user, and an absolute POSIX project root.
An unpinned host is refused outright (`ssh_backend.go:189-201`).

```text
/computers ssh <name> user@host /abs/remote/root --fingerprint SHA256:...
```

Get the fingerprint from the host itself, not from packetcode:

```bash
ssh-keyscan -t ed25519 host 2>/dev/null | ssh-keygen -lf -
```

**Diagnose a failure.** With the log on, `ssh.connect` names the stage:

| `stage` | Cause |
| --- | --- |
| `dial` | host unreachable, wrong port, firewall |
| `handshake` | host key mismatch (refuse and investigate), or no usable key |

Authentication uses `SSH_AUTH_SOCK` or the configured identity file; an
encrypted key that is not in the agent is reported as such
(`ssh_backend.go:203-265`).

**Caveat.** Remote execution lives only as long as the local process, and remote
teardown after cancellation is reported as unconfirmed because SSH offers no
process-group kill (`ssh_backend.go:545-572`). A detached remote descendant may
need manual cleanup on that host.

---

## R14. Respond to a dependency vulnerability report

```bash
make vulncheck        # go run golang.org/x/vuln/cmd/govulncheck@v1.3.0 ./...
```

Only advisories govulncheck reports as *reachable* matter; it distinguishes
those from ones merely present in the module graph.

**Order of action:**

1. If the fix is in a module and keeps the module's `go` directive at or below
   this project's (`go.mod:3`), bump it:

```bash
go get golang.org/x/crypto@<version>
go build ./... && go test ./... && make vulncheck
```

2. If the fix requires a newer Go directive, that is a project decision, not a
   dependency bump: it changes what every contributor must install. See
   `docs/audit/patches/P10b-x-crypto-v0.56.0-go1.26.patch` for the prepared
   version of exactly this case.
3. If the fix is in the standard library, it is a **toolchain** upgrade: install
   the newer Go and change `GO_VERSION` in `.github/workflows/ci.yml` and
   `release.yml`. No code change.

**Do not** bump these without the stated extra check (audit section 8.5):
BurntSushi/toml (run `internal/config` tests), bubbletea/lipgloss/bubbles (v2 is
a breaking migration with its own golden harness), mattn/go-runewidth (re-run
`make tui-golden-check`).

---

## R15. Verify a downloaded release

```bash
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig \
  --certificate checksums.txt.pem \
  --certificate-identity-regexp 'https://github.com/packetloss404/packetcode/\.github/workflows/release\.yml@refs/tags/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums.txt
gh attestation verify packetcode-linux-amd64.tar.gz --repo packetloss404/packetcode
```

`install.sh` and `install.ps1` do the first two for you when `cosign` is
present. **They proceed without it unless you insist**
(`install.sh:98-99`, `install.ps1:53-54`):

```bash
REQUIRE_SIGNATURE=1 curl -fsSL .../install.sh | bash
```

```powershell
.\install.ps1 -RequireSignature
```

A signature that is present and *invalid* always aborts, in both scripts.
`bash scripts/test-install-verify.sh` exercises those branches against stubs.

---

## R16. Verify the build after a change

```bash
go build ./...
go vet ./...
go test -race -count=1 ./...     # make test
make vulncheck
bash smoke.sh                     # make smoke-e2e
```

For TUI changes, additionally:

```bash
make tui-golden-check            # POSIX only; needs python3 + scripts/requirements-tui.txt
./bin/packetcode --tui-fixture=normal
```

**If `internal/jobs` hangs or fails**, rerun it alone before believing it:

```bash
go test ./internal/jobs/ -count=1 -timeout 420s
```

Its tests are asynchronous and wait on real timers scaled by
`PACKETCODE_TEST_TIMEOUT_SCALE` (default x10, `internal/testwait`). Heavy
concurrent compilation, or a slow PowerShell start on Windows, starves them. If
it still fails, bisect in a throwaway worktree before blaming your change:

```bash
git worktree add --detach /tmp/pc-bisect <previous-commit>
go test ./internal/jobs/ -count=1 -run <TestName> -timeout 240s
git worktree remove /tmp/pc-bisect
```

**If a Windows test that runs a hook or status line command fails on its
timeout**, suspect the interpreter before the code. The first
`powershell -Command` on a machine costs 4.3-4.9s on GitHub's `windows-latest`
runners; every later one costs under 0.2s. `internal/hooks` pays that once in
`TestMain` and every such budget scales through `internal/testwait`, so a
failure here usually means either a new package spawned PowerShell before
`internal/hooks` did, or a hook budget was written as a bare number instead of
`testwait.Seconds(...)`. Confirm which before changing a timeout:

```bash
go test ./internal/hooks/ -count=1 -run TestRunUserPromptSubmit_CollectsStdout -v
```

---

## R17. Recover without discarding evidence

Start with the failed operation and its saved history. Use
[the troubleshooting guide](troubleshooting.md) for specific symptoms.

```text
/clear                      clear the screen, keep the session
/queue                      inspect pending prompts after a failure
/queue resume               continue the reviewed queue while idle
/queue clear                discard pending prompts and start fresh
/permissions reset          drop session rules, restore startup policy
/jobs resubmit              list eligible recovered jobs with full IDs
```

```bash
packetcode --resume ID       # review saved headless-run history in the same directory
```

Resubmitting a job starts a new run and does not undo earlier effects. Inspect
its transcript and worktree first. A storage error needs a writable data
directory; it does not justify removing job or session records.

If configuration repair is necessary, back up `config.toml` and fix the named
setting using R1. Keep sessions, jobs, backups, and worktrees available for
inspection. Check `$PC/worktrees` for unmerged work before any separately
planned state cleanup (R8). `/cost reset --yes` clears accounting history; it
is not a runtime recovery step.
