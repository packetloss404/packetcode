# Compatibility contract

What packetcode writes to disk, what it promises about reading it back, and what
happens when a build meets a file it was not built for.

This is a published contract, not a description. `internal/compat` is its
executable form, and `internal/compat/compat_doc_test.go` fails when this
document and that package disagree — the version numbers below cannot go stale
without breaking the build.

## The rule

**An older build must never silently misread a newer file.**

That is the whole policy, and it is narrower than it sounds. It is not about
being able to read everything; it is about the difference between failing and
appearing to succeed. Go's decoders discard fields they do not recognise, so a
build that reads a newer file and then writes it back has not merely misread
it — it has destroyed everything it could not see, permanently, in a file the
user never touched, with no error at any point. Refusing is the only outcome
that leaves the data intact.

Refusing, or not writing it back through a decoder at all. `config.toml` takes
the second route, which is why it alone can report and continue; see
[Saving `config.toml`](#saving-configtoml).

The reverse direction is not symmetrical. A newer build reading an older file
knows exactly what it is looking at and migrates it forward.

## Formats

| Format | Location | Version | On a version mismatch |
| --- | --- | --- | --- |
| config | `config.toml` | 1 | report and continue |
| session | `sessions/<id>.json` | 2 | read older, refuse newer |
| job | `jobs/<id>.json` | 1 | read older, refuse newer |
| computer registry | `computers.json` | 1 | read older, refuse newer |
| skill approvals | `skill-approvals.json` | 1 | exact version only |
| workflow | `workflows/*.toml` | 1 | exact version only |

Paths are relative to the packetcode home (`~/.packetcode`, or `PACKETCODE_HOME`).
MCP server definitions are not a separate format: they live in `config.toml`
under `[mcp.<name>]` and are covered by its row.

### Why three rules and not one

**Read older, refuse newer** is the default, and applies to everything
packetcode writes itself. Reading is safe; writing back is what destroys data.

**Exact version only** applies where an unfamiliar file cannot be partially
trusted. Skill approvals record which repository-supplied skills a person agreed
to run: a store read wrongly could grant authority nobody granted, so an
unrecognised version means *nothing* is approved rather than a best guess.
Workflows are refused in both directions because a workflow is executable — a
step misread is a command run wrong.

**Report and continue** applies only to `config.toml`, and only because it is a
file a person typed. Refusing to start because they once ran a newer build is a
worse outcome than the misreading it would prevent, and nothing is destroyed by
continuing: saving edits this file in place rather than re-encoding it, so a
setting this build has no field for is left exactly where it was found. But an
ignored setting is how someone spends an afternoon wondering why an option does
nothing, so anything not understood is named on stderr at startup — both a newer
`schema_version` and any key no setting matched, since from the user's chair
those are one question with two answers ("upgrade" and "you have a typo").

### Saving `config.toml`

Saving is surgical. packetcode works out which individual settings it means to
change and rewrites only those, in place, by editing the text of the file.
Comments, key order, spacing, and every key this build has no field for come out
byte-identical, and a save that would change nothing writes nothing at all.

This is what makes "report and continue" safe rather than merely tolerable. An
older build that opens a config written by a newer one names the settings it did
not understand and then leaves them alone — `schema_version` included, which is
never lowered and never added to a file that did not already declare it. Setting
an API key on the older build no longer costs you the options you set on the
newer one.

The editor is deliberately narrow, and refuses by name what it cannot express
rather than approximating it:

- A key inside a `[[array of tables]]` — `[[permissions.rules]]` and the
  `[[hooks.*]]` blocks — occurs once per element, so there is no single value to
  replace. Changing one from inside packetcode is refused; edit the file.
- A path that names a table rather than a setting.
- A file that no longer parses, and a value with no TOML literal.

Every patch is re-parsed before it is committed and compared against the
original: the result must differ in exactly the settings that were asked for and
in nothing else, or the write is refused and the file is left as it was. A
patcher that quietly mangles a config would be a worse failure than the
rewriting it replaced, so the check is not optional and the refusal is loud.

One case still writes a whole file: a `config.toml` that does not exist yet, or
that holds nothing but whitespace. There is nothing to preserve, and a first run
deserves a complete, readable config rather than the two keys setup happened to
set.

## When to bump a version

Only when the change is one an older build would get **wrong**.

- Adding an optional field that older builds can ignore: **do not bump.** They
  will drop it on rewrite, which is a real cost — but spending a version makes
  every older build refuse a file it could otherwise have used, which is worse
  for every user who has not upgraded. Bump when losing the field silently
  matters.
- Changing the meaning of an existing field: **bump.** This is the case the
  contract exists for.
- Removing a field, or changing its type: **bump.**
- Renaming a field: **bump**, and keep reading the old name for at least one
  version.

A bump requires, in the same change: the constant in `internal/compat`, a row in
the changelog below, and a note in `CHANGELOG.md` under the release. The doc test
enforces the first two agreeing.

## Migration

Migrations are forward-only and run on read. A newer build that opens an older
file upgrades it in memory and writes the upgraded form back on the next save.
There is no downgrade path and none is planned: the older build's refusal is the
downgrade story, and it is deliberate.

Migrations must be **idempotent** and must not change user-visible content.
`internal/session/projection.go` is the worked example — it fills in a derived
field and bumps the version, and running it twice does nothing the second time.

## Before any daemon work

A daemon with clients turns every one of these into a wire format as well as a
file format, and a wire format has no equivalent of "the user upgraded and
restarted". This contract lands first so that the daemon inherits a settled
answer rather than inventing a second one.

## Format changelog

Separate from `CHANGELOG.md`, which tracks releases. This tracks the on-disk
formats, which change on their own schedule and matter to anyone whose files
outlive a build.

### session 2 — 2026-06 (packetcode v0.4.0)

Added the immutable model projection for oversized tool results
(`ModelContent`). A version 1 session is migrated on read: the projection is
derived from content already present, so nothing is invented and nothing is
lost.

### session 1, job 1, computer registry 1, skill approvals 1, workflow 1 — initial

The first versioned form of each. Records written before their format carried a
version decode as 0 and are treated as version 1.

### config 1 — 2026-08-30

`schema_version` introduced. It is optional and normally absent: an existing
config decodes as 0 and is treated as current, because refusing every config
written before this field existed would guard a case that has not happened yet
at the cost of every case that has.

The format is unchanged by how the file is written, so this stays version 1 —
but the writing changed on 2026-09-03, from a whole-struct re-encode to the
in-place edit described in [Saving `config.toml`](#saving-configtoml). Existing
files need nothing done to them; they simply stop losing keys and comments.

## What is not covered

These files carry no version. Most are replaceable preferences or inputs;
the cost tally is best-effort local accounting with the downgrade limitation
described below:

- `theme.toml` — a parse error falls back to the built-in theme and says so.
- `cost-tally.json` — accounting; corrupt data is reported by the cost tracker.
  New records include additive `usage_by_model` buckets while retaining the
  aggregate token counts and latest provider/model fields. Legacy records seed
  one bucket from their stored label on the next update; any attribution lost
  before that update cannot be recovered. This format remains unversioned:
  older binaries ignore and can discard the buckets when saving, so downgrading
  loses accurate mixed-model attribution.
- `.env` — read, never written by packetcode. Problems are reported at startup.

New formats whose fields cannot be reconstructed should carry a version and a
row above. Adding a version now cannot make already-shipped unversioned cost
readers refuse a newer tally, so the downgrade limitation is explicit.
