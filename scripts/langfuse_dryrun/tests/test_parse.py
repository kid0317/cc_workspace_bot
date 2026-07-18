"""TDD tests for the langfuse_hook parsing layer.

Pure functions only — no Langfuse SDK, no I/O. RED first, GREEN after.
"""
from __future__ import annotations

import pytest

from lf_hook import parse


# ── _normalize_usage ─────────────────────────────────────────────────────────

class TestNormalizeUsage:
    def test_empty_returns_empty(self):
        assert parse.normalize_usage(None) == {}
        assert parse.normalize_usage({}) == {}

    def test_basic_input_output(self):
        out = parse.normalize_usage({"input_tokens": 100, "output_tokens": 50})
        assert out == {"input": 100, "output": 50}

    def test_zero_values_preserved(self):
        # CRITICAL: 0 is a real value, not missing — design §5.2.3
        out = parse.normalize_usage({"input_tokens": 1, "output_tokens": 0})
        assert out == {"input": 1, "output": 0}

    def test_all_zero_preserved(self):
        # bailian/kimi sub-agent case
        out = parse.normalize_usage({"input_tokens": 0, "output_tokens": 0})
        assert out == {"input": 0, "output": 0}

    def test_cache_5m_and_1h_split(self):
        out = parse.normalize_usage({
            "input_tokens": 6,
            "output_tokens": 141,
            "cache_creation": {
                "ephemeral_5m_input_tokens": 100,
                "ephemeral_1h_input_tokens": 21210,
            },
            "cache_read_input_tokens": 18022,
        })
        assert out == {
            "input": 6,
            "output": 141,
            "input_cache_creation_5m": 100,
            "input_cache_creation_1h": 21210,
            "input_cache_read": 18022,
        }

    def test_legacy_cache_creation_falls_back_to_5m(self):
        # No ephemeral split → assume 5m
        out = parse.normalize_usage({
            "input_tokens": 1,
            "output_tokens": 1,
            "cache_creation_input_tokens": 500,
        })
        assert out["input_cache_creation_5m"] == 500
        assert "input_cache_creation_1h" not in out

    def test_zero_cache_preserved(self):
        out = parse.normalize_usage({
            "input_tokens": 1,
            "output_tokens": 1,
            "cache_creation": {"ephemeral_5m_input_tokens": 0, "ephemeral_1h_input_tokens": 0},
            "cache_read_input_tokens": 0,
        })
        assert out["input_cache_creation_5m"] == 0
        assert out["input_cache_creation_1h"] == 0
        assert out["input_cache_read"] == 0


# ── _is_zero_usage ───────────────────────────────────────────────────────────

class TestIsZeroUsage:
    def test_all_zero(self):
        assert parse.is_zero_usage({"input_tokens": 0, "output_tokens": 0}) is True

    def test_nonzero_input(self):
        assert parse.is_zero_usage({"input_tokens": 1, "output_tokens": 0}) is False

    def test_empty_dict(self):
        # No fields = effectively zero
        assert parse.is_zero_usage({}) is True

    def test_with_cache(self):
        assert parse.is_zero_usage({
            "input_tokens": 0, "output_tokens": 0,
            "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
        }) is True

    def test_nonzero_cache_only(self):
        assert parse.is_zero_usage({
            "input_tokens": 0, "output_tokens": 0,
            "cache_read_input_tokens": 100,
        }) is False


# ── _estimate_usage_from_text ────────────────────────────────────────────────

class TestEstimateUsageFromText:
    def test_pure_english(self):
        # 4 chars/token approximation
        out = parse.estimate_usage_from_text("hello world", "")
        assert out == {"input": len("hello world") // 4, "output": 0}

    def test_pure_chinese(self):
        # 1 char/token approximation
        text = "你好世界"
        out = parse.estimate_usage_from_text(text, "")
        assert out == {"input": 4, "output": 0}

    def test_mixed(self):
        # 2 ASCII + 2 CJK = 2 + 2//4 = 2
        out = parse.estimate_usage_from_text("ab你好", "")
        assert out["input"] == 2 + 0  # 2 chinese + 2 ascii//4=0

    def test_empty(self):
        out = parse.estimate_usage_from_text("", "")
        assert out == {"input": 0, "output": 0}

    def test_returns_int(self):
        out = parse.estimate_usage_from_text("hello", "world")
        assert isinstance(out["input"], int)
        assert isinstance(out["output"], int)


# ── _merge_assistant_rows ────────────────────────────────────────────────────

def _row(uuid, mid, request_id, content_blocks, usage=None, model="claude-opus-4-7", ts="2026-05-06T10:00:00.000Z"):
    return {
        "type": "assistant",
        "uuid": uuid,
        "requestId": request_id,
        "timestamp": ts,
        "message": {
            "id": mid,
            "model": model,
            "content": content_blocks,
            "usage": usage or {"input_tokens": 1, "output_tokens": 1},
        },
    }


class TestMergeAssistantRows:
    def test_single_row_passthrough(self):
        rows = [_row("u1", "msg_A", "req_1", [{"type": "text", "text": "hi"}])]
        merged = parse.merge_assistant_rows(rows)
        assert len(merged) == 1
        key = ("msg_A", "req_1")
        assert key in merged
        assert merged[key]["model"] == "claude-opus-4-7"
        assert merged[key]["content_blocks"] == [{"type": "text", "text": "hi"}]
        assert merged[key]["first_uuid"] == "u1"

    def test_split_blocks_merged_in_order(self):
        rows = [
            _row("u1", "msg_A", "req_1", [{"type": "thinking", "thinking": "T"}]),
            _row("u2", "msg_A", "req_1", [{"type": "tool_use", "id": "t1", "name": "Bash", "input": {}}]),
            _row("u3", "msg_A", "req_1", [{"type": "tool_use", "id": "t2", "name": "Read", "input": {}}]),
        ]
        merged = parse.merge_assistant_rows(rows)
        assert len(merged) == 1
        blocks = merged[("msg_A", "req_1")]["content_blocks"]
        assert [b["type"] for b in blocks] == ["thinking", "tool_use", "tool_use"]
        assert blocks[1]["id"] == "t1"
        assert blocks[2]["id"] == "t2"

    def test_distinct_message_ids_kept_separate(self):
        rows = [
            _row("u1", "msg_A", "req_1", [{"type": "text", "text": "a"}]),
            _row("u2", "msg_B", "req_2", [{"type": "text", "text": "b"}]),
        ]
        merged = parse.merge_assistant_rows(rows)
        assert len(merged) == 2

    def test_same_msgid_different_request_id_kept_separate(self):
        # Defensive: different requestId means different bill even if mid collides
        rows = [
            _row("u1", "msg_A", "req_1", [{"type": "text", "text": "a"}]),
            _row("u2", "msg_A", "req_2", [{"type": "text", "text": "b"}]),
        ]
        merged = parse.merge_assistant_rows(rows)
        assert len(merged) == 2

    def test_usage_taken_from_first(self):
        rows = [
            _row("u1", "msg_A", "req_1", [{"type": "thinking", "thinking": "T"}],
                 usage={"input_tokens": 6, "output_tokens": 438}),
            _row("u2", "msg_A", "req_1", [{"type": "tool_use", "id": "t1", "name": "B", "input": {}}],
                 usage={"input_tokens": 6, "output_tokens": 438}),
        ]
        merged = parse.merge_assistant_rows(rows)
        assert merged[("msg_A", "req_1")]["usage"] == {"input_tokens": 6, "output_tokens": 438}

    def test_first_uuid_is_first_seen(self):
        rows = [
            _row("FIRST", "msg_A", "req_1", [{"type": "text", "text": "a"}]),
            _row("SECOND", "msg_A", "req_1", [{"type": "text", "text": "b"}]),
        ]
        merged = parse.merge_assistant_rows(rows)
        assert merged[("msg_A", "req_1")]["first_uuid"] == "FIRST"

    def test_skips_non_assistant_rows(self):
        rows = [
            {"type": "user", "message": {"content": "hi"}},
            _row("u1", "msg_A", "req_1", [{"type": "text", "text": "ok"}]),
            {"type": "summary"},
        ]
        merged = parse.merge_assistant_rows(rows)
        assert len(merged) == 1

    def test_skips_rows_without_message_id(self):
        rows = [
            {"type": "assistant", "uuid": "x", "message": {"content": []}},  # no id
            _row("u1", "msg_A", "req_1", [{"type": "text", "text": "ok"}]),
        ]
        merged = parse.merge_assistant_rows(rows)
        assert len(merged) == 1


# ── _build_turns ─────────────────────────────────────────────────────────────

def _user_row(text, uuid="u_user"):
    return {
        "type": "user",
        "uuid": uuid,
        "message": {"role": "user", "content": text},
    }


def _tool_result_row(tool_id, output, uuid="u_tr"):
    return {
        "type": "user",
        "uuid": uuid,
        "message": {
            "role": "user",
            "content": [{"type": "tool_result", "tool_use_id": tool_id, "content": output}],
        },
    }


class TestBuildTurns:
    def test_single_turn(self):
        rows = [
            _user_row("question 1", uuid="user1"),
            _row("u1", "msg_A", "req_1", [{"type": "text", "text": "answer"}]),
        ]
        turns = parse.build_turns(rows)
        assert len(turns) == 1
        assert turns[0].turn_num == 1
        assert turns[0].user_text == "question 1"
        assert len(turns[0].llm_calls) == 1
        assert turns[0].llm_calls[0].message_id == "msg_A"

    def test_two_turns_split_by_real_user_message(self):
        rows = [
            _user_row("Q1", uuid="user1"),
            _row("u1", "msg_A", "req_1", [{"type": "text", "text": "A1"}]),
            _user_row("Q2", uuid="user2"),
            _row("u2", "msg_B", "req_2", [{"type": "text", "text": "A2"}]),
        ]
        turns = parse.build_turns(rows)
        assert len(turns) == 2
        assert turns[0].user_text == "Q1"
        assert turns[1].user_text == "Q2"

    def test_tool_result_does_not_split_turn(self):
        rows = [
            _user_row("Q1", uuid="user1"),
            _row("u1", "msg_A", "req_1", [{"type": "tool_use", "id": "t1", "name": "Bash", "input": {}}]),
            _tool_result_row("t1", "result"),
            _row("u2", "msg_B", "req_2", [{"type": "text", "text": "final"}]),
        ]
        turns = parse.build_turns(rows)
        assert len(turns) == 1
        assert len(turns[0].llm_calls) == 2  # both A and B in same turn

    def test_multi_row_message_groups_correctly(self):
        # The actual production case: one message split across multiple rows
        rows = [
            _user_row("Q", uuid="user1"),
            _row("u1", "msg_A", "req_1", [{"type": "thinking", "thinking": "T"}]),
            _row("u2", "msg_A", "req_1", [{"type": "tool_use", "id": "t1", "name": "Bash", "input": {}}]),
            _row("u3", "msg_A", "req_1", [{"type": "tool_use", "id": "t2", "name": "Read", "input": {}}]),
        ]
        turns = parse.build_turns(rows)
        assert len(turns) == 1
        assert len(turns[0].llm_calls) == 1  # ONE call after merge
        call = turns[0].llm_calls[0]
        assert len(call.tool_calls) == 2  # t1 and t2 preserved

    def test_zero_usage_uses_user_text_for_input_estimate(self):
        # When usage is all-zero, char estimation must use the turn's user_text for input,
        # not empty string. Otherwise sub-agent input cost is undercounted to 0.
        rows = [
            _user_row("问个问题", uuid="user1"),  # 4 CJK chars → 4 tokens
            _row("u1", "msg_A", "req_1", [{"type": "text", "text": "answer"}],
                 usage={"input_tokens": 0, "output_tokens": 0}, model="kimi-k2.5"),
        ]
        turns = parse.build_turns(rows)
        call = turns[0].llm_calls[0]
        assert call.usage_source == "estimated_char"
        assert call.usage["input"] == 4   # NOT 0
        assert call.usage["output"] > 0

    def test_tool_result_string_content_attached(self):
        rows = [
            _user_row("Q", uuid="user1"),
            _row("u1", "msg_A", "req_1", [{"type": "tool_use", "id": "t1", "name": "Read", "input": {"f": "x"}}]),
            _tool_result_row("t1", "file contents here"),
        ]
        turns = parse.build_turns(rows)
        tc = turns[0].llm_calls[0].tool_calls[0]
        assert tc.output == "file contents here"

    def test_tool_result_block_list_content_joined(self):
        # tool_result content can be a list of {type:text, text:...} blocks
        rows = [
            _user_row("Q", uuid="user1"),
            _row("u1", "msg_A", "req_1", [{"type": "tool_use", "id": "t1", "name": "Read", "input": {}}]),
            {
                "type": "user", "uuid": "u_tr",
                "message": {"role": "user", "content": [{
                    "type": "tool_result", "tool_use_id": "t1",
                    "content": [{"type": "text", "text": "part1"}, {"type": "text", "text": "part2"}],
                }]},
            },
        ]
        turns = parse.build_turns(rows)
        assert turns[0].llm_calls[0].tool_calls[0].output == "part1\npart2"

    def test_tool_result_is_error_preserved(self):
        rows = [
            _user_row("Q", uuid="user1"),
            _row("u1", "msg_A", "req_1", [{"type": "tool_use", "id": "t1", "name": "Bash", "input": {}}]),
            {
                "type": "user", "uuid": "u_tr",
                "message": {"role": "user", "content": [{
                    "type": "tool_result", "tool_use_id": "t1",
                    "content": "boom", "is_error": True,
                }]},
            },
        ]
        turns = parse.build_turns(rows)
        out = turns[0].llm_calls[0].tool_calls[0].output
        assert out is not None and "boom" in out and "error" in out.lower()

    def test_missing_tool_result_leaves_output_none(self):
        rows = [
            _user_row("Q", uuid="user1"),
            _row("u1", "msg_A", "req_1", [{"type": "tool_use", "id": "t1", "name": "Read", "input": {}}]),
        ]
        turns = parse.build_turns(rows)
        assert turns[0].llm_calls[0].tool_calls[0].output is None

    def test_multiple_tool_results_attached_by_id(self):
        rows = [
            _user_row("Q", uuid="user1"),
            _row("u1", "msg_A", "req_1", [
                {"type": "tool_use", "id": "t1", "name": "Read", "input": {}},
                {"type": "tool_use", "id": "t2", "name": "Bash", "input": {}},
            ]),
            {
                "type": "user", "uuid": "u_tr",
                "message": {"role": "user", "content": [
                    {"type": "tool_result", "tool_use_id": "t2", "content": "out2"},
                    {"type": "tool_result", "tool_use_id": "t1", "content": "out1"},
                ]},
            },
        ]
        turns = parse.build_turns(rows)
        tcs = {tc.id: tc.output for tc in turns[0].llm_calls[0].tool_calls}
        assert tcs == {"t1": "out1", "t2": "out2"}

    def test_synthetic_model_skipped(self):
        rows = [
            _user_row("Q", uuid="user1"),
            _row("u1", "msg_A", "req_1", [{"type": "text", "text": "real"}], model="claude-opus-4-7"),
            _row("u2", "msg_B", "req_2", [{"type": "text", "text": "syn"}], model="<synthetic>"),
        ]
        turns = parse.build_turns(rows)
        assert len(turns[0].llm_calls) == 1
        assert turns[0].llm_calls[0].model == "claude-opus-4-7"


# ── deterministic ids ────────────────────────────────────────────────────────

class TestDeterministicIds:
    def test_trace_id_stable(self):
        a = parse.trace_id("session-foo", "msg_first")
        b = parse.trace_id("session-foo", "msg_first")
        assert a == b
        assert len(a) == 32

    def test_trace_id_changes_with_session(self):
        a = parse.trace_id("session-foo", "msg_first")
        b = parse.trace_id("session-bar", "msg_first")
        assert a != b

    def test_trace_id_changes_with_message(self):
        a = parse.trace_id("session-foo", "msg_first")
        b = parse.trace_id("session-foo", "msg_other")
        assert a != b

    def test_obs_id_stable(self):
        a = parse.obs_id("session-foo", "msg_X")
        b = parse.obs_id("session-foo", "msg_X")
        assert a == b
        assert len(a) == 32

    def test_obs_id_distinct_namespace_from_trace(self):
        # Same inputs but different namespace prefix
        assert parse.trace_id("s", "m") != parse.obs_id("s", "m")
