"""飞书文档上传索引：基于 sha256 + SQLite 的本地缓存索引。

记录 (sha256, feishu_doc_token, feishu_url, local_path, ...) 的映射，用于：
- 上传前去重：同内容已上传过即复用旧链接
- 读取时回退：飞书文档已由本工作区上传过则直接读本地源文件

存储：{workspace}/.claude/skills/feishu_ops/index/doc_cache.db（SQLite WAL）

设计文档：docs/REDESIGN_doc_cache_index_v2.md
"""

import hashlib
import os
import sqlite3
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Protocol

# ───────────────────────── 常量 ─────────────────────────

SCHEMA_VERSION = 1

# 推导工作区根目录，兼容两种部署布局：
#   A: <workspace>/.claude/skills/feishu_ops/scripts/_doc_index.py  → 上溯 3 层
#   B: <workspace>/skills/feishu_ops/scripts/_doc_index.py          → 上溯 2 层
_FEISHU_OPS_DIR = Path(__file__).resolve().parent.parent  # .../feishu_ops
if _FEISHU_OPS_DIR.parent.name == "skills" and _FEISHU_OPS_DIR.parent.parent.name == ".claude":
    WORKSPACE_ROOT = _FEISHU_OPS_DIR.parent.parent.parent
elif _FEISHU_OPS_DIR.parent.name == "skills":
    WORKSPACE_ROOT = _FEISHU_OPS_DIR.parent.parent
else:
    WORKSPACE_ROOT = _FEISHU_OPS_DIR.parent

DEFAULT_DB_PATH = _FEISHU_OPS_DIR / "index" / "doc_cache.db"

_VALID_STATUS = {"active", "deleted", "remote_missing"}


# ───────────────────────── hash & path ─────────────────────────

def compute_sha256(path: Path) -> str:
    """流式计算文件内容的 sha256（hex）。"""
    h = hashlib.sha256()
    with Path(path).open("rb") as f:
        for chunk in iter(lambda: f.read(8192), b""):
            h.update(chunk)
    return h.hexdigest()


def validate_workspace_path(p: Path, root: Path = WORKSPACE_ROOT) -> Path:
    """校验路径存在、解析符号链接后位于 workspace 子树内。

    Raises:
        FileNotFoundError: 路径不存在
        ValueError: 路径在 workspace 之外
    """
    resolved = Path(p).resolve(strict=True)
    root_resolved = Path(root).resolve()
    try:
        resolved.relative_to(root_resolved)
    except ValueError:
        raise ValueError(f"路径不在工作区内：{resolved}（工作区：{root_resolved}）")
    return resolved


def is_inside_workspace(p: Path, root: Path = WORKSPACE_ROOT) -> bool:
    """非抛错版本：路径是否存在且在 workspace 内。"""
    try:
        validate_workspace_path(p, root=root)
        return True
    except (ValueError, OSError):
        return False


# ───────────────────────── 数据模型 ─────────────────────────

@dataclass(frozen=True)
class DocRecord:
    sha256: str
    feishu_doc_token: str
    feishu_url: str
    local_path: str
    title: str
    folder_token: str = ""
    file_size: int = 0
    uploaded_at: str = ""
    uploaded_by: str = "claude-code"
    workspace_id: str = str(WORKSPACE_ROOT)
    schema_version: int = SCHEMA_VERSION
    remote_revision: str = ""
    last_verified_at: str = ""
    status: str = "active"
    id: int | None = None


_COLUMNS = (
    "sha256", "feishu_doc_token", "feishu_url", "local_path", "title",
    "folder_token", "file_size", "uploaded_at", "uploaded_by", "workspace_id",
    "schema_version", "remote_revision", "last_verified_at", "status",
)


# ───────────────────────── 存储接口 ─────────────────────────

class IndexStore(Protocol):
    def find_active_by_sha256(self, sha: str) -> DocRecord | None: ...
    def find_by_doc_token(self, token: str) -> DocRecord | None: ...
    def insert(self, record: DocRecord) -> DocRecord: ...
    def mark_remote_missing(self, record_id: int) -> None: ...
    def update_verified(self, record_id: int, now: str, revision: str) -> None: ...
    def all_records(self, status: str = "active") -> list[DocRecord]: ...
    def close(self) -> None: ...


# ───────────────────────── SQLite 实现 ─────────────────────────

_SCHEMA_SQL = """
CREATE TABLE IF NOT EXISTS doc_records (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    sha256            TEXT NOT NULL,
    feishu_doc_token  TEXT NOT NULL,
    feishu_url        TEXT NOT NULL,
    local_path        TEXT NOT NULL,
    title             TEXT NOT NULL,
    folder_token      TEXT NOT NULL DEFAULT '',
    file_size         INTEGER NOT NULL DEFAULT 0,
    uploaded_at       TEXT NOT NULL DEFAULT '',
    uploaded_by       TEXT NOT NULL DEFAULT 'claude-code',
    workspace_id      TEXT NOT NULL DEFAULT '',
    schema_version    INTEGER NOT NULL DEFAULT 1,
    remote_revision   TEXT NOT NULL DEFAULT '',
    last_verified_at  TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'active'
);
CREATE INDEX IF NOT EXISTS idx_sha256    ON doc_records(sha256, status);
CREATE INDEX IF NOT EXISTS idx_doc_token ON doc_records(feishu_doc_token);
"""


class SQLiteIndexStore:
    """SQLite 实现的 IndexStore（WAL 模式，支持多 session 并发写）。"""

    def __init__(self, db_path: Path | str = DEFAULT_DB_PATH) -> None:
        self._db_path = Path(db_path)
        self._db_path.parent.mkdir(parents=True, exist_ok=True)
        self._conn = sqlite3.connect(str(self._db_path), isolation_level=None,
                                     timeout=10.0)
        self._conn.row_factory = sqlite3.Row
        self._conn.execute("PRAGMA journal_mode=WAL;")
        self._conn.execute("PRAGMA synchronous=NORMAL;")
        self._conn.executescript(_SCHEMA_SQL)

    # -- 内部 --
    def _row_to_record(self, row: sqlite3.Row) -> DocRecord:
        return DocRecord(
            id=row["id"],
            sha256=row["sha256"],
            feishu_doc_token=row["feishu_doc_token"],
            feishu_url=row["feishu_url"],
            local_path=row["local_path"],
            title=row["title"],
            folder_token=row["folder_token"],
            file_size=row["file_size"],
            uploaded_at=row["uploaded_at"],
            uploaded_by=row["uploaded_by"],
            workspace_id=row["workspace_id"],
            schema_version=row["schema_version"],
            remote_revision=row["remote_revision"],
            last_verified_at=row["last_verified_at"],
            status=row["status"],
        )

    # -- 查询 --
    def find_active_by_sha256(self, sha: str) -> DocRecord | None:
        cur = self._conn.execute(
            "SELECT * FROM doc_records WHERE sha256=? AND status='active' "
            "ORDER BY uploaded_at DESC, id DESC LIMIT 1",
            (sha,),
        )
        row = cur.fetchone()
        return self._row_to_record(row) if row else None

    def find_by_doc_token(self, token: str) -> DocRecord | None:
        cur = self._conn.execute(
            "SELECT * FROM doc_records WHERE feishu_doc_token=? "
            "ORDER BY uploaded_at DESC, id DESC LIMIT 1",
            (token,),
        )
        row = cur.fetchone()
        return self._row_to_record(row) if row else None

    def all_records(self, status: str = "active") -> list[DocRecord]:
        if status == "all":
            cur = self._conn.execute("SELECT * FROM doc_records ORDER BY id")
        else:
            cur = self._conn.execute(
                "SELECT * FROM doc_records WHERE status=? ORDER BY id", (status,)
            )
        return [self._row_to_record(r) for r in cur.fetchall()]

    # -- 写入 --
    def insert(self, record: DocRecord) -> DocRecord:
        if record.status not in _VALID_STATUS:
            raise ValueError(f"非法 status：{record.status}")
        placeholders = ", ".join("?" for _ in _COLUMNS)
        cols = ", ".join(_COLUMNS)
        values = tuple(getattr(record, c) for c in _COLUMNS)
        cur = self._conn.execute(
            f"INSERT INTO doc_records ({cols}) VALUES ({placeholders})", values
        )
        return replace(record, id=cur.lastrowid)

    def mark_remote_missing(self, record_id: int) -> None:
        self._conn.execute(
            "UPDATE doc_records SET status='remote_missing' WHERE id=?", (record_id,)
        )

    def update_verified(self, record_id: int, now: str, revision: str) -> None:
        self._conn.execute(
            "UPDATE doc_records SET last_verified_at=?, remote_revision=? WHERE id=?",
            (now, revision, record_id),
        )

    def close(self) -> None:
        self._conn.close()
