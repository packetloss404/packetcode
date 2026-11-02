#!/usr/bin/env python3
"""Capture an ordinary PacketCode process, never the TUI fixture renderer.

The process runs in a real PTY with an explicit example project and isolated
PACKETCODE_HOME. Send newline-delimited JSON on stdin: {"text": "...\\r"},
{"snapshot": "chat"}, {"status": true}, or {"stop": true}. ANSI captures are
local intermediates; only reviewed screenshots belong in docs/images/.
"""

import argparse
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import struct
import subprocess
import sys
import termios
import threading
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--project", type=Path, required=True)
    parser.add_argument("--home", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--cols", type=int, default=100)
    parser.add_argument("--rows", type=int, default=38)
    args = parser.parse_args()
    for name in ("binary", "project", "home", "output"):
        setattr(args, name, getattr(args, name).resolve())
    args.output.mkdir(parents=True, exist_ok=True)

    # Reuse the repository's PTY environment and standard terminal-query replies.
    root = Path(__file__).resolve().parents[2]
    spec = importlib.util.spec_from_file_location("tui_capture", root / "scripts/tui_capture.py")
    harness = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(harness)
    environment = harness.child_env()
    environment["PACKETCODE_HOME"] = str(args.home)

    command = [str(args.binary), "--provider", "codex", "--model", "gpt-5.6-sol", "--permission-mode", "ask"]
    version = subprocess.check_output([str(args.binary), "--version"], text=True).strip()
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", args.rows, args.cols, 0, 0))
    process = subprocess.Popen(command, cwd=args.project, env=environment,
                               stdin=slave, stdout=slave, stderr=slave,
                               start_new_session=True, close_fds=True)
    os.close(slave)
    raw = bytearray()
    lock = threading.Lock()
    stop = threading.Event()

    def read_output():
        answered = set()
        while not stop.is_set():
            if not select.select([master], [], [], 0.1)[0]:
                continue
            try:
                chunk = os.read(master, 65536)
            except OSError:
                return
            if not chunk:
                return
            with lock:
                raw.extend(chunk)
                harness.answer_terminal_queries(master, raw, answered)

    reader = threading.Thread(target=read_output, daemon=True)
    reader.start()
    print(json.dumps({"ready": True, "pid": process.pid, "version": version,
                      "cols": args.cols, "rows": args.rows}), flush=True)
    try:
        for line in sys.stdin:
            request = json.loads(line)
            if "text" in request:
                os.write(master, request["text"].encode("utf-8"))
                print(json.dumps({"sent": True}), flush=True)
            elif "snapshot" in request:
                name = request["snapshot"]
                if not re.fullmatch(r"[a-z][a-z0-9-]*", name):
                    raise ValueError("Snapshot names must contain lowercase letters, digits, or hyphens.")
                with lock:
                    frame = bytes(raw)
                (args.output / (name + ".ansi")).write_bytes(frame)
                metadata = {"version": version, "command": ["packetcode", *command[1:]],
                            "binary_sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(),
                            "ansi_sha256": hashlib.sha256(frame).hexdigest(),
                            "cols": args.cols, "rows": args.rows,
                            "capture_kind": "real executable in PTY; no --tui-fixture",
                            "provider": "Codex", "model": "gpt-5.6-sol"}
                (args.output / (name + ".json")).write_text(json.dumps(metadata, indent=2) + "\n")
                print(json.dumps({"saved": name, "bytes": len(frame)}), flush=True)
            elif request.get("status"):
                with lock:
                    text = bytes(raw[-18000:]).decode("utf-8", "replace")
                text = re.sub(r"\x1b\][^\x07]*(?:\x07|\x1b\\)", "", text)
                text = re.sub(r"\x1b\[[0-?]*[ -/]*[@-~]", "", text)
                print(json.dumps({"running": process.poll() is None, "tail": text[-7000:]}), flush=True)
            elif request.get("stop"):
                break
    finally:
        if process.poll() is None:
            try:
                os.write(master, b"\x03")
                time.sleep(0.2)
                if process.poll() is None:
                    os.write(master, b"\x03")
            except OSError:
                # A clean first Ctrl+C may already have closed the PTY.
                pass
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=3)
        stop.set()
        reader.join(timeout=1)
        os.close(master)


if __name__ == "__main__":
    main()
