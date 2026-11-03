#!/usr/bin/env python3
"""Drive the released application against a loopback-only demo provider.

No TUI fixtures or injected UI state: the executable handles every prompt,
tool call, permission decision, model lookup, and background job itself.
"""

import argparse
import codecs
import fcntl
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import pty
import select
import signal
import struct
import subprocess
import termios
import threading
import time

import pyte

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("tui_capture", ROOT / "scripts/tui_capture.py")
harness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(harness)
STATES = ("chat", "approval", "agents", "models")
MODELS = ("demo-code", "demo-review", "demo-local")
README = """# Beacon

A small Go HTTP service with a health endpoint.

## Endpoints

- `GET /healthz` returns a JSON status response.
- The server listens on port 8080.

## Development

Run `go test ./...` before making a change.
"""
FILES = {
    "README.md": README,
    "go.mod": "module example.com/beacon\n\ngo 1.26\n",
    "main.go": '''package main

import (
    "encoding/json"
    "net/http"
)

func health(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func main() {
    http.HandleFunc("/healthz", health)
    http.ListenAndServe(":8080", nil)
}
''',
    "main_test.go": '''package main

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestHealth(t *testing.T) {
    response := httptest.NewRecorder()
    health(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
    if response.Code != http.StatusOK {
        t.Fatalf("status = %d, want 200", response.Code)
    }
}
''',
}


def response_for(messages):
    """Respond to scenario prompts; tool results must come from the real app."""
    last_user, results = "", []
    for message in messages:
        if message["role"] == "user":
            last_user, results = message.get("content", ""), []
        elif message["role"] == "tool":
            results.append(message.get("content", ""))
    if "curl example" in last_user:
        if not results:
            return tool("read_file", {"path": "README.md"})
        if "# Beacon" not in results[-1]:
            raise ValueError("README tool result did not contain the example project")
        return tool("patch_file", {"path": "README.md", "patches": [{
            "search": "- `GET /healthz` returns a JSON status response.\n",
            "replace": "- `GET /healthz` returns a JSON status response.\n\n```sh\ncurl http://localhost:8080/healthz\n```\n",
        }]})
    if last_user.startswith("Review main"):
        filename = "main_test.go" if "main_test.go" in last_user else "main.go"
        if not results:
            return tool("read_file", {"path": filename})
        if "package main" not in results[-1]:
            raise ValueError("Review tool result did not contain Go source")
        if filename == "main_test.go":
            return {"content": "Assert JSON and headers; cover HTTP methods; test readiness failures."}
        return {"content": "Check HTTP methods; handle encoding failures; set server timeouts."}
    if last_user.startswith("I am building Beacon"):
        return {"content": "- Return `200 OK` with a small JSON status body.\n- Keep `/healthz` fast and dependency-free.\n- Separate liveness from readiness checks.\n- Use `503 Service Unavailable` when not ready.\n- Add tests for the response body and headers."}
    raise ValueError("Unexpected demo prompt")


def tool(name, arguments):
    return {"tool_calls": [{"index": 0, "id": "demo-" + name, "type": "function",
                            "function": {"name": name, "arguments": json.dumps(arguments)}}]}


class DemoProvider(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        if self.path != "/v1/models":
            self.send_error(404)
            return
        body = json.dumps({"data": [{"id": model} for model in MODELS]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_error(404)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 2_000_000:
                raise ValueError("Invalid request length")
            request = json.loads(self.rfile.read(length))
            delta = response_for(request["messages"])
        except (ValueError, KeyError, TypeError) as error:
            self.send_error(400, str(error))
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        events = [
            {"choices": [{"index": 0, "delta": delta}]},
            {"choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls" if "tool_calls" in delta else "stop"}]},
            {"choices": [], "usage": {"prompt_tokens": 1200, "completion_tokens": 90, "total_tokens": 1290}},
        ]
        for event in events:
            self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()


class Session:
    def __init__(self, binary, project, home, endpoint):
        home.mkdir(parents=True)
        (home / "config.toml").write_text(f'''[default]
provider = "demo"
model = "demo-code"
[providers.demo]
type = "openai_compatible"
display_name = "Local demo"
base_url = "{endpoint}/v1"
api_key_required = false
default_model = "demo-code"
[[providers.demo.models]]
id = "demo-code"
display_name = "Demo Code"
context_window = 128000
supports_tools = true
[[providers.demo.models]]
id = "demo-review"
display_name = "Demo Review"
context_window = 64000
supports_tools = true
[[providers.demo.models]]
id = "demo-local"
display_name = "Demo Local"
context_window = 32000
supports_tools = true
[behavior]
provider_max_retries = 0
[permissions]
profile = "balanced"
[packet_computers]
enabled = false
[sugar]
enabled = false
[conduit]
shadow_enabled = false
''')
        # An allowlist keeps developer credentials, task bridges, and the CI
        # repository token out of the application and its child jobs.
        environment = harness.child_env({"PATH": os.environ["PATH"], "HOME": str(home),
                                         "PACKETCODE_HOME": str(home), "LANG": "en_US.UTF-8"})
        self.home = home
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 38, 100, 0, 0))
        try:
            self.process = subprocess.Popen([str(binary), "--provider", "demo", "--model", "demo-code", "--permission-mode", "ask"],
                                            cwd=project, env=environment, stdin=slave, stdout=slave, stderr=slave,
                                            start_new_session=True, close_fds=True)
        finally:
            os.close(slave)
        self.raw = bytearray()
        self.answered = set()
        self.screen = pyte.Screen(100, 38)
        self.stream = pyte.Stream(self.screen)
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")

    def read(self, timeout=0.1):
        if not select.select([self.master], [], [], timeout)[0]:
            return
        try:
            chunk = os.read(self.master, 65536)
        except OSError:
            chunk = b""
        if not chunk:
            raise RuntimeError("PacketCode exited before capture: " + self.text())
        self.raw.extend(chunk)
        self.stream.feed(self.decoder.decode(chunk))
        harness.answer_terminal_queries(self.master, self.raw, self.answered)

    def text(self):
        return "\n".join(self.screen.display)

    def wait(self, predicate, description, timeout=30):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.read()
            if predicate():
                # Let the event loop finish rendering after the observable
                # result. This is bounded even when cursor ticks keep arriving.
                until = time.monotonic() + 0.4
                while time.monotonic() < until:
                    self.read(0.05)
                return
        raise RuntimeError("Timed out waiting for " + description + "\n" + self.text())

    def send(self, text):
        os.write(self.master, text.encode())

    def completed_jobs(self):
        jobs = [json.loads(path.read_text()) for path in (self.home / "jobs").glob("*.json")]
        failed = [job for job in jobs if job["state"] in ("failed", "cancelled")]
        if failed:
            raise RuntimeError("Demo background job failed")
        return len(jobs) == 2 and all(job["state"] == "completed" for job in jobs)

    def save(self, destination):
        destination.write_bytes(self.raw)
        return hashlib.sha256(self.raw).hexdigest()

    def close(self):
        try:
            if self.process.poll() is None:
                os.killpg(self.process.pid, signal.SIGTERM)
                try:
                    self.process.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    os.killpg(self.process.pid, signal.SIGKILL)
                    self.process.wait(timeout=3)
        finally:
            os.close(self.master)


def capture(binary, output):
    project = output / "beacon"
    project.mkdir()
    for name, content in FILES.items():
        (project / name).write_text(content)
    git_env = {"PATH": os.environ["PATH"], "HOME": str(output),
               "GIT_AUTHOR_NAME": "PacketCode Demo", "GIT_AUTHOR_EMAIL": "demo@example.com",
               "GIT_COMMITTER_NAME": "PacketCode Demo", "GIT_COMMITTER_EMAIL": "demo@example.com"}
    for args in [("init", "-q", "--initial-branch=main"), ("add", "."), ("commit", "-qm", "Beacon example")]:
        subprocess.run(["git", "-c", "core.hooksPath=/dev/null", *args], cwd=project, env=git_env, check=True)
    server = ThreadingHTTPServer(("127.0.0.1", 0), DemoProvider)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    hashes = {}
    try:
        for state in STATES:
            session = Session(binary, project, output / (state + "-home"), "http://127.0.0.1:" + str(server.server_port))
            try:
                session.wait(lambda: "Ask packetcode anything" in session.text(), "welcome screen")
                if state == "chat":
                    session.send("I am building Beacon, a small Go HTTP service. What should a good health endpoint include?\r")
                    session.wait(lambda: "Add tests for the response body" in session.text() and "thinking" not in session.text(), "demo reply")
                elif state == "approval":
                    session.send("Read README.md, then add a curl example for /healthz.\r")
                    session.wait(lambda: "Allow this request?" in session.text() and "curl http://localhost" in session.text(), "real file patch approval")
                elif state == "agents":
                    session.send("/spawn Review main.go for HTTP semantics and reliability.\r")
                    session.wait(lambda: len(list((session.home / "jobs").glob("*.json"))) == 1, "first job")
                    session.send("/spawn Review main_test.go for focused coverage improvements.\r")
                    session.wait(session.completed_jobs, "two completed real jobs")
                    session.send("/agents \r")
                    session.wait(lambda: "2 completed" in session.text() and "packetcode agents" in session.text(), "Agent View")
                else:
                    session.send("/model \r")
                    session.wait(lambda: "Select model" in session.text() and "3 available" in session.text() and "refreshing" not in session.text(), "model catalog")
                hashes[state] = session.save(output / (state + ".ansi"))
                print("Captured actual released app:", state, flush=True)
            finally:
                session.close()
        if (project / "README.md").read_text() != README:
            raise RuntimeError("The pending approval unexpectedly changed the project")
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=3)
    return hashes


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    arguments = parser.parse_args()
    arguments.output.mkdir(parents=True, exist_ok=False)
    capture(arguments.binary.resolve(), arguments.output.resolve())
