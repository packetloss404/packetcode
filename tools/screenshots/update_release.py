#!/usr/bin/env python3
"""Verify a published release, capture its real UI, and update product assets."""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import tarfile
import tempfile

from demo_capture import capture, STATES

ROOT = Path(__file__).resolve().parents[2]
REPOSITORY = "packetloss404/packetcode"
TAG = re.compile(r"v\d+\.\d+\.\d+\Z")


def run(*args, **kwargs):
    return subprocess.check_output(args, text=True, cwd=ROOT, **kwargs).strip()


def latest_release():
    release = json.loads(run("gh", "api", f"repos/{REPOSITORY}/releases/latest"))
    if release["draft"] or release["prerelease"] or not TAG.fullmatch(release["tag_name"]):
        raise ValueError("Only the latest published stable vMAJOR.MINOR.PATCH release is supported")
    return release


def verify_archive(archive, checksums, destination):
    lines = [line.split() for line in checksums.read_text().splitlines() if line.strip()]
    matches = [parts[0] for parts in lines if len(parts) == 2 and parts[1].lstrip("*") == archive.name]
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    if len(matches) != 1 or digest != matches[0]:
        raise ValueError("Release archive checksum does not match")
    with tarfile.open(archive) as bundle:
        members = [member for member in bundle.getmembers() if member.name == "packetcode"]
        if len(members) != 1 or not members[0].isfile():
            raise ValueError("Release must contain exactly one regular packetcode executable")
        with bundle.extractfile(members[0]) as source, destination.open("wb") as target:
            shutil.copyfileobj(source, target)
    destination.chmod(0o700)
    return digest


def recipe_hash():
    digest = hashlib.sha256()
    for name in ["tools/screenshots/demo_capture.py", "tools/screenshots/update_release.py", "tools/screenshots/render.mjs",
                 "tools/screenshots/terminal.html", "package-lock.json", "scripts/requirements-tui.txt"]:
        digest.update((ROOT / name).read_bytes())
    return digest.hexdigest()


def update_sources(root, manifest, output, crops):
    """Keep release labels, previews, originals, and provenance in one update."""
    images = root / "docs/images"
    previous = json.loads((images / "captures.json").read_text())
    version = "v" + manifest["version"]
    old_version = "v" + previous["version"]
    html = (root / "site/index.html").read_text()
    html = html.replace("PacketCode " + old_version, "PacketCode " + version)
    html = re.sub(r"The refreshed v\d+\.\d+\.\d+ model picker, with live Codex choices\.", "The model picker, using the local demo catalog.", html)
    html = html.replace("A real Codex conversation in PacketCode " + version + ".", "A conversation in PacketCode " + version + ", using the local demo provider.")
    html = html.replace("real Codex responses", "a local demo provider")
    html = html.replace("a real Codex reply", "a demo reply")
    html = html.replace("two completed Codex background reviews", "two completed background reviews using the local demo provider")
    html = html.replace("using the local demo provider of the Beacon example project", "of the Beacon example project using the local demo provider")
    html = html.replace("Two real read-only reviews, completed in Agent View.", "Two read-only background jobs, completed in Agent View.")
    html = html.replace("searchable Codex models", "searchable demo models")
    for state, crop in crops.items():
        x, y, width, height = (crop[key] for key in ("x", "y", "width", "height"))
        if not (width > 0 and height > 0 and x >= 0 and y >= 0 and x + width <= 1280 and y + height <= 720):
            raise ValueError("Invalid screenshot crop: " + state)
        pattern = rf'(<a class="screenshot-preview"[^>]*?)data-screenshot-crop="[^"]+" style="[^"]+"(?=[^>]*href="assets/screenshots/packetcode-{state}\.jpg")'
        replacement = rf'\g<1>data-screenshot-crop="{x},{y},{width},{height}" style="--shot-ratio:{width}/{height};--shot-top:{-y / height * 100:.6f}%;--shot-width:{1280 / width * 100:.6f}%;--shot-left:{-x / width * 100:.6f}%"'
        html, count = re.subn(pattern, replacement, html)
        if count != 1:
            raise ValueError("Expected exactly one gallery capture: " + state)
    readme = (root / "README.md").read_text().replace("PacketCode " + old_version + " conversation", "PacketCode " + version + " conversation")
    readme = readme.replace("released " + old_version + " executable", "released " + version + " executable")
    readme = readme.replace("a real Codex response", "a local demo response").replace("real\nCodex responses", "a\nlocal demo provider")
    readme = readme.replace("refreshed [" + old_version + " model picker]", "refreshed [" + version + " model picker]")
    website = (root / "docs/website.md").read_text().replace("PacketCode " + old_version, "PacketCode " + version).replace("a Codex conversation", "a local demo conversation")
    notes = f'''# Product screenshots

Automatically captured on {manifest["captured_on"]} from the unmodified
{manifest["platform"]} executable in [release {version}](https://github.com/{REPOSITORY}/releases/tag/{version}).
The executable reports commit `{manifest["commit"]}`.

These are actual PacketCode screens, driven through a real PTY in an isolated
Beacon example project. **Responses and model choices come from a scripted local
demo provider**, not a live AI service. The executable performs the real file
reads, patch preview, permission prompt, background jobs, and model selection.
No `--tui-fixture` mode, injected application state, or image retouching is used.

| Image | Running application state |
| --- | --- |
| `packetcode-chat.jpg` | A demo conversation about the example health endpoint. |
| `packetcode-approval.jpg` | An actual README patch preview awaiting permission; the edit is never approved. |
| `packetcode-agents.jpg` | Two actual read-only jobs, including real file-read artifacts, completed in Agent View. |
| `packetcode-models.jpg` | The normal model picker with the local demo catalog and current selection. |

The archive SHA-256 is `{manifest["release_archive_sha256"]}`.
`captures.json` records release, executable, image, and raw ANSI hashes,
dimensions, preview bounds, and the generation recipe hash. Raw recordings and
isolated session data stay in ignored `dist/`; only the images and provenance
are committed. Browser screenshots are complete, uncropped 1280 × 720 JPEGs.
xterm.js 6.0.0 displays the executable's recorded ANSI output without repainting
its UI. Gallery previews focus on its visible content; **Full original** opens
the unchanged capture.

## Automatic refresh

The [release screenshot workflow](../../.github/workflows/release-screenshots.yml)
follows a successful Release workflow. It verifies the latest stable release,
captures its published executable, checks the website, commits the generated
assets and version labels to `main`, and deploys GitHub Pages. It uses a
loopback-only demo provider and needs no AI account or API secret. Failed,
prerelease, and superseded releases cannot replace the website captures.

See [the capture tooling guide](../../tools/screenshots/README.md) for local
regeneration, validation, manual reruns, and the optional live-provider helper.
'''
    # Validate every replacement before writing any published source files.
    for state in STATES:
        shutil.copyfile(output / f"packetcode-{state}.jpg", images / f"packetcode-{state}.jpg")
    (images / "captures.json").write_text(json.dumps(manifest, indent=2) + "\n")
    (images / "README.md").write_text(notes)
    (root / "site/index.html").write_text(html)
    (root / "README.md").write_text(readme)
    (root / "docs/website.md").write_text(website)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", default="", help="Must be the current latest stable release; blank selects it")
    parser.add_argument("--expected-commit", default="", help="Release workflow head SHA; superseded runs are skipped")
    parser.add_argument("--no-apply", action="store_true", help="Generate evidence under dist/ without modifying the website")
    parser.add_argument("--force", action="store_true", help="Regenerate an already current capture recipe")
    args = parser.parse_args()
    release = latest_release()
    tag = release["tag_name"]
    if args.tag and args.tag != tag:
        raise ValueError("Refusing to replace website screenshots with an older release")
    commit = run("git", "rev-parse", f"refs/tags/{tag}^{{commit}}")
    if args.expected_commit and args.expected_commit != commit:
        print("Skipping superseded release workflow")
        return
    subprocess.run(["git", "merge-base", "--is-ancestor", commit, "origin/main"], cwd=ROOT, check=True)
    fingerprint = recipe_hash()
    previous = json.loads((ROOT / "docs/images/captures.json").read_text())
    if not args.force and not args.no_apply and previous.get("version") == tag[1:] and previous.get("recipe_sha256") == fingerprint:
        print("Screenshot release and recipe are already current")
        return
    os_name = {"Darwin": "darwin", "Linux": "linux"}.get(platform.system())
    architecture = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64", "AMD64": "amd64"}.get(platform.machine())
    if not os_name or not architecture:
        raise ValueError("Release capture requires macOS or Linux on x86-64 or ARM64")
    parent = ROOT / "dist/release-screenshots"
    parent.mkdir(parents=True, exist_ok=True)
    output = Path(tempfile.mkdtemp(prefix=tag + "-", dir=parent))
    archive = output / f"packetcode-{os_name}-{architecture}.tar.gz"
    subprocess.run(["gh", "release", "download", tag, "--repo", REPOSITORY, "--pattern", archive.name,
                    "--pattern", "checksums.txt", "--dir", str(output)], cwd=ROOT, check=True)
    binary = output / "packetcode"
    archive_hash = verify_archive(archive, output / "checksums.txt", binary)
    version = run(str(binary), "--version", env={"PATH": os.environ["PATH"], "HOME": str(output)})
    if version != f"packetcode {tag[1:]} ({commit})" and version != f"packetcode {tag} ({commit})":
        raise ValueError("Executable version/commit differs from the published release tag: " + version)
    hashes = capture(binary, output)
    renderer_env = {key: os.environ[key] for key in ("PATH", "HOME", "TMPDIR", "PLAYWRIGHT_BROWSERS_PATH") if key in os.environ}
    subprocess.run(["node", "tools/screenshots/render.mjs", str(output)], cwd=ROOT, env=renderer_env, check=True)
    crops = json.loads((output / "crops.json").read_text())
    manifest = {
        "captured_on": datetime.now(timezone.utc).date().isoformat(), "application": "PacketCode",
        "version": tag[1:], "commit": commit, "platform": f"{os_name}-{architecture}",
        "release_archive_sha256": archive_hash, "executable_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "provider": "local-demo", "model": "demo-code", "response_source": "scripted loopback-only demo provider",
        "project": "Beacon example HTTP service", "terminal": {"emulator": "xterm.js 6.0.0", "columns": 100, "rows": 38},
        "uses_tui_fixture": False, "recipe_sha256": fingerprint, "screenshots": [],
    }
    for state in STATES:
        image = output / f"packetcode-{state}.jpg"
        manifest["screenshots"].append({"file": image.name, "image_sha256": hashlib.sha256(image.read_bytes()).hexdigest(),
                                        "ansi_sha256": hashes[state], "viewport": {"width": 1280, "height": 720},
                                        "cropped": False, "preview_bounds": crops[state]})
    (output / "captures.json").write_text(json.dumps(manifest, indent=2) + "\n")
    if not args.no_apply:
        # A newer release may have finished while these captures were running.
        if latest_release()["tag_name"] != tag:
            raise ValueError("A newer release was published during capture; refusing stale update")
        update_sources(ROOT, manifest, output, crops)
    print("Verified release screenshot evidence:", output)


if __name__ == "__main__":
    main()
