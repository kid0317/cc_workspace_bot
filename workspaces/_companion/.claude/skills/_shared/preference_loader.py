#!/usr/bin/env python3
"""v2.2 M13 · 用户偏好加载器。

读取 memory/user_preferences.md 的 YAML frontmatter，
输出对 mode weights 的调整值。

优先级（高→低）：
  FORCE_LISTEN / SOFT_CARE（硬底线）
  > user_preferences（本脚本输出）
  > relationship_heat delta
  > character_params base

用法：
    python3 preference_loader.py --workspace <dir> --mode <mode_from_random>

输出：一行 JSON
    {"override_mode": null_or_forced_mode, "suppress_question": true/false, "note": "..."}

或单行 "NONE" 表示无 override。
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from pathlib import Path


def parse_frontmatter(text: str) -> dict:
    """提取首个 --- YAML --- 块。"""
    m = re.match(r"^---\n(.*?)\n---\n", text, re.DOTALL)
    if not m:
        return {}
    try:
        import yaml  # type: ignore
        return yaml.safe_load(m.group(1)) or {}
    except ImportError:
        # 简易解析（支持当前 schema）
        return _parse_yaml_minimal(m.group(1))


def _parse_yaml_minimal(text: str) -> dict:
    result: dict = {}
    cur_dict: dict = result
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        indent = len(line) - len(line.lstrip())
        if ":" in stripped:
            k, v = stripped.split(":", 1)
            k = k.strip()
            v = v.strip()
            if not v:
                # 嵌套 dict
                cur_dict[k] = {}
                cur_dict = cur_dict[k]
            else:
                if indent == 0:
                    cur_dict = result
                val: object = v.strip('"').strip("'")
                if isinstance(val, str):
                    if val.lower() == "true":
                        val = True
                    elif val.lower() == "false":
                        val = False
                    elif val.lstrip("+-").isdigit():
                        val = int(val)
                cur_dict[k] = val
    return result


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--mode", default="LISTEN")
    args = parser.parse_args()

    if os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        print("NONE")
        return
    if os.environ.get("HOOK_PREFERENCE_LOADER_ENABLED", "true").lower() == "false":
        print("NONE")
        return

    workspace = Path(args.workspace).resolve()
    pref_file = workspace / "memory" / "user_preferences.md"
    if not pref_file.exists():
        print("NONE")
        return

    text = pref_file.read_text(encoding="utf-8")
    config = parse_frontmatter(text)
    if not config:
        print("NONE")
        return

    mode_delta = config.get("mode_delta") or {}
    suppress_question = bool(config.get("suppress_question", False))
    verbosity_override = config.get("verbosity_override")  # null / short / long
    override_mode = config.get("override_mode")  # 强制某 mode

    note_parts: list[str] = []
    if mode_delta:
        signs = [f"{k}{v:+d}" for k, v in mode_delta.items() if v]
        if signs:
            note_parts.append("mode_delta: " + ", ".join(signs))
    if suppress_question:
        note_parts.append("suppress_question: true")
    if verbosity_override:
        note_parts.append(f"verbosity_override: {verbosity_override}")
    if override_mode:
        note_parts.append(f"override_mode: {override_mode}")

    if not note_parts:
        print("NONE")
        return

    out = {
        "mode_delta": mode_delta,
        "suppress_question": suppress_question,
        "verbosity_override": verbosity_override,
        "override_mode": override_mode,
        "note": "; ".join(note_parts),
    }
    print(json.dumps(out, ensure_ascii=False))


if __name__ == "__main__":
    main()
