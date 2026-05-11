"""读取飞书云文档内容——优先用本地缓存（本工作区上传过的源 md 文件）。

用法：
    python read_doc.py --doc "https://xxx.feishu.cn/docx/doccnXXXXXX" [--no-cache] [--verify-remote]
    python read_doc.py --doc doccnXXXXXX

行为：
- 默认按 doc_token 查索引；命中且本地源文件存在、sha256 一致 → 直接读本地（source=local_cache）。
- 本地源文件失效时，按内容 sha256 在索引里找其他同内容副本兜底。
- 索引中的 local_path 必须在工作区内，否则视为索引污染、忽略并走远程。
- --no-cache 始终走远程；--verify-remote 命中本地后顺手校验远程 revision 是否变化。

详见：docs/REDESIGN_doc_cache_index_v2.md
"""

import argparse
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _doc_index as di
import _feishu_auth as auth

_TZ8 = timezone(timedelta(hours=8))


def _fetch_remote(doc_arg: str) -> str:
    data = auth.run_lark_cli(["docs", "+fetch", "--doc", doc_arg])
    return (data.get("markdown") or data.get("content") or data.get("text")
            or str(data))


def _usable_local_copy(rec: di.DocRecord, expected_sha: str,
                       workspace_root: Path) -> Path | None:
    """检查 record.local_path 是否可用作本地缓存源。"""
    if not rec or not rec.local_path:
        return None
    p = Path(rec.local_path)
    if not di.is_inside_workspace(p, root=workspace_root):
        if p.exists():
            print(f"warning: 索引中的 local_path 不在工作区内，忽略：{p}",
                  file=sys.stderr)
        return None
    resolved = p.resolve()
    if di.compute_sha256(resolved) != expected_sha:
        return None
    return resolved


def _run(args, store: di.IndexStore, workspace_root: Path | None = None) -> dict:
    if workspace_root is None:
        workspace_root = di.WORKSPACE_ROOT
    if getattr(args, "no_cache", False):
        return {"content": _fetch_remote(args.doc), "source": "remote"}

    token = auth.parse_doc_token(args.doc)
    rec = store.find_by_doc_token(token)
    if rec is None:
        return {"content": _fetch_remote(args.doc), "source": "remote"}

    # 1) 优先用 record 自己的 local_path
    local = _usable_local_copy(rec, rec.sha256, workspace_root)
    # 2) 失效则按内容 sha 找其他副本兜底
    if local is None:
        alt = store.find_active_by_sha256(rec.sha256)
        if alt is not None and alt.id != rec.id:
            local = _usable_local_copy(alt, rec.sha256, workspace_root)

    if local is None:
        return {"content": _fetch_remote(args.doc), "source": "remote"}

    content = local.read_text(encoding="utf-8")

    if getattr(args, "verify_remote", False):
        try:
            meta = auth.run_lark_cli(["docs", "+meta", "--doc", args.doc])
            remote_rev = str(meta.get("revision_id") or meta.get("revision") or "")
            if remote_rev and rec.remote_revision and remote_rev != rec.remote_revision:
                print(f"warning: 远程文档已更新（revision {rec.remote_revision} -> "
                      f"{remote_rev}），返回的是本地快照；如需最新内容请加 --no-cache",
                      file=sys.stderr)
            if rec.id is not None:
                store.update_verified(rec.id, datetime.now(_TZ8).isoformat(timespec="seconds"),
                                      remote_rev or rec.remote_revision)
        except Exception as e:  # 校验失败不影响返回本地内容
            print(f"warning: --verify-remote 校验失败：{e}", file=sys.stderr)

    return {"content": content, "source": "local_cache", "local_path": str(local)}


def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description="读取飞书文档内容（带本地缓存）")
    p.add_argument("--doc", required=True, help="飞书文档 URL 或 doc_token")
    p.add_argument("--no-cache", dest="no_cache", action="store_true",
                   help="跳过本地缓存，强制远程拉取")
    p.add_argument("--verify-remote", dest="verify_remote", action="store_true",
                   help="命中本地后顺手校验远程 revision 是否变化")
    return p


def main() -> None:
    args = _build_parser().parse_args()
    store = di.SQLiteIndexStore()
    try:
        data = _run(args, store)
    finally:
        store.close()
    auth.output_ok(data)


if __name__ == "__main__":
    main()
