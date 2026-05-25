#!/usr/bin/env python3
"""
backfill_orphan_sessions.py — 修补 migrate_db.py 漏掉的 orphan sessions。

背景：原 migrate_db.py 用 `channels.app_id` 作为分流依据，但实际 chat 数据是
按 `sessions.channel_key` 末段后缀（如 `:mango_daxian`）路由的。`channels`
表只存了 bot 自己的身份（每 app 通常 1 行），导致绝大多数 sessions 在
channels 表里没有对应行，被 migrate_db.py 直接跳过。

本脚本：
  - 遍历 config.yaml 里所有 app
  - 在源库中按 `channel_key LIKE '%:{app_id}'` 找出 orphan sessions
  - 与目标库现有数据求差，INSERT OR IGNORE 写入 sessions + messages
  - 为 orphan sessions 补建 channels 行（从 channel_key 解析）
  - 不动 tasks 表（每个 workspace 已自行重建定时任务）

用法：
    python3 backfill_orphan_sessions.py \
        --src /root/cc_workspace_bot/bot.db.bak \
        --config /root/cc_workspace_bot/config.yaml \
        [--dry-run]              # 只打印不写入
        [--app mango_daxian]     # 只跑单个 app（调试用）

输出：每个 app 的统计 + 总览。目标 DB 在写入前会备份到 bot.db.pre-backfill.bak。
"""

import argparse
import os
import shutil
import sqlite3
import sys
import time
from datetime import datetime
from pathlib import Path

import yaml


def load_config(config_path: str) -> dict[str, str]:
    with open(config_path) as f:
        cfg = yaml.safe_load(f)
    return {
        app["id"].strip(): app["workspace_dir"].strip()
        for app in cfg.get("apps", [])
        if app.get("id") and app.get("workspace_dir")
    }


def parse_channel_key(channel_key: str) -> tuple[str, str, str] | None:
    """`p2p:oc_xxx:mango_daxian` -> ('p2p', 'oc_xxx', 'mango_daxian')。"""
    parts = channel_key.split(":")
    if len(parts) < 3:
        return None
    chat_type = parts[0]
    app_id = parts[-1]
    chat_id = ":".join(parts[1:-1])
    return chat_type, chat_id, app_id


def fetch_orphan_data(src: sqlite3.Connection, app_id: str) -> tuple[list, list, list]:
    """返回 (sessions, messages, synthesized_channels) 三元组。"""
    sessions = src.execute(
        "SELECT * FROM sessions WHERE channel_key LIKE ?",
        (f"%:{app_id}",),
    ).fetchall()
    if not sessions:
        return [], [], []

    session_ids = [s["id"] for s in sessions]
    channel_keys = {s["channel_key"] for s in sessions}

    messages = []
    for i in range(0, len(session_ids), 500):
        chunk = session_ids[i : i + 500]
        placeholders = ",".join("?" * len(chunk))
        rows = src.execute(
            f"SELECT * FROM messages WHERE session_id IN ({placeholders})",
            chunk,
        ).fetchall()
        messages.extend(rows)

    # synthesize channels rows for those not already in source channels table
    synth_channels = []
    seen = set()
    for ck in channel_keys:
        if ck in seen:
            continue
        seen.add(ck)
        parsed = parse_channel_key(ck)
        if not parsed:
            continue
        chat_type, chat_id, parsed_app = parsed
        # use earliest session created_at as channel created_at
        earliest = min(
            (s["created_at"] for s in sessions if s["channel_key"] == ck),
            default=None,
        )
        synth_channels.append(
            (ck, parsed_app, chat_type, chat_id, "", earliest)
        )

    return sessions, messages, synth_channels


def get_existing_ids(dst_conn: sqlite3.Connection) -> tuple[set, set, set]:
    sess = {r[0] for r in dst_conn.execute("SELECT id FROM sessions")}
    msgs = {r[0] for r in dst_conn.execute("SELECT id FROM messages")}
    chans = {r[0] for r in dst_conn.execute("SELECT channel_key FROM channels")}
    return sess, msgs, chans


def write_with_retry(dst_path: str, fn, max_retries: int = 5) -> None:
    """在 SQLite 锁竞争下重试。fn 接收 connection 参数。"""
    last_err = None
    for attempt in range(max_retries):
        try:
            conn = sqlite3.connect(dst_path, timeout=10.0)
            conn.execute("PRAGMA journal_mode=WAL")
            try:
                fn(conn)
                conn.commit()
                return
            finally:
                conn.close()
        except sqlite3.OperationalError as e:
            last_err = e
            time.sleep(0.5 * (attempt + 1))
    raise RuntimeError(f"failed after {max_retries} retries: {last_err}")


def backfill_app(
    src: sqlite3.Connection,
    app_id: str,
    workspace_dir: str,
    dry_run: bool,
) -> dict:
    stats = {
        "app_id": app_id,
        "src_sessions": 0,
        "src_messages": 0,
        "new_sessions": 0,
        "new_messages": 0,
        "new_channels": 0,
        "skipped_reason": None,
    }

    dst_path = f"{workspace_dir}/bot.db"
    if not os.path.exists(dst_path):
        stats["skipped_reason"] = f"target DB missing: {dst_path}"
        return stats

    sessions, messages, synth_channels = fetch_orphan_data(src, app_id)
    stats["src_sessions"] = len(sessions)
    stats["src_messages"] = len(messages)

    if not sessions:
        return stats

    # diff against existing
    with sqlite3.connect(f"file:{dst_path}?mode=ro", uri=True) as dst_ro:
        existing_sess, existing_msgs, existing_chans = get_existing_ids(dst_ro)

    new_sessions = [s for s in sessions if s["id"] not in existing_sess]
    new_messages = [m for m in messages if m["id"] not in existing_msgs]
    new_channels = [c for c in synth_channels if c[0] not in existing_chans]

    stats["new_sessions"] = len(new_sessions)
    stats["new_messages"] = len(new_messages)
    stats["new_channels"] = len(new_channels)

    if dry_run:
        return stats

    if not new_sessions and not new_messages and not new_channels:
        return stats

    # backup once per target DB
    backup_path = f"{dst_path}.pre-backfill.bak"
    if not os.path.exists(backup_path):
        shutil.copy2(dst_path, backup_path)

    def do_write(conn: sqlite3.Connection) -> None:
        for row in new_channels:
            conn.execute(
                "INSERT OR IGNORE INTO channels VALUES (?,?,?,?,?,?)",
                row,
            )
        for row in new_sessions:
            conn.execute(
                "INSERT OR IGNORE INTO sessions VALUES (?,?,?,?,?,?,?)",
                tuple(row),
            )
        for i in range(0, len(new_messages), 500):
            chunk = new_messages[i : i + 500]
            conn.executemany(
                "INSERT OR IGNORE INTO messages VALUES (?,?,?,?,?,?,?)",
                [tuple(r) for r in chunk],
            )

    write_with_retry(dst_path, do_write)
    return stats


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--src", default="/root/cc_workspace_bot/bot.db.bak")
    parser.add_argument("--config", default="/root/cc_workspace_bot/config.yaml")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--app", default="", help="只跑单个 app（调试用）")
    args = parser.parse_args()

    print(f"[backfill] {'DRY-RUN — ' if args.dry_run else ''}start at {datetime.now():%Y-%m-%d %H:%M:%S}")
    print(f"  src:    {args.src}")
    print(f"  config: {args.config}")
    print()

    if not os.path.exists(args.src):
        sys.exit(f"[ERROR] source not found: {args.src}")

    app_map = load_config(args.config)
    if args.app:
        if args.app not in app_map:
            sys.exit(f"[ERROR] app '{args.app}' not in config")
        app_map = {args.app: app_map[args.app]}

    src = sqlite3.connect(f"file:{args.src}?mode=ro", uri=True)
    src.row_factory = sqlite3.Row

    all_stats = []
    for app_id, wd in app_map.items():
        s = backfill_app(src, app_id, wd, args.dry_run)
        all_stats.append(s)
        label = "DRY" if args.dry_run else "OK"
        if s["skipped_reason"]:
            print(f"  [SKIP] {app_id:25s} {s['skipped_reason']}")
        else:
            print(
                f"  [{label}] {app_id:25s} "
                f"src(sess={s['src_sessions']:4d}, msg={s['src_messages']:5d}) "
                f"new(chan={s['new_channels']:2d}, sess={s['new_sessions']:4d}, msg={s['new_messages']:5d})"
            )

    src.close()

    # summary
    total_new_sess = sum(s["new_sessions"] for s in all_stats)
    total_new_msg = sum(s["new_messages"] for s in all_stats)
    total_new_chan = sum(s["new_channels"] for s in all_stats)
    print()
    print(f"[summary] {'would add' if args.dry_run else 'added'}: "
          f"channels={total_new_chan}, sessions={total_new_sess}, messages={total_new_msg}")


if __name__ == "__main__":
    main()
