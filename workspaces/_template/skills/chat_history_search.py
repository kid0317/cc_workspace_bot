#!/usr/bin/env python3
"""
chat_history_search.py — Query chat history from bot.db.

The channel scope is derived from SESSION_CONTEXT.md in the current session
directory — it cannot be overridden by the user via CLI arguments.

Usage:
    python3 skills/chat_history_search.py \\
        --session-dir <session_dir>   # SESSION_CONTEXT.md is read from here
        [--keyword <text>]            # full-text search (LIKE)
        [--days   <n>]                # look back N days (default 7)
        [--limit  <n>]                # max messages returned (default 50)
        [--role   user|assistant|all] # (default all)
        [--sessions]                  # list sessions only, no message detail
"""

import argparse
import os
import sqlite3
import sys
from datetime import datetime, timedelta, timezone
from typing import Any

_CONTENT_MAX = 500        # characters before truncation
_CONTEXT_FILE = "SESSION_CONTEXT.md"


# ── SESSION_CONTEXT.md reader (security boundary) ────────────────────────────

def read_channel_key(session_dir: str) -> str:
    """
    Parse Channel key from SESSION_CONTEXT.md in session_dir.

    This is the security boundary: channel scope is always taken from the
    framework-injected file, never from user-supplied CLI arguments.

    Raises:
        FileNotFoundError: SESSION_CONTEXT.md does not exist.
        ValueError: Channel key line is absent or blank.
    """
    ctx_path = os.path.join(session_dir, _CONTEXT_FILE)
    if not os.path.exists(ctx_path):
        raise FileNotFoundError(f"SESSION_CONTEXT.md not found in: {session_dir}")

    with open(ctx_path) as f:
        for line in f:
            if line.startswith("- Channel key:"):
                value = line.split(":", 1)[1].strip()
                if not value:
                    raise ValueError(
                        "Channel key is blank in SESSION_CONTEXT.md — "
                        "framework may not have set it yet."
                    )
                return value

    raise ValueError("'Channel key' line not found in SESSION_CONTEXT.md")


def read_db_path(session_dir: str) -> str:
    """
    Parse DB path from SESSION_CONTEXT.md in session_dir.

    Raises:
        FileNotFoundError: SESSION_CONTEXT.md does not exist.
        ValueError: DB path line is absent or blank.
    """
    ctx_path = os.path.join(session_dir, _CONTEXT_FILE)
    if not os.path.exists(ctx_path):
        raise FileNotFoundError(f"SESSION_CONTEXT.md not found in: {session_dir}")

    with open(ctx_path) as f:
        for line in f:
            if line.startswith("- DB path:"):
                value = line.split(":", 1)[1].strip()
                if not value:
                    raise ValueError("DB path is blank in SESSION_CONTEXT.md")
                return value

    raise ValueError("'DB path' line not found in SESSION_CONTEXT.md")


# ── Core query ────────────────────────────────────────────────────────────────

def query_history(
    db_path: str,
    channel_key: str,
    *,
    keyword: str | None = None,
    days: int = 7,
    limit: int = 50,
    role: str = "all",
    sessions_only: bool = False,
) -> list[dict[str, Any]]:
    """
    Return chat history rows scoped strictly to channel_key.

    Raises FileNotFoundError if db_path does not exist.
    """
    if not os.path.exists(db_path):
        raise FileNotFoundError(f"Database not found: {db_path}")

    cutoff = (
        datetime.now(timezone.utc) - timedelta(days=days)
    ).strftime("%Y-%m-%d %H:%M:%S")

    con = sqlite3.connect(db_path)
    con.row_factory = sqlite3.Row
    try:
        if sessions_only:
            return _query_sessions_only(con, channel_key, cutoff)
        return _query_messages(con, channel_key, cutoff, keyword, role, limit)
    finally:
        con.close()


def _query_sessions_only(
    con: sqlite3.Connection, channel_key: str, cutoff: str
) -> list[dict[str, Any]]:
    sql = """
        SELECT
            s.id            AS session_id,
            s.status        AS session_status,
            s.created_at    AS session_created_at,
            s.updated_at    AS session_updated_at,
            COUNT(m.id)     AS message_count
        FROM sessions s
        LEFT JOIN messages m ON m.session_id = s.id
        WHERE s.channel_key = ?
          AND s.created_at  >= ?
        GROUP BY s.id
        ORDER BY s.created_at DESC
    """
    rows = con.execute(sql, (channel_key, cutoff)).fetchall()
    return [dict(r) for r in rows]


def _query_messages(
    con: sqlite3.Connection,
    channel_key: str,
    cutoff: str,
    keyword: str | None,
    role: str,
    limit: int,
) -> list[dict[str, Any]]:
    params: list[Any] = [channel_key, cutoff]

    role_clause = ""
    if role in ("user", "assistant"):
        role_clause = "AND m.role = ?"
        params.append(role)

    keyword_clause = ""
    if keyword:
        keyword_clause = "AND m.content LIKE ?"
        params.append(f"%{keyword}%")

    params.append(limit)

    sql = f"""
        SELECT
            s.id            AS session_id,
            s.status        AS session_status,
            s.created_at    AS session_created_at,
            s.updated_at    AS session_updated_at,
            m.id            AS message_id,
            m.role          AS role,
            m.content       AS content,
            m.created_at    AS created_at
        FROM messages m
        JOIN sessions s ON s.id = m.session_id
        WHERE s.channel_key = ?
          AND s.created_at  >= ?
          {role_clause}
          {keyword_clause}
        ORDER BY m.created_at ASC
        LIMIT ?
    """
    rows = con.execute(sql, params).fetchall()

    result = []
    for r in rows:
        row = dict(r)
        row["content"] = _truncate(row.get("content") or "")
        result.append(row)
    return result


def _truncate(text: str) -> str:
    if len(text) <= _CONTENT_MAX:
        return text
    return text[:_CONTENT_MAX] + f" [已截断，完整内容 {len(text)} 字符]"


# ── Formatter ─────────────────────────────────────────────────────────────────

def format_output(
    rows: list[dict[str, Any]],
    channel_key: str,
    days: int,
    *,
    sessions_only: bool = False,
) -> str:
    if not rows:
        return f"未找到符合条件的聊天记录（频道：{channel_key}，最近 {days} 天）"

    lines = [
        "## 聊天记录查询结果",
        f"频道：{channel_key}　查询范围：最近 {days} 天",
        "",
    ]

    if sessions_only:
        lines.append(f"共 {len(rows)} 个 session\n")
        for r in rows:
            lines.append(
                f"- **{r['session_id']}**  状态：{r['session_status']}  "
                f"消息数：{r['message_count']}  "
                f"创建：{r['session_created_at']}"
            )
        return "\n".join(lines)

    # Group messages by session
    sessions: dict[str, list[dict]] = {}
    session_meta: dict[str, dict] = {}
    for r in rows:
        sid = r["session_id"]
        if sid not in sessions:
            sessions[sid] = []
            session_meta[sid] = {
                "status": r["session_status"],
                "created_at": r["session_created_at"],
                "updated_at": r["session_updated_at"],
            }
        sessions[sid].append(r)

    lines.append(f"共 {len(sessions)} 个 session，{len(rows)} 条消息\n")

    for sid, msgs in sessions.items():
        meta = session_meta[sid]
        lines.append(
            f"### Session {sid}  "
            f"（{meta['created_at']} — {meta['updated_at']}，状态：{meta['status']}）"
        )
        for m in msgs:
            ts = (m["created_at"] or "")[:16]
            lines.append(f"**[{ts}] {m['role']}**：{m['content']}")
        lines.append("")

    return "\n".join(lines)


# ── CLI entry point ───────────────────────────────────────────────────────────

def _parse_args(argv=None):
    p = argparse.ArgumentParser(
        description=(
            "Query chat history from bot.db. "
            "Channel scope is read from SESSION_CONTEXT.md — not user-supplied."
        )
    )
    p.add_argument("--session-dir", required=True,
                   help="Current session directory (contains SESSION_CONTEXT.md)")
    p.add_argument("--keyword", default=None,  help="Full-text search keyword")
    p.add_argument("--days",    type=int, default=7,  help="Look back N days (default 7)")
    p.add_argument("--limit",   type=int, default=50, help="Max messages (default 50)")
    p.add_argument("--role",    default="all", choices=["user", "assistant", "all"])
    p.add_argument("--sessions", action="store_true",
                   help="List sessions only, no message detail")
    return p.parse_args(argv)


def main(argv=None):
    args = _parse_args(argv)
    try:
        channel_key = read_channel_key(args.session_dir)
        db_path = read_db_path(args.session_dir)
        rows = query_history(
            db_path,
            channel_key,
            keyword=args.keyword,
            days=args.days,
            limit=args.limit,
            role=args.role,
            sessions_only=args.sessions,
        )
    except (FileNotFoundError, ValueError) as e:
        print(f"错误：{e}", file=sys.stderr)
        sys.exit(1)

    print(format_output(rows, channel_key, args.days, sessions_only=args.sessions))


if __name__ == "__main__":
    main()
