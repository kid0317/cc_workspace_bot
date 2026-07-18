#!/usr/bin/env python3
"""output_filter.py — 陪伴 workspace 输出过滤器（Stop hook 主流程）。

设计文档：docs/archive/companion-output-filter-design.md §4.2/§4.3
部署位置：安装脚本同步到 <workspace>/.claude/hooks/output_filter.py

流程：门控 → transcript 提取候选文本 → Layer 1 句级规则 → Layer 2 过滤
sub agent（只返回删除句序号，结构化）→ hook 端确定性重组 → 空结果 persona
兜底 → 原子写 sessions/<id>/FINAL_REPLY.md → 全文日志。

纪律（QA-H2）：本进程在任何情况下都以 exit 0 结束，绝不阻塞主回复链路。
"""

import hashlib
import json
import os
import re
import subprocess
import sys
import time
from datetime import datetime
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import filter_rules as fr  # noqa: E402

SNAPSHOT_FILE = ".init_status_at_turn_start"
FINAL_REPLY = "FINAL_REPLY.md"
LOG_FILE = ".filter_log.jsonl"
DEFAULT_FALLBACKS = ["嗯。", "在。"]

_STATUS_RE = re.compile(r"^initialization_status:\s*([A-Za-z0-9_]+)", re.MULTILINE)
_WORKSPACE_RE = re.compile(r"^- Workspace:\s*(.+)$", re.MULTILINE)


# ── 解析与定位 ────────────────────────────────────────────────────────────────


def parse_status(text: str):
    """MEMORY.md 解析契约：取 initialization_status 行冒号后第一个词。
    该行带内联注释（含全部四个状态词），禁止子串匹配（需求-H2）。
    """
    m = _STATUS_RE.search(text)
    return m.group(1) if m else None


def find_workspace(session_dir: Path):
    """优先从 SESSION_CONTEXT.md 读 Workspace 行；回退 sessions/<id> 上两级。"""
    ctx = session_dir / "SESSION_CONTEXT.md"
    if ctx.exists():
        m = _WORKSPACE_RE.search(ctx.read_text(encoding="utf-8"))
        if m:
            return Path(m.group(1).strip())
    return session_dir.parent.parent


def read_turn_status(session_dir: Path, workspace: Path):
    """轮开始状态快照优先（消除轮内 done 切换竞态，产品-C2）；
    快照缺失回退 MEMORY.md；都失败返回 None（放行 + 记日志）。
    """
    snap = session_dir / SNAPSHOT_FILE
    if snap.exists():
        val = snap.read_text(encoding="utf-8").strip()
        if val:
            return val
    mem = workspace / "memory" / "MEMORY.md"
    if mem.exists():
        return parse_status(mem.read_text(encoding="utf-8"))
    return None


def extract_candidate(transcript_path: str) -> str:
    """复刻 Go executor.parseLine 的拼接语义：依序拼接所有 assistant 行的
    text 内容块（无分隔符）。跳过 isSidechain 行（子 agent 不进主回复）。
    契约测试：tests/output_filter/test_contract_golden.py（与 Go 共用 fixtures）。
    """
    parts = []
    with open(transcript_path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if obj.get("type") != "assistant" or obj.get("isSidechain"):
                continue
            msg = obj.get("message") or {}
            for block in msg.get("content") or []:
                if isinstance(block, dict) and block.get("type") == "text":
                    parts.append(block.get("text") or "")
    return "".join(parts)


# ── Layer 2：过滤 sub agent ──────────────────────────────────────────────────


def render_system_prompt(workspace: Path) -> str:
    tpl_path = Path(__file__).resolve().parent / "filter_prompt.md"
    tpl = tpl_path.read_text(encoding="utf-8")

    persona_name = "角色"
    persona_md = workspace / "memory" / "persona.md"
    if persona_md.exists():
        text = persona_md.read_text(encoding="utf-8")
        # 优先取设定块的「**名字**：X」字段；回退首个一级标题
        m = re.search(r"\*\*名字\*\*[：:]\s*(\S+)", text)
        if not m:
            m = re.search(r"^#\s+(.+)$", text, re.MULTILINE)
        if m:
            persona_name = m.group(1).split("—")[0].split("(")[0].strip()

    vocab = ""
    vocab_md = workspace / "memory" / "filter_vocab.md"
    if vocab_md.exists():
        vocab = vocab_md.read_text(encoding="utf-8").strip()

    return tpl.replace("{persona_name}", persona_name).replace("{persona_vocab}", vocab)


def _layer2_env() -> dict:
    """env 清洗：递归守卫用自定义变量（POC 实锤 CLAUDECODE 会被 CLI 重设，
    不可依赖）；剥离 CC_LF_* 避免过滤调用污染 Langfuse 主链路。"""
    env = {
        k: v for k, v in os.environ.items()
        if k != "CLAUDECODE" and not k.startswith("CLAUDE_CODE_")
        and not k.startswith("CC_LF_")
    }
    env["CC_OUTPUT_FILTER"] = "1"
    return env


def run_layer2(sentences, system_prompt: str, timeout: float):
    """调过滤 sub agent，返回要删除的句子序号列表；任何失败返回 None（降级）。
    sub agent 只输出 JSON {"remove":[...]}，文本重组不经过模型。
    """
    claude_bin = os.environ.get("CC_FILTER_CLAUDE_BIN", "claude")
    # 完整模型 ID：CLI 不接受 "haiku" 别名（与 executor.go modelAliases 同源）
    model = os.environ.get("CC_FILTER_MODEL", "claude-haiku-4-5-20251001")
    cwd = os.environ.get("CC_FILTER_CWD", "/tmp/cc_output_filter")
    os.makedirs(cwd, exist_ok=True)

    user_msg = "<candidate>\n" + "\n".join(
        f"{i}. {s.strip()}" for i, s in sentences) + "\n</candidate>"
    cmd = [
        claude_bin, "-p",
        "--model", model,
        "--max-turns", "1",
        "--tools", "",
        "--setting-sources", "project",
        "--system-prompt", system_prompt,
        "--output-format", "text",
    ]
    try:
        proc = subprocess.run(
            cmd, input=user_msg, capture_output=True, text=True,
            timeout=timeout, cwd=cwd, env=_layer2_env(),
        )
    except (subprocess.TimeoutExpired, OSError):
        return None
    if proc.returncode != 0:
        return None

    m = re.search(r"\{[^{}]*\}", proc.stdout, re.DOTALL)
    if not m:
        return None
    try:
        data = json.loads(m.group(0))
    except json.JSONDecodeError:
        return None
    remove = data.get("remove")
    if not isinstance(remove, list) or not all(isinstance(i, int) for i in remove):
        return None
    valid_idx = {i for i, _ in sentences}
    if not set(remove).issubset(valid_idx):
        return None  # 非法序号 → 整体不信任，降级（QA-H6）
    return remove


# ── 兜底与交付 ────────────────────────────────────────────────────────────────


def load_fallback(workspace: Path) -> str:
    """persona 最小回应池（产品-C1：系统失声会被按角色规则解读为敌意）。"""
    replies = []
    f = workspace / "memory" / "fallback_replies.yaml"
    if f.exists():
        for line in f.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if line.startswith("- "):
                val = line[2:].strip().strip("\"'")
                if val:
                    replies.append(val)
    if not replies:
        replies = DEFAULT_FALLBACKS
    return replies[datetime.now().hour % len(replies)]


def write_final_reply(session_dir: Path, text: str):
    tmp = session_dir / (FINAL_REPLY + ".tmp")
    tmp.write_text(text, encoding="utf-8")
    tmp.rename(session_dir / FINAL_REPLY)


def append_log(workspace: Path, record: dict):
    try:
        record["ts"] = datetime.now().isoformat(timespec="seconds")
        with open(workspace / LOG_FILE, "a", encoding="utf-8") as f:
            f.write(json.dumps(record, ensure_ascii=False) + "\n")
    except OSError:
        pass


def _hash(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()[:16]


# ── 主流程 ────────────────────────────────────────────────────────────────────


def _run(hook_input: dict) -> None:
    t0 = time.monotonic()

    # ① 递归守卫（设计 §4.2①）
    if os.environ.get("CC_OUTPUT_FILTER") == "1":
        return
    if hook_input.get("stop_hook_active"):
        return

    session_dir = Path(hook_input.get("cwd") or os.getcwd())
    workspace = find_workspace(session_dir)

    def log(rec):
        append_log(workspace, {"session_id": hook_input.get("session_id", ""), **rec})

    # ② 正向门控（设计 §4.2②）：互动会话 + 非任务 + 轮开始已完成初始化
    if not os.environ.get("CC_LF_CHANNEL_KEY"):
        return  # 非互动会话（系统任务等），不记日志避免任务噪声
    if os.environ.get("CC_LF_TASK_NAME"):
        return  # 定时任务（proactive 等），由发送脚本侧 Layer 1 负责
    status = read_turn_status(session_dir, workspace)
    if status is None:
        log({"result": "gate_skipped", "reason": "status_parse_error"})
        return
    if status != "done":
        log({"result": "gate_skipped", "reason": f"init_{status}"})
        return

    # ③ 候选文本提取
    transcript = hook_input.get("transcript_path", "")
    if not transcript or not os.path.exists(transcript):
        log({"result": "error", "reason": "transcript_missing"})
        return
    candidate = extract_candidate(transcript)
    if not candidate.strip():
        return  # 本轮无文本输出（如被上游 hook 拦截），无事可做

    # ④⑤ Layer 1 句级规则
    layer1_text, layer1_hits = fr.layer1_filter(candidate)

    # ⑥ Layer 2 语义过滤（结构化序号删除）
    layer2_status = "skipped"
    final_text = layer1_text
    sentences = fr.numbered_sentences(layer1_text)
    if sentences:
        # 本机代理实测 13-30s 波动（POC 沙盒 5-7s），默认放宽到 25s
        timeout = float(os.environ.get("CC_FILTER_TIMEOUT", "25"))
        system_prompt = render_system_prompt(workspace)
        remove = run_layer2(sentences, system_prompt, timeout)
        if remove is None:
            layer2_status = "degraded"  # 失败/超时 → 用 Layer 1 结果
        elif not remove:
            layer2_status = "passed"
        else:
            all_idx = [i for i, _ in sentences]
            if set(remove) == set(all_idx):
                # 全删 → 重试一次（宁可保留可疑句）
                retry = run_layer2(sentences, system_prompt + "\n\n注意：上一轮你判定全部删除。请复核——只要有任何一句可能是角色台词，就保留它。", timeout)
                remove = retry if retry is not None else remove
            final_text = fr.remove_by_index(layer1_text, remove)
            layer2_status = "changed" if remove else "passed"

    # ⑦⑧ 空结果 → persona 兜底（绝不静默失声）
    result_kind = "passed" if (final_text == candidate) else "filtered"
    emptied = not final_text.strip()
    if emptied:
        final_text = load_fallback(workspace)
        result_kind = "emptied_fallback"

    # ⑨ 交付 + 日志（变更轮存全文，QA-C3）
    write_final_reply(session_dir, final_text)
    rec = {
        "result": result_kind,
        "layer1_hits": len(layer1_hits),
        "layer1_rules": [h["rule"] for h in layer1_hits],
        "layer2": layer2_status,
        "latency_ms": int((time.monotonic() - t0) * 1000),
        "in_len": len(candidate), "out_len": len(final_text),
        "in_hash": _hash(candidate), "out_hash": _hash(final_text),
    }
    if result_kind != "passed" or layer2_status == "degraded":
        rec["before"] = candidate
        rec["after"] = final_text
    log(rec)


def main() -> int:
    """顶层兜底：任何异常 exit 0（exit 2 = 阻止停止，绝对禁止）。"""
    try:
        hook_input = json.loads(sys.stdin.read() or "{}")
        if not isinstance(hook_input, dict):
            return 0
        _run(hook_input)
    except Exception:  # noqa: BLE001 — 故意吞掉一切，保 exit 0
        try:
            sys.stderr.write("output_filter internal error (fail-open)\n")
        except Exception:  # noqa: BLE001
            pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
