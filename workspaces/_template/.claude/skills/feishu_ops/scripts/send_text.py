"""发送飞书文字消息。

用法：
    python send_text.py --routing_key p2p:ou_xxx --text "消息内容"
    python send_text.py --routing_key group:oc_xxx --text "群消息"
"""

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="发送飞书文字消息")
    parser.add_argument("--routing_key", required=True,
                        help="目标路由键，格式：p2p:ou_xxx（用户）或 group:oc_xxx（群组）")
    parser.add_argument("--text", required=True, help="消息文本内容")
    args = parser.parse_args()

    data = auth.run_lark_cli(
        ["im", "+messages-send"] +
        auth.get_lark_cli_target_flags(args.routing_key) +
        ["--text", args.text]
    )
    auth.output_ok({"message_id": data.get("message_id", "")})


if __name__ == "__main__":
    main()
