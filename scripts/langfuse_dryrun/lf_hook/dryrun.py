"""Dry-run renderer: parse a transcript JSONL into the canonical emit-payload JSON.

This is the gate input for P0 fixtures. It does NOT call Langfuse SDK — it returns
the exact payload that would be sent, in a stable serialization, so we can byte-compare
against golden expected files.
"""
from __future__ import annotations

import json
from dataclasses import asdict
from pathlib import Path
from typing import Any

from . import parse


def _read_jsonl(path: Path) -> list[dict]:
    rows = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    return rows


def _llm_call_to_dict(call: parse.LLMCall) -> dict:
    return {
        "message_id": call.message_id,
        "request_id": call.request_id,
        "model": call.model,
        "output_text": call.output_text,
        "tool_calls": [
            {"id": tc.id, "name": tc.name, "input": tc.input}
            for tc in call.tool_calls
        ],
        "usage_details": call.usage,
        "usage_source": call.usage_source,
        "started_at": call.started_at,
    }


def is_subagent_path(transcript_path: str) -> bool:
    """Detect a sub-agent transcript by path shape (design §5.6)."""
    return "/subagents/agent-" in str(transcript_path)


def render(
    transcript_path: str | Path,
    framework_session_id: str,
    app_id: str = "test-app",
    channel_key: str = "test-channel",
    user_open_id: str = "test-user",
    task_name: str | None = None,
) -> dict:
    """Produce a canonical dict mirroring what would be emitted to Langfuse."""
    rows = _read_jsonl(Path(transcript_path))
    turns = parse.build_turns(rows)
    is_sub = is_subagent_path(transcript_path)

    # Probe for sub-agent identifying fields (only present in sub-agent rows)
    agent_id = None
    agent_slug = None
    for row in rows:
        if row.get("isSidechain") and row.get("agentId"):
            agent_id = row.get("agentId")
            agent_slug = row.get("slug")
            break

    kind = "subagent" if is_sub else "main"
    tags = [f"app:{app_id}", f"kind:{kind}"]
    if task_name:
        tags.append(f"task:{task_name}")
    if agent_slug:
        tags.append(f"agent:{agent_slug}")
    # tags must be deterministic — sort
    tags = sorted(tags)

    base_metadata = {
        "app_id": app_id,
        "channel_key": channel_key,
        "framework_session_id": framework_session_id,
        "task_name": task_name,
        "kind": kind,
        "agent_id": agent_id,
        "agent_slug": agent_slug,
        "usage_aggregation": "per_message_dedupe_v1",
    }

    out_traces = []
    for turn in turns:
        if not turn.llm_calls:
            continue
        first_mid = turn.llm_calls[0].message_id
        trace_name = f"subagent[{agent_slug}] turn {turn.turn_num}" if is_sub else f"turn {turn.turn_num}"
        observations = []
        for call in turn.llm_calls:
            observations.append({
                "id": parse.obs_id(framework_session_id, call.message_id),
                "type": "generation",
                "name": call.model,
                "model": call.model,
                "input_preview": turn.user_text,
                **_llm_call_to_dict(call),
            })
        out_traces.append({
            "id": parse.trace_id(framework_session_id, first_mid),
            "name": trace_name,
            "user_id": user_open_id,
            "session_id": framework_session_id,
            "tags": tags,
            "metadata": base_metadata,
            "input": turn.user_text,
            "turn_num": turn.turn_num,
            "observations": observations,
        })

    # Normalize to basename so goldens are portable between absolute / relative invocations
    summary = {
        "transcript": Path(str(transcript_path)).name,
        "is_subagent": is_sub,
        "rows_total": len(rows),
        "rows_assistant": sum(1 for r in rows if r.get("type") == "assistant"),
        "distinct_messages": len(parse.merge_assistant_rows(rows)),
        "turns_emitted": len(out_traces),
        "traces": out_traces,
    }
    return summary


def render_to_json(transcript_path: str | Path, framework_session_id: str, **kwargs) -> str:
    """Stable JSON serialization for byte-compare."""
    data = render(transcript_path, framework_session_id, **kwargs)
    return json.dumps(data, indent=2, ensure_ascii=False, sort_keys=True) + "\n"
