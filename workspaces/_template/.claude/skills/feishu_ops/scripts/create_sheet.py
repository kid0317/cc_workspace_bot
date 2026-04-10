"""创建飞书电子表格（Spreadsheet）。

用法：
    python create_sheet.py --title "销售数据" [--folder_token <token>]
"""

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="创建飞书电子表格")
    parser.add_argument("--title", required=True, help="表格标题")
    parser.add_argument("--folder_token", default="", help="目标文件夹 token（留空放根目录）")
    args = parser.parse_args()

    cmd = ["sheets", "+create", "--title", args.title]
    if args.folder_token:
        cmd += ["--folder-token", args.folder_token]

    data = auth.run_lark_cli(cmd)
    spreadsheet = data.get("spreadsheet") or data
    auth.output_ok({
        "spreadsheet_token": spreadsheet.get("spreadsheetToken") or spreadsheet.get("spreadsheet_token", ""),
        "url": spreadsheet.get("url", ""),
    })


if __name__ == "__main__":
    main()
