# Product screenshots

Captured on 2026-11-02 from the unmodified macOS Apple silicon executable in
[release v0.6.2](https://github.com/packetloss404/packetcode/releases/tag/v0.6.2).
The application reports commit `5df4e06b2b55a013ec4c3476b09f2d9c8205958c`.

These are screenshots of ordinary PacketCode sessions using the real Codex
provider and `gpt-5.6-sol`. The full application runs in a real PTY, with an
isolated `PACKETCODE_HOME` and a disposable Git project named Beacon. The
project is a small Go HTTP health service containing `README.md`, `main.go`,
and `main_test.go`.

| Image | What happened in the running app |
| --- | --- |
| `packetcode-chat.jpg` | A real Codex response to a question about designing the example service's health endpoint. |
| `packetcode-approval.jpg` | Codex proposed a README patch; PacketCode displayed the diff and waited for permission. The edit was not approved. |
| `packetcode-agents.jpg` | Two `/spawn` commands launched actual read-only background reviews. `/agents` shows their completed jobs and artifacts. |

## Capture method

[`capture_pty.py`](https://github.com/packetloss404/packetcode/blob/main/tools/screenshots/capture_pty.py)
starts the released executable and records its actual ANSI output. It never
passes `--tui-fixture` or seeds UI state. Codex responses, tool proposals,
background-job results, and timings come from the running application.

The recorded terminal stream is displayed by xterm.js 6.0.0 using
[`terminal.html`](https://github.com/packetloss404/packetcode/blob/main/tools/screenshots/terminal.html).
Browser screenshots are saved directly as complete, uncropped 1280 × 720
JPEGs. The application's text, colors, layout, prompts, and menus are not
repainted or retouched, and no window chrome is composited onto the images.

`captures.json` records executable and image hashes, terminal dimensions,
and screenshot bounds. Local ANSI recordings and session data stay in ignored
`dist/`; only the reviewed images and provenance are published.

## Regenerating

1. Download the desired release and verify its archive against that release's
   `checksums.txt`. The original archive SHA-256 for these captures is
   `48df00987572d9954918b0e025fcbe885fbefefc6a62284eba57d42f0eb100c6`.
2. Prepare a disposable example project and a dedicated PacketCode data
   directory. Configure the Codex provider there and use an existing official
   Codex CLI login. Keep ordinary permission prompts enabled.
3. Run the capture helper with explicit paths:

   ```bash
   python3 tools/screenshots/capture_pty.py \
     --binary /absolute/path/to/packetcode \
     --project /absolute/path/to/example-project \
     --home /absolute/path/to/capture-state \
     --output dist/screenshot-render
   ```

4. Send newline-delimited JSON to the helper's stdin. Text is delivered to the
   real app; `\r` presses Enter. Save a state only after it is visible:

   ```json
   {"text":"Your example prompt\r"}
   {"snapshot":"chat"}
   {"stop":true}
   ```

5. Install the terminal renderer into ignored build output, then serve the
   recordings locally:

   ```bash
   npm install --prefix dist/screenshot-tools --no-package-lock --ignore-scripts @xterm/xterm@6.0.0
   mkdir -p dist/screenshot-render/vendor
   cp tools/screenshots/terminal.html dist/screenshot-render/index.html
   cp dist/screenshot-tools/node_modules/@xterm/xterm/lib/xterm.js dist/screenshot-render/vendor/
   cp dist/screenshot-tools/node_modules/@xterm/xterm/css/xterm.css dist/screenshot-render/vendor/
   python3 -m http.server 4181 --bind 127.0.0.1 --directory dist/screenshot-render
   ```

6. Open `http://127.0.0.1:4181/?state=chat` in a browser, review the actual
   terminal state, and capture its visible contents. Model replies and job
   timings will vary on subsequent runs. Check the new images for public-safe
   example content, update their dimensions and provenance, then run the
   website checks.
