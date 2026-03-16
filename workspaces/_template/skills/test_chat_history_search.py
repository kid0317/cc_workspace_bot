"""
TDD tests for chat_history_search.py

Run:  python3 -m pytest skills/test_chat_history_search.py -v
  or: python3 -m unittest skills/test_chat_history_search -v
"""

import os
import sqlite3
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone

# Allow running from workspace root or skills/ directory
_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, _HERE)

from chat_history_search import (  # noqa: E402
    format_output,
    query_history,
    read_channel_key,
)

# ── Helpers ──────────────────────────────────────────────────────────────────

def _utc(days_ago: int = 0, hours_ago: int = 0) -> str:
    """Return an ISO-8601 UTC timestamp N days/hours before now."""
    dt = datetime.now(timezone.utc) - timedelta(days=days_ago, hours=hours_ago)
    return dt.strftime("%Y-%m-%d %H:%M:%S")


def _make_db(path: str) -> None:
    """Create a minimal bot.db schema with seed data."""
    con = sqlite3.connect(path)
    con.executescript("""
        CREATE TABLE sessions (
            id TEXT PRIMARY KEY,
            channel_key TEXT NOT NULL,
            claude_session_id TEXT,
            status TEXT NOT NULL DEFAULT 'active',
            created_by TEXT,
            created_at DATETIME,
            updated_at DATETIME
        );
        CREATE TABLE messages (
            id TEXT PRIMARY KEY,
            session_id TEXT NOT NULL,
            sender_id TEXT,
            role TEXT NOT NULL,
            content TEXT,
            feishu_msg_id TEXT,
            created_at DATETIME
        );
    """)

    channel_a = "p2p:ou_alice:cli_app1"
    channel_b = "group:oc_team:cli_app1"

    # channel_a — two sessions
    con.execute("INSERT INTO sessions VALUES (?,?,?,?,?,?,?)",
                ("s1", channel_a, "cs1", "archived", "ou_alice", _utc(5), _utc(4)))
    con.execute("INSERT INTO sessions VALUES (?,?,?,?,?,?,?)",
                ("s2", channel_a, "cs2", "active",   "ou_alice", _utc(1), _utc(0)))

    # channel_b — one session
    con.execute("INSERT INTO sessions VALUES (?,?,?,?,?,?,?)",
                ("s3", channel_b, "cs3", "active",   "ou_bob",   _utc(2), _utc(0)))

    # Messages for s1
    con.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?)",
                ("m1", "s1", "ou_alice", "user",      "帮我分析今天行情", "", _utc(5)))
    con.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?)",
                ("m2", "s1", "",         "assistant", "今日沪指收跌",      "", _utc(5)))

    # Messages for s2
    con.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?)",
                ("m3", "s2", "ou_alice", "user",      "投资组合怎么调整",  "", _utc(1)))
    con.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?)",
                ("m4", "s2", "",         "assistant", "建议增配债券",       "", _utc(1)))

    # Messages for s3 (different channel — must NOT appear in channel_a queries)
    con.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?)",
                ("m5", "s3", "ou_bob",   "user",      "团队每日站会",       "", _utc(2)))

    con.commit()
    con.close()


def _make_session_context(session_dir: str, channel_key: str, db_path: str = "/data/bot.db") -> None:
    """Write a minimal SESSION_CONTEXT.md into session_dir."""
    content = (
        "# Session Context\n\n"
        "- App ID: app1\n"
        "- Current date: 2026-03-16\n"
        "- Workspace: /workspace/app1\n"
        f"- Channel key: {channel_key}\n"
        f"- DB path: {db_path}\n"
    )
    os.makedirs(session_dir, exist_ok=True)
    with open(os.path.join(session_dir, "SESSION_CONTEXT.md"), "w") as f:
        f.write(content)


# ── TestReadChannelKey ────────────────────────────────────────────────────────

class TestReadChannelKey(unittest.TestCase):
    """read_channel_key() reads channel_key from SESSION_CONTEXT.md.

    This is the security-critical function: the script derives the channel scope
    from the framework-injected file, not from user-supplied arguments.
    """

    def setUp(self):
        self.session_dir = tempfile.mkdtemp()

    def tearDown(self):
        import shutil
        shutil.rmtree(self.session_dir, ignore_errors=True)

    def test_reads_p2p_channel_key(self):
        _make_session_context(self.session_dir, "p2p:ou_alice:cli_app1")
        self.assertEqual(read_channel_key(self.session_dir), "p2p:ou_alice:cli_app1")

    def test_reads_group_channel_key(self):
        _make_session_context(self.session_dir, "group:oc_team:cli_app1")
        self.assertEqual(read_channel_key(self.session_dir), "group:oc_team:cli_app1")

    def test_missing_session_context_raises(self):
        empty_dir = tempfile.mkdtemp()
        try:
            with self.assertRaises(FileNotFoundError):
                read_channel_key(empty_dir)
        finally:
            import shutil
            shutil.rmtree(empty_dir)

    def test_missing_channel_key_line_raises(self):
        """SESSION_CONTEXT.md exists but has no Channel key line."""
        ctx = os.path.join(self.session_dir, "SESSION_CONTEXT.md")
        with open(ctx, "w") as f:
            f.write("# Session Context\n\n- App ID: app1\n")
        with self.assertRaises(ValueError):
            read_channel_key(self.session_dir)

    def test_empty_channel_key_raises(self):
        """Channel key line exists but value is blank."""
        ctx = os.path.join(self.session_dir, "SESSION_CONTEXT.md")
        with open(ctx, "w") as f:
            f.write("# Session Context\n\n- Channel key: \n")
        with self.assertRaises(ValueError):
            read_channel_key(self.session_dir)


# ── TestQueryHistory ──────────────────────────────────────────────────────────

class TestQueryHistory(unittest.TestCase):

    def setUp(self):
        self.tmp = tempfile.NamedTemporaryFile(suffix=".db", delete=False)
        self.tmp.close()
        _make_db(self.tmp.name)
        self.db = self.tmp.name
        self.ch = "p2p:ou_alice:cli_app1"

    def tearDown(self):
        os.unlink(self.tmp.name)

    # ── channel isolation ────────────────────────────────────────

    def test_returns_only_matching_channel(self):
        rows = query_history(self.db, self.ch)
        session_ids = {r["session_id"] for r in rows}
        self.assertIn("s1", session_ids)
        self.assertIn("s2", session_ids)
        self.assertNotIn("s3", session_ids)

    def test_wrong_channel_returns_empty(self):
        rows = query_history(self.db, "p2p:ou_nobody:cli_app1")
        self.assertEqual(rows, [])

    # ── keyword filter ───────────────────────────────────────────

    def test_keyword_filters_content(self):
        rows = query_history(self.db, self.ch, keyword="行情")
        contents = [r["content"] for r in rows]
        self.assertTrue(any("行情" in c for c in contents))
        self.assertFalse(any("投资组合" in c for c in contents))

    def test_keyword_no_match_returns_empty(self):
        rows = query_history(self.db, self.ch, keyword="zzz_no_match_zzz")
        self.assertEqual(rows, [])

    # ── role filter ──────────────────────────────────────────────

    def test_role_user_only(self):
        rows = query_history(self.db, self.ch, role="user")
        self.assertTrue(all(r["role"] == "user" for r in rows))
        self.assertGreater(len(rows), 0)

    def test_role_assistant_only(self):
        rows = query_history(self.db, self.ch, role="assistant")
        self.assertTrue(all(r["role"] == "assistant" for r in rows))
        self.assertGreater(len(rows), 0)

    def test_role_all_returns_both(self):
        rows = query_history(self.db, self.ch, role="all")
        roles = {r["role"] for r in rows}
        self.assertIn("user", roles)
        self.assertIn("assistant", roles)

    # ── days filter ──────────────────────────────────────────────

    def test_days_excludes_old_sessions(self):
        rows = query_history(self.db, self.ch, days=3)
        session_ids = {r["session_id"] for r in rows}
        self.assertNotIn("s1", session_ids)
        self.assertIn("s2", session_ids)

    def test_days_includes_recent_sessions(self):
        rows = query_history(self.db, self.ch, days=7)
        session_ids = {r["session_id"] for r in rows}
        self.assertIn("s1", session_ids)
        self.assertIn("s2", session_ids)

    # ── limit ────────────────────────────────────────────────────

    def test_limit_caps_results(self):
        rows = query_history(self.db, self.ch, limit=1)
        self.assertEqual(len(rows), 1)

    def test_limit_default_not_exceeded(self):
        rows = query_history(self.db, self.ch)
        self.assertLessEqual(len(rows), 50)

    # ── content truncation ───────────────────────────────────────

    def test_long_content_truncated(self):
        con = sqlite3.connect(self.db)
        long_text = "x" * 600
        con.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?)",
                    ("m_long", "s2", "ou_alice", "user", long_text, "", _utc(0)))
        con.commit()
        con.close()

        rows = query_history(self.db, self.ch)
        long_rows = [r for r in rows if r["message_id"] == "m_long"]
        self.assertEqual(len(long_rows), 1)
        self.assertLessEqual(len(long_rows[0]["content"]), 520)
        self.assertIn("[已截断", long_rows[0]["content"])

    # ── db not found ─────────────────────────────────────────────

    def test_db_not_found_raises(self):
        with self.assertRaises(FileNotFoundError):
            query_history("/nonexistent/path/bot.db", self.ch)

    # ── sessions-only mode ───────────────────────────────────────

    def test_sessions_only_mode(self):
        rows = query_history(self.db, self.ch, sessions_only=True)
        for row in rows:
            self.assertIn("session_id", row)
            self.assertNotIn("content", row)


# ── TestFormatOutput ──────────────────────────────────────────────────────────

class TestFormatOutput(unittest.TestCase):

    def _make_rows(self):
        return [
            {"session_id": "s1", "session_status": "archived",
             "session_created_at": "2026-03-11 09:00:00",
             "session_updated_at": "2026-03-11 18:00:00",
             "message_id": "m1", "role": "user",      "content": "问题1",
             "created_at": "2026-03-11 09:00:00"},
            {"session_id": "s1", "session_status": "archived",
             "session_created_at": "2026-03-11 09:00:00",
             "session_updated_at": "2026-03-11 18:00:00",
             "message_id": "m2", "role": "assistant", "content": "回答1",
             "created_at": "2026-03-11 09:01:00"},
        ]

    def test_output_contains_channel_key(self):
        out = format_output(self._make_rows(), "p2p:ou_alice:cli_app1", days=7)
        self.assertIn("p2p:ou_alice:cli_app1", out)

    def test_output_contains_session_id(self):
        out = format_output(self._make_rows(), "p2p:ou_alice:cli_app1", days=7)
        self.assertIn("s1", out)

    def test_output_contains_role_labels(self):
        out = format_output(self._make_rows(), "p2p:ou_alice:cli_app1", days=7)
        self.assertIn("user", out)
        self.assertIn("assistant", out)

    def test_empty_rows_returns_no_results_message(self):
        out = format_output([], "p2p:ou_alice:cli_app1", days=7)
        self.assertIn("未找到", out)

    def test_sessions_only_no_content(self):
        rows = [
            {"session_id": "s1", "session_status": "active",
             "session_created_at": "2026-03-15 10:00:00",
             "session_updated_at": "2026-03-15 12:00:00",
             "message_count": 5},
        ]
        out = format_output(rows, "p2p:ou_alice:cli_app1", days=7, sessions_only=True)
        self.assertIn("s1", out)
        self.assertIn("5", out)


if __name__ == "__main__":
    unittest.main()
