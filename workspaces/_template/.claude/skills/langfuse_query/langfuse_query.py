#!/usr/bin/env python3
"""
langfuse_query.py — 查询当前 workspace 最近的可观测数据。

从 SESSION_CONTEXT.md 自动读取 workspace 信息，
通过 Langfuse REST API 拉取 traces / observations，
帮助 Claude 定位任务执行异常。

用法：
  python3 {_skill_base}/langfuse_query.py [选项]

选项：
  --mode traces|observations|session|errors   查询模式（默认 traces）
  --limit N                                    返回条数（默认 10）
  --session-id SID                             指定 session（默认读 SESSION_CONTEXT.md）
  --trace-id TID                               查单条 trace 的 observations
  --hours N                                    最近 N 小时（默认 24）
  --task-name NAME                             按任务名过滤（部分匹配 trace.name）
  --errors-only                                只显示含错误的 trace

依赖：SESSION_CONTEXT.md 中的 Session dir 字段
凭证：从 LANGFUSE_BASE_URL / LANGFUSE_PUBLIC_KEY / LANGFUSE_SECRET_KEY 环境变量读取
     或 workspace_dir/.claude/settings.local.json 中的 env 字段
"""

import argparse
import json
import os
import sys
import urllib.request
import urllib.parse
import base64
from datetime import datetime, timedelta, timezone
from pathlib import Path


# ── 凭证加载 ────────────────────────────────────────────────────────────────

def _load_creds(session_dir: Path) -> dict:
    """
    按优先级读取 Langfuse 凭证：
    1. 环境变量
    2. workspace .claude/settings.local.json
    """
    base_url = os.environ.get("LANGFUSE_BASE_URL", "")
    pub = os.environ.get("LANGFUSE_PUBLIC_KEY", "")
    sec = os.environ.get("LANGFUSE_SECRET_KEY", "")

    if not (base_url and pub and sec):
        # 向上查找 settings.local.json
        check = session_dir
        for _ in range(6):
            candidate = check / ".claude" / "settings.local.json"
            if candidate.exists():
                try:
                    cfg = json.loads(candidate.read_text())
                    env = cfg.get("env", {})
                    base_url = base_url or env.get("LANGFUSE_BASE_URL", "")
                    pub = pub or env.get("LANGFUSE_PUBLIC_KEY", "")
                    sec = sec or env.get("LANGFUSE_SECRET_KEY", "")
                    if base_url and pub and sec:
                        break
                except Exception:
                    pass
            check = check.parent
            if check == check.parent:
                break

    return {
        "base_url": base_url.rstrip("/") or "http://localhost:3000",
        "public_key": pub,
        "secret_key": sec,
    }


def _auth_header(pub: str, sec: str) -> str:
    token = base64.b64encode(f"{pub}:{sec}".encode()).decode()
    return f"Basic {token}"


# ── SESSION_CONTEXT.md 解析 ──────────────────────────────────────────────────

def _read_session_context(session_dir: Path) -> dict:
    ctx_file = session_dir / "SESSION_CONTEXT.md"
    result = {}
    if not ctx_file.exists():
        return result
    for line in ctx_file.read_text().splitlines():
        if line.startswith("- "):
            line = line[2:]
        if ": " in line:
            k, v = line.split(": ", 1)
            result[k.strip()] = v.strip()
    return result


# ── HTTP helper ──────────────────────────────────────────────────────────────

def _get(url: str, auth: str, params: dict = None) -> dict:
    if params:
        url = url + "?" + urllib.parse.urlencode({k: v for k, v in params.items() if v is not None})
    req = urllib.request.Request(url, headers={"Authorization": auth, "Accept": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read().decode())
    except urllib.error.HTTPError as e:
        body = e.read().decode()
        print(f"[HTTP {e.code}] {url}\n{body}", file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        print(f"[ERROR] {e}", file=sys.stderr)
        sys.exit(1)


# ── 格式化输出 ───────────────────────────────────────────────────────────────

def _ts(iso: str) -> str:
    """ISO → 本地可读时间"""
    if not iso:
        return "?"
    try:
        dt = datetime.fromisoformat(iso.replace("Z", "+00:00"))
        local = dt.astimezone()
        return local.strftime("%m-%d %H:%M:%S")
    except Exception:
        return iso[:19]


def _truncate(s: str, n: int = 200) -> str:
    if not s:
        return ""
    s = str(s)
    return s[:n] + "…" if len(s) > n else s


def _print_trace(t: dict, verbose: bool = False) -> None:
    name = t.get("name", "?")
    ts = _ts(t.get("timestamp", ""))
    trace_id = t.get("id", "")[:16]
    session_id = (t.get("sessionId") or "")[:16] or "—"
    level = t.get("level", "")
    status = "❌" if level in ("ERROR", "WARNING") else "✓"

    inp = _truncate(json.dumps(t.get("input", ""), ensure_ascii=False), 120)
    out = _truncate(json.dumps(t.get("output", ""), ensure_ascii=False), 120)

    print(f"\n{'─'*60}")
    print(f"{status} [{ts}] {name}")
    print(f"  trace_id : {trace_id}...")
    print(f"  session  : {session_id}...")
    if inp and inp != '""':
        print(f"  input    : {inp}")
    if out and out != '""' and out != 'null':
        print(f"  output   : {out}")

    if verbose:
        meta = t.get("metadata", {})
        if meta:
            print(f"  metadata : {_truncate(json.dumps(meta, ensure_ascii=False), 300)}")


def _print_observation(o: dict) -> None:
    name = o.get("name", "?")
    obs_type = o.get("type", "")
    ts = _ts(o.get("startTime", ""))
    level = o.get("level", "")
    status = "❌" if level in ("ERROR", "WARNING") else "  "

    inp = _truncate(json.dumps(o.get("input", ""), ensure_ascii=False), 150)
    out = _truncate(json.dumps(o.get("output", ""), ensure_ascii=False), 150)
    status_msg = o.get("statusMessage", "")

    print(f"  {status} [{ts}] [{obs_type}] {name}")
    if inp and inp not in ('""', 'null', '{}'):
        print(f"      input  : {inp}")
    if out and out not in ('""', 'null', '{}'):
        print(f"      output : {out}")
    if status_msg:
        print(f"      status : {status_msg}")


# ── 查询模式 ─────────────────────────────────────────────────────────────────

def mode_traces(api_base: str, auth: str, args, ctx: dict) -> None:
    """列出最近 traces"""
    since = (datetime.now(timezone.utc) - timedelta(hours=args.hours)).isoformat()

    params = {
        "limit": args.limit,
        "fromTimestamp": since,
    }
    if args.session_id:
        params["sessionId"] = args.session_id

    data = _get(f"{api_base}/api/public/traces", auth, params)
    traces = data.get("data", [])

    if args.errors_only:
        traces = [t for t in traces if t.get("level") in ("ERROR", "WARNING")]

    if args.task_name:
        traces = [t for t in traces if args.task_name.lower() in (t.get("name") or "").lower()]

    ws_dir = ctx.get("Workspace", "")
    print(f"\n=== Langfuse Traces  [workspace: {ws_dir or 'unknown'}] ===")
    print(f"过去 {args.hours}h，共 {len(traces)} 条 trace")

    for t in traces:
        _print_trace(t)

    if traces:
        print(f"\n提示：用 --mode observations --trace-id <id> 查看某条 trace 的工具调用详情")


def mode_observations(api_base: str, auth: str, args, ctx: dict) -> None:
    """查某条 trace 的所有 observations（工具调用 + 生成）"""
    trace_id = args.trace_id
    if not trace_id:
        print("请用 --trace-id 指定 trace id（从 --mode traces 输出中获取）", file=sys.stderr)
        sys.exit(1)

    # 先拿 trace 详情
    trace = _get(f"{api_base}/api/public/traces/{trace_id}", auth)
    print(f"\n=== Trace: {trace.get('name')} ===")
    print(f"time     : {_ts(trace.get('timestamp'))}")
    print(f"session  : {trace.get('sessionId', '—')}")
    inp = _truncate(json.dumps(trace.get("input", ""), ensure_ascii=False), 300)
    out = _truncate(json.dumps(trace.get("output", ""), ensure_ascii=False), 300)
    if inp and inp != '""':
        print(f"input    : {inp}")
    if out and out != '""' and out != 'null':
        print(f"output   : {out}")

    # observations
    obs_data = _get(f"{api_base}/api/public/observations", auth, {
        "traceId": trace_id,
        "limit": 100,
    })
    observations = obs_data.get("data", [])
    print(f"\nobservations ({len(observations)} 条):")
    for o in observations:
        _print_observation(o)


def mode_session(api_base: str, auth: str, args, ctx: dict) -> None:
    """查某个 session 的所有 traces"""
    sid = args.session_id or ctx.get("Channel key", "")
    if not sid:
        print("请用 --session-id 指定 session id", file=sys.stderr)
        sys.exit(1)

    data = _get(f"{api_base}/api/public/sessions/{sid}", auth)
    traces = data.get("traces", [])

    print(f"\n=== Session: {sid} ===")
    print(f"共 {len(traces)} 条 trace")
    for t in traces:
        _print_trace(t)


def mode_errors(api_base: str, auth: str, args, ctx: dict) -> None:
    """专门列出最近的错误 trace 和 observation"""
    since = (datetime.now(timezone.utc) - timedelta(hours=args.hours)).isoformat()

    # 先拿最近 traces
    data = _get(f"{api_base}/api/public/traces", auth, {
        "limit": 50,
        "fromTimestamp": since,
    })
    traces = data.get("data", [])

    print(f"\n=== 最近 {args.hours}h 异常概览 ===")

    error_count = 0
    for t in traces:
        obs_data = _get(f"{api_base}/api/public/observations", auth, {
            "traceId": t["id"],
            "limit": 100,
        })
        obs = obs_data.get("data", [])
        error_obs = [o for o in obs if o.get("level") in ("ERROR", "WARNING") or o.get("statusMessage")]

        if error_obs or t.get("level") in ("ERROR", "WARNING"):
            error_count += 1
            _print_trace(t, verbose=True)
            for o in error_obs:
                _print_observation(o)

    if error_count == 0:
        print("✓ 无异常 trace")
    else:
        print(f"\n共发现 {error_count} 条异常 trace")


# ── Entry point ──────────────────────────────────────────────────────────────

def main():
    parser = argparse.ArgumentParser(description="查询 Langfuse 可观测数据")
    parser.add_argument("--mode", choices=["traces", "observations", "session", "errors"],
                        default="traces")
    parser.add_argument("--limit", type=int, default=10)
    parser.add_argument("--session-id", default="")
    parser.add_argument("--trace-id", default="")
    parser.add_argument("--hours", type=int, default=24)
    parser.add_argument("--task-name", default="")
    parser.add_argument("--errors-only", action="store_true")
    parser.add_argument("--session-dir", default="")
    args = parser.parse_args()

    # 确定 session dir
    session_dir = Path(args.session_dir) if args.session_dir else Path.cwd()

    # 读 SESSION_CONTEXT.md
    ctx = _read_session_context(session_dir)

    # 加载凭证
    creds = _load_creds(session_dir)
    if not creds["public_key"] or not creds["secret_key"]:
        print("错误：未找到 Langfuse 凭证。请检查 .claude/settings.local.json 中的 LANGFUSE_PUBLIC_KEY / LANGFUSE_SECRET_KEY。",
              file=sys.stderr)
        sys.exit(1)

    auth = _auth_header(creds["public_key"], creds["secret_key"])
    api_base = creds["base_url"]

    # 路由到对应模式
    if args.mode == "traces":
        mode_traces(api_base, auth, args, ctx)
    elif args.mode == "observations":
        mode_observations(api_base, auth, args, ctx)
    elif args.mode == "session":
        mode_session(api_base, auth, args, ctx)
    elif args.mode == "errors":
        mode_errors(api_base, auth, args, ctx)


if __name__ == "__main__":
    main()
