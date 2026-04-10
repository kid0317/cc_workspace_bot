"""读取飞书电子表格数据。

用法：
    python read_sheet.py --sheet "https://xxx.feishu.cn/sheets/shtcnXXXXXX" \
        [--sheet_id Sheet1] [--range A1:D10]
"""

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="读取飞书电子表格")
    parser.add_argument("--sheet", required=True, help="电子表格 URL 或 spreadsheet_token")
    parser.add_argument("--sheet_id", default="", help="Sheet ID（不填读第一个 Sheet）")
    parser.add_argument("--range", default="", help="读取范围，如 A1:D10（不填读整表）")
    args = parser.parse_args()

    cmd = ["sheets", "+read", "--url", args.sheet]
    if args.sheet_id:
        cmd += ["--sheet-id", args.sheet_id]
    if args.range:
        cmd += ["--range", args.range]

    data = auth.run_lark_cli(cmd)
    auth.output_ok({"values": data.get("values") or data.get("valueRange", {}).get("values", [])})


if __name__ == "__main__":
    main()
