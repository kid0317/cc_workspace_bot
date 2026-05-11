#!/usr/bin/env python3
"""Phase 3a · M8 · 反例注入。

从最近 3 轮角色消息中，选"得分最高"的 A+P+Q 样本作为反例注入。
得分 = 首字符"嗯/对/这个" + 含"（停顿）"旁白 + 问号结尾，三项满分 3。
仅当最高分 ≥ 2 时注入（避免从正常输出造反例）。

用法：
    python3 anti_example.py --workspace <dir>
"""

from __future__ import annotations

import argparse
import os
import re
from pathlib import Path


OPENER_RE = re.compile(r"^[嗯对这那]")
PAUSE_RE = re.compile(r"（[^）]*停顿[^）]*）")
QUESTION_SUFFIX = ("?", "？")


def extract_last_n_role_messages(recent_history: Path, n: int = 3) -> list[str]:
    if not recent_history.exists():
        return []
    text = recent_history.read_text(encoding="utf-8")
    lines = text.splitlines()
    role_msgs: list[str] = []
    current_msg: list[str] = []
    collecting = False
    for line in lines:
        # "**角色**（...）：content" 开始
        m = re.match(r"\*\*角色\*\*.*?：(.*)", line)
        if m:
            if current_msg:
                role_msgs.append("\n".join(current_msg))
            current_msg = [m.group(1)]
            collecting = True
        elif line.startswith("**用户**"):
            if current_msg:
                role_msgs.append("\n".join(current_msg))
                current_msg = []
            collecting = False
        elif collecting:
            current_msg.append(line)
    if current_msg:
        role_msgs.append("\n".join(current_msg))
    return role_msgs[-n:]


def score_message(text: str) -> int:
    """0-3 分，分越高越像 A+P+Q 模板。"""
    s = 0
    stripped = text.strip()
    if OPENER_RE.search(stripped):
        s += 1
    if PAUSE_RE.search(stripped):
        s += 1
    if stripped.endswith(QUESTION_SUFFIX):
        s += 1
    return s


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", required=True)
    args = parser.parse_args()

    if os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        return
    if os.environ.get("HOOK_ANTI_EXAMPLE_ENABLED", "true").lower() == "false":
        return

    workspace = Path(args.workspace).resolve()
    recent = workspace / "memory" / "RECENT_HISTORY.md"
    msgs = extract_last_n_role_messages(recent, 3)
    if not msgs:
        return

    scored = [(score_message(m), m) for m in msgs]
    scored.sort(reverse=True, key=lambda x: x[0])
    top_score, top_msg = scored[0]

    if top_score < 2:
        return  # 不像模板，不注入反例

    # 截短显示
    snippet = top_msg.strip().replace("\n", " ")[:120]

    print("")
    print("## 本轮反例（M8 · 不要这样写）")
    print("")
    print(f"上轮角色输出（得分 {top_score}/3）：")
    print(f"> {snippet}")
    print("")
    print("这是 A+P+Q 模板（认同 + 停顿 + 追问）。本轮必须换一个骨架：")
    print("- 不以「嗯/对/这个/那」开头")
    print("- 不含「（停顿）」")
    print("- 不以问号结尾")
    print("")


if __name__ == "__main__":
    main()
