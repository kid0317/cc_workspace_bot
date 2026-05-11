#!/usr/bin/env python3
"""v2.2 M11 · FORCE_LISTEN / SOFT_CARE / CLOSING 共现检测（替代老 OR 逻辑）。

读 memory/trigger_words.yaml，对最近用户消息做：
- AND 共现（require_cooccurrence）
- 否定前词窗口排除（explicit_crisis.negate_if_preceded_by）
- SOFT_CARE 单点命中
- CLOSING 单点命中

输出一行：`FORCE_LISTEN|SOFT_CARE|CLOSING|NONE`，供 bash 读取。

用法：
    python3 trigger_check.py --workspace <dir>
"""

from __future__ import annotations

import argparse
import os
import re
import sys
from pathlib import Path


def load_yaml_simple(path: Path) -> dict:
    """轻量 YAML 读取，支持本 yaml 文件的结构（不引 PyYAML 依赖）。"""
    if not path.exists():
        return {}
    try:
        import yaml  # type: ignore
        return yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    except ImportError:
        pass
    # fallback: 手工解析（简化版）
    return _parse_yaml_manual(path.read_text(encoding="utf-8"))


def _parse_yaml_manual(text: str) -> dict:
    """手工 YAML 解析（仅覆盖本文件 schema）。"""
    result: dict = {}
    lines = text.splitlines()
    stack: list[tuple[int, object]] = [(0, result)]

    i = 0
    while i < len(lines):
        raw = lines[i]
        stripped = raw.strip()
        if not stripped or stripped.startswith("#"):
            i += 1
            continue

        indent = len(raw) - len(raw.lstrip())
        while len(stack) > 1 and stack[-1][0] >= indent:
            stack.pop()

        parent = stack[-1][1]

        # 列表项
        if stripped.startswith("- "):
            val_str = stripped[2:].strip()
            if ":" in val_str and not val_str.startswith("["):
                # 对象列表项
                new_obj: dict = {}
                if isinstance(parent, list):
                    parent.append(new_obj)
                k, v = val_str.split(":", 1)
                v = v.strip()
                if v:
                    new_obj[k.strip()] = _parse_value(v)
                stack.append((indent + 2, new_obj))
            else:
                # 纯值列表项
                if isinstance(parent, list):
                    parent.append(_parse_value(val_str))
            i += 1
            continue

        # key: value
        if ":" in stripped:
            k, v = stripped.split(":", 1)
            k = k.strip()
            v = v.strip()
            if not v:
                # 可能是嵌套 dict 或 list
                # 向前看下一行判断
                j = i + 1
                while j < len(lines) and not lines[j].strip():
                    j += 1
                if j < len(lines):
                    nxt = lines[j]
                    nxt_indent = len(nxt) - len(nxt.lstrip())
                    if nxt_indent > indent and nxt.strip().startswith("- "):
                        new_list: list = []
                        if isinstance(parent, dict):
                            parent[k] = new_list
                        stack.append((nxt_indent, new_list))
                    elif nxt_indent > indent:
                        new_dict: dict = {}
                        if isinstance(parent, dict):
                            parent[k] = new_dict
                        stack.append((nxt_indent, new_dict))
            else:
                if isinstance(parent, dict):
                    parent[k] = _parse_value(v)
        i += 1

    return result


def _parse_value(v: str) -> object:
    v = v.strip()
    if v.startswith("[") and v.endswith("]"):
        inner = v[1:-1].strip()
        if not inner:
            return []
        return [_parse_scalar(x.strip()) for x in inner.split(",")]
    return _parse_scalar(v)


def _parse_scalar(v: str) -> object:
    v = v.strip().strip('"').strip("'")
    if v.isdigit():
        return int(v)
    if v == "true":
        return True
    if v == "false":
        return False
    return v


def extract_recent_user_messages(recent_history: Path, n: int = 3) -> list[str]:
    if not recent_history.exists():
        return []
    text = recent_history.read_text(encoding="utf-8")
    msgs: list[str] = []
    for line in text.splitlines():
        m = re.match(r"\*\*用户\*\*.*?：(.+)", line)
        if m:
            msgs.append(m.group(1).strip())
    return msgs[-n:]


def check_cooccurrence(messages: list[str], rules: list) -> bool:
    """任一消息同时命中 group_a 中一词 AND group_b 中一词（group_b 空表示单独命中即可）。"""
    if not rules:
        return False
    for msg in messages:
        for rule in rules:
            if not isinstance(rule, dict):
                continue
            ga = rule.get("group_a") or []
            gb = rule.get("group_b") or []
            hit_a = any(w in msg for w in ga) if ga else False
            if not ga:
                continue
            if not gb:
                # 空 group_b：单独命中即可
                if hit_a:
                    return True
            else:
                hit_b = any(w in msg for w in gb)
                if hit_a and hit_b:
                    return True
    return False


def check_explicit_crisis(messages: list[str], config: dict) -> bool:
    words = config.get("words") or []
    negates = config.get("negate_if_preceded_by") or []
    window = config.get("window", 3)
    if not words:
        return False
    for msg in messages:
        for w in words:
            idx = msg.find(w)
            while idx >= 0:
                prefix_start = max(0, idx - window)
                prefix = msg[prefix_start:idx]
                if not any(n in prefix for n in negates):
                    return True
                idx = msg.find(w, idx + 1)
    return False


def check_single_hit(messages: list[str], words: list) -> bool:
    if not words:
        return False
    for msg in messages:
        for w in words:
            if w in msg:
                return True
    return False


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", required=True)
    args = parser.parse_args()

    if os.environ.get("HOOKS_V2_EMERGENCY_DISABLE") == "true":
        print("NONE")
        return
    if os.environ.get("HOOK_TRIGGER_CHECK_ENABLED", "true").lower() == "false":
        print("NONE")
        return

    workspace = Path(args.workspace).resolve()
    trigger_file = workspace / "memory" / "trigger_words.yaml"
    recent_file = workspace / "memory" / "RECENT_HISTORY.md"

    cfg = load_yaml_simple(trigger_file)
    if not cfg:
        print("NONE")
        return

    messages = extract_recent_user_messages(recent_file, n=3)
    if not messages:
        print("NONE")
        return

    # CLOSING 优先检查（最早退出）
    closing_cfg = cfg.get("closing") or {}
    if check_single_hit(messages, closing_cfg.get("single_hit") or []):
        print("CLOSING")
        return

    # FORCE_LISTEN
    fl_cfg = cfg.get("force_listen") or {}
    if check_cooccurrence(messages, fl_cfg.get("require_cooccurrence") or []):
        print("FORCE_LISTEN")
        return
    if check_explicit_crisis(messages, fl_cfg.get("explicit_crisis") or {}):
        print("FORCE_LISTEN")
        return

    # SOFT_CARE
    sc_cfg = cfg.get("soft_care") or {}
    if check_single_hit(messages, sc_cfg.get("single_hit") or []):
        print("SOFT_CARE")
        return

    print("NONE")


if __name__ == "__main__":
    main()
