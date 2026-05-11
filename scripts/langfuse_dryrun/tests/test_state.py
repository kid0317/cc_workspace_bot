"""Tests for state-file keying + concurrent-safe load/save."""
from __future__ import annotations

from pathlib import Path

import pytest

from lf_hook import state


class TestStateKey:
    def test_three_element_key_distinct(self):
        # Same framework session, same claude session, different transcripts → distinct keys
        a = state.compute_key("fs1", "cs1", "/p/a.jsonl")
        b = state.compute_key("fs1", "cs1", "/p/b.jsonl")
        assert a != b

    def test_same_inputs_same_key(self):
        a = state.compute_key("fs", "cs", "/p")
        b = state.compute_key("fs", "cs", "/p")
        assert a == b

    def test_framework_session_change_changes_key(self):
        # /new rotates framework session_id → must produce new key (no offset reuse)
        a = state.compute_key("fs1", "cs", "/p")
        b = state.compute_key("fs2", "cs", "/p")
        assert a != b


class TestStateLoadSave:
    def test_load_missing_file_returns_empty(self, tmp_path: Path):
        loaded = state.load(tmp_path / "nonexistent.json")
        assert loaded == {}

    def test_save_then_load_round_trip(self, tmp_path: Path):
        path = tmp_path / "state.json"
        state.save(path, {"key1": {"offset": 100, "turn_count": 5}})
        loaded = state.load(path)
        assert loaded == {"key1": {"offset": 100, "turn_count": 5}}

    def test_save_atomic_via_tmpfile(self, tmp_path: Path):
        # Save should write to .tmp then rename, not corrupt existing file
        path = tmp_path / "state.json"
        state.save(path, {"a": {"offset": 1}})
        state.save(path, {"a": {"offset": 2}})  # overwrite
        loaded = state.load(path)
        assert loaded["a"]["offset"] == 2

    def test_concurrent_writes_no_corruption(self, tmp_path: Path):
        # Sanity: two save() calls in sequence don't corrupt JSON
        # (real flock concurrency tested in integration; this guards against
        # accidental refactor that drops atomicity)
        path = tmp_path / "state.json"
        for i in range(20):
            state.save(path, {f"k{i}": {"offset": i}})
        loaded = state.load(path)
        assert loaded == {"k19": {"offset": 19}}


class TestUpdateOne:
    def test_update_one_key_preserves_others(self, tmp_path: Path):
        path = tmp_path / "state.json"
        state.save(path, {"a": {"offset": 1}, "b": {"offset": 2}})
        state.update_one(path, "a", {"offset": 99, "turn_count": 3})
        loaded = state.load(path)
        assert loaded["a"] == {"offset": 99, "turn_count": 3}
        assert loaded["b"] == {"offset": 2}

    def test_update_one_creates_key(self, tmp_path: Path):
        path = tmp_path / "state.json"
        state.update_one(path, "new", {"offset": 5})
        loaded = state.load(path)
        assert loaded == {"new": {"offset": 5}}
