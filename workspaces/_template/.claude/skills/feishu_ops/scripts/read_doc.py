"""读取飞书云文档纯文本内容。

用法：
    python read_doc.py --doc "https://xxx.feishu.cn/docx/doccnXXXXXX"
    python read_doc.py --doc doccnXXXXXX
"""

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="读取飞书文档内容")
    parser.add_argument("--doc", required=True, help="飞书文档 URL 或 doc_token")
    args = parser.parse_args()

    data = auth.run_lark_cli(["docs", "+fetch", "--doc", args.doc])
    # lark-cli 返回文档内容，提取纯文本
    content = data.get("content") or data.get("text") or str(data)
    auth.output_ok({"content": content})


if __name__ == "__main__":
    main()
