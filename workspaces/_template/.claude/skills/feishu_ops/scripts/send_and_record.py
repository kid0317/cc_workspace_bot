"""发送飞书文字消息，并将其写入会话消息数据库，保持对话历史一致性。

当 Claude 通过 Skill 主动调用飞书发送消息（send_output=false 任务）时，
发出的消息不会经过 runner 的 DB 写入路径。此脚本同时完成发送和记录，
确保用户下次进入同一频道时，Claude 能从历史中看到自己发过的内容。

用法：
    python send_and_record.py --routing_key p2p:oc_xxx \
        --text "消息内容" \
        --db_path /path/to/workspace.db \
        --session_id <session_uuid>
"""

import argparse
import sqlite3
import sys
import time
import uuid
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="发送飞书消息并写入 DB")
    parser.add_argument("--routing_key", required=True,
                        help="目标路由键，格式：p2p:ou_xxx 或 group:oc_xxx")
    parser.add_argument("--text", required=True, help="消息文本内容")
    parser.add_argument("--db_path", required=True, help="SQLite 数据库路径")
    parser.add_argument("--session_id", required=True, help="当前会话 UUID（来自 SESSION_CONTEXT.md）")
    args = parser.parse_args()

    # 1. 发送飞书消息
    data = auth.run_lark_cli(
        ["im", "+messages-send"] +
        auth.get_lark_cli_target_flags(args.routing_key) +
        ["--text", args.text]
    )
    feishu_msg_id = data.get("message_id", "")

    # 2. 写入 messages 表（role=assistant），保持对话历史完整
    db_path = Path(args.db_path)
    if not db_path.exists():
        auth.output_error(f"db_path 不存在：{args.db_path}")

    msg_id = str(uuid.uuid4())
    now_iso = time.strftime("%Y-%m-%dT%H:%M:%S+00:00", time.gmtime())

    try:
        conn = sqlite3.connect(str(db_path))
        conn.execute(
            """INSERT INTO messages (id, session_id, sender_id, role, content, feishu_msg_id, created_at)
               VALUES (?, ?, ?, ?, ?, ?, ?)""",
            (msg_id, args.session_id, "", "assistant", args.text, feishu_msg_id, now_iso),
        )
        conn.commit()
        conn.close()
    except sqlite3.Error as e:
        # 发送已成功，DB 写入失败仅记录错误，不影响用户体验
        auth.output_ok({
            "message_id": feishu_msg_id,
            "recorded": False,
            "record_error": str(e),
        })
        return

    auth.output_ok({
        "message_id": feishu_msg_id,
        "recorded": True,
        "db_message_id": msg_id,
    })


if __name__ == "__main__":
    main()
