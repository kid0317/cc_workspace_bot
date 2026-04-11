#!/bin/bash
# migrate_workspaces.sh — 迁移所有已有 workspace 到 lark-cli feishu_ops v3
#
# 对 config.yaml 中每个 app 执行：
#   1. 注册 lark-cli profile（已存在则跳过）
#   2. 更新 feishu.json 添加 lark_profile 字段
#   3. 同步最新 scripts/ 和 SKILL.md 到该 workspace 的 feishu_ops 目录
#
# Usage:
#   ./scripts/migrate_workspaces.sh            # 实际执行
#   ./scripts/migrate_workspaces.sh --dry-run  # 预览，不实际修改

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
CONFIG_FILE="$PROJECT_DIR/config.yaml"
TEMPLATE_SCRIPTS="$PROJECT_DIR/workspaces/_template/.claude/skills/feishu_ops/scripts"
TEMPLATE_SKILL="$PROJECT_DIR/workspaces/_template/.claude/skills/feishu_ops/SKILL.md"

DRY_RUN=false
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=true

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; BOLD='\033[1m'; NC='\033[0m'
info()  { echo -e "${GREEN}  ✅ $*${NC}"; }
warn()  { echo -e "${YELLOW}  ⚠️  $*${NC}"; }
error() { echo -e "${RED}  ❌ $*${NC}"; }
step()  { echo -e "\n${BOLD}── $*${NC}"; }

if [[ ! -f "$CONFIG_FILE" ]]; then
    error "config.yaml 不存在: $CONFIG_FILE"; exit 1
fi

TOTAL=0; OK=0; SKIP=0; FAIL=0

# 从 config.yaml 解析所有 app
while IFS= read -r line; do
    APP_ID=$(echo "$line" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); print(d['id'])")
    FEISHU_APP_ID=$(echo "$line" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); print(d['app_id'])")
    FEISHU_APP_SECRET=$(echo "$line" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); print(d['app_secret'])")
    WORKSPACE_DIR=$(echo "$line" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); print(d['workspace_dir'])")

    TOTAL=$((TOTAL+1))
    step "[$APP_ID] $WORKSPACE_DIR"

    FEISHU_OPS_DIR="$WORKSPACE_DIR/.claude/skills/feishu_ops"
    FEISHU_JSON="$FEISHU_OPS_DIR/feishu.json"
    DEST_SCRIPTS="$FEISHU_OPS_DIR/scripts"

    # 检查目录存在
    if [[ ! -d "$FEISHU_OPS_DIR" ]]; then
        warn "feishu_ops 目录不存在，跳过"
        SKIP=$((SKIP+1)); continue
    fi
    if [[ ! -f "$FEISHU_JSON" ]]; then
        warn "feishu.json 不存在，跳过"
        SKIP=$((SKIP+1)); continue
    fi

    # ── Step 1: 注册 lark-cli profile ──────────────────────────────
    PROFILE_EXISTS=false
    if lark-cli profile list 2>/dev/null | \
        python3 -c "import sys,json; names=[p['name'] for p in json.loads(sys.stdin.read())]; exit(0 if '${APP_ID}' in names else 1)" 2>/dev/null; then
        PROFILE_EXISTS=true
    fi

    if $PROFILE_EXISTS; then
        warn "profile '${APP_ID}' 已存在，跳过注册"
    elif $DRY_RUN; then
        echo "  [dry] lark-cli config init --name '${APP_ID}' --app-id '${FEISHU_APP_ID}'"
    else
        if echo "${FEISHU_APP_SECRET}" | lark-cli config init \
            --name "${APP_ID}" --app-id "${FEISHU_APP_ID}" --app-secret-stdin 2>/dev/null; then
            info "profile '${APP_ID}' 注册完成"
        else
            error "profile 注册失败，跳过该 workspace"
            FAIL=$((FAIL+1)); continue
        fi
    fi

    # ── Step 2: 更新 feishu.json (添加 lark_profile) ───────────────
    HAS_PROFILE=$(python3 -c "import json; d=json.load(open('$FEISHU_JSON')); print('yes' if 'lark_profile' in d else 'no')" 2>/dev/null || echo "no")
    if [[ "$HAS_PROFILE" == "yes" ]]; then
        warn "feishu.json 已含 lark_profile，跳过"
    elif $DRY_RUN; then
        echo "  [dry] 添加 lark_profile='${APP_ID}' 到 $FEISHU_JSON"
    else
        python3 - "${FEISHU_JSON}" "${APP_ID}" << 'PYEOF'
import json, sys
path, profile = sys.argv[1], sys.argv[2]
with open(path) as f:
    d = json.load(f)
d["lark_profile"] = profile
with open(path, "w") as f:
    json.dump(d, f, indent=2, ensure_ascii=False)
    f.write("\n")
PYEOF
        chmod 600 "$FEISHU_JSON"
        info "feishu.json 已添加 lark_profile='${APP_ID}'"
    fi

    # ── Step 3: 同步 scripts/ 和 SKILL.md ──────────────────────────
    if [[ ! -d "$DEST_SCRIPTS" ]]; then
        warn "scripts/ 不存在，跳过同步"
        SKIP=$((SKIP+1)); continue
    fi

    if $DRY_RUN; then
        echo "  [dry] cp scripts/*.py → $DEST_SCRIPTS/"
        echo "  [dry] cp SKILL.md → $FEISHU_OPS_DIR/"
    else
        cp -f "$TEMPLATE_SCRIPTS"/*.py "$DEST_SCRIPTS/"
        cp -f "$TEMPLATE_SKILL" "$FEISHU_OPS_DIR/SKILL.md"
        info "scripts/ 和 SKILL.md 已同步"
    fi

    OK=$((OK+1))

done < <(python3 - "$CONFIG_FILE" << 'PYEOF'
import yaml, json, sys
with open(sys.argv[1]) as f:
    cfg = yaml.safe_load(f)
for app in cfg.get("apps", []):
    print(json.dumps({
        "id": app.get("id", ""),
        "app_id": app.get("feishu_app_id", ""),
        "app_secret": app.get("feishu_app_secret", ""),
        "workspace_dir": app.get("workspace_dir", ""),
    }))
PYEOF
)

echo ""
echo -e "${BOLD}================================================${NC}"
echo -e "${GREEN}  迁移完成${NC} | 总计: $TOTAL | 成功: $OK | 跳过: $SKIP | 失败: $FAIL"
$DRY_RUN && echo -e "${YELLOW}  （dry-run 模式，未实际修改任何文件）${NC}"
