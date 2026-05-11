#!/bin/bash
# bootstrap_v5_1.sh — 一次性迁移脚本，把 v5.1 结构带到所有老陪伴 workspace
#
# 对每个 workspace：
#   1. 调用 calibrate_templates.py 生成 keyword_templates.yaml
#   2. 写初始 .calibrate_state（已由 calibrate_templates.py 处理）
#   3. 如果 material_pool.md 存在老条目（无 fit_score 字段），补字段 fit_score=0.7 / suggested_form=null / suggested_verb=看见
#   4. 写初始 .material_fetch_state.json（如不存在）
#
# 用法:
#   bootstrap_v5_1.sh --all      对所有陪伴 workspace
#   bootstrap_v5_1.sh <workspace>
#   bootstrap_v5_1.sh --dry-run --all

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TEMPLATE_DIR="$REPO_ROOT/workspaces/_companion"
CONFIG_FILE="$REPO_ROOT/config.yaml"
CALIBRATE_SCRIPT="$TEMPLATE_DIR/.claude/skills/calibrate_params/calibrate_templates.py"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BOLD='\033[1m'; NC='\033[0m'
info()  { echo -e "${GREEN}✓${NC} $*"; }
warn()  { echo -e "${YELLOW}⚠${NC} $*"; }
err()   { echo -e "${RED}✗${NC} $*" >&2; }
step()  { echo -e "${BOLD}── $*${NC}"; }

DRY_RUN=false
APPLY_ALL=false
TARGET_WS=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN=true ;;
        --all) APPLY_ALL=true ;;
        -h|--help) head -20 "$0" | grep '^#' | sed 's/^# \?//'; exit 0 ;;
        *) TARGET_WS="$1" ;;
    esac
    shift
done

[[ ! -f "$CALIBRATE_SCRIPT" ]] && { err "calibrate_templates.py 不存在：$CALIBRATE_SCRIPT"; exit 1; }

# 收集陪伴 workspace（同 sync_template.sh 的逻辑）
collect_companion_workspaces() {
    CFG="$CONFIG_FILE" python3 << 'PYEOF'
import re, os
cfg = open(os.environ['CFG']).read()
blocks = re.split(r'\n(?=  - id:)', cfg)
for b in blocks:
    m = re.search(r'workspace_dir:\s*"([^"]+)"', b)
    if not m: continue
    ws = m.group(1)
    is_companion = ('workspace_mode: "companion"' in b) or os.path.isdir(ws + '/.claude/skills/life_sim')
    if is_companion:
        print(ws)
PYEOF
}

if [[ "$APPLY_ALL" == "true" ]]; then
    WORKSPACES=$(collect_companion_workspaces)
elif [[ -n "$TARGET_WS" ]]; then
    if [[ -d "$TARGET_WS" ]]; then
        WORKSPACES="$TARGET_WS"
    elif [[ -d "/root/$TARGET_WS" ]]; then
        WORKSPACES="/root/$TARGET_WS"
    else
        err "workspace 不存在：$TARGET_WS"; exit 1
    fi
else
    err "需要 --all 或 <workspace>"; exit 1
fi

[[ -z "$WORKSPACES" ]] && { err "没有识别到任何陪伴 workspace"; exit 1; }

# ── 处理每个 workspace ────────────────────────────────────────────────
TOTAL=0; KT_CREATED=0; POOL_BACKFILLED=0; FETCH_STATE_CREATED=0

while IFS= read -r ws; do
    [[ -z "$ws" ]] && continue
    [[ ! -d "$ws" ]] && { warn "跳过不存在：$ws"; continue; }

    TOTAL=$((TOTAL + 1))
    WS_NAME=$(basename "$ws")
    step "[$WS_NAME]"

    # 1. 生成 keyword_templates.yaml
    KT_FILE="$ws/memory/keyword_templates.yaml"
    if [[ "$DRY_RUN" == "true" ]]; then
        if [[ ! -f "$KT_FILE" ]]; then
            echo "  WOULD CREATE: memory/keyword_templates.yaml"
        fi
    else
        python3 "$CALIBRATE_SCRIPT" "$ws" 2>&1 | sed 's/^/  /'
        if [[ -f "$KT_FILE" ]]; then
            KT_CREATED=$((KT_CREATED + 1))
        fi
    fi

    # 2. 回填老 material_pool.md 的缺失字段
    POOL_FILE="$ws/memory/material_pool.md"
    if [[ -f "$POOL_FILE" ]]; then
        if [[ "$DRY_RUN" == "true" ]]; then
            MISSING=$(python3 -c "
import re
c = open('$POOL_FILE').read()
entries = re.split(r'(?=## \[MAT)', c)
missing = sum(1 for e in entries if e.strip().startswith('## [MAT') and 'fit_score' not in e)
print(missing)
" 2>/dev/null || echo 0)
            [[ ${MISSING:-0} -gt 0 ]] && echo "  WOULD BACKFILL: material_pool.md ($MISSING entries)"
        else
            BACKFILLED=$(python3 << PYEOF 2>/dev/null
import re
path = '$POOL_FILE'
try:
    c = open(path).read()
except:
    print(0); raise SystemExit
entries = re.split(r'(?=## \[MAT)', c)
header = [e for e in entries if not e.strip().startswith('## [MAT')]
data = [e for e in entries if e.strip().startswith('## [MAT')]
n = 0
new_data = []
for e in data:
    if 'fit_score' in e:
        new_data.append(e)
        continue
    # 补字段：在首行后插入（保持格式）
    lines = e.split('\n', 1)
    head = lines[0]
    rest = lines[1] if len(lines) > 1 else ''
    backfill = 'fit_score: 0.7\nsuggested_form: null\nsuggested_verb: 看见\n'
    # 判断条目结束：下一个 ## 之前
    new_entry = head + '\n' + backfill + rest
    new_data.append(new_entry)
    n += 1
if n > 0:
    open(path, 'w').write(''.join(header) + ''.join(new_data))
print(n)
PYEOF
)
            if [[ ${BACKFILLED:-0} -gt 0 ]]; then
                info "  backfilled material_pool.md: $BACKFILLED entries"
                POOL_BACKFILLED=$((POOL_BACKFILLED + BACKFILLED))
            fi
        fi
    fi

    # 3. 初始化 .material_fetch_state.json（若不存在）
    FETCH_STATE="$ws/.material_fetch_state.json"
    if [[ ! -f "$FETCH_STATE" ]]; then
        if [[ "$DRY_RUN" == "true" ]]; then
            echo "  WOULD CREATE: .material_fetch_state.json"
        else
            cat > "$FETCH_STATE" << EOF
{
  "last_success_ts": "",
  "last_run_ts": "",
  "consecutive_failures": 0,
  "platform_failures": {},
  "next_platform_rotation": "xiaohongshu"
}
EOF
            info "  created .material_fetch_state.json"
            FETCH_STATE_CREATED=$((FETCH_STATE_CREATED + 1))
        fi
    fi

done <<< "$WORKSPACES"

echo ""
step "bootstrap 完成"
info "workspace 处理：$TOTAL"
info "keyword_templates.yaml 生成：$KT_CREATED"
info "material_pool.md 回填：$POOL_BACKFILLED 条目"
info "material_fetch_state.json 创建：$FETCH_STATE_CREATED"
[[ "$DRY_RUN" == "true" ]] && warn "DRY RUN 模式，实际未落盘"
