#!/bin/bash
# sync_template.sh — 把 _companion/ 模板的变化传播到所有陪伴 workspace
#
# Usage:
#   sync_template.sh [--dry-run] [--force] [--preserve-slots] [--all | <workspace-name>]
#
# Modes:
#   --dry-run         只列出会改什么，不落盘
#   --force           直接覆盖（用于 SKILL.md 这类无 slot 的文件）
#   --preserve-slots  保留 workspace 侧填槽值，仅同步骨架（默认）
#   --all             对 config.yaml 中所有陪伴 workspace 应用
#
# 识别陪伴 workspace：
#   - 从 config.yaml 读取 workspace_dir 列表
#   - 其中有 workspace_mode: "companion" 字段的，或有 .claude/skills/life_sim/ 目录的
#
# 同步范围：
#   --force  覆盖：
#     - .claude/skills/material_fetch/SKILL.md
#     - .claude/skills/material_fetch/filters.yaml
#     - .claude/skills/life_sim/SKILL.md
#     - .claude/skills/memory_distill/SKILL.md
#     - .claude/skills/memory_write/SKILL.md
#     - .claude/skills/proactive/SKILL.md
#     - .claude/skills/knowledge_growth/SKILL.md
#     - .claude/skills/calibrate_params/SKILL.md
#     - .claude/skills/calibrate_params/recalculate.sh
#     - .claude/hooks/inject_history.py
#     - .claude/hooks/inject_background_diff.py
#     - .claude/hooks/reply_checklist.sh
#     - memory/keyword_templates.default.yaml
#   --preserve-slots  仅创建（不覆盖已存在）：
#     - memory/unresolved.md
#     - memory/keyword_templates.yaml（基于 default 复制一份）
#   不同步：
#     - memory/persona.md / user_profile.md / life_log.md / material_pool.md
#     - character_params.yaml（新字段需合并，见 --merge-params）
#     - CLAUDE.md
#
# 版本追踪：
#   - 模板侧：workspaces/_companion/VERSION
#   - 实例侧：<workspace>/.template_version
#   同步成功后自动更新 .template_version。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TEMPLATE_DIR="$REPO_ROOT/workspaces/_companion"
CONFIG_FILE="$REPO_ROOT/config.yaml"

# 颜色
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BOLD='\033[1m'; NC='\033[0m'
info()  { echo -e "${GREEN}✓${NC} $*"; }
warn()  { echo -e "${YELLOW}⚠${NC} $*"; }
err()   { echo -e "${RED}✗${NC} $*" >&2; }
step()  { echo -e "${BOLD}── $*${NC}"; }

# ── 参数解析 ─────────────────────────────────────────────────────────────────
DRY_RUN=false
FORCE_MODE=false
PRESERVE_SLOTS=true
TARGET_WS=""
APPLY_ALL=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN=true ;;
        --force) FORCE_MODE=true; PRESERVE_SLOTS=false ;;
        --preserve-slots) PRESERVE_SLOTS=true ;;
        --all) APPLY_ALL=true ;;
        -h|--help)
            head -30 "$0" | grep '^#' | sed 's/^# \?//'
            exit 0
            ;;
        -*)
            err "未知选项：$1"; exit 1
            ;;
        *)
            TARGET_WS="$1"
            ;;
    esac
    shift
done

if [[ "$APPLY_ALL" == "false" && -z "$TARGET_WS" ]]; then
    err "需要指定 <workspace-name> 或 --all"
    exit 1
fi

# ── 读取模板版本 ─────────────────────────────────────────────────────────────
if [[ ! -f "$TEMPLATE_DIR/VERSION" ]]; then
    err "模板 VERSION 文件不存在：$TEMPLATE_DIR/VERSION"
    exit 1
fi
TEMPLATE_VERSION=$(cat "$TEMPLATE_DIR/VERSION" | tr -d '[:space:]')
info "模板版本：$TEMPLATE_VERSION"

# ── 识别陪伴 workspace ───────────────────────────────────────────────────────
collect_companion_workspaces() {
    # 返回 workspace_dir 绝对路径列表（每行一个）
    python3 << 'PYEOF'
import re, os, sys

cfg = open(os.environ['CFG']).read()
# 粗切块：每个 app 从 "  - id:" 开始
blocks = re.split(r'\n(?=  - id:)', cfg)
for b in blocks:
    m_ws = re.search(r'workspace_dir:\s*"([^"]+)"', b)
    if not m_ws: continue
    ws = m_ws.group(1)
    # 判定陪伴：workspace_mode: "companion" 或有 life_sim skill
    is_companion = ('workspace_mode: "companion"' in b) or os.path.isdir(ws + '/.claude/skills/life_sim')
    if is_companion:
        print(ws)
PYEOF
}

export CFG="$CONFIG_FILE"

if [[ "$APPLY_ALL" == "true" ]]; then
    WORKSPACES=$(collect_companion_workspaces)
else
    # 支持传 workspace 名字（如 mango_daxian）或路径
    if [[ -d "$TARGET_WS" ]]; then
        WORKSPACES="$TARGET_WS"
    elif [[ -d "/root/$TARGET_WS" ]]; then
        WORKSPACES="/root/$TARGET_WS"
    else
        err "workspace 不存在：$TARGET_WS"
        exit 1
    fi
fi

if [[ -z "$WORKSPACES" ]]; then
    err "没有识别到任何陪伴 workspace"
    exit 1
fi

# ── 定义要 force 同步的文件（相对模板路径）───────────────────────────────────
FORCE_FILES=(
    ".claude/skills/material_fetch/SKILL.md"
    ".claude/skills/material_fetch/filters.yaml"
    ".claude/skills/life_sim/SKILL.md"
    ".claude/skills/memory_distill/SKILL.md"
    ".claude/skills/memory_write/SKILL.md"
    ".claude/skills/proactive/SKILL.md"
    ".claude/skills/knowledge_growth/SKILL.md"
    ".claude/skills/calibrate_params/SKILL.md"
    ".claude/skills/calibrate_params/recalculate.sh"
    ".claude/skills/calibrate_params/calibrate_templates.py"
    ".claude/hooks/inject_history.py"
    ".claude/hooks/inject_background_diff.py"
    ".claude/hooks/reply_checklist.sh"
    "memory/keyword_templates.default.yaml"
    "VERSION"
)

CREATE_IF_MISSING=(
    "memory/unresolved.md"
)

# character_params.yaml 字段合并（只 append 不存在的字段）
MERGE_PARAMS_FILE="character_params.yaml"

# ── 对每个 workspace 执行 ────────────────────────────────────────────────────
TOTAL=0; UPDATED=0; CREATED=0; SKIPPED=0

while IFS= read -r ws; do
    [[ -z "$ws" ]] && continue
    [[ ! -d "$ws" ]] && { warn "跳过不存在的 workspace: $ws"; continue; }

    TOTAL=$((TOTAL + 1))
    WS_NAME=$(basename "$ws")
    WS_VERSION_FILE="$ws/.template_version"
    WS_VERSION=$(cat "$WS_VERSION_FILE" 2>/dev/null | tr -d '[:space:]' || echo "0.0.0")

    step "[$WS_NAME] 当前版本: $WS_VERSION → $TEMPLATE_VERSION"

    # 1. force 覆盖
    for f in "${FORCE_FILES[@]}"; do
        src="$TEMPLATE_DIR/$f"
        dst="$ws/$f"
        if [[ ! -f "$src" ]]; then
            warn "模板不存在：$f，跳过"
            continue
        fi
        mkdir -p "$(dirname "$dst")" 2>/dev/null || true
        if [[ "$DRY_RUN" == "true" ]]; then
            if [[ -f "$dst" ]]; then
                if ! cmp -s "$src" "$dst"; then
                    echo "  WOULD UPDATE: $f"
                fi
            else
                echo "  WOULD CREATE: $f"
            fi
        else
            if [[ -f "$dst" ]] && cmp -s "$src" "$dst"; then
                :  # 相同，跳过
            else
                cp "$src" "$dst"
                UPDATED=$((UPDATED + 1))
                info "  updated: $f"
            fi
        fi
    done

    # 2. 仅创建（不覆盖）
    for f in "${CREATE_IF_MISSING[@]}"; do
        src="$TEMPLATE_DIR/$f"
        dst="$ws/$f"
        if [[ -f "$dst" ]]; then
            :  # 已存在
        elif [[ ! -f "$src" ]]; then
            warn "  模板缺失：$f"
        else
            if [[ "$DRY_RUN" == "true" ]]; then
                echo "  WOULD CREATE: $f"
            else
                mkdir -p "$(dirname "$dst")" 2>/dev/null || true
                cp "$src" "$dst"
                CREATED=$((CREATED + 1))
                info "  created: $f"
            fi
        fi
    done

    # 3. character_params.yaml 字段合并
    PARAMS_DST="$ws/character_params.yaml"
    PARAMS_SRC="$TEMPLATE_DIR/character_params.yaml"
    if [[ -f "$PARAMS_DST" && -f "$PARAMS_SRC" ]]; then
        if [[ "$DRY_RUN" == "false" ]]; then
            DST="$PARAMS_DST" SRC="$PARAMS_SRC" python3 << 'PYEOF' 2>/dev/null
import os, re

def parse_simple_yaml(path):
    """很浅的 YAML 解析，只抽 top-level 和 life_sim: 下的 key:value"""
    with open(path) as f: lines = f.readlines()
    result = {'__top': [], 'life_sim': {}}
    in_life_sim = False
    for line in lines:
        stripped = line.rstrip('\n')
        if stripped.startswith('life_sim:'):
            in_life_sim = True; continue
        if in_life_sim and re.match(r'^[a-zA-Z_]', stripped):
            in_life_sim = False
        if in_life_sim:
            m = re.match(r'^  ([a-zA-Z_]+):\s*(.*)$', stripped)
            if m:
                result['life_sim'][m.group(1)] = (m.group(2), line)
    return result

dst_path = os.environ['DST']
src_path = os.environ['SRC']
dst = parse_simple_yaml(dst_path)
src = parse_simple_yaml(src_path)

# 把 src 中 dst 没有的 life_sim 字段 append 到 dst
missing = {k: v for k, v in src['life_sim'].items() if k not in dst['life_sim']}
if missing:
    with open(dst_path, 'a') as f:
        f.write('\n  # v5.1 字段（sync_template 自动追加）\n')
        for k, (val, raw) in missing.items():
            f.write(raw)
    print(f"MERGED: {len(missing)} new life_sim fields")
PYEOF
        fi
    fi

    # 4. 更新 .template_version
    if [[ "$DRY_RUN" == "false" ]]; then
        echo "$TEMPLATE_VERSION" > "$WS_VERSION_FILE"
    fi

done <<< "$WORKSPACES"

# ── 汇总 ──────────────────────────────────────────────────────────────────────
echo ""
step "同步完成"
info "workspace 总数：$TOTAL"
info "文件更新：$UPDATED  新建：$CREATED"
if [[ "$DRY_RUN" == "true" ]]; then
    warn "DRY RUN 模式，实际未落盘"
fi
