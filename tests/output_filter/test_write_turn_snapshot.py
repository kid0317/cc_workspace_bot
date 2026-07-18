"""write_turn_snapshot.py（UserPromptSubmit hook）— TDD spec。

把轮开始时的 initialization_status 写入 session 目录快照，
供 Stop hook 门控使用（消除轮内 done 切换竞态，产品-C2）。
"""

import io
import json
import sys
from pathlib import Path

import write_turn_snapshot as wts


def make_ws(tmp_path: Path, status: str = "phase2_done") -> Path:
    ws = tmp_path / "ws"
    (ws / "memory").mkdir(parents=True)
    (ws / "memory" / "MEMORY.md").write_text(
        f"```\ninitialization_status: {status}   # pending | phase1_done | phase2_done | done\n```\n",
        encoding="utf-8")
    sess = ws / "sessions" / "s1"
    sess.mkdir(parents=True)
    (sess / "SESSION_CONTEXT.md").write_text(
        f"- Workspace: {ws}\n", encoding="utf-8")
    return ws


def run(sess: Path, monkeypatch, capsys) -> int:
    monkeypatch.setattr(sys, "stdin", io.StringIO(json.dumps({"cwd": str(sess)})))
    code = wts.main()
    # UserPromptSubmit hook 的 stdout 会被注入主 agent 上下文 → 必须零输出
    assert capsys.readouterr().out == ""
    return code


def test_writes_snapshot(tmp_path, monkeypatch, capsys):
    ws = make_ws(tmp_path, "phase2_done")
    sess = ws / "sessions" / "s1"
    assert run(sess, monkeypatch, capsys) == 0
    snap = sess / ".init_status_at_turn_start"
    assert snap.read_text(encoding="utf-8").strip() == "phase2_done"


def test_overwrites_previous_turn_snapshot(tmp_path, monkeypatch, capsys):
    ws = make_ws(tmp_path, "done")
    sess = ws / "sessions" / "s1"
    (sess / ".init_status_at_turn_start").write_text("pending", encoding="utf-8")
    assert run(sess, monkeypatch, capsys) == 0
    assert (sess / ".init_status_at_turn_start").read_text(
        encoding="utf-8").strip() == "done"


def test_missing_memory_md_no_snapshot_exit_zero(tmp_path, monkeypatch, capsys):
    ws = make_ws(tmp_path)
    (ws / "memory" / "MEMORY.md").unlink()
    sess = ws / "sessions" / "s1"
    assert run(sess, monkeypatch, capsys) == 0
    assert not (sess / ".init_status_at_turn_start").exists()


def test_garbage_stdin_exit_zero(monkeypatch, capsys):
    monkeypatch.setattr(sys, "stdin", io.StringIO("garbage"))
    assert wts.main() == 0
    assert capsys.readouterr().out == ""
