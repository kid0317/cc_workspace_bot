#!/usr/bin/env bash
# 一次性清理：删除旧版 create_doc.py 残留在 skill 目录下的 tmp*.md 文件。
# 新版 create_doc.py 直接把源 md 路径传给 lark-cli，不再产生临时文件。
#
# 用法：bash cleanup_stale_tmp.sh [--dry-run]
set -euo pipefail

SKILL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRY_RUN="${1:-}"

# 旧版 create_doc.py 用 tempfile.NamedTemporaryFile(suffix=".md") 生成的文件名
# 形如 tmp + 8 个随机字符 + .md（不会误删 tmp_report.md 这类人工命名文件）
mapfile -t FILES < <(find "$SKILL_DIR" -maxdepth 2 -type f -name 'tmp????????.md')

if [[ ${#FILES[@]} -eq 0 ]]; then
  echo "无残留 tmp*.md 文件。"
  exit 0
fi

echo "发现 ${#FILES[@]} 个残留文件："
printf '  %s\n' "${FILES[@]}"

if [[ "$DRY_RUN" == "--dry-run" ]]; then
  echo "(--dry-run，未删除)"
  exit 0
fi

for f in "${FILES[@]}"; do rm -f "$f"; done
echo "已删除 ${#FILES[@]} 个文件。"
