"""Pure-function parsing layer for the Langfuse hook.

Extracts logical LLM calls from Claude Code JSONL transcripts:
  - merges multi-row messages by (message_id, request_id)
  - normalizes Anthropic usage to Langfuse usage_details keys
  - estimates usage from text when the meter reports all-zero (bailian sub-agent case)
  - splits the row stream into turns by real user messages

No I/O, no Langfuse SDK — kept testable in isolation.
"""
from __future__ import annotations

import hashlib
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any

SKIP_MODELS: frozenset[str | None] = frozenset({"<synthetic>", "", None})

_USAGE_KEYS_FOR_ZERO_CHECK = (
    "input_tokens",
    "output_tokens",
    "cache_creation_input_tokens",
    "cache_read_input_tokens",
)


# ── data structures ─────────────────────────────────────────────────────────


@dataclass
class ToolCall:
    id: str
    name: str
    input: Any
    output: str | None = None


@dataclass(frozen=True)
class LLMCall:
    message_id: str
    request_id: str
    model: str
    output_text: str
    tool_calls: tuple[ToolCall, ...]
    usage: dict[str, int]          # normalized usage_details
    usage_source: str              # "reported" | "estimated_char"
    started_at: str | None         # iso timestamp from JSONL


@dataclass
class Turn:
    turn_num: int
    user_text: str
    llm_calls: list[LLMCall] = field(default_factory=list)


# ── normalize_usage ─────────────────────────────────────────────────────────


def normalize_usage(raw: dict | None) -> dict[str, int]:
    """Anthropic usage → Langfuse usage_details keys. Preserves 0 values."""
    if not raw:
        return {}
    out: dict[str, int] = {}
    if (v := raw.get("input_tokens")) is not None:
        out["input"] = v
    if (v := raw.get("output_tokens")) is not None:
        out["output"] = v

    cc = raw.get("cache_creation") or {}
    cc5 = cc.get("ephemeral_5m_input_tokens")
    cc1h = cc.get("ephemeral_1h_input_tokens")
    if cc5 is None and cc1h is None:
        legacy = raw.get("cache_creation_input_tokens")
        if legacy is not None:
            out["input_cache_creation_5m"] = legacy
    else:
        if cc5 is not None:
            out["input_cache_creation_5m"] = cc5
        if cc1h is not None:
            out["input_cache_creation_1h"] = cc1h

    if (v := raw.get("cache_read_input_tokens")) is not None:
        out["input_cache_read"] = v
    return out


# ── zero-usage detection + char estimation ──────────────────────────────────


def is_zero_usage(raw: dict | None) -> bool:
    """True if every known Anthropic usage field is 0 or absent."""
    if not raw:
        return True
    return all(raw.get(k, 0) == 0 for k in _USAGE_KEYS_FOR_ZERO_CHECK)


def _approx_tokens(s: str) -> int:
    if not s:
        return 0
    ascii_chars = sum(1 for c in s if ord(c) < 128)
    cjk_chars = len(s) - ascii_chars
    return cjk_chars + ascii_chars // 4


def estimate_usage_from_text(input_text: str, output_text: str) -> dict[str, int]:
    """Char-based fallback when the upstream meter is broken (bailian sub-agent case)."""
    return {
        "input": _approx_tokens(input_text or ""),
        "output": _approx_tokens(output_text or ""),
    }


# ── content merging by (message_id, request_id) ─────────────────────────────


def merge_assistant_rows(rows: list[dict]) -> dict[tuple[str, str], dict]:
    """Group JSONL assistant rows by (message.id, requestId), concatenating content blocks.

    Same (mid, rid) across rows = same Anthropic API response, just split into multiple
    JSONL rows (one block per row in production data). We merge blocks in row order.
    Usage is taken from the first row; if a later row disagrees, log via WARN sink (not here).
    """
    merged: dict[tuple[str, str], dict] = {}
    for row in rows:
        if row.get("type") != "assistant":
            continue
        msg = row.get("message") or {}
        mid = msg.get("id")
        if not mid:
            continue
        rid = row.get("requestId") or ""
        key = (mid, rid)
        new_blocks = msg.get("content") or []
        if key not in merged:
            merged[key] = {
                "message_id": mid,
                "request_id": rid,
                "model": msg.get("model"),
                "usage": dict(msg.get("usage") or {}),
                # JSONL row order is the canonical Anthropic block order; if a future
                # SDK starts emitting blocks out of order this assumption breaks.
                "content_blocks": list(new_blocks),
                "first_seen_at": row.get("timestamp"),
                "first_uuid": row.get("uuid"),
            }
        else:
            merged[key]["content_blocks"].extend(new_blocks)
    return merged


# ── turn building ───────────────────────────────────────────────────────────


def _extract_content(row: dict) -> Any:
    msg = row.get("message")
    if isinstance(msg, dict) and "content" in msg:
        return msg["content"]
    return row.get("content")


def _is_tool_result_only(content: Any) -> bool:
    if not isinstance(content, list) or not content:
        return False
    return all(
        isinstance(b, dict) and b.get("type") == "tool_result"
        for b in content
    )


def _stringify_tool_result_content(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = [b.get("text", "") for b in content
                 if isinstance(b, dict) and b.get("type") == "text"]
        return "\n".join(parts)
    return ""


def _extract_tool_results(content: Any) -> dict[str, str]:
    """tool_use_id → stringified output. Marks is_error results with a prefix."""
    out: dict[str, str] = {}
    if not isinstance(content, list):
        return out
    for b in content:
        if not isinstance(b, dict) or b.get("type") != "tool_result":
            continue
        tid = b.get("tool_use_id")
        if not tid or tid in out:
            continue
        text = _stringify_tool_result_content(b.get("content"))
        if b.get("is_error"):
            text = f"[error] {text}"
        out[tid] = text
    return out


def _extract_user_text(row: dict) -> str:
    content = _extract_content(row)
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = [b.get("text", "") for b in content
                 if isinstance(b, dict) and b.get("type") == "text"]
        return "\n".join(parts)
    return ""


def _build_llm_call(merged_entry: dict, user_text_for_estimate: str = "") -> LLMCall:
    blocks = merged_entry["content_blocks"]
    text_parts: list[str] = []
    tool_calls: list[ToolCall] = []
    for b in blocks:
        if not isinstance(b, dict):
            continue
        btype = b.get("type")
        if btype == "text":
            t = b.get("text", "")
            if t:
                text_parts.append(t)
        elif btype == "tool_use":
            tool_calls.append(ToolCall(
                id=b.get("id", ""),
                name=b.get("name", ""),
                input=b.get("input"),
            ))
    output_text = "\n".join(text_parts)

    raw_usage = merged_entry["usage"]
    if is_zero_usage(raw_usage):
        # Use the turn's user_text as input proxy. Imperfect (doesn't include accumulated
        # tool_results in long turns) but bounded and clearly marked via usage_source.
        usage = estimate_usage_from_text(user_text_for_estimate, output_text)
        usage_source = "estimated_char"
    else:
        usage = normalize_usage(raw_usage)
        usage_source = "reported"

    return LLMCall(
        message_id=merged_entry["message_id"],
        request_id=merged_entry["request_id"],
        model=merged_entry["model"] or "",
        output_text=output_text,
        tool_calls=tuple(tool_calls),
        usage=usage,
        usage_source=usage_source,
        started_at=merged_entry["first_seen_at"],
    )


def build_turns(rows: list[dict]) -> list[Turn]:
    """Split row stream into turns. Each turn = one real user msg + N merged LLM calls."""
    merged = merge_assistant_rows(rows)
    uuid_to_idx = {r.get("uuid"): i for i, r in enumerate(rows) if r.get("uuid")}
    merged_sorted = sorted(
        merged.values(),
        key=lambda m: uuid_to_idx.get(m["first_uuid"], 10**9),
    )

    turns: list[Turn] = []
    cur_user = ""
    cur_calls: list[LLMCall] = []
    iter_merged = iter(merged_sorted)
    next_call = next(iter_merged, None)

    def _flush_calls_up_to(row_idx: int):
        nonlocal next_call, cur_calls
        while next_call and uuid_to_idx.get(next_call["first_uuid"], 10**9) <= row_idx:
            if next_call["model"] not in SKIP_MODELS:
                cur_calls.append(_build_llm_call(next_call, user_text_for_estimate=cur_user))
            next_call = next(iter_merged, None)

    pending_tool_calls: dict[str, ToolCall] = {}

    def _register_tool_calls(calls: list[LLMCall]) -> None:
        for c in calls:
            for tc in c.tool_calls:
                if tc.id:
                    pending_tool_calls[tc.id] = tc

    def _apply_tool_results(content: Any) -> None:
        for tid, text in _extract_tool_results(content).items():
            tc = pending_tool_calls.get(tid)
            if tc is not None and tc.output is None:
                tc.output = text

    for i, row in enumerate(rows):
        rtype = row.get("type")
        if rtype == "user":
            content = _extract_content(row)
            if not _is_tool_result_only(content):
                # flush any pending calls up to (but not including) this user row
                _flush_calls_up_to(i - 1)
                _register_tool_calls(cur_calls)
                if cur_calls or cur_user:
                    turns.append(Turn(
                        turn_num=len(turns) + 1,
                        user_text=cur_user,
                        llm_calls=cur_calls,
                    ))
                cur_user = _extract_user_text(row)
                cur_calls = []
            # Harvest tool_result blocks from any user row (pure tool_result
            # rows AND mixed-content rows).
            _flush_calls_up_to(i)
            _register_tool_calls(cur_calls)
            _apply_tool_results(content)

    # final flush
    _flush_calls_up_to(len(rows))
    _register_tool_calls(cur_calls)
    if cur_calls or cur_user:
        turns.append(Turn(
            turn_num=len(turns) + 1,
            user_text=cur_user,
            llm_calls=cur_calls,
        ))
    return turns


# ── deterministic ids ────────────────────────────────────────────────────────


def trace_id(framework_session_id: str, first_message_id: str) -> str:
    h = hashlib.sha256(f"trace::{framework_session_id}::{first_message_id}".encode())
    return h.hexdigest()[:32]


def obs_id(framework_session_id: str, message_id: str) -> str:
    h = hashlib.sha256(f"obs::{framework_session_id}::{message_id}".encode())
    return h.hexdigest()[:32]
