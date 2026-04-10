"""在多维表格（Bitable）内创建数据表，并定义字段结构。

用法：
    python create_bitable_table.py \
        --app "https://xxx.feishu.cn/base/BxxXXXX" \
        --name "任务清单" \
        --fields '[
            {"name": "任务名称", "type": "text"},
            {"name": "优先级", "type": "select", "options": ["高","中","低"]},
            {"name": "截止日期", "type": "date"},
            {"name": "完成", "type": "checkbox"}
        ]'
"""

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


# 简易类型映射（用户友好名称 → 飞书 type 数字）
_TYPE_MAP = {
    "text": 1, "number": 2, "select": 3, "multiselect": 4,
    "date": 5, "checkbox": 7, "url": 15,
}


def _build_field(f: dict) -> dict:
    """将用户友好字段定义转为飞书 API 格式。"""
    type_name = f.get("type", "text")
    type_num = _TYPE_MAP.get(type_name, 1)
    field: dict = {"field_name": f["name"], "type": type_num}
    options = f.get("options", [])
    if options and type_name in ("select", "multiselect"):
        field["property"] = {"options": [{"name": o} for o in options]}
    return field


def main() -> None:
    parser = argparse.ArgumentParser(description="创建多维表格数据表")
    parser.add_argument("--app", required=True, help="多维表格 URL 或 app_token")
    parser.add_argument("--name", required=True, help="数据表名称")
    parser.add_argument("--fields", default="[]", help="JSON 字段定义数组")
    args = parser.parse_args()

    app_token = auth.parse_bitable_token(args.app)

    try:
        fields_input = json.loads(args.fields)
        fields = [_build_field(f) for f in fields_input]
    except (json.JSONDecodeError, ValueError, KeyError) as e:
        auth.output_error(f"--fields 格式错误：{e}")

    cmd = ["base", "+table-create",
           "--base-token", app_token,
           "--name", args.name]
    if fields:
        cmd += ["--fields", json.dumps(fields)]

    data = auth.run_lark_cli(cmd)
    table = data.get("table") or data
    table_id = table.get("table_id", "") or table.get("tableId", "")
    auth.output_ok({
        "table_id": table_id,
        "fields_created": [f["field_name"] for f in fields],
    })


if __name__ == "__main__":
    main()
