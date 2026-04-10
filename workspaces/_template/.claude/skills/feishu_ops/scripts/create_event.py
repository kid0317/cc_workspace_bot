"""创建飞书日历事件。

用法：
    python create_event.py \
        --calendar_id primary \
        --summary "周例会" \
        --start_time 2026-03-09T10:00:00+08:00 \
        --end_time 2026-03-09T11:00:00+08:00 \
        [--description "本周进度同步"] \
        [--attendees '["ou_aaa", "ou_bbb"]']
"""

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="创建飞书日历事件")
    parser.add_argument("--calendar_id", default="primary")
    parser.add_argument("--summary", required=True, help="事件标题")
    parser.add_argument("--start_time", required=True, help="开始时间（ISO 8601）")
    parser.add_argument("--end_time", required=True, help="结束时间（ISO 8601）")
    parser.add_argument("--description", default="")
    parser.add_argument("--attendees", default="[]",
                        help="JSON 数组，元素为与会者 open_id")
    args = parser.parse_args()

    cmd = [
        "calendar", "+create",
        "--summary", args.summary,
        "--start", args.start_time,
        "--end", args.end_time,
    ]
    if args.calendar_id and args.calendar_id != "primary":
        cmd += ["--calendar-id", args.calendar_id]
    if args.description:
        cmd += ["--description", args.description]

    try:
        attendees = json.loads(args.attendees)
        if attendees:
            cmd += ["--attendee-ids", ",".join(attendees)]
    except (json.JSONDecodeError, ValueError):
        auth.output_error("--attendees 必须是 JSON 数组，如 [\"ou_aaa\", \"ou_bbb\"]")

    data = auth.run_lark_cli(cmd)
    event = data.get("event") or data
    auth.output_ok({
        "event_id": event.get("event_id", ""),
        "summary": event.get("summary", args.summary),
    })


if __name__ == "__main__":
    main()
