#!/usr/bin/env python3
"""v2.2 · 动态 opener blacklist 计算。

扫描 RECENT_HISTORY.md 最近 3 条角色消息：
若某 opener（来自 slop_blacklist.yaml.opener_blacklist_trigger）在 ≥ 2/3 中出现，
本轮禁用该 opener。

输出：空行 或 一行"本轮禁止以下 opener 开头：{char1} {char2}"
"""

from __future__ import annotations

import argparse
import os
import re
from pathlib import Path


DEFAULT_OPENER_TRIGGERS = ["嗯", "对", "这个", "那", "哈", "好", "啊"]


def load_opener_list(workspace: Path) -> list[str]:
    p = workspace / "memory" / "slop_blacklist.yaml"
    if not p.exists():
        return DEFAULT_OPENER_TRIGGERS

    text = p.read_text(encoding="utf-8")
    # 提取 opener_blacklist_trigger 下的 pattern (如 "^嗯"、"^对"...)
    found: list[str] = []
    in_block = False
    for line in text.splitlines():
        s = line.strip()
        if s.startswith("opener_blacklist_trigger"):
            in_block = True
            continue
        if in_block:
            if s.startswith("- pattern:"):
                m = re.search(r'pattern:\s*"?\^([^"\s]+)', s)
                if m:
                    found.append(m.group(1))
            elif s and not s.startswith("-") and not s.startswith("trigger_threshold") and not s.startswith("note") and ":" in s:
                # 遇到下一个顶层键 → 结束
                break
    return found or DEFAULT_OPENER_TRIGGERS


def load_voice_whitelist_openers(workspace: Path) -> list[str]:
    p = workspace / "memory" / "voice_whitelist.yaml"
    if not p.exists():
        return []
    found: list[str] = []
    in_openers = False
    for line in p.read_text(encoding="utf-8").splitlines():
        s = line.strip()
        if s.startswith("openers:"):
            in_openers = True
            continue
        if in_openers:
            m = re.search(r'char:\s*"([^"]+)"', s)
            if m:
                found.append(m.group(1))
            elif s.startswith("phrases:") or s.startswith("user_confirmed") or s.startswith("last_reviewed"):
                break
    return found


def extract_recent_role_messages(recent_history: Path, n: int = 3) -> list[str]:
    if not recent_history.exists():
        return []
    text = recent_history.read_text(encoding="utf-8")
    lines = text.splitlines()
    msgs: list[str] = []
    current: list[str] = []
    collecting = False
    for line in lines:
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


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", required=True)
    args = parser.parse_args()

    if os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        return
    if os.environ.get("HOOK_OPENER_BLACKLIST_ENABLED", "true").lower() == "false":
        return

    workspace = Path(args.workspace).resolve()
    triggers = load_opener_list(workspace)
    msgs = extract_recent_role_messages(workspace / "memory" / "RECENT_HISTORY.md", 3)

    if len(msgs) < 2:
        return  # 样本不够

    # 统计每个 opener 在最近 3 条中的出现次数
    banned: list[str] = []
    for opener in triggers:
        count = 0
        for msg in msgs:
            stripped = msg.strip()
            # 跳过旁白前缀
            clean = re.sub(r"^（[^）]*）\s*", "", stripped)
            if clean.startswith(opener):
                count += 1
        if count >= 2:  # 2-in-3 触发
            banned.append(opener)

    if not banned:
        return

    # 输出硬约束
    print("")
    print("## 动态 opener 禁用（v2.2 · 2-in-3 触发）")
    print("")
    print(f"- 最近 3 轮里以下 opener 出现 ≥2 次，本轮**不得以其开头**：")
    for o in banned:
        print(f"  - 禁止：`{o}` 开头")
    print("")
    print("> 即使白名单保护了某个声音 opener，重复使用 2-in-3 仍需规避。")
    print("> 改用其他形状起头（画面/动作/直接台词）。")
    print("")


if __name__ == "__main__":
    main()
