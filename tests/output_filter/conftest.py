"""Shared fixtures: make the companion template hooks importable."""

import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
HOOKS_DIR = REPO_ROOT / "companion_filter"
GOLDEN_DIR = REPO_ROOT / "internal" / "claude" / "testdata" / "golden_transcripts"

sys.path.insert(0, str(HOOKS_DIR))
