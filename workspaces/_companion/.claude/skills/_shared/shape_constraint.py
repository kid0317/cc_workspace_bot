#!/usr/bin/env python3
"""Phase 1a · M1+M2 · 形状约束 + 语气副词注入。

由 reply_checklist.sh 在确定 mode 后调用：
    python3 shape_constraint.py --workspace <dir> --mode <LISTEN|SHARE|OBSERVE|SILENCE>

输出：markdown 片段（追加到 checklist 末尾），包含：
    - 本轮形状约束（段落数 × 起头类型 × 问号策略）
    - 本轮语气副词（调色盘）
    - 本轮反模板提示（mood 敏感）

mood 次级过滤：
    - heavy:   阻止 pure_silence_1_char / off_topic_joke 类 shape
    - elated:  阻止 pure_silence / single_symbol
    - stuck:   阻止 pure_silence
    - neutral: 不过滤
"""

from __future__ import annotations

import argparse
import os
import random
import re
import sys
from pathlib import Path


# 默认 shape 库（写死，防 yaml 加载失败时也能工作）
DEFAULT_OPENER_TYPES = [
    "action",
    "emotion_reflect",
    "direct_speech",
    "ambient_detail",
    "inner_monologue",
    "pure_silence",
]
DEFAULT_PARAGRAPHS = ["1", "1", "2", "2_short", "3_short"]
DEFAULT_QUESTION_POLICY = ["none", "none", "A_level_1", "B_level_1"]

# mode 过滤表
MODE_FILTER = {
    "LISTEN": {
        "opener_type": ["emotion_reflect", "ambient_detail", "pure_silence", "action"],
        "question_policy": ["none", "A_level_1"],
    },
    "SHARE": {
        "opener_type": ["action", "direct_speech", "inner_monologue", "ambient_detail"],
        "question_policy": ["none", "B_level_1"],
    },
    "OBSERVE": {
        "opener_type": ["ambient_detail", "emotion_reflect", "inner_monologue"],
        "question_policy": ["none"],
    },
    "SILENCE": {
        "opener_type": ["pure_silence", "action"],
        "question_policy": ["none"],
        "paragraph_count": ["1"],
    },
}

# mood 次级过滤
MOOD_BLOCK = {
    "heavy": {
        "opener_type": [],  # heavy 下 pure_silence 依然允许（重大披露时沉默是合理的）
        "paragraph_count": ["3_short"],  # 重大时刻不说多
    },
    "elated": {
        "opener_type": ["pure_silence"],  # 开心时不要沉默
    },
    "stuck": {
        "opener_type": ["pure_silence"],
    },
    "neutral": {},
}

OPENER_DESCRIPTIONS = {
    "action": "一个具体动作或物件（不是情绪回应）",
    "emotion_reflect": "先接住用户情绪（一个字或短句）",
    "direct_speech": "直接台词，无铺垫",
    "ambient_detail": "环境细节（声音/光/物件），不描述人",
    "inner_monologue": "内心独白（角色此刻在想什么）",
    "pure_silence": "纯沉默：一个字或一个符号（嗯。 / ……）",
}

PARAGRAPH_DESCRIPTIONS = {
    "1": "1 段（紧凑）",
    "2": "2 段（正常）",
    "2_short": "2 段短句",
    "3_short": "3 段都很短",
}

QUESTION_DESCRIPTIONS = {
    "none": "不含问号",
    "A_level_1": "最多 1 个 A 级问题（引用用户具体内容真诚好奇）",
    "B_level_1": "最多 1 个 B 级问题（有具体方向的追问）",
}


# 语气副词池（按 persona 维度粗分）
ADVERB_POOLS = {
    "stable_introvert": [
        "克制地", "静静地", "平静地", "认真想了想", "没立刻接话地",
        "略停顿了一下", "淡淡地", "慢慢地说",
    ],
    "unstable_introvert": [
        "心不在焉地", "有点走神地", "散漫地", "略微困惑地", "带一点疲倦地",
    ],
    "extrovert": [
        "快速地", "兴致勃勃地", "有点意外地", "轻轻笑了一下", "带着好奇地",
    ],
    "warm_neutral": [
        "温和地", "好奇地", "轻轻地", "带点笑意地", "不急不慢地",
    ],
}


def read_persona_dims(workspace: Path) -> dict:
    """读 persona.md 或 CLAUDE.md 里的 personality_dims。"""
    candidates = [
        workspace / "memory" / "persona.md",
        workspace / "CLAUDE.md",
    ]
    dims = {
        "extraversion": 3,
        "stability": 3,
        "empathy": 3,
        "verbosity": 3,
        "initiative": 3,
        "openness": 3,
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


def select_adverb_pool(dims: dict) -> str:
    """按维度选池子。"""
    ex = dims.get("extraversion", 3)
    st = dims.get("stability", 3)
    if ex <= 2 and st >= 4:
        return "stable_introvert"
    if ex <= 2 and st <= 3:
        return "unstable_introvert"
    if ex >= 4:
        return "extrovert"
    return "warm_neutral"


def classify_mood(workspace: Path) -> str:
    """从 RECENT_HISTORY.md 识别 mood。"""
    try:
        sys.path.insert(0, str(Path(__file__).parent))
        from mood_classifier import classify_last_user_message  # type: ignore
    except ImportError:
        return "neutral"
    return classify_last_user_message(workspace / "memory" / "RECENT_HISTORY.md")


def filter_candidates(base: list[str], mode_allow: list[str] | None, mood_block: list[str]) -> list[str]:
    result = base
    if mode_allow:
        result = [x for x in result if x in mode_allow]
    if mood_block:
        result = [x for x in result if x not in mood_block]
    return result or base  # 过滤后为空时 fallback 回原池


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--mode", default="LISTEN", choices=["LISTEN", "SHARE", "OBSERVE", "SILENCE"])
    args = parser.parse_args()

    # 紧急停用
    if os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        return
    if os.environ.get("HOOK_SHAPE_CONSTRAINT_ENABLED", "true").lower() == "false":
        return

    workspace = Path(args.workspace).resolve()
    if not workspace.exists():
        return

    dims = read_persona_dims(workspace)
    adverb_pool_name = select_adverb_pool(dims)
    mood = classify_mood(workspace)

    # mode × mood 过滤
    mode_filter = MODE_FILTER.get(args.mode, {})
    mood_filter = MOOD_BLOCK.get(mood, {})

    openers = filter_candidates(
        DEFAULT_OPENER_TYPES,
        mode_filter.get("opener_type"),
        mood_filter.get("opener_type", []),
    )
    paragraphs = filter_candidates(
        DEFAULT_PARAGRAPHS,
        mode_filter.get("paragraph_count"),
        mood_filter.get("paragraph_count", []),
    )
    questions = filter_candidates(
        DEFAULT_QUESTION_POLICY,
        mode_filter.get("question_policy"),
        mood_filter.get("question_policy", []),
    )

    # 抽取
    chosen_opener = random.choice(openers)
    chosen_paragraph = random.choice(paragraphs)
    chosen_question = random.choice(questions)
    chosen_adverb = random.choice(ADVERB_POOLS[adverb_pool_name])

    # 输出 markdown
    print("")
    print("## 本轮形状约束（v2.2 · 每轮独立随机）")
    print("")
    print(f"- **段落数**：{PARAGRAPH_DESCRIPTIONS.get(chosen_paragraph, chosen_paragraph)}")
    print(f"- **起头类型**：{OPENER_DESCRIPTIONS.get(chosen_opener, chosen_opener)}")
    print(f"- **问号策略**：{QUESTION_DESCRIPTIONS.get(chosen_question, chosen_question)}")
    print(f"- **语气调色**：{chosen_adverb}（作为整体语气基调，不必写进台词）")
    print(f"- **当前 mood 识别**：{mood}")
    print("")
    print("> 这是形状约束，不是内容要求。内容你自己写。")
    print("> 不要把上面的词作为台词复刻——它们是形状提示，不是台词。")
    print("")


if __name__ == "__main__":
    main()
