#!/usr/bin/env python3
"""write_turn_snapshot.py — UserPromptSubmit hook：轮开始状态快照。

把本轮开始时的 initialization_status 写入 {session_dir}/.init_status_at_turn_start。
Stop hook（output_filter.py）门控优先读该快照——若初始化在轮内完成切换
（phase2_done → done），本轮仍按轮开始状态豁免，主动唤醒询问等收尾话术
零误杀风险（设计 §4.2②c，产品-C2）。

纪律：
- stdout 必须零输出（UserPromptSubmit 的 stdout 会被注入主 agent 上下文）
- 任何异常 exit 0，不阻塞对话
"""

import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from output_filter import SNAPSHOT_FILE, find_workspace, parse_status  # noqa: E402


def main() -> int:
    try:
        data = json.loads(sys.stdin.read() or "{}")
        if not isinstance(data, dict):
            return 0
        session_dir = Path(data.get("cwd") or os.getcwd())
        workspace = find_workspace(session_dir)
        mem = workspace / "memory" / "MEMORY.md"
        if not mem.exists():
            return 0
        status = parse_status(mem.read_text(encoding="utf-8"))
        if status:
            (session_dir / SNAPSHOT_FILE).write_text(status, encoding="utf-8")
    except Exception:  # noqa: BLE001 — 故意吞掉一切
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
