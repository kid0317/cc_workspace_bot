"""Business metadata loading: env vars first, sidecar fallback.

Design ref: docs/langfuse-cost-tracking-design.md §5.2.5.
Env vars are fork-frozen on the claude subprocess so they're immune to
file races. The sidecar exists only for OOB usage (e.g. backfill, manual
testing).
"""
from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

SIDECAR_FILENAME = ".langfuse_meta.json"


@dataclass(frozen=True)
class Meta:
    app_id: str
    framework_session_id: str
    channel_key: str
    user_open_id: str
    task_name: str | None = None


def _build_meta(app_id, fsid, ck, uoid, task_name) -> Meta | None:
    # All four required; reject if any missing/empty
    if not (app_id and fsid):
        return None
    return Meta(
        app_id=app_id,
        framework_session_id=fsid,
        channel_key=ck or "",
        user_open_id=uoid or "",
        task_name=task_name or None,
    )


def load_from_env(env: dict[str, str]) -> Meta | None:
    return _build_meta(
        app_id=env.get("CC_LF_APP_ID", ""),
        fsid=env.get("CC_LF_FRAMEWORK_SESSION_ID", ""),
        ck=env.get("CC_LF_CHANNEL_KEY", ""),
        uoid=env.get("CC_LF_USER_OPEN_ID", ""),
        task_name=env.get("CC_LF_TASK_NAME") or None,
    )


def load_from_sidecar(session_dir: Path) -> Meta | None:
    sidecar = session_dir / SIDECAR_FILENAME
    if not sidecar.exists():
        return None
    try:
        data = json.loads(sidecar.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        return None
    if not isinstance(data, dict):
        return None
    return _build_meta(
        app_id=data.get("app_id", ""),
        fsid=data.get("framework_session_id", ""),
        ck=data.get("channel_key", ""),
        uoid=data.get("user_open_id", ""),
        task_name=data.get("task_name"),
    )


def load(env: dict[str, str], session_dir: Path) -> Meta | None:
    """Env wins. Sidecar is fallback for OOB callers (backfill, testing)."""
    return load_from_env(env) or load_from_sidecar(session_dir)
