# Release screenshot automation

Every successful **Release** workflow is followed by
[Release screenshots](../../.github/workflows/release-screenshots.yml). It runs
from `main`, selects the latest stable release, verifies the published archive's
SHA-256 and the executable's version/commit against its Git tag, and captures
four ordinary application sessions. A release commit must belong to `main`;
failed releases, prereleases, and superseded workflow runs cannot replace the
current images.

The real executable runs in a POSIX PTY against a loopback-only, scripted
OpenAI-compatible provider named **Local demo**. Each scenario has fresh app
and home directories. Conversations and the catalog are demo data; file reads,
the pending patch and permission prompt, background jobs, and picker UI come
from the running app. The patch is never approved. No TUI fixture mode or
injected UI state is involved, and no AI account or API secret is needed.

The app receives an allowlisted environment without developer credentials or
the Actions repository token. ANSI recordings are rendered by pinned xterm.js
in Chromium, then captured as full, unedited 1280 × 720 JPEGs. Content bounds
are measured from the rendered terminal so previews stay focused even if a
release changes layout or runner fonts. Original files remain available from
the website's enlarged viewer.

The workflow updates the four images, `docs/images/captures.json`, the capture
notes, website preview bounds and labels, and README version references. It
rebases onto current `main`, builds and checks the resulting website, and only
then pushes its bot commit. It uploads that tested build and deploys Pages in
the same workflow: pushes made with `GITHUB_TOKEN` do not start the usual Pages
workflow. Both workflows share the production concurrency queue. A failed
capture or browser check leaves published source and Pages unchanged; failures
retain image/ANSI evidence for seven days.

Repository `contents: write` is scoped to the refresh job. Pages deployment has
separate `pages: write` and `id-token: write` permissions. The existing
`github-pages` environment remains restricted to `main`. If branch protection
is added later, allow the bot's generated-asset update through that policy or
switch publication to reviewed pull requests; the workflow never force-pushes
or bypasses branch rules.

## Local regeneration

Requirements: macOS or Linux, GitHub CLI authenticated for the repository,
Python 3.13+, Node.js 20+, and the repository's pinned dependencies. Fetch the
release tags before running:

```bash
git fetch origin main --tags
npm ci --ignore-scripts
npx playwright install chromium
python3 -m venv dist/screenshot-venv
dist/screenshot-venv/bin/python -m pip install -r scripts/requirements-tui.txt
dist/screenshot-venv/bin/python tools/screenshots/update_release.py
```

An optional `--tag vMAJOR.MINOR.PATCH` must match the latest stable release.
`--no-apply` captures evidence under ignored `dist/release-screenshots/` without
changing product assets. `--force` regenerates an already current release and
recipe. The recipe hash includes the capture/renderer scripts and dependency
locks; unchanged reruns are otherwise a no-op. After applying captures:

```bash
dist/screenshot-venv/bin/python -m unittest discover -s tools/screenshots -p '*_test.py'
npm run build:site
npm run test:site
```

`npm run screenshots:release` is a convenience entry point when `python3` has
the dependencies installed. CI also builds the current application code and
drives all four scenarios on every pull request, catching UI/protocol changes
before a release is tagged. Preview images are retained as an Actions artifact.

## Manual workflow rerun

After the workflow exists on `main`, use **Actions → Release screenshots → Run
workflow**, or:

```bash
gh workflow run release-screenshots.yml --ref main -f force=true
```

Leave the tag blank to use latest. Older releases are deliberately rejected.
If the release is newer than an earlier queued run, that earlier run is skipped.
If a concurrent source edit conflicts during rebase, the job fails without
pushing; rerun against the updated `main`. If only deployment failed after the
bot commit, rerun the existing **Website** workflow from `main` to deploy it.

## Optional live-provider captures

[`capture_pty.py`](capture_pty.py) remains available for manual, real Codex
sessions using an existing official CLI login. It records ordinary released
executable output and accepts newline-delimited JSON on stdin:

```bash
python3 tools/screenshots/capture_pty.py \
  --binary /absolute/path/to/packetcode \
  --project /absolute/path/to/example-project \
  --home /absolute/path/to/isolated-app-state \
  --output dist/manual-capture
```

```json
{"text":"Your example prompt\r"}
{"snapshot":"chat"}
{"stop":true}
```

Manual live captures must identify their provider and provenance accurately;
they are not part of the credential-free release automation.
