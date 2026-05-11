"""Integration tests for emit + main with a mocked Langfuse SDK.

Tests verify that:
- emit calls Langfuse with the right id/usage_details/tags
- main reads stdin payload, advances offset, handles fail-open
- per-turn errors are isolated
"""
from __future__ import annotations

import json
import os
import sys
from contextlib import contextmanager, nullcontext
from io import StringIO
from pathlib import Path
from unittest import mock

import pytest

from lf_hook import emit, main as main_mod, meta, parse


FIXTURES = Path(__file__).parent.parent / "fixtures"


# ── Fake Langfuse SDK ───────────────────────────────────────────────────────


class FakeObservation:
    def __init__(self, parent_calls: list, **kwargs):
        self.kwargs = kwargs
        self.parent_calls = parent_calls
        self.updates = []

    def update(self, **kwargs):
        self.updates.append(kwargs)

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False


class FakeLangfuse:
    def __init__(self):
        self.observations: list[FakeObservation] = []
        self.flushed = False
        self.shutdown_called = False

    def start_as_current_observation(self, **kwargs):
        ob = FakeObservation(self.observations, **kwargs)
        self.observations.append(ob)
        return ob

    def flush(self, timeout=None):
        self.flushed = True

    def shutdown(self):
        self.shutdown_called = True


@contextmanager
def fake_propagate_attributes(**kwargs):
    yield


# ── emit_turns ──────────────────────────────────────────────────────────────


def _load_rows(fixture: str) -> list[dict]:
    rows = []
    with open(FIXTURES / fixture, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                rows.append(json.loads(line))
    return rows


def _meta(**overrides):
    return meta.Meta(
        app_id=overrides.get("app_id", "test-app"),
        framework_session_id=overrides.get("framework_session_id", "sess-test"),
        channel_key=overrides.get("channel_key", "ck"),
        user_open_id=overrides.get("user_open_id", "u"),
        task_name=overrides.get("task_name"),
    )


class TestEmitTurns:
    def test_emits_one_trace_per_turn(self):
        rows = _load_rows("transcript_multi_turn.jsonl")
        turns = parse.build_turns(rows)
        lf = FakeLangfuse()
        n = emit.emit_turns(lf, turns, _meta(), "/p/t.jsonl", rows,
                            propagate_attributes_fn=fake_propagate_attributes)
        assert n == 2
        # 2 turns × (1 span + 1 generation) = 4 observations minimum
        spans = [o for o in lf.observations if o.kwargs.get("as_type") == "span"]
        gens = [o for o in lf.observations if o.kwargs.get("as_type") == "generation"]
        assert len(spans) == 2
        assert len(gens) == 2

    def test_generation_carries_usage_details(self):
        rows = _load_rows("transcript_basic.jsonl")
        turns = parse.build_turns(rows)
        lf = FakeLangfuse()
        emit.emit_turns(lf, turns, _meta(), "/p/t.jsonl", rows,
                        propagate_attributes_fn=fake_propagate_attributes)
        gen = next(o for o in lf.observations if o.kwargs.get("as_type") == "generation")
        assert gen.kwargs["model"] == "claude-sonnet-4-6"
        assert gen.kwargs["usage_details"] == {"input": 10, "output": 5}

    def test_subagent_path_yields_subagent_kind_tags(self):
        rows = _load_rows("parent-session/subagents/agent-test_subagent_kimi.jsonl")
        turns = parse.build_turns(rows)
        lf = FakeLangfuse()
        with mock.patch.object(emit, "_emit_one_turn") as m:
            emit.emit_turns(
                lf, turns, _meta(),
                "/foo/parent/subagents/agent-X.jsonl",  # path triggers subagent detection
                rows, propagate_attributes_fn=fake_propagate_attributes,
            )
            args, kwargs = m.call_args
            tags = args[3]  # tags positional
            assert "kind:subagent" in tags
            assert any(t.startswith("agent:") for t in tags)

    def test_main_path_yields_main_kind_tag(self):
        rows = _load_rows("transcript_basic.jsonl")
        turns = parse.build_turns(rows)
        lf = FakeLangfuse()
        with mock.patch.object(emit, "_emit_one_turn") as m:
            emit.emit_turns(lf, turns, _meta(), "/foo/main.jsonl", rows,
                            propagate_attributes_fn=fake_propagate_attributes)
            tags = m.call_args[0][3]
            assert "kind:main" in tags
            assert not any(t.startswith("agent:") for t in tags)

    def test_per_turn_exception_does_not_block_others(self):
        rows = _load_rows("transcript_multi_turn.jsonl")
        turns = parse.build_turns(rows)
        assert len(turns) == 2

        class ExplodingLF(FakeLangfuse):
            def __init__(self):
                super().__init__()
                self.calls = 0

            def start_as_current_observation(self, **kwargs):
                self.calls += 1
                if self.calls == 1:  # blow up only on first turn's span
                    raise RuntimeError("boom")
                return super().start_as_current_observation(**kwargs)

        lf = ExplodingLF()
        n = emit.emit_turns(lf, turns, _meta(), "/p/t.jsonl", rows,
                            propagate_attributes_fn=fake_propagate_attributes)
        assert n == 1   # second turn still emitted

    def test_task_name_in_tags(self):
        rows = _load_rows("transcript_basic.jsonl")
        turns = parse.build_turns(rows)
        lf = FakeLangfuse()
        with mock.patch.object(emit, "_emit_one_turn") as m:
            emit.emit_turns(lf, turns, _meta(task_name="daily_briefing"),
                            "/p/t.jsonl", rows,
                            propagate_attributes_fn=fake_propagate_attributes)
            tags = m.call_args[0][3]
            assert "task:daily_briefing" in tags


# ── main entrypoint (fail-open semantics) ──────────────────────────────────


class TestMain:
    def test_no_payload_returns_0(self, monkeypatch):
        monkeypatch.setattr(sys, "stdin", StringIO(""))
        assert main_mod.main(stdin=StringIO("")) == 0

    def test_missing_transcript_returns_0(self, monkeypatch):
        payload = json.dumps({"session_id": "x", "transcript_path": "/nonexistent"})
        assert main_mod.main(stdin=StringIO(payload)) == 0

    def test_no_meta_returns_0(self, monkeypatch, tmp_path):
        # Strip any CC_LF_* env so meta load returns None
        for k in list(os.environ):
            if k.startswith("CC_LF_"):
                monkeypatch.delenv(k, raising=False)
        # Use a real fixture so transcript_path exists, but no env / sidecar nearby
        fixture = FIXTURES / "transcript_basic.jsonl"
        payload = json.dumps({"session_id": "claude-sid-1", "transcript_path": str(fixture)})
        # Must not raise; must return 0
        assert main_mod.main(stdin=StringIO(payload)) == 0

    def test_advances_offset_and_emits(self, monkeypatch, tmp_path):
        # Inject env meta
        monkeypatch.setenv("CC_LF_APP_ID", "test-app")
        monkeypatch.setenv("CC_LF_FRAMEWORK_SESSION_ID", "fs-1")
        monkeypatch.setenv("CC_LF_CHANNEL_KEY", "ck")
        monkeypatch.setenv("CC_LF_USER_OPEN_ID", "u")
        monkeypatch.setenv("TRACE_TO_LANGFUSE", "true")
        monkeypatch.setenv("LANGFUSE_PUBLIC_KEY", "pk")
        monkeypatch.setenv("LANGFUSE_SECRET_KEY", "sk")
        # Redirect state file to tmp
        monkeypatch.setattr(main_mod, "STATE_DIR", tmp_path)
        monkeypatch.setattr(main_mod, "LOG_FILE", tmp_path / "h.log")
        monkeypatch.setattr(main_mod, "STATE_FILE", tmp_path / "state.json")
        # Patch Langfuse SDK init
        fake_lf = FakeLangfuse()
        monkeypatch.setattr(main_mod, "_init_langfuse",
                            lambda: (fake_lf, fake_propagate_attributes))

        fixture = FIXTURES / "transcript_basic.jsonl"
        payload = json.dumps({"session_id": "cs-1", "transcript_path": str(fixture)})
        rc = main_mod.main(stdin=StringIO(payload))
        assert rc == 0
        # Should have emitted at least one observation
        gens = [o for o in fake_lf.observations if o.kwargs.get("as_type") == "generation"]
        assert len(gens) == 1
        # State should advance offset
        from lf_hook import state as state_mod
        st = state_mod.load(tmp_path / "state.json")
        assert len(st) == 1
        for v in st.values():
            assert v["offset"] > 0
