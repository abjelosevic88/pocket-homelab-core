#!/usr/bin/env python3
"""Pair a terminal with Pocket Core and configure Codex or Claude Code.

Python 3.11+, standard library only. No localhost listener or server SSH needed.
Credentials are exchanged over HTTPS and written only to a private user config.
"""
import argparse
import copy
import getpass
import json
import os
from pathlib import Path
import re
import socket
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

try:
    import tomllib
except ImportError:
    sys.exit("Pocket Connect needs Python 3.11 or newer.")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def core_url(value):
    parsed = urllib.parse.urlsplit(value)
    if (not parsed.hostname or parsed.username or parsed.password or parsed.query
            or parsed.fragment or parsed.path not in ("", "/")
            or any(ord(c) < 33 for c in value)):
        raise ValueError("Use the Core origin, for example https://core.example.com")
    if parsed.scheme != "https" and not (parsed.scheme == "http" and parsed.hostname in ("localhost", "127.0.0.1", "::1")):
        raise ValueError("Remote Core connections require HTTPS.")
    return value.rstrip("/")


def post(base, path, payload, token=None):
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(base + path, data=json.dumps(payload).encode(), headers=headers, method="POST")
    try:
        with urllib.request.build_opener(NoRedirect()).open(request, timeout=20) as response:
            raw = response.read(4 * 1024 * 1024 + 1)
            if len(raw) > 4 * 1024 * 1024:
                raise ValueError("Core response exceeded the size limit.")
            return json.loads(raw)
    except urllib.error.HTTPError as error:
        # Never echo server response bodies: a proxy could reflect credentials.
        messages = {400: "Code expired, invalid or already used; start again.",
                    401: "Core rejected the credential.", 403: "This access is disabled on Core.",
                    409: "Connection limit reached. Disconnect an unused token in AI connections.",
                    429: "Too many attempts. Wait briefly and retry."}
        raise ValueError(messages.get(error.code, "Core returned HTTP " + str(error.code))) from None
    except (urllib.error.URLError, TimeoutError):
        raise ValueError("Cannot reach Core. Check the URL and network connection.") from None


def config_path(client):
    if client == "codex":
        return Path(os.environ.get("CODEX_HOME", str(Path.home() / ".codex"))) / "config.toml"
    if os.environ.get("CLAUDE_CONFIG_DIR"):
        return Path(os.environ["CLAUDE_CONFIG_DIR"]) / ".claude.json"
    return Path.home() / ".claude.json"


def read_config(path):
    if path.is_symlink():
        raise ValueError("Config is a symlink; pass --config with its real file path.")
    return path.read_text() if path.exists() else ""


def updated_config(original, client, name, endpoint, token, replace=False):
    if client == "claude":
        data = json.loads(original or "{}")
        servers = data.setdefault("mcpServers", {})
        if name in servers and not replace:
            raise ValueError("That MCP name already exists. Choose --name or use --replace to replace it.")
        servers[name] = {"type": "http", "url": endpoint, "headers": {"Authorization": "Bearer " + token}}
        return json.dumps(data, indent=2, ensure_ascii=False) + "\n"
    data = tomllib.loads(original)
    before = copy.deepcopy(data)
    servers = data.setdefault("mcp_servers", {})
    begin, end = f"# Pocket Connect: {name} BEGIN", f"# Pocket Connect: {name} END"
    pattern = re.compile(r"(?m)^" + re.escape(begin) + r"\n.*?^" + re.escape(end) + r"\n?", re.S)
    matches = list(pattern.finditer(original))
    if name in servers:
        if not replace:
            raise ValueError("That MCP name already exists. Choose --name or use --replace to renew it.")
        if len(matches) != 1:
            raise ValueError("Existing Codex entry was not created by Pocket Connect. Choose a different --name.")
    elif matches:
        raise ValueError("Incomplete Pocket Connect config block; fix it before connecting.")
    block = (begin + "\n[mcp_servers." + name + "]\nurl = " + json.dumps(endpoint)
             + '\nhttp_headers = { Authorization = ' + json.dumps("Bearer " + token) + ' }\n' + end + "\n")
    updated = pattern.sub(lambda _: block, original, count=1) if matches else original.rstrip() + "\n\n" + block
    # Parse the result and compare every other setting. This also catches unusual
    # TOML layouts where appending a table would change another section's meaning.
    after = tomllib.loads(updated)
    expected = before
    expected.setdefault("mcp_servers", {})[name] = {"url": endpoint, "http_headers": {"Authorization": "Bearer " + token}}
    if after != expected:
        raise ValueError("Cannot safely update this TOML layout; choose a separate --config file.")
    return updated


def write_config(path, original, updated):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    # Do not clobber edits made while the browser approval was in progress.
    if read_config(path) != original:
        raise ValueError("Config changed while pairing. No changes written; disconnect the new connection in Core and retry.")
    fd, temporary = tempfile.mkstemp(prefix=".pocket-connect-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            os.fchmod(stream.fileno(), 0o600)
            stream.write(updated)
            stream.flush()
            os.fsync(stream.fileno())
        if read_config(path) != original:
            raise ValueError("Config changed while pairing; retry after disconnecting the new connection in Core.")
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def check_mcp(base, token):
    initialized = post(base, "/mcp", {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
        "protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "pocket-connect", "version": "1"}}}, token)
    if initialized.get("error") or "result" not in initialized:
        raise ValueError("MCP initialization failed. Configuration was saved; check Core availability.")
    result = post(base, "/mcp", {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}, token)
    if result.get("error") or not isinstance(result.get("result", {}).get("tools"), list):
        raise ValueError("MCP tool discovery failed. Configuration was saved; check Core availability.")
    return len(result["result"]["tools"])


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True, help="Core origin, e.g. https://core.example.com")
    parser.add_argument("--client", choices=["codex", "claude"], required=True)
    parser.add_argument("--name", default="pocket_core", help="MCP name in the assistant config")
    parser.add_argument("--label", help="Connection label displayed in Core")
    parser.add_argument("--access", choices=["status", "read", "write"], default="write")
    parser.add_argument("--days", type=int, choices=[1, 7, 30], default=30)
    parser.add_argument("--config", type=Path, help="Override the assistant's user config file")
    parser.add_argument("--replace", action="store_true", help="Replace this named connection (old Core grant remains revocable)")
    args = parser.parse_args(argv)
    base = core_url(args.url)
    if not re.fullmatch(r"[A-Za-z][A-Za-z0-9_-]{0,63}", args.name):
        raise ValueError("Use an MCP name with letters, numbers, underscores or hyphens (max 64).")
    path = (args.config or config_path(args.client)).expanduser().absolute()
    original = read_config(path)
    # Validate config before the user spends time approving a connection.
    updated_config(original, args.client, args.name, base + "/mcp", "preflight", args.replace)
    scopes = {"status": "status:read", "read": "status:read apps:read", "write": "status:read apps:read apps:write"}
    label = args.label or ("Codex" if args.client == "codex" else "Claude Code") + " on " + socket.gethostname()[:40]
    pending = post(base, "/mcp/pair/start", {"name": label, "scope": scopes[args.access], "expires_in_days": args.days})
    if pending.get("verification_uri") != base + "/ai-connections/pair" or not re.fullmatch(r"[A-Z2-9]{5}-[A-Z2-9]{5}", pending.get("user_code", "")):
        raise ValueError("Core returned an unexpected pairing response.")
    print("\nOpen this link on any device and sign in to Core:\n" + pending["verification_uri"], flush=True)
    print("\nEnter this pairing code: " + pending["user_code"], flush=True)
    print("Review the access and approve. Paste the completion code here within 10 minutes.\n", flush=True)
    # A normal terminal suppresses echo. Pipe input remains useful for automation
    # and testing without requiring a browser on this machine.
    completion = getpass.getpass("Completion code: ") if sys.stdin.isatty() else input("Completion code: ")
    completion = completion.strip()
    if not re.fullmatch(r"pcmp_done_[A-Za-z0-9_-]{43}", completion):
        raise ValueError("Paste the full completion code shown by Core after approval.")
    result = post(base, "/mcp/pair/exchange", {"request_secret": pending["request_secret"], "completion_code": completion})
    token = result.get("token", "")
    if result.get("url") != base + "/mcp" or not re.fullmatch(r"pcma_[A-Za-z0-9_-]{43}", token):
        raise ValueError("Core returned unexpected credentials.")
    updated = updated_config(original, args.client, args.name, result["url"], token, args.replace)
    write_config(path, original, updated)
    print("\nMCP connection saved to " + str(path) + " (readable only by your user).", flush=True)
    print("Checking MCP…", flush=True)
    count = check_mcp(base, token)
    print(f"Connected: {count} tools available. Expires {result['connection']['expires_at']}.")
    print("Start a new " + ("Codex" if args.client == "codex" else "Claude Code") + " session. No separate MCP login is needed.")
    print("Try: Check the status of all applications I can access.")
    print("Disconnect or renew from Core → AI connections. Keep the config private; it contains your credential.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, TypeError, EOFError) as error:
        # Parsing errors can include user config fragments. Avoid dumping them.
        message = "Invalid config or server response." if isinstance(error, (json.JSONDecodeError, tomllib.TOMLDecodeError, KeyError, TypeError)) else str(error)
        print("Pocket Connect: " + message, file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print("\nPairing cancelled. Unused pairing requests expire after 10 minutes.", file=sys.stderr)
        sys.exit(130)
