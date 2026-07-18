#!/usr/bin/env bash
# install_to_workspace.sh — 把陪伴输出过滤器安装到一个 companion workspace。
#
# 用法：
#   bash install_to_workspace.sh ./workspaces/companion-demo   # 安装到实例
#   bash install_to_workspace.sh --template                    # 安装到 _companion 模板
#                                                              # （settings 用 __WORKSPACE_DIR__ 占位符）
#
# 动作（全部幂等，可重复执行）：
#   1. 复制 output_filter.py / filter_rules.py / filter_prompt.md /
#      write_turn_snapshot.py 到 <ws>/.claude/hooks/
#   2. settings.local.json 注册 Stop hook + UserPromptSubmit 快照 hook
#   3. send_and_record.py 插入 proactive Layer 1 过滤（存在时；带幂等标记）
#   4. memory/fallback_replies.yaml 不存在则写入默认兜底池（应按 persona 调整）
#
# 仅作用于 companion workspace；任务型 workspace 不要运行本脚本。
# 回滚：bash rollback_filter.sh <ws>（摘除 hook 注册，下一轮对话即生效）

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SRC_DIR")"

TEMPLATE_MODE=0
if [[ "${1:-}" == "--template" ]]; then
  TEMPLATE_MODE=1
  WS="$REPO_ROOT/workspaces/_companion"
  CMD_PREFIX="__WORKSPACE_DIR__"
else
  WS="${1:?用法: install_to_workspace.sh <workspace_dir> | --template}"
  WS="$(cd "$WS" && pwd)"
  CMD_PREFIX="$WS"
fi

HOOKS_DIR="$WS/.claude/hooks"
SETTINGS="$WS/.claude/settings.local.json"

echo "==> 安装输出过滤器到: $WS"

# ── 1. 复制 hook 文件 ─────────────────────────────────────────────────────────
mkdir -p "$HOOKS_DIR"
for f in output_filter.py filter_rules.py filter_prompt.md write_turn_snapshot.py; do
  cp "$SRC_DIR/$f" "$HOOKS_DIR/$f"
done
echo "    hooks 文件已同步 (4 个)"

# ── 2. settings.local.json 注册 ──────────────────────────────────────────────
python3 - "$SETTINGS" "$CMD_PREFIX" << 'PYEOF'
import json, sys
from pathlib import Path

settings_path = Path(sys.argv[1])
prefix = sys.argv[2]

cfg = {}
if settings_path.exists():
    cfg = json.loads(settings_path.read_text(encoding="utf-8"))

hooks = cfg.setdefault("hooks", {})

# Stop hook（输出过滤器）
stop_cmd = f"python3 {prefix}/.claude/hooks/output_filter.py"
stop = hooks.setdefault("Stop", [])
if not any(stop_cmd in json.dumps(g) for g in stop):
    stop.append({"hooks": [{"type": "command", "command": stop_cmd, "timeout": 40}]})

# UserPromptSubmit 快照 hook
snap_cmd = f"python3 {prefix}/.claude/hooks/write_turn_snapshot.py"
ups = hooks.setdefault("UserPromptSubmit", [])
if not any(snap_cmd in json.dumps(g) for g in ups):
    if ups and "hooks" in ups[0]:
        ups[0]["hooks"].append({"type": "command", "command": snap_cmd})
    else:
        ups.append({"hooks": [{"type": "command", "command": snap_cmd}]})

settings_path.parent.mkdir(parents=True, exist_ok=True)
settings_path.write_text(json.dumps(cfg, ensure_ascii=False, indent=2) + "\n",
                         encoding="utf-8")
print("    settings.local.json 已注册 Stop + 快照 hook")
PYEOF

# ── 3. proactive 链路 Layer 1（send_and_record.py） ──────────────────────────
SAR="$WS/.claude/skills/feishu_ops/scripts/send_and_record.py"
if [[ -f "$SAR" ]]; then
  python3 - "$SAR" << 'PYEOF'
import sys
from pathlib import Path

p = Path(sys.argv[1])
src = p.read_text(encoding="utf-8")
MARKER = "companion output filter layer1"
if MARKER in src:
    print("    send_and_record.py 已接入过滤（跳过）")
    sys.exit(0)

PATCH = '''
    # --- companion output filter layer1（proactive 链路，设计 §4.7）---
    try:
        _hooks_dir = Path(__file__).resolve().parents[3] / "hooks"
        sys.path.insert(0, str(_hooks_dir))
        import filter_rules as _fr
        _filtered, _hits = _fr.layer1_filter(args.text)
        if _filtered.strip():
            args.text = _filtered
    except Exception:
        pass  # fail-open：过滤不可用时按原文发送
    # --- end companion output filter layer1 ---
'''

anchor = "    # 1. 发送飞书消息"
if anchor not in src:
    print("    ⚠️ send_and_record.py 结构与预期不符，未自动接入（需手工处理）")
    sys.exit(0)
src = src.replace(anchor, PATCH + "\n" + anchor, 1)
p.write_text(src, encoding="utf-8")
print("    send_and_record.py 已接入 proactive Layer 1")
PYEOF
else
  echo "    send_and_record.py 不存在（模板模式正常），跳过 proactive 接入"
fi

# ── 4. 兜底回应池 ─────────────────────────────────────────────────────────────
FALLBACK="$WS/memory/fallback_replies.yaml"
if [[ ! -f "$FALLBACK" && $TEMPLATE_MODE -eq 0 ]]; then
  mkdir -p "$WS/memory"
  cat > "$FALLBACK" << 'EOF'
# persona 最小回应池：过滤后整段为空时的兜底（绝不系统性失声）。
# 请按角色声音调整，保持极短、中性、在角色内。
- 嗯。
- 在。
EOF
  echo "    fallback_replies.yaml 已创建（默认池，建议按 persona 调整）"
fi

echo "==> 完成。验证: python3 -m pytest $REPO_ROOT/tests/output_filter/ -q"
echo "    回滚: bash $SRC_DIR/rollback_filter.sh $WS"
