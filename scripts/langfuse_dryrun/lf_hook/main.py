"""Hook entrypoint: read stdin, parse transcript, emit to Langfuse.

Replaces the old monolithic langfuse_hook.py. Fail-open: any unhandled exception
returns 0 so claude exit is never blocked.
"""
from __future__ import annotations

import json
import logging
import os
import socket
import sys
from datetime import datetime
from pathlib import Path
from typing import Any

from . import meta as meta_mod
from . import parse, state

log = logging.getLogger("langfuse_hook")

STATE_DIR = Path.home() / ".claude" / "state"
LOG_FILE = STATE_DIR / "langfuse_hook.log"
STATE_FILE = STATE_DIR / "langfuse_state.json"


def _setup_logging():
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    # Idempotent: don't stack handlers across repeated main() invocations
    # (one-shot hook usually only runs once, but tests + future refactors can re-enter).
    if any(getattr(h, "_lf_hook_handler", False) for h in log.handlers):
        return
    handler = logging.FileHandler(LOG_FILE)
    handler.setFormatter(logging.Formatter("%(asctime)s [%(levelname)s] %(message)s"))
    handler._lf_hook_handler = True  # type: ignore[attr-defined]
    log.addHandler(handler)
    log.setLevel(
        logging.DEBUG if os.environ.get("CC_LANGFUSE_DEBUG", "").lower() == "true"
        else logging.INFO
    )


def _read_jsonl_from_offset(path: Path, from_offset: int) -> tuple[list[dict], int]:
    rows = []
    new_offset = from_offset
    try:
        with open(path, "rb") as f:
            f.seek(from_offset)
            while True:
                line = f.readline()
                if not line:
                    break
                new_offset = f.tell()
                line = line.strip()
                if not line:
                    continue
                try:
                    rows.append(json.loads(line))
                except json.JSONDecodeError:
                    pass
    except OSError as e:
        log.debug("read_jsonl error: %s", e)
    return rows, new_offset


def _init_langfuse():
    """Lazy import langfuse SDK. Returns (Langfuse_instance, propagate_attributes_fn) or (None, None)."""
    if os.environ.get("TRACE_TO_LANGFUSE", "").lower() != "true":
        log.debug("TRACE_TO_LANGFUSE != true; skipping")
        return None, None

    pub = os.environ.get("CC_LANGFUSE_PUBLIC_KEY") or os.environ.get("LANGFUSE_PUBLIC_KEY")
    sec = os.environ.get("CC_LANGFUSE_SECRET_KEY") or os.environ.get("LANGFUSE_SECRET_KEY")
    host = (
        os.environ.get("CC_LANGFUSE_BASE_URL")
        or os.environ.get("LANGFUSE_BASE_URL")
        or "https://cloud.langfuse.com"
    )
    if not (pub and sec):
        log.debug("missing langfuse keys")
        return None, None

    try:
        from langfuse import Langfuse, propagate_attributes
    except ImportError:
        log.debug("langfuse SDK not installed")
        return None, None

    try:
        return Langfuse(public_key=pub, secret_key=sec, host=host), propagate_attributes
    except Exception as e:  # noqa: BLE001
        log.warning("Langfuse init failed: %s", e)
        return None, None


def main(stdin: Any = None) -> int:
    _setup_logging()

    try:
        raw = (stdin or sys.stdin).read().strip()
        payload = json.loads(raw) if raw else {}
    except (json.JSONDecodeError, OSError):
        payload = {}

    claude_session_id = (
        payload.get("session_id")
        or payload.get("sessionId")
        or (payload.get("session") or {}).get("id", "")
    )
    transcript_path_raw = payload.get("transcript_path") or payload.get("transcriptPath")
    if not (claude_session_id and transcript_path_raw):
        log.debug("missing session_id or transcript_path: %s", payload)
        return 0
    transcript_path = Path(str(transcript_path_raw)).expanduser().resolve()
    if not transcript_path.exists():
        log.debug("transcript not found: %s", transcript_path)
        return 0

    # Meta loading: env first, then sidecar (search up from cwd / transcript)
    sidecar_search_dirs = [Path.cwd(), transcript_path.parent]
    m = meta_mod.load_from_env(dict(os.environ))
    if m is None:
        for d in sidecar_search_dirs:
            m = meta_mod.load_from_sidecar(d)
            if m is not None:
                break
    if m is None:
        log.warning("no meta available (env + sidecars empty); transcript=%s", transcript_path)
        return 0

    # State / incremental read
    key = state.compute_key(m.framework_session_id, claude_session_id, str(transcript_path))
    cur_state = state.load(STATE_FILE)
    cur = cur_state.get(key, {"offset": 0, "turn_count": 0})

    rows, new_offset = _read_jsonl_from_offset(transcript_path, cur["offset"])
    if not rows:
        state.update_one(STATE_FILE, key, {**cur, "offset": new_offset})
        return 0

    turns = parse.build_turns(rows)
    if not turns:
        state.update_one(STATE_FILE, key, {**cur, "offset": new_offset})
        return 0

    # Emit (best-effort; per-turn try/except inside)
    lf, propagate_fn = _init_langfuse()
    if lf is None:
        # Even if SDK is unavailable / disabled, advance offset so we don't re-read same rows
        state.update_one(STATE_FILE, key, {**cur, "offset": new_offset})
        return 0

    from . import emit as emit_mod
    try:
        emitted = emit_mod.emit_turns(
            lf, turns, m, str(transcript_path), rows,
            propagate_attributes_fn=propagate_fn,
        )
        log.info("emitted %d turns (session=%s, transcript=%s)", emitted,
                 m.framework_session_id, transcript_path.name)
    except Exception as e:  # noqa: BLE001
        log.warning("emit_turns failed: %s", e)

    try:
        lf.flush(timeout=2.0) if hasattr(lf.flush, "__call__") else lf.flush()
    except Exception:  # noqa: BLE001
        pass
    try:
        lf.shutdown()
    except Exception:  # noqa: BLE001
        pass

    state.update_one(STATE_FILE, key, {
        "offset": new_offset,
        "turn_count": cur.get("turn_count", 0) + len(turns),
    })
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as e:  # fail-open at the very edge
        try:
            STATE_DIR.mkdir(parents=True, exist_ok=True)
            with open(LOG_FILE, "a") as f:
                f.write(f"{datetime.now()} [ERROR] uncaught: {e}\n")
        except Exception:
            pass
        sys.exit(0)
