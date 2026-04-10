"""发送飞书图片消息（自动上传）。

用法：
    python send_image.py \
        --routing_key group:oc_xxx \
        --image_path /workspace/outputs/chart.png
"""

import argparse
import subprocess
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="发送飞书图片消息")
    parser.add_argument("--routing_key", required=True)
    parser.add_argument("--image_path", required=True,
                        help="图片绝对路径（jpg/png/gif/webp，≤30MB）")
    args = parser.parse_args()

    image_path = Path(args.image_path).resolve()
    if not image_path.exists():
        auth.output_error(f"图片文件不存在：{args.image_path}")

    # lark-cli 要求相对路径：cd 到文件目录，传文件名
    cmd = auth.get_lark_cli_base_cmd() + \
          ["im", "+messages-send"] + \
          auth.get_lark_cli_target_flags(args.routing_key) + \
          ["--image", image_path.name]

    result = subprocess.run(cmd, capture_output=True, text=True, cwd=str(image_path.parent))

    if result.returncode != 0:
        try:
            err = json.loads(result.stderr)
            error_info = err.get("error", {})
            msg = error_info.get("message", "lark-cli 执行失败")
            hint = error_info.get("hint", "")
        except (json.JSONDecodeError, ValueError):
            msg = result.stderr.strip() or result.stdout.strip() or "lark-cli 执行失败"
            hint = ""
        auth.output_error(msg, hint)

    try:
        out = json.loads(result.stdout)
    except (json.JSONDecodeError, ValueError):
        auth.output_error(f"lark-cli 输出解析失败: {result.stdout[:300]}")

    data = out.get("data") or {}
    auth.output_ok({"message_id": data.get("message_id", "")})


if __name__ == "__main__":
    main()
