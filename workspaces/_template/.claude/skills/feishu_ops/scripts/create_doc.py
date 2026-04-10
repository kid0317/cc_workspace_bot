"""创建飞书文档（Docx），可写入 Markdown 内容。

用法：
    python create_doc.py --title "季度报告" [--folder_token <token>] \
        [--content "# 标题\n\n正文"] [--content_file /path/to/report.md]
"""

import argparse
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="创建飞书文档")
    parser.add_argument("--title", required=True, help="文档标题")
    parser.add_argument("--folder_token", default="", help="目标文件夹 token（留空放根目录）")
    parser.add_argument("--content", default="", help="Markdown 字符串")
    parser.add_argument("--content_file", default="", help="Markdown 文件路径（优先于 --content）")
    args = parser.parse_args()

    cmd = ["docs", "+create", "--title", args.title]
    if args.folder_token:
        cmd += ["--folder-token", args.folder_token]

    # 处理内容：文件优先
    markdown_text = ""
    if args.content_file:
        p = Path(args.content_file)
        if not p.exists():
            auth.output_error(f"content_file 不存在：{args.content_file}")
        markdown_text = p.read_text(encoding="utf-8")
    elif args.content:
        markdown_text = args.content

    if markdown_text:
        # 写临时文件，用 @file 语法传给 lark-cli
        with tempfile.NamedTemporaryFile(mode="w", suffix=".md", delete=False, encoding="utf-8") as tf:
            tf.write(markdown_text)
            tmp_path = tf.name
        cmd += ["--markdown", f"@{tmp_path}"]

    data = auth.run_lark_cli(cmd)
    auth.output_ok({
        "document_id": data.get("document_id") or data.get("objToken", ""),
        "url": data.get("url", ""),
        "blocks_written": data.get("blocks_written", 0),
    })


if __name__ == "__main__":
    main()
