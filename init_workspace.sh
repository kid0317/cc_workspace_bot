#!/bin/bash
# init_workspace.sh — 初始化一个新的 workspace 并追加到 config.yaml
#
# Usage:
#   ./init_workspace.sh <app-id> <workspace-dir> <feishu-app-id> <feishu-app-secret>
#
# Arguments:
#   app-id            唯一应用标识，如 investment-assistant
#   workspace-dir     workspace 目录路径（绝对或相对路径）
#   feishu-app-id     飞书 App ID（以 cli_ 开头）
#   feishu-app-secret 飞书 App Secret
#
# Example:
#   ./init_workspace.sh my-bot ./workspaces/my-bot cli_abc123 secretxxx

set -euo pipefail

# ── 颜色 ─────────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

info()    { echo -e "${GREEN}✅ $*${NC}"; }
warn()    { echo -e "${YELLOW}⚠️  $*${NC}"; }
error()   { echo -e "${RED}❌ $*${NC}" >&2; }
step()    { echo -e "${BOLD}── $*${NC}"; }

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
    echo "  $0 investment-assistant /root/investment cli_abc123 secretXXX"
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
TEMPLATE_DIR="$SCRIPT_DIR/workspaces/_template"

# workspace-dir 转为绝对路径
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
    error "模版目录不存在: $TEMPLATE_DIR"
    exit 1
fi

# 检查 app-id 是否已存在
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
    "$WORKSPACE_DIR/memory" \
    "$WORKSPACE_DIR/tasks" \
    "$WORKSPACE_DIR/sessions"

# 创建 flock 锁文件
LOCK_FILE="$WORKSPACE_DIR/.memory.lock"
if [[ ! -f "$LOCK_FILE" ]]; then
    touch "$LOCK_FILE"
    info "创建 .memory.lock"
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
    info "写入 .claude/skills/feishu_ops/feishu.json（0600，含 lark_profile）"
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
    warn "lark-cli 未安装，跳过 profile 注册（运行 'npm install -g @larksuite/cli' 后手动补注册）"
fi

# ── 复制模版文件 ───────────────────────────────────────────────────────────────
step "从模版复制初始文件"

COPIED=0
SKIPPED=0

while IFS= read -r -d '' src; do
    # 跳过 symlink（安全）
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

# ── 追加到 config.yaml ────────────────────────────────────────────────────────
step "更新 config.yaml"

# 备份
BACKUP_FILE="${CONFIG_FILE}.bak.$(date +%Y%m%d_%H%M%S)"
cp "$CONFIG_FILE" "$BACKUP_FILE"
info "已备份 config.yaml → $(basename "$BACKUP_FILE")"

# 构造新 app 块（缩进与现有格式一致）
NEW_APP_BLOCK="  - id: \"${APP_ID}\"
    feishu_app_id: \"${FEISHU_APP_ID}\"
    feishu_app_secret: \"${FEISHU_APP_SECRET}\"
    feishu_verification_token: \"\"
    feishu_encrypt_key: \"\"
    workspace_dir: \"${WORKSPACE_DIR}\"
    allowed_chats: []
    claude:
      permission_mode: \"acceptEdits\"
      model: \"haiku\"          # 默认使用 haiku（可改为 sonnet/opus）
      # provider: \"bailian\"   # 覆盖默认供应商（可选）；对应 providers 中的 key
      allowed_tools:
        - \"Bash\"
        - \"Read\"
        - \"Edit\"
        - \"Write\"
        - \"Glob\"
        - \"Grep\"
        - \"WebFetch\"
        - \"WebSearch\""

# 在 `server:` 行之前插入新 app 块
python3 - <<PYEOF
import sys

with open('${CONFIG_FILE}', 'r') as f:
    content = f.read()

marker = '\nserver:'
idx = content.find(marker)
if idx == -1:
    # 追加到末尾
    new_content = content.rstrip('\n') + '\n' + '''${NEW_APP_BLOCK}''' + '\n'
else:
    new_content = content[:idx] + '\n' + '''${NEW_APP_BLOCK}''' + content[idx:]

with open('${CONFIG_FILE}', 'w') as f:
    f.write(new_content)

print("config.yaml 已更新")
PYEOF

info "已追加 app '${APP_ID}' 到 config.yaml"

# ── 完成摘要 ──────────────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}================================================${NC}"
echo -e "${GREEN}  初始化完成！${NC}"
echo -e "${BOLD}================================================${NC}"
echo ""
echo "  App ID        : ${APP_ID}"
echo "  Workspace     : ${WORKSPACE_DIR}"
echo "  Config        : ${CONFIG_FILE}"
echo ""
echo -e "${YELLOW}下一步：${NC}"
echo "  1. 在 ${WORKSPACE_DIR}/CLAUDE.md 中自定义助理角色"
echo "  2. 重启服务使新配置生效：./start.sh restart"
echo "  3. 在飞书中向新应用发消息，说「初始化」开始配置"
echo ""
echo -e "${YELLOW}注意：${NC}"
echo "  - config.yaml 备份于 $(basename "$BACKUP_FILE")"
echo "  - 飞书凭证已写入 ${WORKSPACE_DIR}/.claude/skills/feishu_ops/feishu.json（0600）"
echo "  - 服务尚未重启，新 workspace 在下次启动时生效"
