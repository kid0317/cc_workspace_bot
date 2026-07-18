#!/usr/bin/env bash
# rollback_filter.sh — 摘除输出过滤器的 hook 注册（< 5 分钟回滚预案）。
#
# 用法: bash rollback_filter.sh <workspace_dir>
#
# 只摘 settings.local.json 中的注册条目，hook 文件保留（无注册即无行为）；
# 框架侧 worker.go 的读取代码无文件即无行为，无需重启框架进程。
# 下一轮对话即生效。回滚后建议发一条测试消息验证原链路。

set -euo pipefail

WS="${1:?用法: rollback_filter.sh <workspace_dir>}"
SETTINGS="$WS/.claude/settings.local.json"

[[ -f "$SETTINGS" ]] || { echo "settings 不存在: $SETTINGS"; exit 1; }

python3 - "$SETTINGS" << 'PYEOF'
import json, sys
from pathlib import Path

p = Path(sys.argv[1])
cfg = json.loads(p.read_text(encoding="utf-8"))
hooks = cfg.get("hooks", {})
removed = 0

# 摘除 Stop 中的 output_filter 条目
if "Stop" in hooks:
    before = len(hooks["Stop"])
    hooks["Stop"] = [g for g in hooks["Stop"]
                     if "output_filter.py" not in json.dumps(g)]
    removed += before - len(hooks["Stop"])
    if not hooks["Stop"]:
        del hooks["Stop"]

# 摘除 UserPromptSubmit 中的快照 hook 命令
for group in hooks.get("UserPromptSubmit", []):
    cmds = group.get("hooks", [])
    before = len(cmds)
    group["hooks"] = [h for h in cmds
                      if "write_turn_snapshot.py" not in h.get("command", "")]
    removed += before - len(group["hooks"])

p.write_text(json.dumps(cfg, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
print(f"已摘除 {removed} 个 hook 注册条目。下一轮对话即生效。")
PYEOF
