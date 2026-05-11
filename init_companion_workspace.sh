#!/bin/bash
# init_companion_workspace.sh — 初始化一个 Companion Workspace 并追加到 config.yaml
#
# Usage:
#   ./init_companion_workspace.sh <app-id> <workspace-dir> <feishu-app-id> <feishu-app-secret>
#
# Example:
#   ./init_companion_workspace.sh aria-companion /root/aria cli_xxx secretxxx
#
# 与 init_workspace.sh 的关键差异：
#   - 模板目录：workspaces/_companion/（不继承 _template）
#   - claude.model: sonnet（companion 需要更强模型）
#   - claude.companion: true（标识为陪伴型 workspace）
#   - 初始化后替换 settings.local.json 中的 __WORKSPACE_DIR__ 占位符

set -euo pipefail

# ── 颜色 ─────────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

info()  { echo -e "${GREEN}✅ $*${NC}"; }
warn()  { echo -e "${YELLOW}⚠️  $*${NC}"; }
error() { echo -e "${RED}❌ $*${NC}" >&2; }
step()  { echo -e "${BOLD}── $*${NC}"; }

usage() {
    echo "Usage: $0 <app-id> <workspace-dir> <feishu-app-id> <feishu-app-secret>"
    echo ""
    echo "Arguments:"
    echo "  app-id            唯一应用标识（只含字母、数字、连字符）"
    echo "  workspace-dir     workspace 目录（绝对或相对路径）"
    echo "  feishu-app-id     飞书 App ID（以 cli_ 开头）"
    echo "  feishu-app-secret 飞书 App Secret"
    echo ""
    echo "Example:"
    echo "  $0 aria-companion /root/aria cli_abc123 secretXXX"
    exit 1
}

# ── 参数检查 ──────────────────────────────────────────────────────────────────
if [[ $# -lt 4 ]]; then
    usage
fi

APP_ID="$1"
WORKSPACE_DIR="$2"
FEISHU_APP_ID="$3"
FEISHU_APP_SECRET="$4"

# ── 路径解析 ──────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="$SCRIPT_DIR/config.yaml"
TEMPLATE_DIR="$SCRIPT_DIR/workspaces/_companion"

if [[ "$WORKSPACE_DIR" != /* ]]; then
    WORKSPACE_DIR="$SCRIPT_DIR/$WORKSPACE_DIR"
fi

# ── 输入校验 ──────────────────────────────────────────────────────────────────
step "校验输入参数"

if [[ ! "$APP_ID" =~ ^[a-zA-Z0-9_-]+$ ]]; then
    error "app-id 只能包含字母、数字、下划线、连字符，当前值: $APP_ID"
    exit 1
fi

if [[ ! "$FEISHU_APP_ID" =~ ^cli_ ]]; then
    warn "feishu-app-id 通常以 cli_ 开头，当前值: $FEISHU_APP_ID"
fi

if [[ ! -f "$CONFIG_FILE" ]]; then
    error "config.yaml 不存在: $CONFIG_FILE"
    exit 1
fi

if [[ ! -d "$TEMPLATE_DIR" ]]; then
    error "companion 模版目录不存在: $TEMPLATE_DIR"
    exit 1
fi

if grep -q "^  - id: \"${APP_ID}\"" "$CONFIG_FILE"; then
    error "app-id '${APP_ID}' 已存在于 config.yaml，请使用其他名称"
    exit 1
fi

info "参数校验通过"
echo "  app-id        : $APP_ID"
echo "  workspace-dir : $WORKSPACE_DIR"
echo "  feishu-app-id : $FEISHU_APP_ID"

# ── 初始化 workspace 目录 ─────────────────────────────────────────────────────
step "初始化 workspace 目录结构"

mkdir -p \
    "$WORKSPACE_DIR" \
    "$WORKSPACE_DIR/.claude/skills" \
    "$WORKSPACE_DIR/.claude/hooks" \
    "$WORKSPACE_DIR/memory" \
    "$WORKSPACE_DIR/tasks" \
    "$WORKSPACE_DIR/sessions"

LOCK_FILE="$WORKSPACE_DIR/.memory.lock"
if [[ ! -f "$LOCK_FILE" ]]; then
    touch "$LOCK_FILE"
    info "创建 .memory.lock"
fi

PROACTIVE_STATE_FILE="$WORKSPACE_DIR/.proactive_state"
if [[ ! -f "$PROACTIVE_STATE_FILE" ]]; then
    echo "skip_count: 0" > "$PROACTIVE_STATE_FILE"
    info "创建 .proactive_state（skip_count 初始为 0）"
fi

info "目录结构就绪: $WORKSPACE_DIR"

# ── 写入飞书凭证 ──────────────────────────────────────────────────────────────
step "写入飞书凭证"

FEISHU_OPS_DIR="$WORKSPACE_DIR/.claude/skills/feishu_ops"
mkdir -p "$FEISHU_OPS_DIR"

FEISHU_JSON="$FEISHU_OPS_DIR/feishu.json"
if [[ -f "$FEISHU_JSON" ]]; then
    warn "feishu.json 已存在，跳过覆盖（如需更新请手动编辑）"
else
    cat > "$FEISHU_JSON" << EOF
{
  "app_id": "${FEISHU_APP_ID}",
  "app_secret": "${FEISHU_APP_SECRET}",
  "lark_profile": "${APP_ID}"
}
EOF
    chmod 600 "$FEISHU_JSON"
    info "写入 .claude/skills/feishu_ops/feishu.json（0600）"
fi

# ── 注册 lark-cli profile ─────────────────────────────────────────────────────
step "注册 lark-cli profile"

if command -v lark-cli &>/dev/null; then
    if lark-cli profile list 2>/dev/null | \
        python3 -c "import sys,json; names=[p['name'] for p in json.load(sys.stdin)]; exit(0 if '${APP_ID}' in names else 1)" 2>/dev/null; then
        warn "lark-cli profile '${APP_ID}' 已存在，跳过注册"
    else
        echo "${FEISHU_APP_SECRET}" | lark-cli config init \
            --name "${APP_ID}" \
            --app-id "${FEISHU_APP_ID}" \
            --app-secret-stdin
        info "lark-cli profile '${APP_ID}' 注册完成"
    fi
else
    warn "lark-cli 未安装，跳过 profile 注册"
fi

# ── 复制模版文件 ───────────────────────────────────────────────────────────────
step "从 companion 模版复制初始文件"

COPIED=0
SKIPPED=0

while IFS= read -r -d '' src; do
    if [[ -L "$src" ]]; then
        continue
    fi
    rel="${src#$TEMPLATE_DIR/}"
    dst="$WORKSPACE_DIR/$rel"
    dst_dir="$(dirname "$dst")"
    mkdir -p "$dst_dir"
    if [[ -f "$dst" ]]; then
        SKIPPED=$((SKIPPED + 1))
    else
        cp "$src" "$dst"
        COPIED=$((COPIED + 1))
    fi
done < <(find "$TEMPLATE_DIR" -type f -print0)

info "模版文件：复制 ${COPIED} 个，跳过已存在 ${SKIPPED} 个"

# ── 复制 feishu_ops scripts（从 _template 共享）────────────────────────────────
step "复制 feishu_ops scripts"

TEMPLATE_FEISHU_DIR="$SCRIPT_DIR/workspaces/_template/.claude/skills/feishu_ops"
TEMPLATE_FEISHU_SCRIPTS="$TEMPLATE_FEISHU_DIR/scripts"
DST_FEISHU_DIR="$WORKSPACE_DIR/.claude/skills/feishu_ops"
DST_FEISHU_SCRIPTS="$DST_FEISHU_DIR/scripts"

if [[ -d "$TEMPLATE_FEISHU_SCRIPTS" ]]; then
    mkdir -p "$DST_FEISHU_SCRIPTS"
    FCOPIED=0
    while IFS= read -r -d '' src; do
        rel="${src#$TEMPLATE_FEISHU_SCRIPTS/}"
        dst="$DST_FEISHU_SCRIPTS/$rel"
        if [[ ! -f "$dst" ]]; then
            mkdir -p "$(dirname "$dst")"
            cp "$src" "$dst"
            FCOPIED=$((FCOPIED + 1))
        fi
    done < <(find "$TEMPLATE_FEISHU_SCRIPTS" -type f -not -path '*/__pycache__/*' -print0)
    info "feishu_ops scripts：复制 ${FCOPIED} 个"
    # 同步 SKILL.md（feishu_ops 技能描述文件）
    TEMPLATE_SKILL_MD="$TEMPLATE_FEISHU_DIR/SKILL.md"
    DST_SKILL_MD="$DST_FEISHU_DIR/SKILL.md"
    if [[ -f "$TEMPLATE_SKILL_MD" ]] && [[ ! -f "$DST_SKILL_MD" ]]; then
        cp "$TEMPLATE_SKILL_MD" "$DST_SKILL_MD"
        info "feishu_ops SKILL.md 已复制"
    fi
else
    warn "feishu_ops scripts 模版不存在（$TEMPLATE_FEISHU_SCRIPTS），跳过"
fi

# ── 替换占位符 ────────────────────────────────────────────────────────────────
step "替换文件占位符（settings.local.json + .claude/task_templates/*.yaml）"

SETTINGS_JSON="$WORKSPACE_DIR/.claude/settings.local.json"
if [[ -f "$SETTINGS_JSON" ]]; then
    sed -i "s|__WORKSPACE_DIR__|$WORKSPACE_DIR|g" "$SETTINGS_JSON"
    info "settings.local.json 占位符已替换为: $WORKSPACE_DIR"
else
    warn "settings.local.json 不存在，跳过"
fi

# 替换 .claude/task_templates/*.yaml 中的 __WORKSPACE_DIR__ 占位符。
# 注意：id / app_id 字段已废弃（由系统从文件名/路径派生，RC-1 根因），不再替换。
# __TARGET_TYPE__ 和 __TARGET_ID__ 由 Claude 在 phase2_done 阶段从 SESSION_CONTEXT.md
# 解析 channel_key 后写入 tasks/，init 脚本不提前替换。
TMPL_COUNT=0
while IFS= read -r -d '' yaml_file; do
    sed -i -e "s|__WORKSPACE_DIR__|$WORKSPACE_DIR|g" "$yaml_file"
    TMPL_COUNT=$((TMPL_COUNT + 1))
done < <(find "$WORKSPACE_DIR/.claude/task_templates" -name "*.yaml" -type f -print0 2>/dev/null)

if [[ $TMPL_COUNT -gt 0 ]]; then
    info ".claude/task_templates/*.yaml 已替换 __WORKSPACE_DIR__（共 $TMPL_COUNT 个文件）"
    info "__TARGET_TYPE__ / __TARGET_ID__ 将在 phase2_done 时由 Claude 从 SESSION_CONTEXT.md 解析后填入 tasks/"
else
    warn ".claude/task_templates/ 下未找到 yaml 文件，跳过"
fi

# ── 追加到 config.yaml ────────────────────────────────────────────────────────
step "更新 config.yaml"

BACKUP_FILE="${CONFIG_FILE}.bak.$(date +%Y%m%d_%H%M%S)"
cp "$CONFIG_FILE" "$BACKUP_FILE"
info "已备份 config.yaml → $(basename "$BACKUP_FILE")"

NEW_APP_BLOCK="  - id: \"${APP_ID}\"
    feishu_app_id: \"${FEISHU_APP_ID}\"
    feishu_app_secret: \"${FEISHU_APP_SECRET}\"
    feishu_verification_token: \"\"
    feishu_encrypt_key: \"\"
    workspace_dir: \"${WORKSPACE_DIR}\"
    workspace_mode: \"companion\"
    allowed_chats: []
    claude:
      permission_mode: \"acceptEdits\"
      model: \"sonnet\"
      allowed_tools:
        - \"Bash\"
        - \"Read\"
        - \"Edit\"
        - \"Write\"
        - \"Glob\"
        - \"Grep\""

python3 - <<PYEOF
with open('${CONFIG_FILE}', 'r') as f:
    content = f.read()

marker = '\nserver:'
idx = content.find(marker)
if idx == -1:
    new_content = content.rstrip('\n') + '\n' + '''${NEW_APP_BLOCK}''' + '\n'
else:
    new_content = content[:idx] + '\n' + '''${NEW_APP_BLOCK}''' + content[idx:]

with open('${CONFIG_FILE}', 'w') as f:
    f.write(new_content)
print("config.yaml 已更新")
PYEOF

info "已追加 companion app '${APP_ID}' 到 config.yaml"

# ── v5.1 写入 .template_version ────────────────────────────────────────────────
step "写入 .template_version"

TEMPLATE_VERSION_FILE="$TEMPLATE_DIR/VERSION"
if [[ -f "$TEMPLATE_VERSION_FILE" ]]; then
    TEMPLATE_VER=$(cat "$TEMPLATE_VERSION_FILE" | tr -d '[:space:]')
    echo "$TEMPLATE_VER" > "$WORKSPACE_DIR/.template_version"
    info "已记录模板版本: $TEMPLATE_VER"
else
    warn "$TEMPLATE_VERSION_FILE 不存在，跳过版本记录"
fi

# ── v5.1 calibrate_templates + 冷启动种子 ─────────────────────────────────────
step "生成 keyword_templates.yaml（仅 persona 存在时）"

CALIBRATE_SCRIPT="$TEMPLATE_DIR/.claude/skills/calibrate_params/calibrate_templates.py"
PERSONA_PATH="$WORKSPACE_DIR/memory/persona.md"

if [[ -f "$PERSONA_PATH" ]] && [[ -s "$PERSONA_PATH" ]] && [[ -f "$CALIBRATE_SCRIPT" ]]; then
    python3 "$CALIBRATE_SCRIPT" "$WORKSPACE_DIR" 2>&1 | sed 's/^/  /' || true
    info "keyword_templates.yaml 已生成"
else
    warn "persona.md 不存在或为空，跳过 keyword_templates.yaml 生成（phase1_done 后将由 recalculate.sh 自动触发）"
fi

step "冷启动种子 life_log"
echo "  新角色前 24h 无 life_log 会让用户感觉'没灵魂'。v5.1 支持在 done 阶段自动生成 stub。"
echo ""
echo "  建议：初始化对话走到 done 后，手动触发一次："
echo "    cd $WORKSPACE_DIR && claude -p '加载 .claude/skills/life_sim/SKILL.md'"
echo ""
echo "  或首次 life_sim cron 触发时（每 4 小时），骰子通过后会自动生成。"
echo "  （注：由于 cron 骰子带概率，建议手动触发一次以确保前 24h 可见）"

# ── 完成摘要 ──────────────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}================================================${NC}"
echo -e "${GREEN}  Companion Workspace 初始化完成！${NC}"
echo -e "${BOLD}================================================${NC}"
echo ""
echo "  App ID        : ${APP_ID}"
echo "  Workspace     : ${WORKSPACE_DIR}"
echo "  Config        : ${CONFIG_FILE}"
echo ""
echo -e "${YELLOW}下一步：${NC}"
echo "  1. 重启服务使新配置生效：./start.sh restart"
echo "  2. 在飞书中向新应用发任意消息，开始初始化（创建角色）"
echo ""
echo -e "${YELLOW}注意：${NC}"
echo "  - config.yaml 备份于 $(basename "$BACKUP_FILE")"
echo "  - 飞书凭证已写入 ${WORKSPACE_DIR}/.claude/skills/feishu_ops/feishu.json（0600）"
echo "  - settings.local.json 和 .claude/task_templates/*.yaml 中 __WORKSPACE_DIR__ 已替换"
echo "  - tasks/ 目录为空（.gitkeep）；任务文件在 phase2_done 由 Claude 从模板生成"
echo "  - 使用 sonnet 模型（比 haiku 成本更高，角色一致性更好）"
