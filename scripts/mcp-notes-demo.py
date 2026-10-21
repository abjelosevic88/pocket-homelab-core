#!/usr/bin/env python3
"""Run an isolated local Core MCP preview against an existing Notes service."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import shlex
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--notes-url", default="http://127.0.0.1:8182")
    parser.add_argument("--port", type=int, default=8190)
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    # Refuse an occupied port before sending any registration credential.
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", args.port))
    with tempfile.TemporaryDirectory(prefix="pocket-mcp-demo-") as directory:
        root = Path(directory)
        token = secrets.token_urlsafe(32)
        registration_key = "pcreg_" + secrets.token_urlsafe(32)
        client_env = root / "client.env"
        client_env.write_text("export POCKET_MCP_TOKEN=" + shlex.quote(token) + "\n")
        client_env.chmod(0o600)
        binary = root / "pocket-core"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/pocket-core"], cwd=repo, check=True)
        base = f"http://127.0.0.1:{args.port}"
        # Only inherit system execution settings, never another Core's database/auth settings.
        env = {key: value for key, value in os.environ.items()
               if key in {"PATH", "HOME", "TMPDIR", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR"}}
        env.update(HOST="127.0.0.1", PORT=str(args.port), BASE_URL=base,
                   DATA_DIR=str(root / "data"), AUTH_MODE="local", TRUST_PROXY="false",
                   REGISTRATION_KEY=registration_key, MCP_APPS="notes",
                   MCP_TOKEN_HASH=hashlib.sha256(token.encode()).hexdigest())
        with (root / "server.log").open("w") as log:
            process = subprocess.Popen([str(binary)], env=env, stdout=log, stderr=log)
            try:
                for _ in range(100):
                    if process.poll() is not None:
                        raise RuntimeError("Preview failed to start: " + (root / "server.log").read_text())
                    try:
                        with urllib.request.urlopen(base + "/readyz", timeout=1):
                            break
                    except (urllib.error.URLError, TimeoutError):
                        time.sleep(0.1)
                else:
                    raise RuntimeError("Preview did not become ready")
                request = urllib.request.Request(base + "/api/v1/apps/register",
                    data=json.dumps({"slug": "notes", "name": "Pocket Notes", "url": args.notes_url,
                                     "health_path": "/healthz"}).encode(),
                    headers={"Authorization": "Bearer " + registration_key, "Content-Type": "application/json"})
                with urllib.request.urlopen(request, timeout=5) as response:
                    response.read()  # The temporary app token stays out of terminal output.
                print(f"MCP preview ready: {base}/mcp", flush=True)
                print(f"In a second terminal: source {shlex.quote(str(client_env))}", flush=True)
                print(f'''Add to ~/.codex/config.toml, then start Codex in that second terminal:

[mcp_servers.pocket_status]
url = "{base}/mcp"
bearer_token_env_var = "POCKET_MCP_TOKEN"

Ask: Check Pocket Notes with a fresh health probe.
Ctrl-C stops the preview and removes its database and token file.''', flush=True)
                process.wait()
            finally:
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
