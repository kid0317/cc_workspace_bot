"""State file: per-(framework_session, claude_session, transcript) byte offsets.

Design ref: docs/langfuse-cost-tracking-design.md §5.4.
- Three-element keying so /new rotation cannot reuse stale offset.
- flock around save() to prevent concurrent-hook offset stomp.
- Atomic write via .tmp + rename.
"""
from __future__ import annotations

import fcntl
import hashlib
import json
import os
from contextlib import contextmanager
from pathlib import Path
from typing import Any


def compute_key(framework_session_id: str, claude_session_id: str, transcript_path: str) -> str:
    h = hashlib.sha256(
        f"{framework_session_id}::{claude_session_id}::{transcript_path}".encode()
    )
    return h.hexdigest()


def load(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        return {}


@contextmanager
def _flocked(path: Path):
    """Hold an exclusive flock on a sibling lockfile while yielding.

    We lock on a sibling (.lock) rather than the state file itself so that
    the atomic-rename in save() doesn't replace the locked inode.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    lockfile = path.with_suffix(path.suffix + ".lock")
    f = open(lockfile, "w")
    try:
        fcntl.flock(f.fileno(), fcntl.LOCK_EX)
        yield
    finally:
        try:
            fcntl.flock(f.fileno(), fcntl.LOCK_UN)
        finally:
            f.close()


def save(path: Path, data: dict[str, Any]) -> None:
    with _flocked(path):
        tmp = path.with_suffix(path.suffix + ".tmp")
        tmp.write_text(json.dumps(data, indent=2), encoding="utf-8")
        os.replace(tmp, path)


def update_one(path: Path, key: str, value: dict[str, Any]) -> None:
    """Read-modify-write a single key under the flock."""
    with _flocked(path):
        try:
            cur = json.loads(path.read_text(encoding="utf-8")) if path.exists() else {}
        except (json.JSONDecodeError, OSError):
            cur = {}
        cur[key] = value
        tmp = path.with_suffix(path.suffix + ".tmp")
        tmp.write_text(json.dumps(cur, indent=2), encoding="utf-8")
        os.replace(tmp, path)
