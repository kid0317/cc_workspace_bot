"""Layer 1 sentence-level rules — TDD spec.

Samples are synthetic fixtures that represent operational text which must not
be sent to users in companion mode.
"""

import pytest

import filter_rules as fr

# ── tokenize / reassemble round-trip ─────────────────────────────────────────


def test_roundtrip_plain_text():
    text = "嗯。今天怎么样。"
    assert fr.reassemble(fr.tokenize(text)) == text


def test_roundtrip_with_send_and_newlines():
    text = "嗯。[[SEND]]在。\n\n楼下那只猫又在叫。"
    assert fr.reassemble(fr.tokenize(text)) == text


# ── rule matching (synthetic samples) ────────────────────────────────────────


@pytest.mark.parametrize(
    "sentence",
    [
        "我来读取一下当前的会话上下文和记忆状态。",
        "让我先检查一下初始化状态和记忆档案。",
        "我需要先读取上下文信息，了解你的情况。",
        "读取最近对话历史和当前事件进度。",
        "我需要先读取一下当前的上下文和记忆，才能以测试角色的身份回应你。",
        "让我执行记忆更新。",
        "现在执行后台记忆更新。",
        "Now let me update the memory files.",
        "（后台更新 last_active 时间戳）",
        "（读取上下文和记忆中...）",
        "（执行后台记忆更新）",
        "（在后台更新记忆）",
        "{{后台更新记忆中...}}",
        "现在我以测试角色的身份来回应。",
        "根据上下文，用户在强调行程的新调整。",
        "---",
    ],
)
def test_operational_sentences_removed(sentence):
    assert fr.match_rule(sentence) is not None, f"should match a rule: {sentence}"


@pytest.mark.parametrize(
    "sentence",
    [
        "嗯。",
        "嗯，记住了。",  # 角色语义的记忆表达 = 台词，绝不能删（评测 0 容忍类）
        "我记着呢。",
        "玩着总比窝着好。",
        "（尾巴摇得更欢了）",  # 合法场景旁白
        "（灯光压着，人没动）",
        "你上次说的那家店，后来去了吗。",
        "我先睡了。",  # 句首"我先"但无操作宾语
        "让我想想。",  # 句首"让我"但无操作宾语
        "这个我得记下来——回头写进稿子里。",  # 编剧 persona 双栖词汇，保留
        "等你闲了，想听听你怎么看。",
    ],
)
def test_character_sentences_kept(sentence):
    assert fr.match_rule(sentence) is None, f"false positive on: {sentence}"


# ── layer1_filter end to end (synthetic mixed message) ───────────────────────


def test_layer1_mixed_message():
    # Synthetic mixed output: operational narration and user-facing text.
    text = (
        "我需要先读取上下文信息，了解你的情况。读取最近对话历史和当前事件进度。嗯。\n"
        "玩着总比窝着好。\n\n"
        "改稿改通了一些，互为镜像那个场景现在有感觉了。\n\n"
        "等你闲了，想听听你怎么看。"
    )
    filtered, hits = fr.layer1_filter(text)
    assert "读取" not in filtered
    assert "嗯。" in filtered
    assert "玩着总比窝着好。" in filtered
    assert "等你闲了，想听听你怎么看。" in filtered
    assert len(hits) == 2


def test_layer1_message_with_trailing_op_note():
    # Synthetic mixed output: trailing operational narration + dangling [[SEND]].
    text = "嗯。\n好久没听你说话了。[[SEND]]（后台更新 last_active 时间戳）"
    filtered, hits = fr.layer1_filter(text)
    assert "后台" not in filtered
    assert "嗯。" in filtered
    # 尾部悬空 [[SEND]] 必须折叠
    assert not filtered.rstrip().endswith("[[SEND]]")
    assert len(hits) == 1


def test_layer1_clean_text_untouched():
    text = "嗯，记住了。[[SEND]]（尾巴摇得更欢了）你呢，今天累不累。"
    filtered, hits = fr.layer1_filter(text)
    assert filtered == text
    assert hits == []


# ── [[SEND]] invariants ──────────────────────────────────────────────────────


def test_send_count_never_increases():
    text = "（后台更新记忆中）[[SEND]]嗯。[[SEND]]在。"
    filtered, _ = fr.layer1_filter(text)
    assert filtered.count("[[SEND]]") <= text.count("[[SEND]]")


def test_leading_send_stripped_after_removal():
    text = "（执行后台记忆更新）[[SEND]]嗯。"
    filtered, _ = fr.layer1_filter(text)
    assert not filtered.lstrip().startswith("[[SEND]]")
    assert "嗯。" in filtered


def test_adjacent_sends_collapse_when_middle_removed():
    text = "在。[[SEND]]（后台更新记忆中）[[SEND]]嗯。"
    filtered, _ = fr.layer1_filter(text)
    assert filtered.count("[[SEND]]") == 1
    assert "在。" in filtered and "嗯。" in filtered


# ── numbered sentences / remove_by_index (Layer 2 support) ───────────────────


def test_numbered_sentences():
    text = "嗯。[[SEND]]我先把这段记进档案。你说呢。"
    numbered = fr.numbered_sentences(text)
    assert [n for n, _ in numbered] == [1, 2, 3]
    assert numbered[1][1] == "我先把这段记进档案。"


def test_remove_by_index_is_subsequence():
    text = "嗯。我先把这段记进档案。你说呢。"
    out = fr.remove_by_index(text, [2])
    assert out == "嗯。你说呢。"


def test_remove_by_index_all_returns_empty():
    text = "（后台更新记忆中）"
    out = fr.remove_by_index(text, [1])
    assert out.strip() == ""


def test_remove_by_index_ignores_invalid_indices():
    text = "嗯。在。"
    out = fr.remove_by_index(text, [99])
    assert out == text
