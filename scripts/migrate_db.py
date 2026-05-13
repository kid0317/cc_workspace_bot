#!/usr/bin/env python3
"""
migrate_db.py — 将共享 bot.db 拆分为每个 App 的独立数据库。

用法:
    python3 migrate_db.py --src /root/cc_workspace_bot/bot.db \
                          --config /root/cc_workspace_bot/config.yaml \
                          [--dry-run]   # 只打印不写入
                          [--force]     # 目标 DB 已存在时强制覆盖（危险）

输出:
    - 每个 App 的 {workspace_dir}/bot.db
    - 对于 config.yaml 中找不到的 app_id，数据写入 ./archive_{app_id}.db

迁移逻辑（按 referential chain 保持完整性）:
    channels  WHERE app_id = X
    sessions  WHERE channel_key IN (channels of X)
    messages  WHERE session_id  IN (sessions above)
    tasks     WHERE app_id = X
"""

import argparse
import os
import shutil
import sqlite3
import sys
from pathlib import Path
from datetime import datetime

import yaml


# ── Schema (mirrors GORM AutoMigrate output) ─────────────────────────────────

SCHEMA_SQL = """
CREATE TABLE IF NOT EXISTS channels (
    channel_key TEXT PRIMARY KEY,
    app_id      TEXT NOT NULL,
    chat_type   TEXT NOT NULL,
    chat_id     TEXT NOT NULL,
    thread_id   TEXT,
    created_at  DATETIME
);
CREATE INDEX IF NOT EXISTS idx_channels_app_id ON channels(app_id);

CREATE TABLE IF NOT EXISTS sessions (
    id                TEXT PRIMARY KEY,
    channel_key       TEXT NOT NULL,
    claude_session_id TEXT,
    status            TEXT NOT NULL DEFAULT 'active',
    created_by        TEXT,
    created_at        DATETIME,
    updated_at        DATETIME
);
CREATE INDEX IF NOT EXISTS idx_sessions_channel_key ON sessions(channel_key);

CREATE TABLE IF NOT EXISTS messages (
    id           TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL,
    sender_id    TEXT,
    role         TEXT NOT NULL,
    content      TEXT,
    feishu_msg_id TEXT,
    created_at   DATETIME
);
CREATE INDEX IF NOT EXISTS idx_messages_session_id ON messages(session_id);

CREATE TABLE IF NOT EXISTS tasks (
    id           TEXT PRIMARY KEY,
    app_id       TEXT NOT NULL,
    name         TEXT,
    cron_expr    TEXT,
    target_type  TEXT,
    target_id    TEXT,
    prompt       TEXT,
    enabled      NUMERIC DEFAULT 1,
    created_by   TEXT,
    created_at   DATETIME,
    last_run_at  DATETIME,
    deleted_at   DATETIME,
    send_output  NUMERIC NOT NULL,
    post_archive NUMERIC NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_tasks_app_id      ON tasks(app_id);
CREATE INDEX IF NOT EXISTS idx_tasks_deleted_at  ON tasks(deleted_at);
"""


def load_config(config_path: str) -> dict[str, str]:
    """返回 app_id -> workspace_dir 映射。"""
    with open(config_path) as f:
        cfg = yaml.safe_load(f)
    result = {}
    for app in cfg.get("apps", []):
        app_id = app.get("id", "").strip()
        workspace_dir = app.get("workspace_dir", "").strip()
        if app_id and workspace_dir:
            result[app_id] = workspace_dir
    return result


def open_src(src_path: str) -> sqlite3.Connection:
    if not os.path.exists(src_path):
        sys.exit(f"[ERROR] source DB not found: {src_path}")
    conn = sqlite3.connect(f"file:{src_path}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    return conn


def open_dst(dst_path: str, dry_run: bool, force: bool) -> sqlite3.Connection | None:
    """创建或打开目标 DB，初始化 schema。dry_run 时返回 None。"""
    if dry_run:
        return None

    dst_dir = os.path.dirname(dst_path)
    if dst_dir:
        os.makedirs(dst_dir, exist_ok=True)

    if os.path.exists(dst_path):
        if not force:
            print(f"  [SKIP] target DB already exists (use --force to overwrite): {dst_path}")
            return None
        print(f"  [WARN] overwriting existing target DB: {dst_path}")

    conn = sqlite3.connect(dst_path)
    conn.executescript(SCHEMA_SQL)
    conn.commit()
    return conn


def migrate_app(
    src: sqlite3.Connection,
    dst_path: str,
    app_id: str,
    dry_run: bool,
    force: bool,
) -> dict:
    """迁移单个 app 的数据，返回统计信息。"""
    stats = {"channels": 0, "sessions": 0, "messages": 0, "tasks": 0, "skipped": False}

    # ── 1. channels ──────────────────────────────────────────────────────────
    channels = src.execute(
        "SELECT * FROM channels WHERE app_id = ?", (app_id,)
    ).fetchall()
    channel_keys = [r["channel_key"] for r in channels]
    stats["channels"] = len(channels)

    # ── 2. sessions ──────────────────────────────────────────────────────────
    sessions = []
    session_ids = []
    if channel_keys:
        placeholders = ",".join("?" * len(channel_keys))
        sessions = src.execute(
            f"SELECT * FROM sessions WHERE channel_key IN ({placeholders})",
            channel_keys,
        ).fetchall()
        session_ids = [r["id"] for r in sessions]
    stats["sessions"] = len(sessions)

    # ── 3. messages ──────────────────────────────────────────────────────────
    messages = []
    if session_ids:
        placeholders = ",".join("?" * len(session_ids))
        messages = src.execute(
            f"SELECT * FROM messages WHERE session_id IN ({placeholders})",
            session_ids,
        ).fetchall()
    stats["messages"] = len(messages)

    # ── 4. tasks ─────────────────────────────────────────────────────────────
    tasks = src.execute(
        "SELECT * FROM tasks WHERE app_id = ?", (app_id,)
    ).fetchall()
    stats["tasks"] = len(tasks)

    if dry_run:
        return stats

    dst = open_dst(dst_path, dry_run=False, force=force)
    if dst is None:
        stats["skipped"] = True
        return stats

    try:
        with dst:
            # channels
            for row in channels:
                dst.execute(
                    "INSERT OR IGNORE INTO channels VALUES (?,?,?,?,?,?)",
                    tuple(row),
                )
            # sessions
            for row in sessions:
                dst.execute(
                    "INSERT OR IGNORE INTO sessions VALUES (?,?,?,?,?,?,?)",
                    tuple(row),
                )
            # messages — batch in chunks of 500 to avoid variable-limit
            for i in range(0, len(messages), 500):
                chunk = messages[i : i + 500]
                dst.executemany(
                    "INSERT OR IGNORE INTO messages VALUES (?,?,?,?,?,?,?)",
                    [tuple(r) for r in chunk],
                )
            # tasks
            for row in tasks:
                dst.execute(
                    "INSERT OR IGNORE INTO tasks VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    tuple(row),
                )
    finally:
        dst.close()

    return stats


def verify(dst_path: str, expected: dict) -> bool:
    """验证目标 DB 中数据量与预期一致。"""
    if not os.path.exists(dst_path):
        return False
    conn = sqlite3.connect(dst_path)
    try:
        ok = True
        for table in ("channels", "sessions", "messages", "tasks"):
            count = conn.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
            if count != expected[table]:
                print(f"  [VERIFY FAIL] {table}: expected {expected[table]}, got {count}")
                ok = False
        return ok
    finally:
        conn.close()


def main():
    parser = argparse.ArgumentParser(description="Migrate shared bot.db to per-workspace DBs")
    parser.add_argument("--src", default="/root/cc_workspace_bot/bot.db", help="Source shared DB path")
    parser.add_argument("--config", default="/root/cc_workspace_bot/config.yaml", help="config.yaml path")
    parser.add_argument("--dry-run", action="store_true", help="Preview only, no writes")
    parser.add_argument("--force", action="store_true", help="Overwrite existing target DBs")
    parser.add_argument("--archive-dir", default="/root/cc_workspace_bot", help="Dir for unknown-app-id archives")
    args = parser.parse_args()

    print(f"[migrate_db] {'DRY RUN — ' if args.dry_run else ''}starting {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    print(f"  src:    {args.src}")
    print(f"  config: {args.config}")
    print()

    # ── Load config ───────────────────────────────────────────────────────────
    app_map = load_config(args.config)
    print(f"[config] {len(app_map)} apps found: {', '.join(app_map.keys())}")
    print()

    src = open_src(args.src)

    # ── Collect all app_ids present in the source DB ──────────────────────────
    db_app_ids = set()
    for row in src.execute("SELECT DISTINCT app_id FROM channels"):
        db_app_ids.add(row[0])
    for row in src.execute("SELECT DISTINCT app_id FROM tasks"):
        db_app_ids.add(row[0])

    print(f"[source] app_ids in DB: {', '.join(sorted(db_app_ids))}")
    print()

    # ── Back up src before writing anything ───────────────────────────────────
    if not args.dry_run:
        bak = args.src + ".bak"
        if not os.path.exists(bak):
            print(f"[backup] copying {args.src} → {bak}")
            shutil.copy2(args.src, bak)
        else:
            print(f"[backup] {bak} already exists, skipping")
        print()

    # ── Migrate each app_id ───────────────────────────────────────────────────
    total_ok = 0
    total_skip = 0
    total_unknown = 0

    for app_id in sorted(db_app_ids):
        if app_id in app_map:
            workspace_dir = app_map[app_id]
            dst_path = os.path.join(workspace_dir, "bot.db")
            label = "known"
        else:
            # unknown app_id — archive to a separate file
            dst_path = os.path.join(args.archive_dir, f"archive_{app_id}.db")
            label = "UNKNOWN→archive"
            total_unknown += 1

        stats = migrate_app(src, dst_path, app_id, dry_run=args.dry_run, force=args.force)

        status = "DRY" if args.dry_run else ("SKIP" if stats["skipped"] else "OK")
        print(
            f"  [{status}] [{label}] app={app_id}"
            f"  ch={stats['channels']} sess={stats['sessions']}"
            f" msg={stats['messages']} task={stats['tasks']}"
        )
        if not args.dry_run:
            print(f"         → {dst_path}")

        if not args.dry_run and not stats["skipped"]:
            ok = verify(dst_path, stats)
            if ok:
                print(f"         ✓ verify passed")
                total_ok += 1
            else:
                print(f"         ✗ verify FAILED")
        elif stats["skipped"]:
            total_skip += 1

    src.close()

    print()
    print(f"[done] ok={total_ok} skipped={total_skip} unknown_app_ids={total_unknown}")
    if total_unknown > 0:
        print(f"       {total_unknown} app_ids not found in config.yaml → archived under {args.archive_dir}/archive_*.db")
    if not args.dry_run and total_ok > 0:
        print(f"[next] Run: go build ./... && go test ./... then deploy.")
        print(f"       Old shared DB backed up at: {args.src}.bak")


if __name__ == "__main__":
    main()
