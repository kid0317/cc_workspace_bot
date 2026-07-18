"""output_filter.py (Stop hook) — TDD spec: gates, extraction, layer2, e2e."""

import io
import json
import os
import stat
import sys
from pathlib import Path

import pytest

import output_filter as of

# ── helpers ──────────────────────────────────────────────────────────────────


def make_workspace(tmp_path: Path, status: str = "done") -> Path:
    ws = tmp_path / "ws"
    (ws / "memory").mkdir(parents=True)
    (ws / "memory" / "MEMORY.md").write_text(
        "# 陪伴记忆索引\n\n```\n"
        f"initialization_status: {status}             # pending | phase1_done | phase2_done | done\n"
        "```\n",
        encoding="utf-8",
    )
    sess = ws / "sessions" / "s1"
    sess.mkdir(parents=True)
    (sess / "SESSION_CONTEXT.md").write_text(
        f"# Session Context\n\n- App ID: test\n- Workspace: {ws}\n- Session dir: {sess}\n",
        encoding="utf-8",
    )
    return ws


def make_transcript(tmp_path: Path, blocks) -> Path:
    """blocks: list of assistant text-block lists, e.g. [["a"], ["b","c"]]."""
    lines = [json.dumps({"type": "system", "session_id": "x", "subtype": "init"})]
    for texts in blocks:
        content = [{"type": "text", "text": t} for t in texts]
        lines.append(json.dumps(
            {"type": "assistant", "message": {"role": "assistant", "content": content}},
            ensure_ascii=False))
    p = tmp_path / "transcript.jsonl"
    p.write_text("\n".join(lines), encoding="utf-8")
    return p


def make_stub_claude(tmp_path: Path, body: str) -> Path:
    stub = tmp_path / "stub_claude.sh"
    stub.write_text("#!/bin/bash\ncat > /dev/null\n" + body + "\n", encoding="utf-8")
    stub.chmod(stub.stat().st_mode | stat.S_IEXEC)
    return stub


def base_env(ws: Path) -> dict:
    return {
        "CC_LF_CHANNEL_KEY": "p2p:oc_x:app1",
        "CC_LF_TASK_NAME": "",
        "CC_FILTER_CWD": str(ws / "filter_cwd"),
    }


def run_main(ws: Path, transcript: Path, env: dict, monkeypatch) -> int:
    sess = ws / "sessions" / "s1"
    hook_input = {
        "session_id": "claude-sess-1",
        "transcript_path": str(transcript),
        "cwd": str(sess),
        "stop_hook_active": False,
        "hook_event_name": "Stop",
    }
    for k in ("CC_OUTPUT_FILTER", "CC_LF_CHANNEL_KEY", "CC_LF_TASK_NAME",
              "CC_FILTER_CLAUDE_BIN", "CC_FILTER_TIMEOUT", "CC_FILTER_CWD"):
        monkeypatch.delenv(k, raising=False)
    for k, v in env.items():
        monkeypatch.setenv(k, v)
    monkeypatch.setattr(sys, "stdin", io.StringIO(json.dumps(hook_input)))
    return of.main()


def read_final(ws: Path) -> str:
    return (ws / "sessions" / "s1" / "FINAL_REPLY.md").read_text(encoding="utf-8")


def final_exists(ws: Path) -> bool:
    return (ws / "sessions" / "s1" / "FINAL_REPLY.md").exists()


# ── parse_status contract ────────────────────────────────────────────────────


def test_parse_status_with_inline_comment_listing_all_states():
    text = "initialization_status: done             # pending | phase1_done | phase2_done | done"
    assert of.parse_status(text) == "done"


def test_parse_status_pending():
    assert of.parse_status("initialization_status: pending   # ...") == "pending"


def test_parse_status_missing_returns_none():
    assert of.parse_status("# 没有状态行的文件") is None


# ── candidate extraction (transcript parsing) ────────────────────────────────


def test_extract_concatenates_all_assistant_text_blocks(tmp_path):
    t = make_transcript(tmp_path, [["让我先读取记忆档案。"], ["嗯。今天怎么样。"]])
    assert of.extract_candidate(str(t)) == "让我先读取记忆档案。嗯。今天怎么样。"


def test_extract_skips_sidechain_lines(tmp_path):
    lines = [
        json.dumps({"type": "assistant", "isSidechain": True,
                    "message": {"role": "assistant",
                                "content": [{"type": "text", "text": "子agent内容"}]}},
                   ensure_ascii=False),
        json.dumps({"type": "assistant",
                    "message": {"role": "assistant",
                                "content": [{"type": "text", "text": "在。"}]}},
                   ensure_ascii=False),
    ]
    p = tmp_path / "t.jsonl"
    p.write_text("\n".join(lines), encoding="utf-8")
    assert of.extract_candidate(str(p)) == "在。"


def test_extract_tolerates_garbage_lines(tmp_path):
    p = tmp_path / "t.jsonl"
    p.write_text('not-json\n{"type":"assistant","message":{"content":[{"type":"text","text":"嗯。"}]}}',
                 encoding="utf-8")
    assert of.extract_candidate(str(p)) == "嗯。"


# ── gates ────────────────────────────────────────────────────────────────────


def test_gate_recursion_guard(tmp_path, monkeypatch):
    ws = make_workspace(tmp_path)
    t = make_transcript(tmp_path, [["让我先读取记忆档案。嗯。"]])
    env = base_env(ws)
    env["CC_OUTPUT_FILTER"] = "1"
    assert run_main(ws, t, env, monkeypatch) == 0
    assert not final_exists(ws)


def test_gate_task_run_skipped(tmp_path, monkeypatch):
    ws = make_workspace(tmp_path)
    t = make_transcript(tmp_path, [["让我先读取记忆档案。嗯。"]])
    env = base_env(ws)
    env["CC_LF_TASK_NAME"] = "proactive_reach"
    assert run_main(ws, t, env, monkeypatch) == 0
    assert not final_exists(ws)


def test_gate_no_channel_key_skipped(tmp_path, monkeypatch):
    ws = make_workspace(tmp_path)
    t = make_transcript(tmp_path, [["让我先读取记忆档案。嗯。"]])
    env = base_env(ws)
    env["CC_LF_CHANNEL_KEY"] = ""
    assert run_main(ws, t, env, monkeypatch) == 0
    assert not final_exists(ws)


def test_gate_init_not_done_skipped(tmp_path, monkeypatch):
    ws = make_workspace(tmp_path, status="phase2_done")
    t = make_transcript(tmp_path, [["让我先读取记忆档案。嗯。"]])
    stub = make_stub_claude(tmp_path, "echo '{\"remove\": []}'")
    env = base_env(ws)
    env["CC_FILTER_CLAUDE_BIN"] = str(stub)
    assert run_main(ws, t, env, monkeypatch) == 0
    assert not final_exists(ws)


def test_gate_snapshot_takes_precedence_over_memory_md(tmp_path, monkeypatch):
    # MEMORY.md 已是 done（轮内刚切换），但轮开始快照是 phase2_done → 本轮豁免
    ws = make_workspace(tmp_path, status="done")
    (ws / "sessions" / "s1" / ".init_status_at_turn_start").write_text(
        "phase2_done", encoding="utf-8")
    t = make_transcript(tmp_path, [["让我先读取记忆档案。嗯。"]])
    env = base_env(ws)
    assert run_main(ws, t, env, monkeypatch) == 0
    assert not final_exists(ws)


# ── end to end through main() ────────────────────────────────────────────────


def test_e2e_layer1_only_dirty_message(tmp_path, monkeypatch):
    """Layer 2 returns nothing extra; Layer 1 already removed the lead-in."""
    ws = make_workspace(tmp_path)
    t = make_transcript(
        tmp_path, [["让我先检查一下初始化状态和记忆档案。"], ["嗯。\n这两天都在玩。"]])
    stub = make_stub_claude(tmp_path, "echo '{\"remove\": []}'")
    env = base_env(ws)
    env["CC_FILTER_CLAUDE_BIN"] = str(stub)
    assert run_main(ws, t, env, monkeypatch) == 0
    out = read_final(ws)
    assert "初始化状态" not in out
    assert "嗯。" in out and "这两天都在玩。" in out


def test_e2e_layer2_removes_inline_op(tmp_path, monkeypatch):
    """行内混排句 Layer 1 抓不到的，由 Layer 2 序号删除。"""
    ws = make_workspace(tmp_path)
    t = make_transcript(tmp_path, [["嗯。我把这段悄悄记进档案里了。今天累不累。"]])
    # layer1 留下 3 句；stub 让 layer2 删第 2 句
    stub = make_stub_claude(tmp_path, "echo '{\"remove\": [2]}'")
    env = base_env(ws)
    env["CC_FILTER_CLAUDE_BIN"] = str(stub)
    assert run_main(ws, t, env, monkeypatch) == 0
    out = read_final(ws)
    assert out == "嗯。今天累不累。"


def test_e2e_clean_message_passes_through(tmp_path, monkeypatch):
    ws = make_workspace(tmp_path)
    text = "嗯，记住了。[[SEND]]你呢，今天累不累。"
    t = make_transcript(tmp_path, [[text]])
    stub = make_stub_claude(tmp_path, "echo '{\"remove\": []}'")
    env = base_env(ws)
    env["CC_FILTER_CLAUDE_BIN"] = str(stub)
    assert run_main(ws, t, env, monkeypatch) == 0
    assert read_final(ws) == text


def test_e2e_all_operational_falls_back_to_persona_reply(tmp_path, monkeypatch):
    ws = make_workspace(tmp_path)
    (ws / "memory" / "fallback_replies.yaml").write_text(
        "- 嗯。\n- 在。\n", encoding="utf-8")
    t = make_transcript(tmp_path, [["（执行后台记忆更新）"]])
    stub = make_stub_claude(tmp_path, "echo '{\"remove\": []}'")
    env = base_env(ws)
    env["CC_FILTER_CLAUDE_BIN"] = str(stub)
    assert run_main(ws, t, env, monkeypatch) == 0
    assert read_final(ws) in ("嗯。", "在。")


def test_e2e_layer2_failure_degrades_to_layer1(tmp_path, monkeypatch):
    ws = make_workspace(tmp_path)
    t = make_transcript(
        tmp_path, [["让我先检查一下初始化状态和记忆档案。"], ["嗯。在的。"]])
    stub = make_stub_claude(tmp_path, "echo 'NOT-JSON garbage'")
    env = base_env(ws)
    env["CC_FILTER_CLAUDE_BIN"] = str(stub)
    assert run_main(ws, t, env, monkeypatch) == 0
    out = read_final(ws)
    assert "初始化状态" not in out  # layer1 仍然生效
    assert "嗯。在的。" in out


def test_e2e_layer2_timeout_degrades(tmp_path, monkeypatch):
    ws = make_workspace(tmp_path)
    t = make_transcript(tmp_path, [["让我执行记忆更新。"], ["嗯。"]])
    stub = make_stub_claude(tmp_path, "sleep 5\necho '{\"remove\": []}'")
    env = base_env(ws)
    env["CC_FILTER_CLAUDE_BIN"] = str(stub)
    env["CC_FILTER_TIMEOUT"] = "1"
    assert run_main(ws, t, env, monkeypatch) == 0
    assert read_final(ws) == "嗯。"


def test_e2e_invalid_indices_degrade_to_layer1(tmp_path, monkeypatch):
    ws = make_workspace(tmp_path)
    text = "嗯。你呢。"
    t = make_transcript(tmp_path, [[text]])
    stub = make_stub_claude(tmp_path, "echo '{\"remove\": [7, 99]}'")
    env = base_env(ws)
    env["CC_FILTER_CLAUDE_BIN"] = str(stub)
    assert run_main(ws, t, env, monkeypatch) == 0
    assert read_final(ws) == text


def test_e2e_writes_filter_log(tmp_path, monkeypatch):
    ws = make_workspace(tmp_path)
    t = make_transcript(tmp_path, [["让我执行记忆更新。"], ["嗯。"]])
    stub = make_stub_claude(tmp_path, "echo '{\"remove\": []}'")
    env = base_env(ws)
    env["CC_FILTER_CLAUDE_BIN"] = str(stub)
    run_main(ws, t, env, monkeypatch)
    log = (ws / ".filter_log.jsonl").read_text(encoding="utf-8").strip()
    rec = json.loads(log.splitlines()[-1])
    assert rec["result"] == "filtered"
    assert rec["layer1_hits"] == 1
    # 变更轮必须存全文（误伤审计）
    assert "让我执行记忆更新" in rec["before"]
    assert rec["after"] == "嗯。"


def test_e2e_exit_zero_on_internal_crash(tmp_path, monkeypatch):
    """任何内部异常都不能阻塞主流程（exit 必须为 0）。"""
    ws = make_workspace(tmp_path)
    env = base_env(ws)
    # transcript 路径不存在 → 内部异常路径
    sess = ws / "sessions" / "s1"
    hook_input = {"transcript_path": "/nonexistent/x.jsonl", "cwd": str(sess),
                  "stop_hook_active": False}
    for k, v in env.items():
        monkeypatch.setenv(k, v)
    monkeypatch.setattr(sys, "stdin", io.StringIO(json.dumps(hook_input)))
    assert of.main() == 0
    assert not final_exists(ws)


def test_e2e_garbage_stdin_exits_zero(monkeypatch):
    monkeypatch.setattr(sys, "stdin", io.StringIO("not json at all"))
    assert of.main() == 0
