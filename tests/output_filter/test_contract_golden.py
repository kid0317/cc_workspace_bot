"""Cross-language contract: Python extract_candidate must reproduce the exact
concatenation semantics of Go executor.parseLine over shared golden fixtures.

Fixtures live in internal/claude/testdata/golden_transcripts/ and are also
consumed by internal/claude/golden_test.go. Change them together.
"""

from pathlib import Path

import pytest

import output_filter as of
from conftest import GOLDEN_DIR


def golden_cases():
    files = sorted(GOLDEN_DIR.glob("*.jsonl"))
    assert files, f"no golden fixtures in {GOLDEN_DIR}"
    return files


@pytest.mark.parametrize("fixture", golden_cases(), ids=lambda p: p.stem)
def test_extraction_matches_go_parseline(fixture: Path):
    expected = fixture.with_suffix("").with_suffix("")  # strip .jsonl
    expected = Path(str(fixture)[: -len(".jsonl")] + ".expected.txt")
    want = expected.read_text(encoding="utf-8")
    got = of.extract_candidate(str(fixture))
    assert got == want
