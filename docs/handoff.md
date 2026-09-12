# Audit handoff

## September 11 usability and developer update

The behavior baseline is `7f7ecb3`, with
[all 16 CI jobs passing](https://github.com/packetloss404/packetcode/actions/runs/34664539030).
Failures now pause queued prompts for explicit `/queue resume`; cancellation
and loop stops cannot restart discarded work. Approval choices explain their
session scope, and job/MCP/headless recovery gives usable next steps without
automatically repeating actions. Concurrent job resubmission is guarded.

Start new work from [the developer guide](development.md) and
[maintenance guide](maintenance.md). [The root handoff](../HANDOFF.md) records
the current architecture and verification baseline. The user manuals and
[README](../README.md) describe the shipped controls.

Still open: mixed-model cost estimates, backend file-read bounds and SFTP
cancellation repros, provider replay fixtures, and the trust decisions retained
below. These are tracked in [the backlog](../BACKLOG.md). The older proposed
Go upgrades, smoke wiring, and approval-metadata corrections are already done;
do not apply their historical patches.

## September 8 maintenance update

The September 5 narrative below is historical. Its advice to upgrade the Go
floor, wire smoke tests into CI, and resolve `collect_agent_results` approval
metadata has already been implemented. Do not apply the old audit patches.
CI now pins Go 1.26.8, the module floor is Go 1.26.0, and smoke tests run on all
three supported operating systems.

The [September 8 hardening review](audit/hardening-2026-09-08.md) covers the new
permission, persistence, MCP, and secret-file regression tests. Use
[the maintenance guide](maintenance.md) for routine work and recovery during
the next month; use `BACKLOG.md` for larger feature decisions.

## September 5 historical handoff

Written 2026-09-05, at the end of the one-shot security, correctness, and
half-built-feature audit. It covers what changed, what is deliberately still
open, and what to do about each of those without a high-capability model
available.

This is the audit's handoff. `HANDOFF.md` at the repository root remains the
general maintainer handoff and is not superseded by this file; read that for
architecture and normal development. The audit's own reasoning, with every
finding and its evidence, is in
[docs/audit/security-audit-2026-09-05.md](audit/security-audit-2026-09-05.md).

## Where things stand

`main` moved from `c1bca77` to the audit tip. Twelve commits, each self-contained
and independently revertable, plus this documentation set. Verified on the merged
tree: `go build`, `go vet`, `go mod verify`, `go test -count=1 ./...` (54 packages,
zero failures), and `bash smoke.sh` (27 of 27).

| Commit | What it changes | User-visible? |
| --- | --- | --- |
| `dd71133` | Gemini key moves from the URL query to `x-goog-api-key` | no |
| `6fece2e` | `read_file`, `search_codebase`, and `@`-mentions refuse dotenv files | **yes** |
| `d4f820c` | `@`-mentions resolve symlinks before leaving the project root | **yes**, narrow |
| `571fa98` | a pending approval no longer also reports `NeedsInput` | Agent View icon |
| `f82868e` | the cost tally is fsynced before its rename | no |
| `f04688d` | the ACP permission ceiling matches the policy the server runs | **yes**, for ACP clients |
| `195060b` | config is validated at boot and the missing variable is named | **yes**, new stderr lines |
| `cea8ef7` | opt-in JSON diagnostic log via `PACKETCODE_LOG_FILE` | opt-in |
| `d61a919` | `golang.org/x/crypto` v0.41.0 to v0.43.0 | no |
| `7aa0951` | dead `atomicWrite` helper removed | no |
| `e0ce868` | `smoke.sh` plus `tools/smokestub`, and `make smoke-e2e` | no |
| docs | audit report, patch files, runbooks, this file, QA workbook | no |

## The four behaviour changes, and what they look like when they bite

1. **`.env` is unreadable by the model.** `read_file .env` now returns
   `read_file: refusing to read .env: dotenv secret files hold provider
   credentials...`. `.env.example`, `.env.sample`, `.env.template` and
   `.env.dist` are unaffected. If you genuinely need the agent to see one,
   `execute_command cat .env` still works and is approval-gated, which is the
   point. Revert: `git revert 6fece2e`.
2. **`@`-mentions no longer follow symlinks out of the project.** A mention that
   used to resolve now silently does not attach. Symlinks that stay inside the
   root still work. Revert: `git revert d4f820c`.
3. **ACP clients may be offered fewer permission modes.** If your config has
   `profile = ""` or a `[permissions.profiles.<name>]` table, the ceiling is now
   `ask` rather than `full`, so a client asking for `auto` or `bypass` is
   refused with `permission mode not allowed`. A client that reads
   `_packetcode.permissionModes` from `initialize` adapts on its own. Revert:
   `git revert f04688d`.
4. **New stderr lines at startup** naming settings that were previously inert.
   A new line after a deploy is a config regression, not a code one. Revert:
   `git revert 195060b`.

## Open, and needing a decision that was not mine to make

Each is written up with evidence in the audit report, section 3. None is
critical; all are medium or low.

| ID | Decision | What settles it |
| --- | --- | --- |
| F-08 | Should repository-authored slash commands and workflows be labelled untrusted the way skills already are? Today a cloned repo's `/review` reaches the model as *your* words, and a project workflow can set `system_prompt`, `provider`, and `allow_write` per step. | A product call. `docs/security.md:3` currently accepts this trust class explicitly. |
| F-09 | Should an ACP client be able to name arbitrary MCP commands and any working directory? Fine for a same-user editor; equivalent to arbitrary code execution for anything else on that pipe. | Whether anything other than a same-user editor can reach `packetcode acp` stdin. Check how PacketADE launches it. |
| F-10 | Should a custom provider over plain `http` to a non-loopback host be refused rather than warned? It sends the Bearer key and the whole conversation in cleartext. | Whether any real config does this. `grep 'base_url = "http://' ~/.packetcode/config.toml` on every machine. |
| F-11 | `collect_agent_results` says `RequiresApproval() == true` but is listed as a read-only tool, so it never prompts. The two disagree; pick one. | A one-line change either way: `policy.go:481` or `collect_agent_results.go:35`. |
| F-13 | Should the installers require a signature by default once signed releases exist? | Whether a signed release has shipped yet. |
| U3 | Move the Go floor to 1.26? | Clears the nine remaining stdlib advisories. CI already runs 1.26.3; README says 1.24.2. Patch prepared: `docs/audit/patches/P10b-x-crypto-v0.56.0-go1.26.patch`. |

## What to do in the next thirty days

Ordered by value per unit of risk. Everything here is small and testable without
a strong model.

1. **Move CI to Go 1.26.6.** One line in each of `.github/workflows/ci.yml` and
   `release.yml` (`GO_VERSION`). That alone clears nine of the sixteen remaining
   reachable advisories, with no code change. Verify with `make vulncheck`.
2. **Decide U3.** If yes, `git apply docs/audit/patches/P10b-*.patch`, then edit
   the Go version in `README.md` and `HANDOFF.md`. That clears the rest.
3. **Wire `smoke.sh` into CI.** Add `smoke-e2e` to the `ci` target in the
   `Makefile` and a step to `.github/workflows/ci.yml`. It needs no credentials
   and takes about twenty seconds. Watch the first Windows and macOS runs: the
   script has only been exercised on Windows here.
4. **Answer F-09 and F-10** with the greps named above. Both are cheap to check
   and each turns a "medium, conditional" into either a closed item or a small
   patch.
5. **Prune backups.** Audit item H-08: `$PC/backups/` grows without bound and
   `BackupManager.Cleanup` has no production caller. An age-based prune at
   startup is roughly a hundred lines and closes a real disk-usage leak. See
   runbook R9.
6. **Leave the rest alone.** The half-built inventory (audit section 5) is
   mostly correctly documented as unfinished. Resist finishing Streamable HTTP
   MCP or the Packet Computers daemon in a low-capability window: both are
   security-sensitive and both already have written contracts to build against
   when there is capacity.

## What not to do

- **Do not upgrade bubbletea, lipgloss, or bubbles to v2.** It is a breaking
  migration with a golden-file harness attached; it is on the backlog for a
  reason.
- **Do not bump `mattn/go-runewidth`** without re-running `make tui-golden-check`.
  Width tables change cell goldens.
- **Do not "simplify" `internal/permissions/policy.go`.** The deny-direction
  matching looks redundant and is not: it is what stops `git push origin main; :`
  slipping past a deny rule. `smoke.sh` asserts exactly that case.
- **Do not remove `internal/mcp/http_trust.go`** because the transport does not
  exist. It is the approved contract the transport must be built against, and
  its redaction is live in `/mcp logs`.
- **Do not delete `internal/tools/safefs.go` casually.** Three of its functions
  are dead but `resolveExistingInRoot` is load-bearing for code intelligence
  (audit item H-10). Route those call sites through `LocalBackend.Resolve`
  first, with tests, or leave it.

## Picking this up again

```bash
git log --oneline c1bca77..HEAD          # the audit series
cat docs/audit/security-audit-2026-09-05.md
bash smoke.sh                            # confirm the seams still hold
make vulncheck                           # confirm the dependency picture
packetcode doctor                        # confirm this machine is sane
```

Then run the QA workbook (`python build_qa_workbook.py`, output
`packetcode-qa-workbook.pdf`) for the manual passes that no automated check
covers: the TUI surfaces, the approval flows, and the screenshot evidence
shot list.
