#!/usr/bin/env python3
"""v2.2 M12 · programmatic_remind（偏差预警版）。

不注入"你是谁"的人格描述（会导致角色自恋），
而是观察最近 N 轮角色输出的**偏差**，让 LLM 自行调整。

触发：每 8-12 轮 + jitter（防节律被用户感知）
禁用：不主动型角色（extraversion ≤ 2 且 verbosity ≤ 2）——对阿霖默认关闭

观察指标：
  - 平均句长 vs persona.verbosity 预期
  - 问题密度 vs question_interval 预期
  - 首字符多样性（过于集中视为信号）
"""

from __future__ import annotations

import argparse
import json
import os
import random
import re
import sys
from datetime import datetime
from pathlib import Path


def read_persona_dims(workspace: Path) -> dict:
    """读 persona.md + CLAUDE.md 的 personality_dims。"""
    candidates = [
        workspace / "memory" / "persona.md",
        workspace / "CLAUDE.md",
    ]
    dims = {
        "extraversion": 3, "stability": 3, "empathy": 3,
        "verbosity": 3, "initiative": 3, "openness": 3,
    }
    for p in candidates:
        if not p.exists():
            continue
        text = p.read_text(encoding="utf-8")
        for key in list(dims.keys()):
            m = re.search(rf"^\s*{key}:\s*(\d+)", text, re.MULTILINE)
            if m:
                dims[key] = int(m.group(1))
        break
    return dims


def is_introvert_quiet(dims: dict) -> bool:
    """阿霖型：不主动 + 话少 → 禁用 programmatic_remind。"""
    return dims.get("extraversion", 3) <= 2 and dims.get("verbosity", 3) <= 2


def read_state(state_file: Path) -> dict:
    if not state_file.exists():
        return {"last_remind_turn": 0, "total_turns": 0, "next_trigger_at": 10}
    try:
        return json.loads(state_file.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        return {"last_remind_turn": 0, "total_turns": 0, "next_trigger_at": 10}


def write_state(state_file: Path, state: dict) -> None:
    try:
        state_file.write_text(
            json.dumps(state, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
    except OSError:
        pass


def count_recent_turns(recent_history: Path) -> int:
    if not recent_history.exists():
        return 0
    text = recent_history.read_text(encoding="utf-8")
    return sum(1 for ln in text.splitlines() if ln.startswith("**角色**"))


def extract_last_n_role_messages(recent_history: Path, n: int = 10) -> list[str]:
    if not recent_history.exists():
        return []
    text = recent_history.read_text(encoding="utf-8")
    msgs: list[str] = []
    current: list[str] = []
    collecting = False
    for line in text.splitlines():
        m = re.match(r"\*\*角色\*\*.*?：(.*)", line)
        if m:
            if current:
                msgs.append("\n".join(current))
            current = [m.group(1)]
            collecting = True
        elif line.startswith("**用户**"):
            if current:
                msgs.append("\n".join(current))
                current = []
            collecting = False
        elif collecting:
            current.append(line)
    if current:
        msgs.append("\n".join(current))
    return msgs[-n:]


def verbosity_to_expected_length(verbosity: int) -> tuple[int, int]:
    """verbosity 维度映射到期望句长区间（字数）。"""
    table = {
        1: (3, 10),
        2: (6, 18),
        3: (12, 30),
        4: (20, 50),
        5: (30, 80),
    }
    return table.get(verbosity, (12, 30))


def analyze(msgs: list[str], dims: dict) -> list[str]:
    """返回观察到的偏差列表（字符串描述）。"""
    if len(msgs) < 5:
        return []

    issues: list[str] = []

    # 平均句长 vs verbosity 期望
    lengths = [len(m.strip()) for m in msgs if m.strip()]
    if lengths:
        avg = sum(lengths) / len(lengths)
        lo, hi = verbosity_to_expected_length(dims.get("verbosity", 3))
        if avg > hi * 1.4:
            issues.append(f"平均句长 {avg:.0f} 字，偏长（persona 建议 {lo}-{hi}）")
        elif avg < lo * 0.6:
            issues.append(f"平均句长 {avg:.0f} 字，偏短（persona 建议 {lo}-{hi}）")

    # 问题密度
    q_count = sum(1 for m in msgs if "?" in m or "？" in m)
    q_ratio = q_count / len(msgs)
    if q_ratio > 0.7:
        issues.append(f"问题密度 {int(q_ratio*100)}%（{q_count}/{len(msgs)}），偏高（建议 ≤ 33%）")

    # 首字符多样性
    openers = [m.strip()[:1] for m in msgs if m.strip()]
    unique_openers = len(set(openers))
    if unique_openers <= max(1, len(openers) // 4):
        common = max(set(openers), key=openers.count)
        common_count = openers.count(common)
        if common_count >= 3:
            issues.append(f"首字符集中（「{common}」出现 {common_count}/{len(openers)} 次），建议换起头形状")

    return issues


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--force", action="store_true", help="忽略 interval/禁用，强制触发（调试用）")
    args = parser.parse_args()

    if os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        return
    if os.environ.get("HOOK_PROGRAMMATIC_REMIND_ENABLED", "true").lower() == "false":
        return

    workspace = Path(args.workspace).resolve()
    if not workspace.exists():
        return

    dims = read_persona_dims(workspace)

    # 不主动型角色禁用（阿霖 extraversion=2, verbosity=2）
    if not args.force and is_introvert_quiet(dims):
        return

    # 轮次计数 + interval 判定
    state_file = workspace / "memory" / "_remind_state.json"
    state = read_state(state_file)
    current_turns = count_recent_turns(workspace / "memory" / "RECENT_HISTORY.md")

    if not args.force:
        if current_turns < state["next_trigger_at"]:
            return

    # 分析偏差
    msgs = extract_last_n_role_messages(workspace / "memory" / "RECENT_HISTORY.md", 10)
    issues = analyze(msgs, dims)

    # 更新 state：无论是否有偏差，都重设下次触发点（8-12 轮 + jitter）
    next_interval = random.randint(8, 12)
    state["last_remind_turn"] = current_turns
    state["next_trigger_at"] = current_turns + next_interval
    state["total_turns"] = current_turns
    state["last_check_at"] = datetime.now().astimezone().isoformat(timespec="seconds")
    write_state(state_file, state)

    if not issues:
        return  # 无偏差，静默跳过

    # 输出偏差预警（不描述"你是谁"，只说"最近有什么偏差"）
    print("")
    print("## 偏差预警（v2.2 M12 · 每 8-12 轮一次）")
    print("")
    for issue in issues:
        print(f"- {issue}")
    print("")
    print("> 这不是强制规则，只是观察。本轮请自行调整回 persona 基线。")
    print("")


if __name__ == "__main__":
    main()
