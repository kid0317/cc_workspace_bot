"""查询飞书日历事件。

用法：
    python list_events.py \
        --calendar_id primary \
        --start_time 2026-03-01T00:00:00+08:00 \
        --end_time 2026-03-31T23:59:59+08:00
"""

import argparse
import json
import sys
from datetime import datetime, timezone, timedelta
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def _to_unix(dt_str: str) -> str:
    """将 ISO 8601 时间字符串转为 Unix 时间戳（秒）字符串。"""
    # 支持带时区偏移格式，如 2026-03-01T00:00:00+08:00
    for fmt in ("%Y-%m-%dT%H:%M:%S%z", "%Y-%m-%dT%H:%M:%S"):
        try:
            dt = datetime.strptime(dt_str, fmt)
            if dt.tzinfo is None:
                dt = dt.replace(tzinfo=timezone(timedelta(hours=8)))
            return str(int(dt.timestamp()))
        except ValueError:
            continue
    return dt_str  # fallback: 已是时间戳则直接返回


def main() -> None:
    parser = argparse.ArgumentParser(description="查询飞书日历事件")
    parser.add_argument("--calendar_id", default="primary", help="日历 ID（默认 primary）")
    parser.add_argument("--start_time", required=True, help="开始时间（ISO 8601 或 Unix 时间戳）")
    parser.add_argument("--end_time", required=True, help="结束时间（ISO 8601 或 Unix 时间戳）")
    args = parser.parse_args()

    params = json.dumps({
        "calendar_id": args.calendar_id,
        "start_time": _to_unix(args.start_time),
        "end_time": _to_unix(args.end_time),
    })

    data = auth.run_lark_cli(
        ["calendar", "events", "instance_view", "--params", params]
    )
    auth.output_ok({"events": data.get("items") or data.get("events", [])})


if __name__ == "__main__":
    main()
