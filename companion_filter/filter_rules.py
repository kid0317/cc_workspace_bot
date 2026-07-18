#!/usr/bin/env python3
"""filter_rules.py — 输出过滤 Layer 1：确定性句级规则引擎。

职责（设计文档 docs/archive/companion-output-filter-design.md §4.4）：
- 把回复文本分解为 句子 / [[SEND]] / 换行 三类 token，可无损重组
- 句级匹配操作露出规则（高频形态），删除命中句
- [[SEND]] 不变式：删除句子不丢标记；重组时折叠悬空/相邻标记

部署位置：安装脚本同步到 <workspace>/.claude/hooks/filter_rules.py。
被两处复用：
1. output_filter.py（Stop hook）：预清洗 + Layer 2 失败时的降级路径
2. feishu_ops/scripts/send_and_record.py（proactive 链路，本期仅 Layer 1）

任何修改必须先过 tests/output_filter/test_filter_rules.py。
"""

import re

SEND = "[[SEND]]"

# ── 规则定义 ──────────────────────────────────────────────────────────────────

# 操作宾语：句首动词命中后，还须含其一才算操作句（防误伤"我先睡了"）
_OP_OBJECT = re.compile(
    r"(读取|检查|加载|更新|写入|执行|初始化|记忆|上下文|状态|档案|任务|记录|历史|进度|"
    r"文件|数据|时间戳|memory|context|history|status|archive|file)",
    re.IGNORECASE,
)

# 句首操作导语（中英混语）
_LEAD_VERB = re.compile(
    r"^\s*(让我先?|我需要先?|我来|我先|我得先|现在执行|现在让我|正在|稍等|首先让我|"
    r"读取|检查一下|加载|"
    r"Now let me|Let me|I'll|I will|I need to|Checking|Reading|Updating)",
    re.IGNORECASE,
)

# 整句为括号操作旁白
_OP_BRACKET = re.compile(
    r"^\s*[（(][^（）()]*?(后台|写入|读取|更新|记录中|执行|加载|同步|时间戳|记忆中?|"
    r"checking|reading|saving|updating)[^（）()]*[)）][\s。]*$",
    re.IGNORECASE,
)

# 模板变量残渣
_TEMPLATE_RESIDUE = re.compile(r"\{\{.*?\}\}")

# 作者/系统元话语
_META_IDENTITY = re.compile(r"(以.{1,8}的身份|根据上下文|作为作者)")

# 格式残渣：分隔线 / Markdown 标题 / 代码围栏
_FORMAT_RESIDUE = re.compile(r"^\s*(-{3,}|#{1,6}\s.*|```.*)\s*$")


def match_rule(sentence: str):
    """返回命中的规则名；未命中返回 None。"""
    s = sentence.strip()
    if not s:
        return None
    if _FORMAT_RESIDUE.match(s):
        return "format_residue"
    if _TEMPLATE_RESIDUE.search(s):
        return "template_residue"
    if _OP_BRACKET.match(s):
        return "op_bracket"
    if _META_IDENTITY.search(s):
        return "meta_identity"
    if _LEAD_VERB.match(s) and _OP_OBJECT.search(s):
        return "op_lead"
    return None


# ── tokenize / reassemble ────────────────────────────────────────────────────

_SENT_SPLIT = re.compile(r"(?<=[。！？!?；…])")


def tokenize(text: str):
    """无损分解：[("sent", 句子) | ("send", SEND) | ("nl", "\n")] 列表。"""
    tokens = []
    lines = text.split("\n")
    for li, line in enumerate(lines):
        if li > 0:
            tokens.append(("nl", "\n"))
        # 先按 [[SEND]] 切，再在片段内分句
        parts = line.split(SEND)
        for pi, part in enumerate(parts):
            if pi > 0:
                tokens.append(("send", SEND))
            for piece in _SENT_SPLIT.split(part):
                if piece != "":
                    tokens.append(("sent", piece))
    return tokens


def reassemble(tokens) -> str:
    return "".join(t[1] for t in tokens)


def _fold(tokens):
    """折叠规则（设计 §4.2⑦）：
    - 相邻 [[SEND]] 之间无实质句子 → 合并为一个
    - 首/尾悬空 [[SEND]] 删除
    - 删除产生的多余空行压缩
    """
    has_any_sent = any(k == "sent" and v.strip() for k, v in tokens)
    if not has_any_sent:
        return []

    out = []
    seen_sent_since_send = False
    for kind, val in tokens:
        if kind == "sent":
            if val.strip() == "" and not out:
                continue  # 开头纯空白
            out.append((kind, val))
            if val.strip():
                seen_sent_since_send = True
        elif kind == "send":
            if not seen_sent_since_send:
                continue  # 前面没有实质句子 → 悬空，丢弃
            out.append((kind, val))
            seen_sent_since_send = False
        else:  # nl
            out.append((kind, val))

    # 去掉尾部悬空 send / 空白 / 换行
    while out and (
        out[-1][0] == "send"
        or out[-1][0] == "nl"
        or (out[-1][0] == "sent" and out[-1][1].strip() == "")
    ):
        out.pop()

    # 压缩 3 连以上换行（整行删除的残留）
    compact = []
    nl_run = 0
    for kind, val in out:
        if kind == "nl":
            nl_run += 1
            if nl_run > 2:
                continue
        else:
            nl_run = 0
        compact.append((kind, val))
    return compact


# ── 公开 API ─────────────────────────────────────────────────────────────────


def layer1_filter(text: str):
    """句级规则过滤。返回 (filtered_text, hits)。
    hits: [{"sentence": 原句, "rule": 规则名}]，供日志与评测。
    无命中时原样返回（保证 clean 样本逐字不变）。
    """
    tokens = tokenize(text)
    hits = []
    kept = []
    for kind, val in tokens:
        if kind == "sent":
            rule = match_rule(val)
            if rule:
                hits.append({"sentence": val.strip(), "rule": rule})
                continue
        kept.append((kind, val))
    if not hits:
        return text, []
    return reassemble(_fold(kept)), hits


def numbered_sentences(text: str):
    """为 Layer 2 提供编号句子清单：[(1, 句子), ...]（仅实质 sent token）。"""
    result = []
    idx = 0
    for kind, val in tokenize(text):
        if kind == "sent" and val.strip():
            idx += 1
            result.append((idx, val))
    return result


def remove_by_index(text: str, indices) -> str:
    """按 numbered_sentences 的编号删除句子，确定性重组。
    非法编号忽略；"只删不改"由本函数构造保证。
    """
    valid = {i for i in indices if isinstance(i, int)}
    tokens = tokenize(text)
    kept = []
    idx = 0
    removed_any = False
    for kind, val in tokens:
        if kind == "sent" and val.strip():
            idx += 1
            if idx in valid:
                removed_any = True
                continue
        kept.append((kind, val))
    if not removed_any:
        return text
    return reassemble(_fold(kept))
