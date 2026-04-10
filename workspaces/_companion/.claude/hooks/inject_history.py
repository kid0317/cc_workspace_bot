#!/usr/bin/env python3
"""
inject_history.py — UserPromptSubmit hook for companion workspace.

每次用户发消息前，从 SQLite 查询该 channel 的最近 N 轮对话，
写入 memory/RECENT_HISTORY.md，供 CLAUDE.md 启动时读取。

设计原则：
- 通过 WORKSPACE_DIR 环境变量定位 workspace（executor.go 注入），
  不依赖当前工作目录（Claude Code hook 的 cwd 不保证是 session_dir）
- 输出到 {workspace}/memory/RECENT_HISTORY.md（稳定路径，每次覆盖）
- 任何错误静默跳过，绝不阻塞 Claude 执行

重要区分：
  channel_key（SQLite 会话标识，如 p2p:oc_xxx:cli_yyy）
  ≠ routing_key（飞书发送目标，如 p2p:oc_xxx，存在 user_profile.md 中）
  本脚本只使用 channel_key 查询历史，不涉及 routing_key。
"""
import sqlite3
import os
from pathlib import Path
from datetime import datetime, timezone

RECENT_N = 20  # 最近 N 条消息（user + assistant 各算一条）


def parse_timestamp(ts_val) -> str:
    """安全地将 SQLite 时间戳转为 YYYY-MM-DD 字符串。
    SQLite 可能存储 RFC3339 字符串、ISO 字符串或 Unix 整数/浮点。
    """
    if ts_val is None:
        return "unknown"
    s = str(ts_val).strip()
    for fmt in (
        "%Y-%m-%dT%H:%M:%SZ",
        "%Y-%m-%dT%H:%M:%S+00:00",
        "%Y-%m-%d %H:%M:%S",
        "%Y-%m-%dT%H:%M:%S",
    ):
        try:
            return datetime.strptime(s[: len(fmt)], fmt).strftime("%Y-%m-%d")
        except ValueError:
            continue
    try:
        return datetime.fromtimestamp(float(s), tz=timezone.utc).strftime("%Y-%m-%d")
    except (ValueError, OSError):
        pass
    if len(s) >= 10 and s[:4].isdigit() and s[4] == "-":
        return s[:10]
    return "unknown"


def find_session_context(workspace_dir: Path):
    """在 {workspace}/sessions/ 下找最近修改的 SESSION_CONTEXT.md，
    返回 (db_path, channel_key)，任一缺失返回 (None, None)。
    """
    sessions_dir = workspace_dir / "sessions"
    if not sessions_dir.exists():
        return None, None

    ctx_files = sorted(
        sessions_dir.rglob("SESSION_CONTEXT.md"),
        key=lambda p: p.stat().st_mtime,
        reverse=True,
    )
    if not ctx_files:
        return None, None

    db_path = channel_key = None
    for line in ctx_files[0].read_text().splitlines():
        if line.startswith("- DB path:"):
            db_path = line[len("- DB path:") :].strip()
        elif line.startswith("- Channel key:"):
            # channel_key 本身含冒号（如 p2p:oc_xxx:cli_yyy），用切片而非 split
            channel_key = line[len("- Channel key:") :].strip()
    return db_path, channel_key


def main():
    workspace_dir_str = os.environ.get("WORKSPACE_DIR", "")
    if not workspace_dir_str:
        return  # executor.go 未注入 WORKSPACE_DIR，静默跳过

    workspace_dir = Path(workspace_dir_str)
    if not workspace_dir.exists():
        return

    db_path, channel_key = find_session_context(workspace_dir)
    if not db_path or not channel_key:
        return

    try:
        conn = sqlite3.connect(db_path)
        rows = conn.execute(
            """
            SELECT m.role, m.content, m.created_at
            FROM messages m
            JOIN sessions s ON m.session_id = s.id
            WHERE s.channel_key = ?
              AND m.content != ''
            ORDER BY m.created_at DESC
            LIMIT ?
            """,
            (channel_key, RECENT_N),
        ).fetchall()
        conn.close()
    except Exception:
        return  # DB 异常静默跳过

    if not rows:
        return

    lines = [
        "# 最近对话记录（跨 session）\n",
        f"> 自动注入，最近 {len(rows)} 条，channel: {channel_key}\n\n",
    ]
    for role, content, ts in reversed(rows):
        tag = "**用户**" if role == "user" else "**角色**"
        date_str = parse_timestamp(ts)
        body = content[:500] + ("…" if len(content) > 500 else "")
        lines.append(f"{tag}（{date_str}）：{body}\n\n")

    output = workspace_dir / "memory" / "RECENT_HISTORY.md"
    output.write_text("".join(lines))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        pass  # 全局兜底，绝不阻塞 Claude 执行
