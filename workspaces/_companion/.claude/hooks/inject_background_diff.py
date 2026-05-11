#!/usr/bin/env python3
"""
inject_background_diff.py — UserPromptSubmit hook（陪伴空间专用）。

作用：强行把"上次用户对话之后，到本轮之间"产生的 assistant 消息
注入当前 turn 上下文。

解决的问题：主对话 session 通过 `claude --resume <sid>` 延续，内部上下文
只包含该 session 自己的消息链；proactive / 其他定时任务发出的 assistant
消息虽然写入了 DB，但不属于 resume 上下文，LLM 完全看不见，导致用户
引用这些"主动触达"内容时主对话不承认、不衔接、甚至直接改写事实。

做法：每次 UserPromptSubmit 时，查 DB 取"倒数第二条 user 消息 < created_at
< 倒数第一条 user 消息（即本轮）"区间内的所有 assistant 消息，作为硬约束
文本从 stdout 输出，由 Claude Code hook 机制注入 LLM 上下文。

与 inject_history.py 的区别：
- inject_history.py 写文件（RECENT_HISTORY.md），仅 turn 1 被 LLM 读到
- 本脚本 stdout 直出，每轮都会进入 LLM 可见 prompt
"""
import os
import sqlite3
from datetime import datetime
from pathlib import Path

CONTENT_MAX = 400  # 单条消息裁剪长度


def find_session_context(workspace_dir: Path):
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
            db_path = line[len("- DB path:"):].strip()
        elif line.startswith("- Channel key:"):
            channel_key = line[len("- Channel key:"):].strip()
    return db_path, channel_key


def fmt_ts(ts) -> str:
    if ts is None:
        return "unknown"
    s = str(ts).strip()
    for candidate in (s, s.rstrip("Z"), s.replace("T", " ")):
        try:
            dt = datetime.fromisoformat(candidate)
            if dt.tzinfo is not None:
                dt = dt.astimezone()
            return dt.strftime("%Y-%m-%d %H:%M")
        except ValueError:
            continue
    return s[:16]


def main() -> None:
    workspace_dir_str = os.environ.get("WORKSPACE_DIR", "")
    if not workspace_dir_str:
        return
    workspace_dir = Path(workspace_dir_str)
    if not workspace_dir.exists():
        return

    db_path, channel_key = find_session_context(workspace_dir)
    if not db_path or not channel_key:
        return

    try:
        conn = sqlite3.connect(db_path)
        # 倒数两条 user 消息的 created_at：
        #   [0] = 本轮刚写入的 user 消息（worker.go:174 在调 claude 之前写入）
        #   [1] = 上一轮 user 消息
        prev_user_rows = conn.execute(
            """
            SELECT m.created_at
              FROM messages m
              JOIN sessions s ON m.session_id = s.id
             WHERE s.channel_key = ? AND m.role = 'user'
             ORDER BY m.created_at DESC
             LIMIT 2
            """,
            (channel_key,),
        ).fetchall()
        if len(prev_user_rows) < 2:
            conn.close()
            return
        curr_user_ts = prev_user_rows[0][0]
        prev_user_ts = prev_user_rows[1][0]

        # 区间内 assistant 消息 = 上轮主对话回复 + 期间所有 proactive/定时任务发送。
        # 主对话回复对 LLM 属于冗余注入（它 --resume 自带），但无害；重点是
        # 让 proactive 这类 session 外写入的内容能进入本轮上下文。
        assistant_rows = conn.execute(
            """
            SELECT m.content, m.created_at
              FROM messages m
              JOIN sessions s ON m.session_id = s.id
             WHERE s.channel_key = ?
               AND m.role = 'assistant'
               AND m.created_at > ?
               AND m.created_at < ?
             ORDER BY m.created_at ASC
            """,
            (channel_key, prev_user_ts, curr_user_ts),
        ).fetchall()
        conn.close()
    except Exception:
        return

    if not assistant_rows:
        return

    out = [
        "[系统·背景差分 · 仅作者可见，不发送给用户]",
        "",
        f"自用户上次对话（{fmt_ts(prev_user_ts)}）以来，你（作为角色）已通过",
        "主动触达/定时任务向用户发出以下内容。用户看见了这些，主对话 session",
        "的内部上下文可能不包含（由 --resume 机制隔离）。",
        "",
        "---",
        "",
    ]
    for content, ts in assistant_rows:
        body = content.strip()
        if len(body) > CONTENT_MAX:
            body = body[:CONTENT_MAX] + "…"
        out.append(f"### {fmt_ts(ts)}")
        out.append(body)
        out.append("")

    print("\n".join(out))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        pass
