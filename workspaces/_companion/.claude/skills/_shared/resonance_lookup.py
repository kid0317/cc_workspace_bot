#!/usr/bin/env python3
"""Phase 2 · M4 · 共振查找 hook。

用户本轮消息 → 关键词/情绪匹配 → 从 life_log / long_arc 查匹配素材 → 注入为"背景辐射"。

用法：
    python3 resonance_lookup.py --workspace <dir>

运行时约 <10ms。对无 tag 的历史条目降级为关键词匹配。
"""

from __future__ import annotations

import argparse
import os
import re
import sys
from pathlib import Path


# 用户消息 → 关键词（简单名词/动词提取）
KEYWORD_STOPWORDS = {
    "的", "了", "吗", "是", "在", "和", "我", "你", "也", "就", "都", "这", "那",
    "怎么", "什么", "没", "不", "有", "但", "可以", "会", "要", "过", "一", "上", "下",
}


def extract_keywords(text: str) -> list[str]:
    """粗提关键词：所有 2-4 字汉字子串（含重叠），去停用词，取前 N。"""
    if not text:
        return []
    # 找所有连续汉字段
    segments = re.findall(r"[\u4e00-\u9fa5]+", text)
    tokens: list[str] = []
    for seg in segments:
        # 2-3 字滑窗（重叠）
        for ngram_len in (2, 3):
            for i in range(len(seg) - ngram_len + 1):
                t = seg[i:i + ngram_len]
                if t not in KEYWORD_STOPWORDS and len(t) >= 2:
                    tokens.append(t)
    # 去重保序
    seen: set[str] = set()
    uniq: list[str] = []
    for t in tokens:
        if t not in seen:
            seen.add(t)
            uniq.append(t)
    return uniq[:15]


def read_last_user_message(recent_history: Path) -> str:
    if not recent_history.exists():
        return ""
    text = recent_history.read_text(encoding="utf-8")
    for line in reversed(text.splitlines()):
        m = re.match(r"\*\*用户\*\*.*?：(.+)", line)
        if m:
            return m.group(1).strip()
    return ""


def parse_life_log(life_log_path: Path) -> list[dict]:
    """解析 life_log.md 条目。返回 [{id, content, tags, intimacy_level}]"""
    if not life_log_path.exists():
        return []
    text = life_log_path.read_text(encoding="utf-8")
    entries: list[dict] = []

    # 匹配 ### [Lxxx] xxx · xxx ... \n<metadata>\n<content>\n\n###
    pattern = re.compile(
        r"### \[L(\d+)\] ([^\n]+)\n((?:<!--[^>]+-->\n)*)([^\n#]*(?:\n(?!### )[^\n]*)*)",
        re.MULTILINE,
    )
    for m in pattern.finditer(text):
        lid, header, meta_block, body = m.group(1), m.group(2), m.group(3), m.group(4)

        tags: list[str] = []
        intimacy = 2  # 默认
        for meta in re.finditer(r"<!--\s*(\w+):\s*([^>]+?)\s*-->", meta_block):
            key, val = meta.group(1), meta.group(2).strip()
            if key == "tags":
                tags = [t.strip() for t in val.split(",")]
            elif key == "intimacy_level":
                try:
                    intimacy = int(val)
                except ValueError:
                    pass

        entries.append({
            "id": f"L{lid}",
            "header": header.strip(),
            "tags": tags,
            "intimacy_level": intimacy,
            "content": body.strip()[:200],
        })

    return entries


def parse_long_arc(long_arc_path: Path) -> list[dict]:
    if not long_arc_path.exists():
        return []
    text = long_arc_path.read_text(encoding="utf-8")
    entries: list[dict] = []
    # 匹配 ## [ARC001] active ... \n\n**主题**：xxx
    pattern = re.compile(
        r"## \[ARC(\d+)\] (\w+).*?\n+\*\*主题\*\*：([^\n]+)(.*?)(?=\n##|\Z)",
        re.DOTALL,
    )
    for m in pattern.finditer(text):
        aid, status, theme, rest = m.group(1), m.group(2), m.group(3), m.group(4)
        if status not in ("active", "dormant"):
            continue
        tags_m = re.search(r"\*\*标签\*\*[:：]\s*([^\n]+)", rest)
        tags = [t.strip() for t in tags_m.group(1).split(",")] if tags_m else []
        triggers_m = re.search(r"\*\*触发场景\*\*.*?\n((?:-[^\n]+\n?)+)", rest)
        trigger_text = triggers_m.group(1) if triggers_m else ""
        entries.append({
            "id": f"ARC{aid}",
            "status": status,
            "theme": theme.strip(),
            "tags": tags,
            "trigger_text": trigger_text,
        })
    return entries


def score_life_log_entry(entry: dict, user_keywords: list[str], mood: str) -> float:
    """打分：越高越匹配。"""
    score = 0.0
    # tag 命中
    for kw in user_keywords:
        for tag in entry["tags"]:
            if kw in tag or tag in kw:
                score += 2.0
    # content 关键词命中
    content_lower = entry["content"].lower()
    for kw in user_keywords:
        if kw in content_lower:
            score += 1.0
    # mood 匹配（life_log 有 emotion tag 时）
    for tag in entry["tags"]:
        if tag == mood or (mood == "heavy" and tag in ["weariness", "loss", "heavy"]):
            score += 1.5
    return score


MOOD_TO_ENGLISH_TAGS = {
    "heavy": {"loss", "weariness", "heavy", "grief"},
    "stuck": {"stuck", "frustration", "procrastination"},
    "elated": {"joy", "celebration", "clarity"},
    "neutral": set(),
}


def score_long_arc_entry(entry: dict, user_keywords: list[str], mood: str = "neutral") -> float:
    score = 0.0
    for kw in user_keywords:
        if kw in entry["theme"]:
            score += 3.0
        if kw in entry["trigger_text"]:
            score += 2.0
        for tag in entry["tags"]:
            if kw in tag or tag in kw:
                score += 1.5
    # mood → 英文 tag 映射
    mood_tags = MOOD_TO_ENGLISH_TAGS.get(mood, set())
    for tag in entry["tags"]:
        if tag in mood_tags:
            score += 2.0
    return score


def get_relationship_tier(workspace: Path) -> str:
    """从 user_profile / RECENT_HISTORY 粗估关系热度。"""
    recent = workspace / "memory" / "RECENT_HISTORY.md"
    if not recent.exists():
        return "cold"
    # 粗估：RECENT_HISTORY 行数
    count = sum(1 for _ in recent.read_text(encoding="utf-8").splitlines() if _.strip().startswith("**"))
    if count >= 500:
        return "intimate"
    if count >= 200:
        return "familiar"
    if count >= 50:
        return "warming"
    return "cold"


def intimacy_allowed(entry_intimacy: int, tier: str) -> bool:
    """intimacy_level 分级过滤（M5 Fog of War）。"""
    thresholds = {
        "cold": 2,       # 允许 1-2
        "warming": 3,    # 允许 1-3
        "familiar": 4,   # 允许 1-4
        "intimate": 5,   # 全部
    }
    return entry_intimacy <= thresholds.get(tier, 2)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--top-k", type=int, default=3)
    args = parser.parse_args()

    # 紧急停用
    if os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        return
    if os.environ.get("HOOK_RESONANCE_LOOKUP_ENABLED", "true").lower() == "false":
        return

    workspace = Path(args.workspace).resolve()
    if not workspace.exists():
        return

    # 读用户最后一条消息
    recent = workspace / "memory" / "RECENT_HISTORY.md"
    user_msg = read_last_user_message(recent)
    if not user_msg:
        return

    user_keywords = extract_keywords(user_msg)
    if not user_keywords:
        return

    # mood
    try:
        sys.path.insert(0, str(Path(__file__).parent))
        from mood_classifier import classify  # type: ignore
        mood = classify(user_msg)
    except ImportError:
        mood = "neutral"

    # 关系热度
    tier = get_relationship_tier(workspace)

    # 检索
    life_log = parse_life_log(workspace / "memory" / "life_log.md")
    long_arc = parse_long_arc(workspace / "memory" / "long_arc.md")

    scored_logs = []
    for entry in life_log:
        if not intimacy_allowed(entry["intimacy_level"], tier):
            continue
        s = score_life_log_entry(entry, user_keywords, mood)
        if s > 0:
            scored_logs.append((s, entry))

    scored_arcs = []
    for entry in long_arc:
        s = score_long_arc_entry(entry, user_keywords, mood)
        if s > 0:
            scored_arcs.append((s, entry))

    scored_logs.sort(reverse=True, key=lambda x: x[0])
    scored_arcs.sort(reverse=True, key=lambda x: x[0])

    top_logs = scored_logs[:args.top_k]
    top_arcs = scored_arcs[:1]

    if not top_logs and not top_arcs:
        return

    print("")
    print("## 本轮共振素材（v2.2 M4 · 背景辐射，非必用）")
    print("")

    if top_arcs:
        print("### 持续心事（long_arc）")
        for score, arc in top_arcs:
            print(f"- **[{arc['id']}]** {arc['theme']}（匹配度 {score:.1f}）")
        print("")

    if top_logs:
        print("### 最近画面（life_log · 按亲密度过滤 tier={}）".format(tier))
        for score, log in top_logs:
            body_snippet = log["content"][:80].replace("\n", " ")
            print(f"- **[{log['id']}]** {log['header']}")
            print(f"    {body_snippet}…")
        print("")

    print("> 这是此刻你脑子里可能闪过的画面/心事。")
    print("> 如果用上：不要整段复述，只作为短句/意象出现。")
    print("> 不用也可以。背景辐射，不是剧本线。")
    print("")


if __name__ == "__main__":
    main()
