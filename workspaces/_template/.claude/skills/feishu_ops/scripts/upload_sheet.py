"""将本地 Excel 文件导入为飞书电子表格。

用法：
    python upload_sheet.py \
        --file_path /workspace/outputs/report.xlsx \
        [--title "销售报告"] [--folder_token <token>]
"""

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="导入 Excel 为飞书电子表格")
    parser.add_argument("--file_path", required=True, help="本地 .xlsx/.xls 文件路径（≤20MB）")
    parser.add_argument("--title", default="", help="导入后的表格名称（留空使用文件名）")
    parser.add_argument("--folder_token", default="", help="目标文件夹 token（留空放根目录）")
    args = parser.parse_args()

    file_path = Path(args.file_path)
    if not file_path.exists():
        auth.output_error(f"文件不存在：{args.file_path}")

    cmd = ["drive", "+import", "--file", str(file_path), "--type", "sheet"]
    if args.title:
        cmd += ["--name", args.title]
    if args.folder_token:
        cmd += ["--folder-token", args.folder_token]

    data = auth.run_lark_cli(cmd)
    auth.output_ok({
        "spreadsheet_token": data.get("token") or data.get("spreadsheet_token", ""),
        "url": data.get("url", ""),
        "title": args.title or file_path.stem,
        "file_size": file_path.stat().st_size,
    })


if __name__ == "__main__":
    main()
