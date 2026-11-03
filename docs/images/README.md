# Product screenshots

Automatically captured on 2026-11-03 from the unmodified
linux-amd64 executable in [release v0.6.3](https://github.com/packetloss404/packetcode/releases/tag/v0.6.3).
The executable reports commit `58065b5cc04c0fd69250b9d168e1e572acf5289f`.

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

The archive SHA-256 is `a0404e467e928ad703a01428e5b631c46a519ad75a785f880b6824108e6b1883`.
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
