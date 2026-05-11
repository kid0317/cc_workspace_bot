#!/usr/bin/env python3
"""共享的情绪分类器（M2/M3/M7 共用）。

从用户最近一条消息识别 mood 类别：heavy / elated / stuck / neutral。
"""

from __future__ import annotations

import re
from pathlib import Path


MOOD_KEYWORDS = {
    "heavy": [
        "去世", "走了", "过世", "没了", "不在了",
        "一年了", "两年了", "一年多", "两年多", "一年前", "半年前", "一直没",
        "病了", "住院", "确诊", "化疗", "手术",
        "崩", "哭", "委屈", "绝望", "想死", "撑不住", "压力好大",
        "累死", "好累", "难过", "痛苦", "心酸", "心疼",
    ],
    "elated": [
        "太好了", "终于", "爽", "开心", "厉害", "成了", "爱了", "绝了",
        "太棒", "太美", "太帅", "中了", "过了",
    ],
    "stuck": [
        "不知道", "卡住", "没思路", "烦", "想不明白", "拖着", "犹豫",
        "没头绪", "迷茫", "方向不对", "没动", "没办",
    ],
}

# 否定前词窗口（防"笑崩了"/"甜到受不了"误伤 heavy）
NEGATE_PREFIX_CHARS = set("笑甜美好乐爱帅哈太真")
NEGATE_WINDOW = 3  # 前 3 字窗口


def _is_negated(text: str, idx: int) -> bool:
    """词位置 idx 前 NEGATE_WINDOW 字内是否有否定前词。"""
    start = max(0, idx - NEGATE_WINDOW)
    prefix = text[start:idx]
    return any(c in NEGATE_PREFIX_CHARS for c in prefix)


def classify(text: str) -> str:
    """返回 heavy / elated / stuck / neutral。"""
    if not text:
        return "neutral"

    scores = {"heavy": 0, "elated": 0, "stuck": 0}

    for mood, keywords in MOOD_KEYWORDS.items():
        for kw in keywords:
            idx = text.find(kw)
            while idx >= 0:
                # heavy 需要过否定前词检测
                if mood == "heavy" and _is_negated(text, idx):
                    pass
                else:
                    scores[mood] += 1
                idx = text.find(kw, idx + 1)

    # 最高分决定
    max_mood = max(scores, key=lambda k: scores[k])
    if scores[max_mood] == 0:
        return "neutral"
    return max_mood


def classify_last_user_message(recent_history_path: Path) -> str:
    """从 RECENT_HISTORY.md 提取最后一条用户消息并分类。"""
    try:
        text = recent_history_path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return "neutral"

    # 格式示例：**用户**（2026-04-20 22:20）：我爸
    lines = text.splitlines()
    for line in reversed(lines):
        m = re.match(r"\*\*用户\*\*.*?：(.+)", line)
        if m:
            content = m.group(1).strip()
            return classify(content)

    return "neutral"


if __name__ == "__main__":
    import sys
    import os

    # v2.2 紧急停用开关
    if os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        print("neutral")
        sys.exit(0)

    if len(sys.argv) > 1:
        # 直接传文本或 RECENT_HISTORY.md 路径
        arg = sys.argv[1]
        p = Path(arg)
        if p.exists():
            print(classify_last_user_message(p))
        else:
            print(classify(arg))
    else:
        print(classify(sys.stdin.read()))
