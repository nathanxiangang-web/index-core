#!/usr/bin/env python3
"""P12 verification-only AList/OpenList-compatible mutable provider fixture.

This server is the controlled provider for the P12 deployment soak. It
implements exactly the HTTP surface the accepted AList collector touches:

  POST /api/fs/list     full pagination and scoped refresh=true
  POST /api/auth/login  optional; token-based roots never call it

Plus a verification-only control surface that is never reachable through any
IndexCore or Reference Test Web production API:

  POST /__control/reset
  POST /__control/upsert
  POST /__control/delete
  POST /__control/fail
  POST /__control/block
  POST /__control/unblock
  GET  /__control/state

The fixture never returns a successful truncation: ``data.total`` always equals
the number of direct children of the requested directory, and a page never
claims more entries than it returns.

Provider semantics implemented (per docs/research/d02):
  * directories are addressed by absolute provider path, "/" for the root;
  * ``content`` is the requested page of direct children only (no recursion);
  * an empty directory is ``{"content": [], "total": 0}``;
  * ``refresh`` is accepted and always served from current in-memory state;
  * a failure mode returns a typed non-200 body or an HTTP 5xx;
  * a block mode holds a matching scoped request until unblocked.

No secret is required: the soak binds roots with ``token_env`` reference and
the fixture accepts any bearer token. It never reads provider credentials.
"""

from __future__ import annotations

import argparse
import json
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOCK = threading.RLock()


class State:
    def __init__(self) -> None:
        # path -> list[entry dict]; each path's direct children in stable order.
        self.dirs: dict[str, list[dict]] = {}
        # transient failure: {"count": n, "paths": [..]|None, "status": int, "code": int}
        self.fail: dict | None = None
        # block: {"paths": [..], "event": threading.Event, "active": bool}
        self.block: dict | None = None
        # delay: {"seconds": n, "paths": [..]|None} - bounded provider latency
        self.delay: dict | None = None
        self.list_calls = 0
        self.refresh_calls = 0

    def snapshot(self) -> dict:
        with LOCK:
            return {
                "dirs": {p: [e["name"] for e in v] for p, v in sorted(self.dirs.items())},
                "fail": None
                if self.fail is None
                else {k: v for k, v in self.fail.items() if k != "event"},
                "block": None
                if self.block is None
                else {"paths": self.block["paths"], "active": self.block["active"]},
                "delay": None
                if self.delay is None
                else {"paths": self.delay["paths"], "seconds": self.delay["seconds"]},
                "list_calls": self.list_calls,
                "refresh_calls": self.refresh_calls,
            }


STATE = State()


def normalize_path(raw: str) -> str:
    p = (raw or "").strip()
    if p == "":
        p = "/"
    if not p.startswith("/"):
        p = "/" + p
    while "//" in p:
        p = p.replace("//", "/")
    if len(p) > 1 and p.endswith("/"):
        p = p.rstrip("/")
    if ".." in p.split("/"):
        raise ValueError("path traversal rejected")
    return p


def make_entry(name: str, is_dir: bool, size: int = 0, sha1: str | None = None,
               modified: str | None = None) -> dict:
    entry = {
        "name": name,
        "size": 0 if is_dir else int(size),
        "is_dir": bool(is_dir),
        "modified": modified or "2026-01-02T03:04:05Z",
        "type": 1 if is_dir else 0,
    }
    if sha1:
        entry["hash_info"] = {"sha1": sha1}
    return entry


def path_matches(candidate: str, paths: list[str]) -> bool:
    for p in paths:
        if candidate == p or candidate.startswith(p.rstrip("/") + "/"):
            return True
    return False


class Handler(BaseHTTPRequestHandler):
    server_version = "P12AListFixture/1.0"
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt: str, *args) -> None:  # keep stdout clean
        sys.stderr.write("[fixture] " + (fmt % args) + "\n")

    def _send(self, status: int, payload: dict) -> None:
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read_json(self) -> dict:
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0:
            return {}
        raw = self.rfile.read(length)
        try:
            value = json.loads(raw)
        except json.JSONDecodeError:
            return {}
        return value if isinstance(value, dict) else {}

    def do_GET(self) -> None:
        route = self.path.split("?", 1)[0]
        if route == "/__control/state":
            self._send(200, {"ok": True, "state": STATE.snapshot()})
            return
        if route == "/healthz":
            self._send(200, {"status": "ok"})
            return
        self._send(404, {"code": 404, "message": "not found"})

    def do_POST(self) -> None:
        route = self.path.split("?", 1)[0]
        body = self._read_json()
        if route == "/api/fs/list":
            self._handle_list(body)
            return
        if route == "/api/auth/login":
            self._send(200, {"code": 200, "message": "success",
                             "data": {"token": "p12-fixture-token"}})
            return
        if route.startswith("/__control/"):
            self._handle_control(route, body)
            return
        self._send(404, {"code": 404, "message": "not found"})

    def _handle_list(self, body: dict) -> None:
        try:
            path = normalize_path(str(body.get("path") or ""))
        except ValueError as exc:
            self._send(200, {"code": 400, "message": str(exc)})
            return
        try:
            page = int(body.get("page") or 1)
        except (TypeError, ValueError):
            page = 1
        try:
            per_page = int(body.get("per_page") or 0)
        except (TypeError, ValueError):
            per_page = 0
        refresh = bool(body.get("refresh"))

        with LOCK:
            STATE.list_calls += 1
            if refresh:
                STATE.refresh_calls += 1
            fail = STATE.fail
            block = STATE.block

        # Deterministic block mode: hold the matching scoped request until the
        # driver unblocks it. IndexCore is hard-killed while this request is in
        # flight, so the block must be released after the crash.
        if block is not None and block["active"] and path_matches(path, block["paths"]):
            block["event"].wait(timeout=300)

        with LOCK:
            delay = STATE.delay
        if delay is not None and delay["seconds"] > 0 and (not delay["paths"] or path_matches(path, delay["paths"])):
            time.sleep(float(delay["seconds"]))

        if fail is not None and fail["count"] > 0 and (not fail["paths"] or path_matches(path, fail["paths"])):
            with LOCK:
                fail["count"] -= 1
                if fail["count"] <= 0:
                    STATE.fail = None
            status = int(fail.get("status") or 200)
            code = int(fail.get("code", 500))
            if status == 200:
                self._send(200, {"code": code, "message": "injected provider failure"})
            else:
                self._send(status, {"code": code, "message": "injected provider failure"})
            return

        with LOCK:
            children = list(STATE.dirs.get(path, []))

        total = len(children)
        if per_page > 0:
            start = max(page - 1, 0) * per_page
            content = children[start:start + per_page]
        else:
            content = children
        # total == len(all direct children); a page never over-claims.
        self._send(200, {"code": 200, "message": "success",
                         "data": {"total": total, "content": content}})

    def _handle_control(self, route: str, body: dict) -> None:
        with LOCK:
            if route == "/__control/reset":
                STATE.dirs = {}
                for path, entries in (body.get("dirs") or {}).items():
                    STATE.dirs[normalize_path(path)] = [
                        make_entry(
                            name=str(e.get("name")),
                            is_dir=bool(e.get("is_dir")),
                            size=int(e.get("size") or 0),
                            sha1=e.get("sha1"),
                            modified=e.get("modified"),
                        )
                        for e in entries
                    ]
                STATE.fail = None
                STATE.block = None
                STATE.delay = None
                STATE.list_calls = 0
                STATE.refresh_calls = 0
                self._send(200, {"ok": True})
                return

            if route == "/__control/upsert":
                path = normalize_path(str(body.get("path") or "/"))
                entry = body.get("entry") or {}
                if "is_dir" not in entry:
                    self._send(400, {"ok": False, "error": "entry.is_dir required"})
                    return
                normalized = make_entry(
                    name=str(entry.get("name")),
                    is_dir=bool(entry.get("is_dir")),
                    size=int(entry.get("size") or 0),
                    sha1=entry.get("sha1"),
                    modified=entry.get("modified"),
                )
                bucket = STATE.dirs.setdefault(path, [])
                for i, existing in enumerate(bucket):
                    if existing["name"] == normalized["name"]:
                        bucket[i] = normalized
                        break
                else:
                    bucket.append(normalized)
                self._send(200, {"ok": True})
                return

            if route == "/__control/delete":
                path = normalize_path(str(body.get("path") or "/"))
                name = str(body.get("name") or "")
                bucket = STATE.dirs.get(path, [])
                STATE.dirs[path] = [e for e in bucket if e["name"] != name]
                self._send(200, {"ok": True})
                return

            if route == "/__control/fail":
                STATE.fail = {
                    "count": int(body.get("count") or 1),
                    "paths": body.get("paths"),
                    "status": int(body.get("status") or 200),
                    "code": int(body.get("code") or 500),
                }
                self._send(200, {"ok": True})
                return

            if route == "/__control/delay":
                seconds = int(body.get("seconds") or 0)
                paths = body.get("paths")
                if seconds <= 0:
                    STATE.delay = None
                else:
                    STATE.delay = {"seconds": seconds,
                                   "paths": [normalize_path(p) for p in paths] if paths else None}
                self._send(200, {"ok": True})
                return

            if route == "/__control/block":
                paths = body.get("paths") or ["/"]
                STATE.block = {"paths": [normalize_path(p) for p in paths],
                               "event": threading.Event(), "active": True}
                self._send(200, {"ok": True})
                return

            if route == "/__control/unblock":
                if STATE.block is not None:
                    STATE.block["active"] = False
                    STATE.block["event"].set()
                self._send(200, {"ok": True})
                return

        self._send(404, {"ok": False, "error": "unknown control route"})


def main() -> None:
    parser = argparse.ArgumentParser(description="P12 AList/OpenList provider fixture")
    parser.add_argument("--addr", default="127.0.0.1:9000")
    args = parser.parse_args()
    host, _, port = args.addr.rpartition(":")
    if not host:
        host = "127.0.0.1"
    server = ThreadingHTTPServer((host, int(port)), Handler)
    server.daemon_threads = True
    sys.stderr.write(f"[fixture] listening on {host}:{port}\n")
    server.serve_forever()


if __name__ == "__main__":
    main()