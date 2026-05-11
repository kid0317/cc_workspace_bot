#!/usr/bin/env python3
"""dry-run / golden-file CLI.

Two modes:

  Render mode (default):
    python3 run.py --fixture <path> [--session-id sid]
        → prints rendered JSON to stdout, exit 0

  Compare mode (P0 gate):
    python3 run.py --fixture <path> --expected <golden> [--session-id sid]
        → exit 0 iff render byte-equals golden; exit 1 with diff on mismatch

  Generate-golden mode (one-shot, used to seed expected/ files):
    python3 run.py --fixture <path> --write-golden <out> [--session-id sid]
        → writes render to <out>, exit 0

Exit codes:
  0  match / render OK
  1  diff found
  2  fixture or expected file missing
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))

from lf_hook import dryrun  # noqa: E402


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--fixture", required=True, type=Path)
    p.add_argument("--expected", type=Path, default=None)
    p.add_argument("--write-golden", type=Path, default=None)
    p.add_argument("--session-id", default="test-session")
    p.add_argument("--app-id", default="test-app")
    args = p.parse_args(argv)

    if not args.fixture.exists():
        print(f"fixture not found: {args.fixture}", file=sys.stderr)
        return 2

    rendered = dryrun.render_to_json(
        args.fixture,
        framework_session_id=args.session_id,
        app_id=args.app_id,
    )

    if args.write_golden:
        args.write_golden.parent.mkdir(parents=True, exist_ok=True)
        args.write_golden.write_text(rendered, encoding="utf-8")
        print(f"wrote golden: {args.write_golden}", file=sys.stderr)
        return 0

    if args.expected:
        if not args.expected.exists():
            print(f"expected not found: {args.expected}", file=sys.stderr)
            return 2
        expected_text = args.expected.read_text(encoding="utf-8")
        if rendered == expected_text:
            return 0
        import difflib
        diff = "\n".join(difflib.unified_diff(
            expected_text.splitlines(),
            rendered.splitlines(),
            fromfile=str(args.expected), tofile="actual", lineterm="",
        ))
        print(diff)
        return 1

    sys.stdout.write(rendered)
    return 0


if __name__ == "__main__":
    sys.exit(main())
