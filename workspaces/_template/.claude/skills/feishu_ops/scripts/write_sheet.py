"""向飞书电子表格写入数据（覆盖模式）。

用法：
    python write_sheet.py \
        --sheet "https://xxx.feishu.cn/sheets/shtcnXXXX" \
        --values '[["姓名","年龄"],["Alice",30]]' \
        [--start_cell A1] [--sheet_id Sheet1]
"""

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="向飞书表格写入数据")
    parser.add_argument("--sheet", required=True, help="电子表格 URL 或 spreadsheet_token")
    parser.add_argument("--values", required=True, help="JSON 二维数组")
    parser.add_argument("--start_cell", default="A1", help="写入起始单元格（默认 A1）")
    parser.add_argument("--sheet_id", default="", help="Sheet ID（不填写第一个）")
    args = parser.parse_args()

    try:
        values = json.loads(args.values)
        if not isinstance(values, list):
            raise ValueError
    except (json.JSONDecodeError, ValueError):
        auth.output_error("--values 必须是 JSON 二维数组")

    cmd = ["sheets", "+write", "--url", args.sheet, "--range", args.start_cell,
           "--values", json.dumps(values)]
    if args.sheet_id:
        cmd += ["--sheet-id", args.sheet_id]

    data = auth.run_lark_cli(cmd)
    revision = data.get("revision") or data
    rows_written = len(values)
    auth.output_ok({"range": data.get("range", ""), "rows_written": rows_written})


if __name__ == "__main__":
    main()
