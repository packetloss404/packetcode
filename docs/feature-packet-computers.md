# Packet Computers — SSH Workspaces and Background Agents

A **Packet Computer** is a durable machine PacketCode can work against. The
current direct-SSH slice supports foreground coding plus process-lifetime
background agents and workflows. Core file and shell tools run on a registered
remote project root over host-key-pinned SSH and SFTP connections.

## Feature gate

Packet Computers is enabled by default for compatibility. A standalone local
PacketCode installation can disable the complete surface:

```toml
[packet_computers]
enabled = false
```

`PACKETCODE_PACKET_COMPUTERS_ENABLED=false` is the environment equivalent.
In disabled mode PacketCode does not load or create
`computers/registry.json`, start SSH, or initialize remote placement.
`--computer`, `/computers`, `/spawn --computer`, and
`/workflows run --computer` fail with an explicit disabled message. Omitting
those selectors leaves ordinary local sessions, jobs, and workflows unchanged.
The gate does not delete an existing registry; re-enabling restores access.

## What works today

Register a local record, register an SSH record, remove a record, or inspect
stored detail:

```text
/computers                  list registered computers
/computers status <name>    show one computer's stored record
/computers <name>           same as `status <name>`
/computers register <name> <absolute-local-root>
/computers ssh <name> <user@host> <absolute-remote-root> \
  --fingerprint <SHA256:...> [--port N] [--identity PATH]
/computers remove <name> --yes
```

Start a new foreground session against a registered SSH computer:

```text
packetcode --computer <name>
```

`read_file`, `write_file`, `patch_file`, `list_directory`,
`search_codebase`, and `execute_command` then operate inside the pinned remote
root. Paths cannot escape that root lexically or through an existing symlink.
Commands accept a remote relative `cwd`; each command gets its own SSH channel
over the process-lifetime connection. File writes use a same-directory
temporary file plus SFTP rename.

Authentication uses `SSH_AUTH_SOCK`, including the Windows OpenSSH agent pipe,
and an optional identity file. When no identity is configured PacketCode also
tries `~/.ssh/id_ed25519`, `id_ecdsa`, and `id_rsa`. Encrypted identity files
must be loaded into the SSH agent. Password authentication and interactive
passphrase prompts are not supported.

The host-key fingerprint is mandatory and must use OpenSSH's `SHA256:...`
format. PacketCode fails closed if it is missing or changes. Obtain the
fingerprint from the server operator or another trusted channel; do not treat
an unverified first `ssh-keyscan` result as identity proof.

Remote sessions persist their `ComputerID`, endpoint/root identity digest, and
root in the transcript. New sessions refuse resume after a different computer,
host key, endpoint, or registered root is substituted. Legacy remote sessions
without the digest retain the older ID/root validation.

Background work inherits the active remote computer automatically:

```text
/spawn inspect the service
/spawn --write build the requested change
/workflows run review
```

From a local session, target a registered computer explicitly:

```text
/spawn --computer production inspect the deployment
/spawn --computer production --write migrate the app
/workflows run --computer production review target="the deployed checkout"
```

Each active remote job owns a separate SSH/SFTP connection so parallel workflow
agents remain parallel. Read-only jobs inspect the registered root. A
write-enabled job must create a dedicated remote Git worktree under the remote
user's PacketCode state directory; setup failure fails the job closed rather
than editing the foreground remote checkout. Worktrees are preserved and are
never merged or deleted automatically.

## Current boundaries

Stated plainly, because a registry that looked like a control surface would be
misleading:

- There is no PacketCode daemon, durable remote job runner, or process
  supervision yet. A persistent SSH connection means connection reuse during
  one PacketCode process—not a persistent remote shell.
- **Jobs do not survive a PacketCode restart, and that is out of scope rather
  than pending.** Ruled 2026-08-14: durable execution after the originating app
  closes belongs to PacketAgent. There is no reconnect-and-continue path after
  the PacketCode process exits and none is planned, and the planned daemon
  milestone is session-scoped — it will hold no durable job state.
- Remote job snapshots persist their computer/root/worktree evidence locally,
  but an active job found after restart is reported as abandoned and must be
  resubmitted as a new run. PacketCode never claims it resumed.
- Remote `/undo` is unavailable. `write_file` and `patch_file` still show their
  approval diffs, but no local backup stack is created for remote paths.
- Local and remote whole-file reads require regular files and enforce a 16 MiB
  limit, including files that grow while being read. Requesting fewer rendered
  lines does not bypass that limit. A stalled SFTP operation can still outlive
  cancellation; bounded size does not establish a transport deadline.
- Code-intelligence tools, `@file` expansion, local project hooks, external
  statusline commands, and local git-branch probing are disabled for the remote
  workspace. Remote search uses the SFTP-backed fallback rather than ripgrep.
- Status is a **stored value, not a probe**. With no heartbeat, a record that
  has never been contacted reports `unknown` — which means "never contacted",
  not "offline".
- Normal session permission modes, `--write`, and the computer's write/shell
  policy are composed conservatively. Read-only parents cannot create a
  write-enabled child, and nested agents cannot pivot to another computer.
- Closing an SSH session sends termination to its remote shell channel, but
  PacketCode does not claim process-tree supervision. Detached descendants may
  require operator cleanup until the daemon milestone lands.

Roadmap: [`packet-computers-loop.md`](packet-computers-loop.md) (PCMP1–PCMP10;
PCMP9, persistent job reconcile, was cut on 2026-08-14).
Product definition and the full six-phase arc:
[`../PACKETCOMPUTERS.md`](../PACKETCOMPUTERS.md).

## Storage

`~/.packetcode/computers/registry.json`, honouring `PACKETCODE_HOME` like every
other state directory. Writes are atomic (temp file then rename).

A malformed file, or one written by a newer packetcode, is a **loud error**
rather than a silent reset — quietly losing your machine list would be worse
than refusing to start the feature.

## Record shape

```json
{
  "version": 1,
  "computers": [
    {
      "id": "pc_workstation",
      "name": "workstation",
      "kind": "ssh",
      "status": "unknown",
      "os": "linux",
      "arch": "amd64",
      "project_roots": ["/srv/projects"],
      "ssh_host": "build.example.internal",
      "ssh_user": "ian",
      "ssh_port": 22,
      "ssh_identity_file": "~/.ssh/id_ed25519",
      "ssh_host_fingerprint": "SHA256:replace-with-the-approved-host-key",
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
        "secrets": "deny",
        "approval": "explicit"
      },
      "created_at": "2026-07-31T00:00:00Z"
    }
  ]
}
```

`kind` is `local`, `ssh`, or `managed`. `managed` records are accepted but
nothing provisions them.

Names are unique case-insensitively so `/computers <name>` is unambiguous, and
are restricted to letters, digits, `_`, and `-`.

**No credentials live in this file.** The identity field is only a local path;
private-key contents and passwords are never copied into the registry. The
fingerprint is public host identity material, not a credential.

## Policy defaults

Any record that does not specify a policy — or specifies an unrecognised value
— gets the conservative default:

| Axis | Default | Meaning |
|---|---|---|
| `network` | `ask` | Network access is approved per use. |
| `write` | `ask` | Filesystem writes are approved per use. |
| `shell` | `ask` | Shell execution is approved per use. |
| `secrets` | `deny` | A remote computer does not inherit local credentials. |
| `approval` | `explicit` | Every action is approved explicitly. |

`approval` accepts `explicit`, `trust-workspace`, or `trust-computer`. It is a
string enum rather than a boolean on purpose: the safe value is "explicit", and
a boolean whose safe value is `true` would decode an absent JSON field as
`false` — silently widening trust on exactly the records nobody configured. An
unrecognised value falls back to `explicit` for the same reason.
