"""Tests for meta loading: env-var first, sidecar fallback, validation."""
from __future__ import annotations

import json
from pathlib import Path

import pytest

from lf_hook import meta


class TestLoadFromEnv:
    def test_full_env_returns_meta(self):
        env = {
            "CC_LF_META_VERSION": "1",
            "CC_LF_APP_ID": "yzk_worker",
            "CC_LF_FRAMEWORK_SESSION_ID": "sess-abc",
            "CC_LF_CHANNEL_KEY": "p2p:oc_xxx:cli_yyy",
            "CC_LF_USER_OPEN_ID": "ou_zzz",
            "CC_LF_TASK_NAME": "daily_briefing",
        }
        m = meta.load_from_env(env)
        assert m is not None
        assert m.app_id == "yzk_worker"
        assert m.framework_session_id == "sess-abc"
        assert m.channel_key == "p2p:oc_xxx:cli_yyy"
        assert m.user_open_id == "ou_zzz"
        assert m.task_name == "daily_briefing"

    def test_missing_required_returns_none(self):
        # Missing FRAMEWORK_SESSION_ID
        env = {
            "CC_LF_APP_ID": "x",
            "CC_LF_CHANNEL_KEY": "y",
            "CC_LF_USER_OPEN_ID": "z",
        }
        assert meta.load_from_env(env) is None

    def test_optional_task_name_absent(self):
        env = {
            "CC_LF_APP_ID": "x",
            "CC_LF_FRAMEWORK_SESSION_ID": "s",
            "CC_LF_CHANNEL_KEY": "c",
            "CC_LF_USER_OPEN_ID": "u",
        }
        m = meta.load_from_env(env)
        assert m is not None
        assert m.task_name is None

    def test_empty_env_returns_none(self):
        assert meta.load_from_env({}) is None


class TestLoadFromSidecar:
    def test_loads_valid_sidecar(self, tmp_path: Path):
        sidecar = tmp_path / ".langfuse_meta.json"
        sidecar.write_text(json.dumps({
            "app_id": "yzk",
            "framework_session_id": "fs1",
            "channel_key": "ck",
            "user_open_id": "u",
            "task_name": "t",
        }))
        m = meta.load_from_sidecar(tmp_path)
        assert m is not None
        assert m.app_id == "yzk"
        assert m.task_name == "t"

    def test_missing_file_returns_none(self, tmp_path: Path):
        assert meta.load_from_sidecar(tmp_path) is None

    def test_malformed_json_returns_none(self, tmp_path: Path):
        (tmp_path / ".langfuse_meta.json").write_text("{not json")
        assert meta.load_from_sidecar(tmp_path) is None


class TestEnvWinsOverSidecar:
    def test_env_takes_precedence(self, tmp_path: Path):
        env = {
            "CC_LF_APP_ID": "from-env",
            "CC_LF_FRAMEWORK_SESSION_ID": "fs",
            "CC_LF_CHANNEL_KEY": "ck",
            "CC_LF_USER_OPEN_ID": "u",
        }
        sidecar = tmp_path / ".langfuse_meta.json"
        sidecar.write_text(json.dumps({
            "app_id": "from-sidecar",
            "framework_session_id": "fs",
            "channel_key": "ck",
            "user_open_id": "u",
        }))
        m = meta.load(env=env, session_dir=tmp_path)
        assert m.app_id == "from-env"

    def test_sidecar_used_when_env_empty(self, tmp_path: Path):
        sidecar = tmp_path / ".langfuse_meta.json"
        sidecar.write_text(json.dumps({
            "app_id": "from-sidecar",
            "framework_session_id": "fs",
            "channel_key": "ck",
            "user_open_id": "u",
        }))
        m = meta.load(env={}, session_dir=tmp_path)
        assert m is not None
        assert m.app_id == "from-sidecar"
