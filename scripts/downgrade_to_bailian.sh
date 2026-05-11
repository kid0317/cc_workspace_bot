#!/bin/bash
# downgrade_to_bailian.sh — 一键备份 config.yaml 并把所有 app 切到 bailian provider
#
# 行为：
#   1. 备份 config.yaml → config.yaml.backup.<YYYYMMDD_HHMMSS>
#   2. 对每个 app 的 claude: 块：
#        - 删除 model: / effort: 行（含注释）
#        - 删除已有 provider: 行
#        - 在 permission_mode: 之后插入 provider: "bailian"
#      其余配置（apps 字段、server、claude.providers、cleanup 等）保持不变。
#
# Usage:
#   ./scripts/downgrade_to_bailian.sh            # 实际执行
#   ./scripts/downgrade_to_bailian.sh --dry-run  # 预览 diff，不写回
#   ./scripts/downgrade_to_bailian.sh --restore  # 从最近的备份恢复

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
CONFIG_FILE="$PROJECT_DIR/config.yaml"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; BOLD='\033[1m'; NC='\033[0m'
info()  { echo -e "${GREEN}✅ $*${NC}"; }
warn()  { echo -e "${YELLOW}⚠️  $*${NC}"; }
error() { echo -e "${RED}❌ $*${NC}" >&2; }

MODE="apply"
case "${1:-}" in
  --dry-run) MODE="dry-run" ;;
  --restore) MODE="restore" ;;
  "") ;;
  *) error "未知参数: $1"; echo "用法: $0 [--dry-run|--restore]"; exit 2 ;;
esac

if [[ ! -f "$CONFIG_FILE" ]]; then
  error "找不到 $CONFIG_FILE"
  exit 1
fi

# ---- restore 模式 -------------------------------------------------------
if [[ "$MODE" == "restore" ]]; then
  latest="$(ls -1t "$CONFIG_FILE".backup.* 2>/dev/null | head -n1 || true)"
  if [[ -z "$latest" ]]; then
    error "找不到任何备份文件"
    exit 1
  fi
  cp "$latest" "$CONFIG_FILE"
  info "已从 $latest 恢复到 $CONFIG_FILE"
  exit 0
fi

# ---- backup -------------------------------------------------------------
TS="$(date +%Y%m%d_%H%M%S)"
BACKUP_FILE="$CONFIG_FILE.backup.$TS"
if [[ "$MODE" == "apply" ]]; then
  cp "$CONFIG_FILE" "$BACKUP_FILE"
  info "已备份到 $BACKUP_FILE"
fi

# ---- transform via embedded Python --------------------------------------
TMP_OUT="$(mktemp)"
trap 'rm -f "$TMP_OUT"' EXIT

python3 - "$CONFIG_FILE" "$TMP_OUT" <<'PY'
import re
import sys

src_path, out_path = sys.argv[1], sys.argv[2]
with open(src_path, "r", encoding="utf-8") as f:
    lines = f.readlines()

# State machine over the apps: list. We only mutate lines inside an app's
# `claude:` mapping (4-space indented child of an apps[] item).
#
# Indent levels in the file (2-space):
#   apps:                    (0)
#     - id: ...              (2)  <- app item
#       claude:              (6)  <- app.claude key
#         permission_mode:   (8)  <- claude.* keys

INDENT_APPS_ITEM = 4   # "  - "  → first non-dash char is at col 4
INDENT_APP_KEY = 4     # keys under each app item live at col 4
INDENT_CLAUDE_KEY = 6  # keys under claude: live at col 6

def leading_spaces(s: str) -> int:
    return len(s) - len(s.lstrip(" "))

out = []
in_apps = False
in_claude_block = False
inserted_provider = False

# Recognises lines like:  '    - id: "xxx"'  (start of a new app item)
APP_ITEM_RE = re.compile(r"^ {2,4}- ")
# Recognises top-level (col 0) keys that close `apps:` (e.g. server:, claude:)
TOP_KEY_RE = re.compile(r"^[A-Za-z_]")

# Recognises lines we want to drop from inside a claude: block.
# Match any line whose key (ignoring leading whitespace) is model/effort/provider,
# including lines that have a trailing comment.
DROP_KEY_RE = re.compile(r"^\s*(model|effort|provider)\s*:")

for line in lines:
    stripped = line.lstrip(" ")

    # Detect entering / leaving the top-level `apps:` block
    if line.startswith("apps:"):
        in_apps = True
        in_claude_block = False
        out.append(line)
        continue
    if in_apps and TOP_KEY_RE.match(line) and not line.startswith("apps:"):
        # A new top-level key (e.g. server:, claude:, session:) ends the apps: list
        in_apps = False
        in_claude_block = False

    if not in_apps:
        out.append(line)
        continue

    # Inside apps: list ----------------------------------------------------

    # New app item starts → reset claude-block state
    if APP_ITEM_RE.match(line):
        in_claude_block = False
        inserted_provider = False
        out.append(line)
        continue

    # Detect entering a claude: block (key under an app item)
    if leading_spaces(line) == INDENT_APP_KEY and stripped.startswith("claude:"):
        in_claude_block = True
        inserted_provider = False
        out.append(line)
        continue

    # If we hit another app-level key while we were inside claude: → leaving it
    if in_claude_block and leading_spaces(line) <= INDENT_APP_KEY and stripped.strip() != "":
        in_claude_block = False

    if in_claude_block:
        # Drop existing model/effort/provider lines outright
        if DROP_KEY_RE.match(line):
            continue
        out.append(line)
        # After we've passed the permission_mode line (the conventional first
        # key in every claude: block), inject provider: "bailian" once.
        if not inserted_provider and re.match(r"^\s*permission_mode\s*:", line):
            indent = " " * leading_spaces(line)
            out.append(f'{indent}provider: "bailian"\n')
            inserted_provider = True
        continue

    out.append(line)

with open(out_path, "w", encoding="utf-8") as f:
    f.writelines(out)
PY

# ---- diff & apply -------------------------------------------------------
if ! diff -q "$CONFIG_FILE" "$TMP_OUT" >/dev/null; then
  echo
  echo "── 变更预览 ($CONFIG_FILE → 新内容) ──"
  diff -u "$CONFIG_FILE" "$TMP_OUT" || true
  echo
fi

if [[ "$MODE" == "dry-run" ]]; then
  warn "dry-run 模式，未写回 $CONFIG_FILE"
  exit 0
fi

mv "$TMP_OUT" "$CONFIG_FILE"
trap - EXIT

# Quick sanity check：apps 数量应保持不变
orig_count=$(grep -c "^  - id:" "$BACKUP_FILE" || true)
new_count=$(grep -c "^  - id:" "$CONFIG_FILE" || true)
if [[ "$orig_count" != "$new_count" ]]; then
  error "app 数量发生变化（$orig_count → $new_count），请检查 $BACKUP_FILE 并手动恢复"
  exit 1
fi

bailian_count=$(grep -c 'provider: "bailian"' "$CONFIG_FILE" || true)
info "已将 $bailian_count 个 app 切到 bailian（共 $new_count 个 app）"
info "如需回滚：$0 --restore"
