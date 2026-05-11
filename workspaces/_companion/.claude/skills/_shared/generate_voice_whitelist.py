#!/usr/bin/env python3
"""Phase 0 voice_whitelist 生成脚本。

扫描 workspace 的 CLAUDE.md【角色设定】+ memory/persona.md 的说话示例，
统计高频"疑似 slop 但实为角色声音标志"的字符串，生成 voice_whitelist.yaml。

用法：
    python3 generate_voice_whitelist.py <workspace_dir>

运行时：M6.2 slop 检测前先过此白名单，命中则不判定为 slop。
"""

from __future__ import annotations

import re
import sys
from collections import Counter
from datetime import datetime
from pathlib import Path


PAREN_NARRATION_PATTERN = re.compile(r"（([^（）]{1,20})）")
OPENER_CHARS = [
    "嗯", "对", "这个", "那", "哈", "好", "啊", "噢", "哦",
    "呃", "哎", "哇", "唔", "咦",
]

# 通用 slop（若角色示例里反复出现，也可视为"声音"而非 slop）
NARRATION_SLOP_CANDIDATES = [
    "停顿", "沉默", "笑了一下", "轻轻笑", "轻笑", "叹气",
    "皱眉", "点头", "摇头", "愣了一下", "顿了一下",
]


def extract_character_block(text: str) -> str:
    """从 CLAUDE.md 中提取【角色设定】块（或 persona.md 全文）。"""
    m = re.search(r"##\s*【角色设定】(.*?)(?=\n##\s|\Z)", text, re.DOTALL)
    if m:
        return m.group(1)
    return text


def extract_speech_examples(block: str) -> list[str]:
    """从角色设定块里提取说话示例（代码块或"用户/角色"对话段）。"""
    examples: list[str] = []

    # 1) markdown 代码块
    for m in re.finditer(r"```\n?(.*?)```", block, re.DOTALL):
        code = m.group(1)
        # 认定含"用户"或角色关键字的块是对话示例
        if re.search(r"(用户|角色|阿霖|[\u4e00-\u9fa5]{1,10})\s*[:：]", code):
            examples.append(code)

    # 2) 非代码块的"用户: / 角色:"对话段
    for m in re.finditer(
        r"(?:用户|角色|[A-Za-z_]{2,15}|[\u4e00-\u9fa5]{1,10})\s*[:：][^\n]+", block
    ):
        line = m.group(0)
        if len(line) < 200:
            examples.append(line)

    return examples


def extract_angle_role_lines(examples: list[str], role_name_hints: list[str]) -> list[str]:
    """从示例里抽出角色方（非用户方）的台词行。"""
    lines: list[str] = []
    for ex in examples:
        for raw_line in ex.splitlines():
            raw_line = raw_line.strip()
            if not raw_line:
                continue
            # 过滤掉用户行
            if re.match(r"^用户\s*[:：]", raw_line):
                continue
            # 保留 "角色:" / "<名字>:" / 直接台词
            m = re.match(r"^(?:角色|[\u4e00-\u9fa5]{1,10})\s*[:：]\s*(.+)", raw_line)
            if m:
                lines.append(m.group(1))
            else:
                # 非用户标签的独立句子（代码块里的续行）
                lines.append(raw_line)
    return lines


def analyze_role_lines(lines: list[str]) -> dict:
    """统计开头字符、旁白模式、标志短语。"""
    opener_counter: Counter[str] = Counter()
    narration_counter: Counter[str] = Counter()
    phrases_found: list[dict] = []

    for line in lines:
        # 开头字符（跳过旁白）
        clean = re.sub(r"^（[^）]*）\s*", "", line)
        for opener in OPENER_CHARS:
            if clean.startswith(opener):
                opener_counter[opener] += 1
                break

        # 旁白（括号内容）
        for m in PAREN_NARRATION_PATTERN.finditer(line):
            narration = m.group(0)  # 完整 "（...）"
            inner = m.group(1)
            narration_counter[narration] += 1
            # 是否匹配 slop 候选
            for slop_word in NARRATION_SLOP_CANDIDATES:
                if slop_word in inner:
                    # 记录为"声音白名单候选"
                    pass

    # 高频开头（≥2 次出现视为声音标志）
    high_freq_openers = [
        {"char": c, "frequency": n}
        for c, n in opener_counter.most_common()
        if n >= 2
    ]

    # 高频旁白（≥2 次出现）
    high_freq_narration = [
        {"text": t, "frequency": n}
        for t, n in narration_counter.most_common()
        if n >= 2
    ]

    return {
        "openers": high_freq_openers,
        "narration": high_freq_narration,
    }


def generate_yaml(workspace: Path, analysis: dict) -> str:
    """生成 voice_whitelist.yaml 内容。"""
    lines: list[str] = []
    lines.append("# voice_whitelist.yaml")
    lines.append("# Phase 0 自动生成：保护角色标志性词汇/符号不被 slop 检测误伤")
    lines.append("# 运行时：slop 检测前先过此白名单，命中则不判定为 slop")
    lines.append(f"# workspace: {workspace.name}")
    lines.append(f"# generated_at: {datetime.now().astimezone().isoformat(timespec='seconds')}")
    lines.append("")
    lines.append("voice_whitelist:")

    # phrases（旁白）
    lines.append("  phrases:")
    if analysis["narration"]:
        for item in analysis["narration"]:
            lines.append(f'    - text: "{item["text"]}"')
            lines.append(f'      frequency: {item["frequency"]}')
            lines.append(f'      reason: "高频旁白，角色声音标志"')
    else:
        lines.append("    []")

    # openers
    lines.append("  openers:")
    if analysis["openers"]:
        for item in analysis["openers"]:
            lines.append(f'    - char: "{item["char"]}"')
            lines.append(f'      frequency: {item["frequency"]}')
            lines.append(f'      note: "仍受 3-in-3 同 opener 动态 blacklist 限制"')
    else:
        lines.append("    []")

    lines.append("")
    lines.append("# 用户确认后修改下面字段为 true")
    lines.append("user_confirmed: false")
    lines.append(f"last_reviewed: \"{datetime.now().astimezone().isoformat(timespec='seconds')}\"")
    lines.append("")

    return "\n".join(lines)


def main() -> None:
    # v2.2 紧急停用开关
    if os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        sys.exit(0)

    if len(sys.argv) < 2:
        print("usage: generate_voice_whitelist.py <workspace_dir>", file=sys.stderr)
        sys.exit(1)

    workspace = Path(sys.argv[1]).resolve()
    if not workspace.exists():
        print(f"workspace not found: {workspace}", file=sys.stderr)
        sys.exit(1)

    claude_md = workspace / "CLAUDE.md"
    persona_md = workspace / "memory" / "persona.md"

    # 优先使用 CLAUDE.md（运行时真源）；缺则 fallback persona.md
    source_text = ""
    if claude_md.exists():
        raw = claude_md.read_text(encoding="utf-8")
        source_text = extract_character_block(raw)
    elif persona_md.exists():
        source_text = persona_md.read_text(encoding="utf-8")

    if not source_text:
        print("no persona sources found; skipping", file=sys.stderr)
        sys.exit(0)

    # 去重：同一段示例被 code block + line 双重抽取时只保留一次
    raw_examples = extract_speech_examples(source_text)
    seen_examples: set[str] = set()
    all_examples: list[str] = []
    for ex in raw_examples:
        key = ex.strip()[:80]
        if key not in seen_examples:
            seen_examples.add(key)
            all_examples.append(ex)

    role_lines = extract_angle_role_lines(all_examples, [])
    analysis = analyze_role_lines(role_lines)

    output_path = workspace / "memory" / "voice_whitelist.yaml"
    output_path.write_text(generate_yaml(workspace, analysis), encoding="utf-8")
    print(f"[ok] generated: {output_path}")
    print(f"  openers: {len(analysis['openers'])} entries")
    print(f"  narration: {len(analysis['narration'])} entries")


if __name__ == "__main__":
    main()
