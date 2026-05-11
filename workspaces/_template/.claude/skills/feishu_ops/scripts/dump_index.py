"""导出飞书文档上传索引为 JSON，便于人工排查。

用法：
    python dump_index.py [--status active|deleted|remote_missing|all]
"""

import argparse
import dataclasses
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import _doc_index as di
import _feishu_auth as auth


def main() -> None:
    parser = argparse.ArgumentParser(description="导出飞书文档上传索引")
    parser.add_argument("--status", default="active",
                        choices=["active", "deleted", "remote_missing", "all"])
    args = parser.parse_args()

    store = di.SQLiteIndexStore()
    try:
        records = store.all_records(status=args.status)
    finally:
        store.close()
    auth.output_ok({
        "count": len(records),
        "records": [dataclasses.asdict(r) for r in records],
    })


if __name__ == "__main__":
    main()
