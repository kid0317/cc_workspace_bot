"""发送飞书富文本消息（带标题 + 多段落，保持原生 post 类型）。

用法：
    python send_post.py \
        --routing_key group:oc_xxx \
        --title "消息标题" \
        --paragraphs '["第一段内容", "第二段，含[链接](https://example.com)"]'
"""

import argparse
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def _parse_inline_links(text: str) -> list:
    """将 [文字](URL) 格式解析为飞书 post 内容元素列表。"""
    elements = []
    last = 0
    for m in re.finditer(r'\[([^\]]+)\]\((https?://[^\)]+)\)', text):
        before = text[last:m.start()]
        if before:
            elements.append({"tag": "text", "text": before})
        elements.append({"tag": "a", "text": m.group(1), "href": m.group(2)})
        last = m.end()
    tail = text[last:]
    if tail:
        elements.append({"tag": "text", "text": tail})
    return elements or [{"tag": "text", "text": text}]


def main() -> None:
    parser = argparse.ArgumentParser(description="发送飞书富文本消息")
    parser.add_argument("--routing_key", required=True)
    parser.add_argument("--title", default="", help="消息标题（可为空）")
    parser.add_argument("--paragraphs", required=True,
                        help="JSON 字符串数组，每项为一段文字；支持 [文字](URL) 内嵌链接")
    args = parser.parse_args()

    try:
        paragraphs = json.loads(args.paragraphs)
        if not isinstance(paragraphs, list):
            raise ValueError
    except (json.JSONDecodeError, ValueError):
        auth.output_error("--paragraphs 必须是 JSON 字符串数组，如 [\"第一段\", \"第二段\"]")

    content = [_parse_inline_links(p) for p in paragraphs]
    post_body = json.dumps({"zh_cn": {"title": args.title, "content": content}})

    data = auth.run_lark_cli(
        ["im", "+messages-send"] +
        auth.get_lark_cli_target_flags(args.routing_key) +
        ["--content", post_body, "--msg-type", "post"]
    )
    auth.output_ok({"message_id": data.get("message_id", "")})


if __name__ == "__main__":
    main()
