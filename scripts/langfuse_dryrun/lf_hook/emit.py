"""Langfuse SDK emit. Takes parsed Turns + Meta → calls Langfuse.

Tested at integration level (smoke tests against running Langfuse). Pure-function
parts (id derivation, tag/metadata building) live in dryrun.render() and are
covered by the golden-file fixtures.
"""
from __future__ import annotations

import logging
from pathlib import Path
from typing import Any

from . import parse
from .meta import Meta

log = logging.getLogger(__name__)


def is_subagent_path(transcript_path: str | Path) -> bool:
    return "/subagents/agent-" in str(transcript_path)


def _detect_subagent_fields(rows: list[dict]) -> tuple[str | None, str | None]:
    """Probe sub-agent rows for agentId and slug."""
    for row in rows:
        if row.get("isSidechain") and row.get("agentId"):
            return row.get("agentId"), row.get("slug")
    return None, None


def emit_turns(
    lf: Any,                      # langfuse.Langfuse instance (Any to keep this importable without SDK)
    turns: list[parse.Turn],
    meta: Meta,
    transcript_path: str,
    rows: list[dict],
    *,
    propagate_attributes_fn: Any = None,  # langfuse.propagate_attributes; injected for testability
) -> int:
    """Emit each turn as a trace + N generation observations. Returns count emitted."""
    is_sub = is_subagent_path(transcript_path)
    agent_id, agent_slug = _detect_subagent_fields(rows) if is_sub else (None, None)
    kind = "subagent" if is_sub else "main"

    tags = [f"app:{meta.app_id}", f"kind:{kind}"]
    if meta.task_name:
        tags.append(f"task:{meta.task_name}")
    if agent_slug:
        tags.append(f"agent:{agent_slug}")
    tags = sorted(tags)

    base_metadata = {
        "app_id": meta.app_id,
        "channel_key": meta.channel_key,
        "framework_session_id": meta.framework_session_id,
        "task_name": meta.task_name,
        "kind": kind,
        "agent_id": agent_id,
        "agent_slug": agent_slug,
        "transcript_path": transcript_path,
        "usage_aggregation": "per_message_dedupe_v1",
    }

    emitted = 0
    for turn in turns:
        if not turn.llm_calls:
            continue
        try:
            _emit_one_turn(
                lf, turn, meta, tags, base_metadata,
                is_sub, agent_slug,
                propagate_attributes_fn=propagate_attributes_fn,
            )
            emitted += 1
        except Exception as e:  # noqa: BLE001 — per-turn isolation
            log.warning("emit failed for turn %d: %s", turn.turn_num, e)
    return emitted


def _emit_one_turn(
    lf, turn, meta, tags, base_metadata,
    is_sub, agent_slug,
    *,
    propagate_attributes_fn,
):
    first_mid = turn.llm_calls[0].message_id
    trace_name = (
        f"subagent[{agent_slug}] turn {turn.turn_num}"
        if is_sub else f"turn {turn.turn_num}"
    )

    if propagate_attributes_fn is None:
        # No-op context manager fallback (older SDK / test stub)
        from contextlib import nullcontext
        ctx = nullcontext()
    else:
        ctx = propagate_attributes_fn(
            session_id=meta.framework_session_id,
            user_id=meta.user_open_id or None,
            tags=tags,
        )

    # v4 SDK does not accept `id=` on observations; trace_id is set via
    # trace_context only. Child observation IDs are SDK-auto. Re-runs of the
    # same hook will upsert the same trace (deterministic trace_id) but recreate
    # children. Acceptable for P1; backfill (P6) needs the SDK observation_id
    # API or post-emit mapping table — see design §9 risk #1.
    trace_ctx = {"trace_id": parse.trace_id(meta.framework_session_id, first_mid)}

    with ctx:
        with lf.start_as_current_observation(
            trace_context=trace_ctx,
            name=trace_name,
            as_type="span",
            input={"role": "user", "content": turn.user_text},
            metadata={**base_metadata, "turn_num": turn.turn_num},
        ) as turn_span:
            for call in turn.llm_calls:
                with lf.start_as_current_observation(
                    name=call.model,
                    as_type="generation",
                    model=call.model,
                    input={"role": "user", "content": turn.user_text},
                    usage_details=call.usage,
                    metadata={
                        "message_id": call.message_id,
                        "request_id": call.request_id,
                        "tool_count": len(call.tool_calls),
                        "usage_source": call.usage_source,
                        # obs_id派生值放metadata里，方便backfill mapping用
                        "deterministic_obs_id": parse.obs_id(meta.framework_session_id, call.message_id),
                    },
                ) as gen:
                    gen.update(output={"role": "assistant", "content": call.output_text})
                    for tc in call.tool_calls:
                        with lf.start_as_current_observation(
                            name=f"Tool: {tc.name}",
                            as_type="tool",
                            input=tc.input,
                            metadata={"tool_id": tc.id},
                        ) as tool_span:
                            if tc.output:
                                tool_span.update(output=tc.output)

            turn_span.update(output={
                "role": "assistant",
                "content": turn.llm_calls[-1].output_text if turn.llm_calls else "",
            })
