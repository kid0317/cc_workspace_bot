"""Tests for the dryrun renderer + path detection."""
from __future__ import annotations

import json
from pathlib import Path

import pytest

from lf_hook import dryrun


FIXTURES = Path(__file__).parent.parent / "fixtures"
EXPECTED = FIXTURES / "expected"


# ── path detection ──────────────────────────────────────────────────────────


class TestSubagentPathDetection:
    def test_main_transcript(self):
        assert not dryrun.is_subagent_path(
            "/root/.claude/projects/foo/abc123.jsonl")

    def test_subagent_transcript(self):
        assert dryrun.is_subagent_path(
            "/root/.claude/projects/foo/abc123/subagents/agent-xyz.jsonl")

    def test_subagent_compact(self):
        # acompact-prefixed sub-agent files (compaction agents)
        assert dryrun.is_subagent_path(
            "/root/.claude/projects/foo/sid/subagents/agent-acompact-1234.jsonl")


# ── fixture-based gates ─────────────────────────────────────────────────────


def _golden_compare(fixture_name: str, *, framework_session_id: str = "test-session"):
    """Render fixture, byte-compare against expected golden file."""
    fixture = FIXTURES / fixture_name
    # flatten path for the expected filename
    expected = EXPECTED / f"{fixture_name.replace('/', '_')}.expected.json"
    actual = dryrun.render_to_json(fixture, framework_session_id=framework_session_id)
    assert expected.exists(), (
        f"missing golden file {expected}; first run produced:\n{actual}"
    )
    expected_text = expected.read_text(encoding="utf-8")
    if actual != expected_text:
        # show diff for easy debugging
        import difflib
        diff = "\n".join(difflib.unified_diff(
            expected_text.splitlines(),
            actual.splitlines(),
            fromfile=str(expected), tofile="actual", lineterm="",
        ))
        pytest.fail(f"golden mismatch for {fixture_name}:\n{diff}")


@pytest.mark.parametrize("fixture", [
    "transcript_basic.jsonl",
    "transcript_multi_row_message.jsonl",
    "transcript_multi_turn.jsonl",
    "transcript_zero_usage_kimi.jsonl",
    "transcript_synthetic_skipped.jsonl",
    "parent-session/subagents/agent-test_subagent_kimi.jsonl",
])
def test_fixture_matches_golden(fixture):
    """P0 gate: each fixture renders byte-identically to its expected file."""
    _golden_compare(fixture)


# ── idempotency (P0 fixture #5) ─────────────────────────────────────────────


def test_idempotency_two_runs_identical():
    """Same fixture, same framework_session_id → identical output across runs."""
    fixture = FIXTURES / "transcript_multi_row_message.jsonl"
    a = dryrun.render_to_json(fixture, framework_session_id="sid-X")
    b = dryrun.render_to_json(fixture, framework_session_id="sid-X")
    assert a == b


def test_different_session_id_produces_different_trace_ids():
    fixture = FIXTURES / "transcript_basic.jsonl"
    a = json.loads(dryrun.render_to_json(fixture, framework_session_id="sid-A"))
    b = json.loads(dryrun.render_to_json(fixture, framework_session_id="sid-B"))
    a_ids = [t["id"] for t in a["traces"]]
    b_ids = [t["id"] for t in b["traces"]]
    assert a_ids != b_ids
    assert len(a_ids) == len(b_ids) > 0
