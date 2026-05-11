"""创建飞书文档（Docx）——只接受本地 Markdown 文件，带 sha256 上传缓存。

用法：
    python create_doc.py --file_path /root/course/path/to/report.md [--title "标题"] \
        [--folder_token <token>] [--no-cache]

行为：
- 仅接受 --file_path（本地 .md 文件，必须在工作区内）。--content/--content_file 已废弃。
- 上传前按文件内容 sha256 查索引：命中且未指定 --no-cache 时直接复用旧飞书链接。
- 上传成功后把映射写入索引（index/doc_cache.db）。

详见：docs/REDESIGN_doc_cache_index_v2.md
"""

import argparse
import json
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _doc_index as di
import _feishu_auth as auth

_MAX_SIZE = 10 * 1024 * 1024  # 10MB（飞书导入限制）
_TZ8 = timezone(timedelta(hours=8))


class UserError(Exception):
    """用户侧错误（参数/文件问题），main 转成 errcode 1。"""

    def __init__(self, msg: str, hint: str = "") -> None:
        super().__init__(msg)
        self.msg = msg
        self.hint = hint


class DeprecatedArgError(Exception):
    """使用了已废弃的 --content / --content_file。"""


def _now_iso() -> str:
    return datetime.now(_TZ8).isoformat(timespec="seconds")


def _run(args, store: di.IndexStore, workspace_root: Path | None = None) -> dict:
    if workspace_root is None:
        workspace_root = di.WORKSPACE_ROOT
    if getattr(args, "content", None) or getattr(args, "content_file", None):
        raise DeprecatedArgError(
            "--content / --content_file 已废弃。请改用 --file_path /abs/path.md。"
            "详见 SKILL.md「文件存放规范」。"
        )
    if not args.file_path:
        raise UserError("缺少 --file_path 参数")

    raw = Path(args.file_path)
    try:
        resolved = di.validate_workspace_path(raw, root=workspace_root)
    except FileNotFoundError:
        raise UserError(f"文件不存在：{raw}")
    except ValueError as e:
        raise UserError(str(e), "请把 md 文件放到工作区内（项目目录或 tmp_file/）")

    if resolved.suffix.lower() != ".md":
        raise UserError(f"只接受 .md 文件，收到：{resolved.name}")
    size = resolved.stat().st_size
    if size > _MAX_SIZE:
        raise UserError(f"文件过大（{size} 字节 > 10MB）")
    if size == 0:
        raise UserError("文件为空")

    sha = di.compute_sha256(resolved)

    if not getattr(args, "no_cache", False):
        hit = store.find_active_by_sha256(sha)
        if hit is not None:
            return {
                "url": hit.feishu_url,
                "document_id": hit.feishu_doc_token,
                "cached": True,
                "sha256": sha,
                "record_id": hit.id,
            }

    title = args.title or resolved.stem
    # lark-cli 的 @file 语法要求文件是 cwd 内的相对路径，因此在文件所在目录执行
    cmd = ["docs", "+create", "--title", title, "--markdown", f"@{resolved.name}"]
    if getattr(args, "folder_token", ""):
        cmd += ["--folder-token", args.folder_token]
    data = auth.run_lark_cli(cmd, cwd=str(resolved.parent))

    url = data.get("doc_url") or data.get("url", "")
    doc_id = data.get("doc_id") or data.get("document_id") or data.get("objToken", "")
    revision = str(data.get("revision_id") or data.get("revision") or "")
    doc_token = auth.parse_doc_token(url) if url else doc_id

    record_id = None
    try:
        rec = store.insert(di.DocRecord(
            sha256=sha,
            feishu_doc_token=doc_token,
            feishu_url=url,
            local_path=str(resolved),
            title=title,
            folder_token=getattr(args, "folder_token", "") or "",
            file_size=size,
            uploaded_at=_now_iso(),
            uploaded_by="claude-code",
            workspace_id=str(workspace_root),
            schema_version=di.SCHEMA_VERSION,
            remote_revision=revision,
            last_verified_at=_now_iso(),
            status="active",
        ))
        record_id = rec.id
    except Exception as e:  # 索引写入失败不应遮蔽真实的上传成功
        print(f"warning: 上传成功但写索引失败：{e}", file=sys.stderr)

    return {
        "url": url,
        "document_id": doc_id,
        "cached": False,
        "sha256": sha,
        "record_id": record_id,
    }


def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description="创建飞书文档（仅接受本地 md 文件）")
    p.add_argument("--file_path", help="本地 .md 文件绝对路径（必须在工作区内）")
    p.add_argument("--title", default="", help="文档标题（默认取文件名）")
    p.add_argument("--folder_token", default="", help="目标飞书文件夹 token")
    p.add_argument("--no-cache", dest="no_cache", action="store_true",
                   help="跳过缓存命中，强制重新上传")
    # 已废弃，仅用于给出清晰报错
    p.add_argument("--content", default="", help=argparse.SUPPRESS)
    p.add_argument("--content_file", default="", help=argparse.SUPPRESS)
    return p


def main() -> None:
    args = _build_parser().parse_args()
    if not args.title:
        args.title = ""  # _run 会回退到文件名
    store = di.SQLiteIndexStore()
    try:
        data = _run(args, store)
    except DeprecatedArgError as e:
        print(json.dumps({"errcode": 2, "errmsg": str(e), "data": {}},
                         ensure_ascii=False))
        return
    except UserError as e:
        auth.output_error(e.msg, e.hint)
        return
    finally:
        store.close()
    auth.output_ok(data)


if __name__ == "__main__":
    main()
