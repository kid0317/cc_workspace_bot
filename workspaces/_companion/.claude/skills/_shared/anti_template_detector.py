#!/usr/bin/env python3
"""Phase 3a · M6 · 反模板检测（Stop hook · 仅 log）。

三信号判定：
  1. 末尾字符：最近 3 轮角色消息 ≥2 条以 `?/？` 结尾
  2. 旁白密度：≥2 条含「（\\S*?）」旁白
  3. n-gram 相似度：首 10 字 cosine > 0.7（简化为 n-gram Jaccard）

三选二命中 → 追加日志到 memory/_detector_log.jsonl（Phase 3a 仅 log）。
Phase 3b 再决定是否阻断。

运行时考虑 voice_whitelist（M14）——白名单命中的 opener / narration 不计入模板得分。
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from datetime import datetime
from pathlib import Path


def load_voice_whitelist(workspace: Path) -> dict:
    p = workspace / "memory" / "voice_whitelist.yaml"
    if not p.exists():
        return {"phrases": [], "openers": []}
    # 简化 YAML 读取（不引入依赖）
    text = p.read_text(encoding="utf-8")
    phrases: list[str] = []
    openers: list[str] = []
    section = ""
    for line in text.splitlines():
        s = line.strip()
        if s.startswith("phrases:"):
            section = "phrases"
            continue
        if s.startswith("openers:"):
            section = "openers"
            continue
        if s.startswith("user_confirmed") or s.startswith("last_reviewed") or s.startswith("voice_whitelist"):
            continue
        m = re.search(r'text:\s*"([^"]+)"', s)
        if m and section == "phrases":
            phrases.append(m.group(1))
            continue
        m = re.search(r'char:\s*"([^"]+)"', s)
        if m and section == "openers":
            openers.append(m.group(1))
    return {"phrases": phrases, "openers": openers}


def extract_recent_role_messages(recent_history: Path, n: int = 3) -> list[str]:
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


def signal_ends_with_question(msgs: list[str]) -> int:
    """返回以问号结尾的数量。"""
    return sum(1 for m in msgs if m.strip().endswith(("?", "？")))


def signal_has_narration(msgs: list[str], phrase_whitelist: list[str]) -> int:
    """返回含非白名单旁白的数量。"""
    count = 0
    for m in msgs:
        narration_matches = re.findall(r"（[^（）]{1,20}）", m)
        for narr in narration_matches:
            if narr not in phrase_whitelist:
                count += 1
                break  # 每条消息只算一次
    return count


def signal_ngram_similarity(msgs: list[str], n: int = 3, threshold: float = 0.5) -> int:
    """返回首 10 字符 n-gram Jaccard 相似度超阈值的对数。"""
    if len(msgs) < 2:
        return 0

    def ngrams(s: str, n: int) -> set[str]:
        s = s.strip()[:15]
        return {s[i:i + n] for i in range(len(s) - n + 1)}

    sets = [ngrams(m, n) for m in msgs]
    pairs_matching = 0
    for i in range(len(sets)):
        for j in range(i + 1, len(sets)):
            if not sets[i] or not sets[j]:
                continue
            inter = sets[i] & sets[j]
            union = sets[i] | sets[j]
            if union and len(inter) / len(union) >= threshold:
                pairs_matching += 1
    return pairs_matching


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", required=True)
    args = parser.parse_args()

    if os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        return
    if os.environ.get("HOOK_ANTI_TEMPLATE_DETECTOR_ENABLED", "true").lower() == "false":
        return

    workspace = Path(args.workspace).resolve()
    msgs = extract_recent_role_messages(workspace / "memory" / "RECENT_HISTORY.md", 3)
    if len(msgs) < 2:
        return  # 样本不够

    whitelist = load_voice_whitelist(workspace)

    q_count = signal_ends_with_question(msgs)
    n_count = signal_has_narration(msgs, whitelist.get("phrases", []))
    sim_pairs = signal_ngram_similarity(msgs)

    # 新信号：joint A+P+Q 组合（即使个别成分是声音，组合重复仍是模板）
    # "A" = 任何以 opener_whitelist 外的共情开头（"嗯/对/这个"之一）
    # "P" = 含任何旁白（不管是否白名单）
    # "Q" = 以问号结尾
    # 一条消息同时具备 A+P+Q 算 1 次
    apq_pattern_count = 0
    for m in msgs:
        stripped = m.strip()
        has_opener = bool(re.match(r"^[嗯对这那哈好啊]", stripped))
        # 所有旁白（含白名单）
        has_narration_any = bool(re.search(r"（[^（）]{1,20}）", stripped))
        has_question = stripped.endswith(("?", "？"))
        if has_opener and has_narration_any and has_question:
            apq_pattern_count += 1

    # 三选二信号判定
    hit_signals: list[str] = []
    if q_count >= 2:
        hit_signals.append(f"end_with_question={q_count}/3")
    if n_count >= 2:
        hit_signals.append(f"narration_density={n_count}/3")
    if sim_pairs >= 1:
        hit_signals.append(f"ngram_sim_pairs={sim_pairs}")
    if apq_pattern_count >= 2:
        hit_signals.append(f"apq_joint_pattern={apq_pattern_count}/3")

    # Phase 3a：只 log
    if len(hit_signals) >= 2:
        log_file = workspace / "memory" / "_detector_log.jsonl"
        event = {
            "ts": datetime.now().astimezone().isoformat(timespec="seconds"),
            "signals": hit_signals,
            "last_msg_snippet": msgs[-1].strip()[:100].replace("\n", " "),
            "total_signals": len(hit_signals),
        }
        try:
            with log_file.open("a", encoding="utf-8") as f:
                f.write(json.dumps(event, ensure_ascii=False) + "\n")
        except OSError:
            pass

    # Phase 3a 不输出到 stdout（只 log 不阻断）


if __name__ == "__main__":
    main()
