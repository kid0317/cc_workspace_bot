#!/usr/bin/env python3
"""v2.2 Phase 4 · variant_samples 骨架 lint 脚本。

检查 5 条 variant_samples 之间的骨架多样性，防止 LLM 生成的"variants"
实际上是同一骨架的 5 个变体（type 枚举陷阱）。

用法：
    python3 skeleton_lint.py --samples-file <path>
    python3 skeleton_lint.py --samples-text "<literal text>"

输出：
    - PASS（骨架分散）
    - WARN: <detail>（轻度集中，需要 LLM 重写 N 条）
    - FAIL: <detail>（严重同骨架，必须重新生成）
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


def extract_role_lines(text: str) -> list[str]:
    """抽取每条 variant 中角色方的台词（"角色: xxx" 之后全部，直到下一个"用户:"或段末）。"""
    lines = text.splitlines()
    role_blocks: list[str] = []
    current: list[str] = []
    in_role = False
    for line in lines:
        if re.match(r"^(角色|阿霖|[一-龥]{1,10})\s*[:：]", line.strip()):
            if current:
                role_blocks.append(" ".join(current).strip())
                current = []
            content = re.sub(r"^[^：:]+[：:]\s*", "", line.strip())
            current.append(content)
            in_role = True
        elif re.match(r"^用户\s*[:：]", line.strip()):
            if current:
                role_blocks.append(" ".join(current).strip())
                current = []
            in_role = False
        elif in_role and line.strip():
            current.append(line.strip())
    if current:
        role_blocks.append(" ".join(current).strip())
    return [b for b in role_blocks if b]


def analyze(role_blocks: list[str]) -> dict:
    """统计骨架分布。"""
    n = len(role_blocks)
    if n == 0:
        return {"count": 0}

    # 句长分布
    lengths = [len(b) for b in role_blocks]
    # 首字符
    openers = [b[:1] for b in role_blocks]
    # 是否含问号（体内任意位置）
    has_q = ["?" in b or "？" in b for b in role_blocks]
    # 是否含旁白
    has_narration = [bool(re.search(r"（[^（）]{1,20}）", b)) for b in role_blocks]
    # 是否以嗯/对/这个 开头
    emphatic_opener = [o in {"嗯", "对", "那", "这", "哈", "好"} for o in openers]

    return {
        "count": n,
        "lengths": lengths,
        "avg_length": sum(lengths) / n,
        "max_length": max(lengths),
        "min_length": min(lengths),
        "has_q_count": sum(has_q),
        "has_narration_count": sum(has_narration),
        "emphatic_opener_count": sum(emphatic_opener),
        "unique_openers": len(set(openers)),
    }


def judge(stats: dict) -> tuple[str, list[str]]:
    """返回 (verdict, issues) · verdict in PASS/WARN/FAIL."""
    issues: list[str] = []
    n = stats.get("count", 0)
    if n < 3:
        return "FAIL", [f"variant 数量 {n} < 3（最少要 3 条供 shape hook 抽取）"]

    # 问号分布
    if stats["has_q_count"] == n:
        issues.append(f"所有 {n} 条都含问号（应至少 1 条无问号）")
    elif stats["has_q_count"] == 0:
        issues.append(f"所有 {n} 条都不含问号（应有 1 条含 A 级问题做多样性）")

    # 旁白密度
    if stats["has_narration_count"] >= n - 1:
        issues.append(f"{stats['has_narration_count']}/{n} 条含旁白（旁白是手法要克制，应 ≤ 半数）")

    # 首字符集中度
    if stats["unique_openers"] < max(2, n // 2):
        issues.append(f"首字符只有 {stats['unique_openers']} 种（应至少 {max(2, n // 2)} 种）")

    # 强共情 opener 集中度
    if stats["emphatic_opener_count"] >= n - 1:
        issues.append(f"{stats['emphatic_opener_count']}/{n} 条以「嗯/对/这个」类强共情开头（应至少 1 条不是）")

    # 句长分布
    if stats["max_length"] - stats["min_length"] < 15:
        issues.append(f"句长分布过窄（{stats['min_length']}-{stats['max_length']} 字），应有长短错落")

    if not issues:
        return "PASS", []
    if len(issues) <= 1:
        return "WARN", issues
    return "FAIL", issues


def main() -> None:
    import os as _os
    # v2.2 紧急停用开关
    if _os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        print("=== skeleton_lint · SKIPPED (emergency disable) ===")
        sys.exit(0)

    parser = argparse.ArgumentParser()
    grp = parser.add_mutually_exclusive_group(required=True)
    grp.add_argument("--samples-file")
    grp.add_argument("--samples-text")
    args = parser.parse_args()

    text = ""
    if args.samples_file:
        text = Path(args.samples_file).read_text(encoding="utf-8")
    else:
        text = args.samples_text

    role_blocks = extract_role_lines(text)
    stats = analyze(role_blocks)
    verdict, issues = judge(stats)

    print(f"=== skeleton_lint · {verdict} ===")
    print(f"variants: {stats.get('count', 0)}")
    print(f"unique_openers: {stats.get('unique_openers', 0)}")
    print(f"lengths: {stats.get('lengths', [])}")
    print(f"含问号: {stats.get('has_q_count', 0)}/{stats.get('count', 0)}")
    print(f"含旁白: {stats.get('has_narration_count', 0)}/{stats.get('count', 0)}")
    print(f"强共情 opener: {stats.get('emphatic_opener_count', 0)}/{stats.get('count', 0)}")
    if issues:
        print()
        print("issues:")
        for i in issues:
            print(f"  - {i}")
    sys.exit(0 if verdict == "PASS" else (1 if verdict == "WARN" else 2))


if __name__ == "__main__":
    main()
