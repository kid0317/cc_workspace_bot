"""批量写入多维表格记录（最多 500 条/次，超过自动分批）。

用法：
    python write_bitable_records.py \
        --app "https://xxx.feishu.cn/base/BxxXXXX" \
        --table_id tblXXXXXX \
        --records '[{"任务名称": "完成 API 文档", "优先级": "高"}]'
"""

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth

_BATCH_SIZE = 500


def main() -> None:
    parser = argparse.ArgumentParser(description="批量写入多维表格记录")
    parser.add_argument("--app", required=True, help="多维表格 URL 或 app_token")
    parser.add_argument("--table_id", required=True, help="数据表 ID")
    parser.add_argument("--records", required=True, help="JSON 数组，每项为一条记录（键为字段名）")
    args = parser.parse_args()

    app_token = auth.parse_bitable_token(args.app)

    try:
        records = json.loads(args.records)
        if not isinstance(records, list):
            raise ValueError
    except (json.JSONDecodeError, ValueError):
        auth.output_error("--records 必须是 JSON 数组")

    total_written = 0
    all_ids = []
    api_path = f"/open-apis/bitable/v1/apps/{app_token}/tables/{args.table_id}/records/batch_create"

    for i in range(0, len(records), _BATCH_SIZE):
        batch = records[i:i + _BATCH_SIZE]
        body = json.dumps({"records": [{"fields": r} for r in batch]})
        data = auth.run_lark_cli(["api", "POST", api_path, "--data", body])
        created = data.get("records") or []
        all_ids.extend(r.get("record_id", "") for r in created)
        total_written += len(created) or len(batch)

    auth.output_ok({"record_count": total_written, "record_ids": all_ids})


if __name__ == "__main__":
    main()
