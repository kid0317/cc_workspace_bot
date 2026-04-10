"""创建飞书多维表格（Bitable）应用。

用法：
    python create_bitable.py --name "项目管理" [--folder_token <token>]
"""

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="创建多维表格应用")
    parser.add_argument("--name", required=True, help="多维表格名称")
    parser.add_argument("--folder_token", default="", help="目标文件夹 token（留空放根目录）")
    args = parser.parse_args()

    cmd = ["base", "+base-create", "--name", args.name, "--time-zone", "Asia/Shanghai"]
    if args.folder_token:
        cmd += ["--folder-token", args.folder_token]

    data = auth.run_lark_cli(cmd)
    app = data.get("app") or data
    app_token = app.get("app_token", "") or app.get("appToken", "")
    auth.output_ok({
        "app_token": app_token,
        "url": app.get("url", f"https://open.feishu.cn/base/{app_token}"),
        "name": args.name,
    })


if __name__ == "__main__":
    main()
