#!/usr/bin/env python3
"""Phase 3a · M7 · Silent CoT 随机小意外注入。

每轮掷骰 prob%，若中则按 mood 挑一个日常小意外注入 checklist。
FORCE_LISTEN / SOFT_CARE 命中时禁用（避免"我爸走了" + "手滑泼水"的错配）。

用法：
    python3 silent_cot.py --workspace <dir> [--mood heavy|elated|stuck|neutral] [--force-listen]
"""

from __future__ import annotations

import argparse
import os
import random
import sys
from pathlib import Path


EVENTS_POOL = {
    "low_intensity": [
        "路灯亮了",
        "茶凉了",
        "窗帘被风吹动了一下",
        "楼道里有人推着什么走过去",
        "看见天色有点变",
        "远处有只狗在叫",
        "笔搁在本子上，滚了半圈停下",
    ],
    "neutral": [
        "刚才手滑把水泼了一点",
        "笔尖断了",
        "收到一条短信没点开",
        "想起一个很久没联系的老朋友",
        "煮了一壶水，烧开了没去关",
        "手机震了一下，没看",
    ],
    "high_energy": [
        "刚下楼扔垃圾碰到熟人",
        "忽然想起一个笑话",
        "听到楼下有孩子在笑",
        "阳台的花开了一朵",
    ],
}

MOOD_TO_POOL = {
    "heavy": ["low_intensity"],
    "elated": ["neutral", "high_energy"],
    "stuck": ["low_intensity"],
    "neutral": ["neutral"],
}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--mood", default=None)
    parser.add_argument("--force-listen", action="store_true")
    parser.add_argument("--prob", type=int, default=20)
    args = parser.parse_args()

    if os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        return
    if os.environ.get("HOOK_SILENT_COT_ENABLED", "true").lower() == "false":
        return

    # FORCE_LISTEN / SOFT_CARE 禁用
    if args.force_listen:
        return

    # 掷骰
    if random.randint(1, 100) > args.prob:
        return

    # 识别 mood
    workspace = Path(args.workspace).resolve()
    mood = args.mood
    if not mood:
        try:
            sys.path.insert(0, str(Path(__file__).parent))
            from mood_classifier import classify_last_user_message  # type: ignore
            mood = classify_last_user_message(workspace / "memory" / "RECENT_HISTORY.md")
        except ImportError:
            mood = "neutral"

    # 从 mood 对应池子里抽
    pool_names = MOOD_TO_POOL.get(mood, ["neutral"])
    candidates: list[str] = []
    for name in pool_names:
        candidates.extend(EVENTS_POOL.get(name, []))
    if not candidates:
        return

    event = random.choice(candidates)

    print("")
    print("## 本轮背景小状况（M7 Silent CoT · 可选用可不用）")
    print("")
    print(f"- 角色此刻刚碰到：**{event}**")
    print("")
    print("> 这是背景色调，不是必须写进台词。")
    print("> 若用，以 1 个短句或意象的形式出现（不展开解释）。")
    print("")


if __name__ == "__main__":
    main()
