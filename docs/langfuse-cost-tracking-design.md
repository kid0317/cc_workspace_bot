# Langfuse 成本统计 设计文档

> 状态：设计稿，未实施
> 范围：`/root/.claude/hooks/langfuse_hook.py`、Langfuse 模型注册、`langfuse_query.py`
> 依赖：自托管 Langfuse v4（http://localhost:3000）、langfuse-sdk 4.2+

---

## 1. 背景与问题

### 1.1 当前实现概览

每次 `claude` CLI 子进程结束时（`Stop` hook），`/root/.claude/hooks/langfuse_hook.py` 增量读取 Claude Code 的 transcript JSONL，按"用户输入 → 全部助手回复 → 工具结果"分组成 **turn**，向 Langfuse emit：

```
Trace (claude-code turn N)
 ├─ Generation  "Claude Response"   ← 模型名 + input/output 文本，无 usage
 └─ Tool spans  "Tool: <name>"      ← 工具调用 + 结果
```

### 1.2 当前问题（验证）

抽查 Langfuse 中最近 trace（`/api/public/observations?type=GENERATION`）：

```json
{
  "name": "Claude Response",
  "model": "claude-opus-4-7",
  "usageDetails": {},
  "costDetails": {},
  "calculatedTotalCost": 0,
  "promptTokens": 0,
  "completionTokens": 0
}
```

`totalCost = 0`、`usageDetails = {}` —— Langfuse 无法计算成本，因为 hook **完全没有把 token 用量传过去**。

### 1.3 数据其实就在 transcript 里

每条 assistant 消息的 `message.usage` 都带完整用量。以 `claude-opus-4-7` 为例：

```json
{
  "input_tokens": 6,
  "output_tokens": 141,
  "cache_creation_input_tokens": 21210,
  "cache_read_input_tokens": 18022,
  "cache_creation": {
    "ephemeral_5m_input_tokens": 0,
    "ephemeral_1h_input_tokens": 21210
  },
  "service_tier": "standard"
}
```

不同模型的字段集略有差异（kimi/qwen 只有 input/output 两项），但都能从 JSONL 拿到。

### 1.4 Sub-agent 完全漏

`~/.claude/settings.json` 只注册了 `Stop` 事件 hook。Sub-agent（Task tool）调用产生的独立 transcript（`<project>/<sessionId>/subagents/agent-<id>.jsonl`）由 `SubagentStop` 事件触发，**当前不会进入 hook**。实测一个 session 单 sub-agent 文件 334 条 assistant 行（kimi-k2.5）—— 这部分成本目前根本不在 Langfuse 里。

### 1.5 多模型现状

近 7 天 transcript 中观察到的模型：

| Provider | 模型 | Langfuse 内置定价 |
|---|---|---|
| Anthropic | `claude-opus-4-6`、`claude-sonnet-4-6`、`claude-haiku-4-5-20251001` | ✅ 已注册（含 cache 5m/1h 分层）|
| Anthropic | `claude-opus-4-7` | ❌ 未注册（新模型）|
| 百炼桥接 | `kimi-k2.5`、`qwen3.5-plus` | ❌ 未注册（CNY 计价）|
| 占位 | `<synthetic>` | — 不计费 |

---

## 2. 目标

1. **每条 trace 都有 token 用量与美元成本估算**（精确到 input / output / cache_creation_5m / cache_creation_1h / cache_read 五维）。
2. **支持多 provider**：Anthropic 走 Langfuse 内置定价；百炼系（kimi/qwen）走自建定价表。
3. **可按维度聚合查询**：app / 用户 channel / session / 模型 / 任务名（定时任务）。
4. **不影响 hook 失败兜底**：fail-open，token/成本采集失败不能阻塞 Claude 主流程。
5. **可在 Langfuse UI 看到 totalCost、Daily Spend、按模型分组**（已是 Langfuse 自带能力，前提是 usage_details 正确传入）。

非目标：

- 不做实时预算告警（先看再说）。
- 不替代 `bot.db` 的 session 元数据（Langfuse 只是观测面，DB 是真源）。

---

## 3. Anthropic 计费语义与 JSONL 实证

> 这一章是后续所有设计决策的前提。在 architect review 之后实证补的，不要跳过。

### 3.1 一条 assistant 消息 = 一次独立 LLM 调用 = 一笔独立账单

Anthropic API 计费规则：每次 HTTP 请求按 `input + output + cache_creation + cache_read` 各自单价计费。tool-use 循环里，每个 assistant 消息对应**一次独立的 HTTP 请求**：

```
call #1: bill = 46_372 * cc_1h_price  +  6 * input_price  +  438 * output_price + 0 * cache_read_price
call #2: bill = 556 * cc_1h_price  +  1 * input_price  +  207 * output_price + 46_372 * cache_read_price
                                                                                ↑↑↑
                                                                  call #2 真金白银付钱读 call #1 写入的缓存
```

**call #1 和 call #2 的账单不重叠**。call #2 报告 `cache_read=46_372` 不是"重复 call #1 已付"，而是它本次确实付了缓存读取费。结论：**按 distinct LLM call 加总 = 真实账单**。

### 3.2 JSONL 把一条 message 拆成多行：必须按 `message.id` **合并**（不是去重）

> 起初推测是"重复行"，实证后发现是"内容块拆行"，处理方式完全不同。

实测 `5046303d-…jsonl` 一条 message 的 4 行：

| 行号 | uuid | parent | message.id | content.types | usage |
|---|---|---|---|---|---|
| 14 | e1893dee | f3d2cfea | qRMCfCG4sEPZ | `[thinking]` | in=6, out=438 |
| 15 | 4873e853 | e1893dee | qRMCfCG4sEPZ | `[tool_use]` | in=6, out=438 |
| 21 | f0edb424 | d619cd3f | qRMCfCG4sEPZ | `[tool_use]` | in=6, out=438 |
| 27 | 32805443 | 72ad5e56 | qRMCfCG4sEPZ | `[tool_use]` | in=6, out=438 |

每行只携带 **一个 content block**（thinking / tool_use / text），usage 在所有行上**一字不差地复制**（74 个 message.id 中 41 个有 ≥2 行，全部是块拆分；usage 在所有 dupes 里完全一致）。row.uuid 每行唯一，靠 parentUuid 串起原本的 content 数组顺序。

**正确算法**：按 `message.id` 分组 → 把所有行的 `content` 块按出现顺序拼起来 → usage 取任意一行（带等值校验，diverge 时记 WARN 并取首行）。**不是 dedupe**：dedupe 会丢掉 N-1 个内容块（首行只有 thinking 没 tool_use → 工具调用全消失）。

**没合并的代价**：
- "首行胜出"丢工具调用（38/41 案例）→ Tool span 全空
- "末行胜出"丢首块（thinking + 第一个 tool_use 全没）
- "一行一 generation"→ 同一 API 响应被算 N 次成本（×3 上膨，最危险）

### 3.3 `output_tokens: 0` 是真值，不是缺失

百炼桥接的 kimi/qwen 经常返回 `output_tokens: 0`（推测：bailian 在 streaming 中只在末包写真实数；中间包写 0）。这是真实的 0，不是字段缺失。代码里 `if v := raw.get("output_tokens"):` 会把 0 当假值丢掉 → input 单算 cost、output 不算 cost，再次形成"看起来合理但偏低"的数字。所有 usage 字段都要用 `is not None` 而非真值判断。

### 3.4 cache_read 单调非降

去重后，同一 turn 内 distinct calls 的 `cache_read_input_tokens` 单调非降（call N 必然包含 call N-1 写入 + call N-1 自己读到的）。这是健康的，无需 delta 处理 —— 因为每次都是真实付费。**唯一风险点是 §3.2 的去重**。

---

## 4. Langfuse 计价机制要点

> 验证自 `GET /api/public/models?page=1` 返回的内置模型定义。

Langfuse v4 的 generation observation 接受 `usage_details` 字典，键名匹配模型 `prices.<key>` 即按对应单价计费：

| Anthropic API 字段 | Langfuse 推荐 usage_details 键 | 内置 price key |
|---|---|---|
| `input_tokens` | `input` | `input` |
| `output_tokens` | `output` | `output` |
| `cache_creation.ephemeral_5m_input_tokens` | `input_cache_creation_5m` | `input_cache_creation_5m` |
| `cache_creation.ephemeral_1h_input_tokens` | `input_cache_creation_1h` | `input_cache_creation_1h` |
| `cache_read_input_tokens` | `input_cache_read` | `input_cache_read` |

注意：

- `cache_creation_input_tokens` 是 5m+1h 之和；**优先用细分 ephemeral 字段**避免重复计费。
- 老转录可能没有 `cache_creation` 子对象，回退到 `cache_creation_input_tokens` 作为 5m。
- Langfuse 在 generation 上 `model` 字段只要满足 `matchPattern` 就会匹配并自动算 `costDetails` + `calculatedTotalCost`，无需我们传 `cost_details`。

对 Langfuse 没注册的模型（opus-4-7 / kimi / qwen），有两条路：

- **A 注册自定义模型**（推荐）：在 Langfuse UI 或通过 `POST /api/public/models` 创建，写入 prices map。后续 hook 不变，定价集中维护。
- **B hook 内预计算 cost_details**：本地维护定价表，在 emit 时同时传 `cost_details`。Langfuse 优先使用我们传的 `cost_details`。

本设计采用 **A 唯一路径**：所有要计费的模型都必须在 Langfuse Models 注册。**hook 不预计算 `cost_details`**。理由（来自 architect review #7/#8）：

- 未注册模型 → 自动呈现 `totalCost=0`，是**可见的缺口**，运维很容易发现并补注册。
- 一旦 hook 内置兜底定价表，就会出现"看起来有数但其实是猜的"的静默错误，比 0 更糟。
- 也避免 `usage_details + cost_details` 两套源头打架的歧义。

代价：每个新模型上线必须人工注册（含汇率换算），但这正是想要的"显式 gate"。

---

## 5. 设计

### 5.1 整体改造点

```
JSONL（一条 message 拆 N 行，需按 message.id 合并）
  ├─ user msg
  ├─ assistant rows for msg.id=A   (× N 块拆分)  ← 合并 content，usage 取一份
  ├─ tool_result row(s)
  ├─ assistant rows for msg.id=B   (× M 块拆分)
  ├─ ...
  └─ assistant rows for msg.id=K   (final)

Langfuse trace（一个 turn 一个 trace）
  ├─ Generation A   (usage_details=A.usage,  model=A.model)  ← id 派生自 first message_id
  ├─ Generation B   (usage_details=B.usage,  model=B.model)
  ├─ ...
  └─ Tool span × M   挂在触发它的 generation 下
```

**关键决策**：

1. **合并先于分组**：拿到所有 assistant 行后，按 `(message.id, request_id)` 元组分组合并 content；用 `request_id` 做次级 key 避免极小概率的 id 跨 session 碰撞。每个分组 = 一次 Anthropic API 调用 = 一笔账单。
2. **每个 merged call → 1 generation**：与 Anthropic 计费边界一致（§3.1）。Langfuse `totalCost = Σ generation.calculatedTotalCost`。
3. **deterministic id 不依赖位序**：
   - `trace_id = sha256("trace::" || framework_session_id || first_message_id_of_turn)[:32]`
   - `obs_id   = sha256("obs::"   || framework_session_id || message_id)[:32]`
   - `turn_num` 仅作为 metadata 字段（人看用），**不进 id 派生**。理由：hook 重跑、backfill 从 offset 0 重读、`/new` 后剩余 turn 编号偏移都会让位序变，但 `first_message_id` 不变 → 重跑幂等。

### 5.2 hook 改造

文件：`/root/.claude/hooks/langfuse_hook.py`

#### 5.2.1 数据结构

```python
@dataclass(frozen=True)
class LLMCall:
    message_id: str            # message.id —— 合并 + 派生 obs id 的主键
    request_id: str            # requestId —— 与 message.id 共同构成合并 key
    model: str
    output_text: str           # 合并后所有 text 块拼接
    tool_calls: list[ToolCall] # 合并后所有 tool_use 块（含匹配的 tool_result）
    usage: dict                # 标准化后（§5.2.3），合并组任一行
    started_at: datetime       # 首行 timestamp

@dataclass
class Turn:
    turn_num: int              # 仅作 metadata 显示，不进 deterministic id
    user_text: str
    llm_calls: list[LLMCall]   # 合并后按首行行序排列
```

#### 5.2.2 turn 切分 + content 合并（CRITICAL）

两阶段：

**阶段 A — 按 `(message.id, request_id)` 合并 content 块**：

```python
def _merge_assistant_rows(rows: list[dict]) -> dict[tuple[str,str], dict]:
    """同一 (message.id, request_id) 的多行 → 一个 logical message。"""
    merged: dict[tuple[str,str], dict] = {}
    for row in rows:
        if row.get("type") != "assistant":
            continue
        msg = row.get("message") or {}
        mid = msg.get("id")
        rid = row.get("requestId") or ""
        if not mid:
            continue
        key = (mid, rid)
        new_blocks = msg.get("content") or []
        if key not in merged:
            merged[key] = {
                "message_id": mid,
                "request_id": rid,
                "model": msg.get("model"),
                "usage": msg.get("usage") or {},
                "content_blocks": list(new_blocks),
                "first_seen_at": row.get("timestamp"),
                "first_uuid": row.get("uuid"),  # 用于 turn 归属（按行序）
            }
        else:
            # 等值校验：usage 必须一致；不一致记 WARN 但保留首份
            if (msg.get("usage") or {}) != merged[key]["usage"]:
                _log("WARN", f"usage mismatch across split rows for {mid}: keep first")
            # content 块按 JSONL 行序追加。前提假设：Claude Code 按 Anthropic
            # 响应的 block emission 顺序写行。如未来出现并行流式打乱顺序，需用
            # 块内的位置字段（如 index）排序。
            merged[key]["content_blocks"].extend(new_blocks)
    return merged
```

**阶段 B — 按"真实 user 消息"切 turn**，用每组的 `first_uuid` 行序定位归属：

```python
def _build_turns(rows: list[dict]) -> list[Turn]:
    merged = _merge_assistant_rows(rows)
    # 把 merged 按 first_uuid 在原 rows 序列里的位置排序
    uuid_to_idx = {r.get("uuid"): i for i, r in enumerate(rows) if r.get("uuid")}
    merged_sorted = sorted(merged.values(), key=lambda m: uuid_to_idx.get(m["first_uuid"], 0))

    turns: list[Turn] = []
    cur_user = ""
    cur_calls: list[LLMCall] = []
    merged_iter = iter(merged_sorted)
    next_call = next(merged_iter, None)

    for i, row in enumerate(rows):
        if row.get("type") == "user" and not _is_tool_result_only(_extract_content(row)):
            if cur_calls or cur_user:
                turns.append(Turn(turn_num=len(turns)+1, user_text=cur_user, llm_calls=cur_calls))
            cur_user = _extract_user_text(row)
            cur_calls = []
        # 把所有"first_uuid 在本 user 之前但还没归属"的 calls flush 进 cur_calls
        while next_call and uuid_to_idx.get(next_call["first_uuid"], 10**9) <= i:
            if next_call["model"] not in SKIP_MODELS:
                cur_calls.append(_build_llm_call(next_call))
            next_call = next(merged_iter, None)

    if cur_calls or cur_user:
        turns.append(Turn(turn_num=len(turns)+1, user_text=cur_user, llm_calls=cur_calls))
    return turns
```

**关键点**：
- 合并 key 是 `(message.id, request_id)` 元组：避免极小概率的 id 复用碰撞，同时正确识别 `/clear` 后即使 id 再次出现也是不同请求。
- 不做"全局 dedupe 跨 turn"——若 Anthropic 真的（不太可能）让两次 distinct API call 共享 message.id，requestId 不同就会被识别为两个独立 call，符合实际计费。
- usage 等值校验是 belt-and-suspenders；目前所有实测样本都等。

#### 5.2.3 usage 标准化（修复 0 值丢失 bug）

```python
def _normalize_usage(raw: dict) -> dict:
    """Anthropic usage → Langfuse usage_details. Preserves 0 (distinct from missing)."""
    if not raw:
        return {}
    out = {}
    # ⚠️ 用 `is not None` 而非 truthy 判断，否则 output_tokens=0 会被丢
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
            out["input_cache_creation_5m"] = legacy   # 历史回退（无 ttl 分层）
    else:
        if cc5 is not None:  out["input_cache_creation_5m"] = cc5
        if cc1h is not None: out["input_cache_creation_1h"] = cc1h

    if (v := raw.get("cache_read_input_tokens")) is not None:
        out["input_cache_read"] = v
    return out
```

实施前的 dry-run 验证清单：
- 跑老 transcript 的 turn 1（33 行 → 去重后约 8 calls），手工算预期 cost，对比脚本输出。
- 单测 `_normalize_usage({"input_tokens": 1, "output_tokens": 0})` 必须返回 `{"input": 1, "output": 0}`。

#### 5.2.4 emit（不传 cost_details，未注册模型呈现 0）

```python
import hashlib

def _trace_id(framework_session_id: str, first_message_id: str) -> str:
    # ⚠️ 用 turn 内首个 message.id 派生，不用 turn_num（位序在重跑时不稳定）
    return hashlib.sha256(f"trace::{framework_session_id}::{first_message_id}".encode()).hexdigest()[:32]

def _obs_id(framework_session_id: str, message_id: str) -> str:
    return hashlib.sha256(f"obs::{framework_session_id}::{message_id}".encode()).hexdigest()[:32]

# 每个 turn 自己的 propagate_attributes 上下文（见 §5.4 风险 #11/#12）
for turn in turns:
    with propagate_attributes(
        session_id=meta.framework_session_id,
        user_id=meta.user_open_id,
        tags=[f"app:{meta.app_id}", *(["task:" + meta.task_name] if meta.task_name else [])],
    ):
        first_mid = turn.llm_calls[0].message_id if turn.llm_calls else f"empty-{turn.turn_num}"
        with lf.start_as_current_observation(
            id=_trace_id(meta.framework_session_id, first_mid),
            name=f"turn {turn.turn_num}", as_type="span",
            input={"role": "user", "content": turn.user_text},
            metadata={
                "app_id": meta.app_id,
                "channel_key": meta.channel_key,
                "claude_session_id": meta.claude_session_id,
                "framework_session_id": meta.framework_session_id,
                "task_name": meta.task_name,
                "usage_aggregation": "per_message_dedupe_v1",  # ← 永远写明算法版本
            },
        ) as turn_span:
            for call in turn.llm_calls:
                with lf.start_as_current_observation(
                    id=_obs_id(meta.framework_session_id, call.message_id),
                    name=call.model,
                    as_type="generation",
                    model=call.model,
                    input={"role": "user", "content": call.input_preview},
                    usage_details=call.usage,           # ← 唯一的成本来源
                    metadata={
                        "message_id": call.message_id,
                        "request_id": call.request_id,
                        "tool_count": len(call.tool_calls),
                    },
                    start_time=call.started_at,
                ) as gen:
                    gen.update(output={"role": "assistant", "content": call.output_text})
                    for tc in call.tool_calls:
                        with lf.start_as_current_observation(
                            name=f"Tool: {tc.name}", as_type="tool",
                            input=tc.input, metadata={"tool_id": tc.id},
                        ) as tcs:
                            if tc.output:
                                tcs.update(output=tc.output)
            turn_span.update(output={"role": "assistant", "content": turn.final_assistant_text})
```

**不传 `cost_details`**。policy（来自 architect review #7/#8）：

- 模型在 Langfuse 已注册 → 自动算成本 ✓
- 模型未注册 → `calculatedTotalCost=0`，UI 上明显的"价格未知"缺口，运维补注册即可
- 永远不掩盖未知，不留可疑数字

`<synthetic>` 模型直接跳过 emit（不计费、不污染模型分布图）：

```python
SKIP_MODELS = {"<synthetic>", "", None}
if call.model in SKIP_MODELS:
    continue
```

#### 5.2.5 业务维度注入（env vars + sidecar 双通道）

executor 启动 `claude` 子进程时，**优先用环境变量**传递元数据（不可变、无文件竞态）：

```bash
CC_LF_APP_ID=yzk_worker
CC_LF_CHANNEL_KEY=p2p:oc_xxx:cli_yyy
CC_LF_USER_OPEN_ID=ou_xxx
CC_LF_FRAMEWORK_SESSION_ID=f8c2-...
CC_LF_TASK_NAME=daily_briefing      # 可选
CC_LF_META_VERSION=1
```

为什么不用 sidecar 文件作为唯一通道（architect review v1 #3）：

- `/new` 触发 session_dir 归档时，hook 可能滞后到达 → 读到的可能是新 session 的 meta（错配）。
- cron task 与活跃 chat 在同一 workspace 下并发，sidecar 互相覆盖。
- env var 是 OS 级写时即定的，子进程 fork 后即不可被外部改动，hook（运行在该子进程退出前的 Stop 钩）天然继承正确值。

**注入点**（明确写出代码位置）：

| 入口 | 文件 | 行为 |
|---|---|---|
| 用户消息 | `internal/claude/executor.go` `Run()` 构造 `cmd.Env` 时 | 从 session.Worker 上下文取 `app_id` / `channel_key` / `user_open_id` / `framework_session_id`，append 5 个 `CC_LF_*` 到 `cmd.Env` |
| 定时任务 | `internal/task/runner.go` 触发 claude 调用时 | 同上 + 额外 `CC_LF_TASK_NAME=<task_name>` |

兜底 sidecar `<session_dir>/.langfuse_meta.json`：仅当 env 缺失时尝试读取（适用于 hook 被外部独立调用、或 OOB backfill 场景）。读出来的 meta 必带 `created_at`，hook 校对 transcript 首条消息时间 vs `created_at`，差异 > 5 分钟 warn 并仍 emit。

`framework_session_id` 取舍（architect review #6）：state file 改为按 `(framework_session_id, claude_session_id, transcript_path)` 三元组 keying。trace 上同时写 `session_id=framework_session_id`（用于 Langfuse Sessions 视图）和 metadata.claude_session_id（用于审计）。

### 5.3 模型定价管理（路径 A 唯一）

#### 5.3.1 Anthropic 模型

| 模型 | Langfuse 内置 | 动作 |
|---|---|---|
| `claude-opus-4-6`、`sonnet-4-6`、`haiku-4-5-20251001` | ✅ | 无需任何动作 |
| `claude-opus-4-7` | ❌ | **暂不注册**，先让 trace 显示 `cost=0`。等 Anthropic 公布官方定价（或 owner 决定按 4-6 沿用并打 `tag: pricing-estimate`）再注册。 |

为什么不立即按 opus-4-6 沿用注册（架构 review #7）：opus 跨代涨过价（3 → 3.5 → 4 series 单价持续上调），猜错 ±50% 会让月度报表的 opus-4-7 成本完全错位。"暂时空洞 + 看得见的缺口"比"看不见的偏差"安全。

#### 5.3.2 百炼桥接模型

`kimi-k2.5`、`qwen3.5-plus` 在 Langfuse 注册（手工，附录 §8）。注意：

- 百炼定价以 CNY 计；注册到 Langfuse 时按**写入当日汇率**换算成 USD 直接固定下来（不动态汇率）。
- 在 Langfuse 模型 metadata 上记 `pricing_source: bailian-2026-05-06, fx_rate: 7.2`，便于审计。
- 重大汇率变化（>5%）时人工调整，记 changelog。

#### 5.3.3 未注册模型可见性（三层防护）

让 cost=0 的缺口尽快暴露，否则 opus-4-7 这种"应该 $5 但显示 $0"的 trace 会沉默一周。

| 层 | 实现 | 触发频率 |
|---|---|---|
| 1. emit 时 WARN | hook 启动时拉一次 `GET /api/public/models`（缓存 1h），emit 前若 `call.model not in known_models` → 写 `_log("WARN", f"unregistered model: {model}")` + `~/.claude/state/unregistered_models.txt` 计数 | 每次 emit |
| 2. 日级巡检 | `scripts/check_unregistered_models.py`，列出当日 traces 中所有 model + 是否注册 + 当日 trace 数；通过现有定时任务每天早 9 点发飞书提醒 | 每日 |
| 3. Langfuse Dashboard 告警 | UI 内置 alert：`daily traces with cost=0 AND model NOT IN ('<synthetic>','')` 占比 > 5% 时通知 | 内置 |

第 1 层是防"P3 上线前 P2 漏注册"的快速反馈，第 2 层是防"运维忘记看 Langfuse"，第 3 层是防"hook 也宕了"。

### 5.4 hook 性能、并发与失败语义

| 维度 | 处理 |
|---|---|
| **状态文件 keying** | 改为 `sha256(framework_session_id||claude_session_id||transcript_path)`，避免 `/new` 后 key 串扰 |
| **状态文件并发写** | 加 flock（与项目 memory/ 一致），避免并行 hook 互相踩 offset |
| **observation 数量增长** | 单 turn 平均 3-8 个 distinct calls（实测）→ 较旧设计 3-8× 增长。Langfuse OSS 单实例可承（数十万行/天级别）。**OOM 降级方案不在初版设计中**——若真碰到，通过 Langfuse 端 retention 缩短（>30 天 obs 归档）解决，而不是改 hook 粒度（一旦支持"turn 聚合"，多模型混合 turn 该报哪个 model 没法良定义） |
| **emit 失败容忍** | 每个 turn 自己的 try/except，单 turn 失败不影响后续 turn |
| **flush 超时** | `lf.flush(timeout=2.0)`；超时记 log，不阻塞 hook 退出 |
| **fail-open** | hook 任何阶段异常 → log + `return 0`，不阻塞 claude 主流程 |

### 5.5 backfill 一次性脚本（设计骨架）

> architect review #4 的回应。脚本暂不写，但设计要把 backfill 当成一等公民。

`scripts/langfuse_backfill.py`：

```
INPUT:
  --since 2026-04-01
  --workspace-glob '/root/cc_workspace_bot/workspaces/*/sessions/*'
  --dry-run

PIPELINE（与 hook 共用 _build_turns / _normalize_usage / _emit_turn 模块）:
  1. for each session_dir:
       a. 读 .langfuse_meta.json（必须存在；不存在跳过并记 missing-meta 列表）
       b. 找对应 claude transcript（可从 framework session DB 查）
       c. 全量解析 → turns（不读 STATE_FILE，不写 STATE_FILE）
       d. 用 deterministic trace_id / obs_id（§5.2.4）emit；幂等
  2. 输出：成功 turn 数 / 跳过原因分布 / 总 emit 成本

幂等性保证：trace/obs id 由 framework_session_id + first_message_id / message.id 派生 →
重跑同一 session 必产生同一 id。Langfuse upsert 行为在 P0 实测后**只走一条路径**——
默认 SDK upsert（v4 行为）；若实测发现冲突则全局加 --skip-existing（先 GET 再 POST）。
不在文档里留两条并行路径以免代码两套。
```

历史 .langfuse_meta.json 缺失（hook 改造前的 session）：用 DB session 表反查 app_id / framework_session_id，user_open_id 用占位 `__pre_meta__`。

### 5.6 Sub-agent（Task tool）处理（CRITICAL，v4 新增）

> 实证：本项目 sub-agent 调用量极大（实测一个 session 单 agent 文件 334 条 assistant 行，全 kimi-k2.5）。当前 hook **完全没追踪 sub-agent**，是设计的关键空洞。

#### 5.6.1 数据结构实证

| 维度 | Main session | Sub-agent |
|---|---|---|
| 文件路径 | `<project>/<sessionId>.jsonl` | `<project>/<sessionId>/subagents/agent-<agent_id>.jsonl` |
| `sessionId` 字段 | session uuid | **同 parent**（共用 session uuid）|
| `isSidechain` | `false` | `true` |
| `agentId` 字段 | 不存在 | sub-agent 唯一 id（如 `acfe2becebc909039`）|
| `slug` 字段 | 不存在 | sub-agent 类型 slug（如 `goofy-frolicking-metcalfe`）|
| `message.usage` 结构 | 同 §3.1 | 同 §3.1，但**百炼系经常全 0**（见 §5.6.4）|

#### 5.6.2 Hook 注册：必须加 `SubagentStop`

当前 `~/.claude/settings.json` 只注册了 `Stop` 事件 → sub-agent 结束时不触发 → 全部漏。改造：

```json
"hooks": {
  "Stop":         [{"hooks":[{"type":"command","command":"python3 /root/.claude/hooks/langfuse_hook.py"}]}],
  "SubagentStop": [{"hooks":[{"type":"command","command":"python3 /root/.claude/hooks/langfuse_hook.py"}]}]
}
```

同一脚本两个事件都跑——hook 通过 `transcript_path` 区分主/子（路径含 `/subagents/agent-` 即为 sub-agent）。

#### 5.6.3 emit 结构

sub-agent trace 与 main trace **共享同一 framework_session_id**，便于 Langfuse Sessions 视图把"用户一次提问触发的全部 LLM 调用（含 sub-agent）"聚合到一起。区分手段：

- `tags`: 主 trace `[app:X, kind:main]`；sub-agent trace `[app:X, kind:subagent, agent:<slug>]`
- `metadata`: sub-agent trace 加 `agent_id` / `agent_slug` / `parent_session_id`
- `name`: sub-agent trace 名 `subagent[<slug>] turn N`，与主 trace 区分

deterministic id 公式不变（`framework_session_id || first_message_id` for trace；`framework_session_id || message_id` for obs）。sub-agent 的 message_id 与 main 不冲突（Anthropic 全局唯一），所以幂等保证不变。

state-file keying 三元组里的 `transcript_path` 自然把 main / 各 sub-agent 隔开，offset 不串扰。

#### 5.6.4 sub-agent 全 0 usage 兜底（与 §9.2 合并升级）

实测一个 kimi-k2.5 sub-agent 全部 334 条 assistant 行的 usage：

```
{'input_tokens': 0, 'output_tokens': 0}
```

input 也是 0。说明百炼桥接的流式响应有时候根本不写 usage（不只是 output）。直接发 usage_details=`{"input": 0, "output": 0}` 给 Langfuse → cost=0 → 看起来"sub-agent 不花钱"，实际可能每天烧几十块。

**兜底（升级到设计层）**：

```python
def _is_zero_usage(u: dict) -> bool:
    return all(u.get(k, 0) == 0 for k in
               ["input_tokens","output_tokens","cache_creation_input_tokens","cache_read_input_tokens"])

def _estimate_usage_from_text(input_text: str, output_text: str, model: str) -> dict:
    # 简单字符比例估算（中文 ≈ 1 char/token，英文 ≈ 4 char/token）。
    # 不追求精度，目的是让 cost 不为 0、量级正确。
    def tok(s):
        if not s: return 0
        ascii_chars = sum(1 for c in s if ord(c) < 128)
        cjk_chars = len(s) - ascii_chars
        return cjk_chars + ascii_chars // 4
    return {"input": tok(input_text), "output": tok(output_text)}

# 在 _build_llm_call 里：
if _is_zero_usage(raw_usage):
    usage = _estimate_usage_from_text(input_preview, output_text, model)
    metadata["usage_source"] = "estimated_char"
else:
    usage = _normalize_usage(raw_usage)
    metadata["usage_source"] = "reported"
```

`metadata.usage_source` 让 Langfuse UI 可一眼区分"真实计量"vs"估算"，估算结果**仍然进 cost 计算**（不再让 cost=0 沉默）。误差可接受范围由 P0 fixture #4 的扩展样本回归。

### 5.7 查询能力扩展

`langfuse_query.py` 加：

```python
print(f"  cost     : ${t.get('totalCost', 0):.4f}")
usage = t.get('usage') or {}
print(f"  tokens   : in={usage.get('input',0)} out={usage.get('output',0)}")
```

新模式：

```bash
python3 langfuse_query.py --mode cost --group-by app --days 7
python3 langfuse_query.py --mode cost --group-by model --days 7
python3 langfuse_query.py --mode cost --group-by task --days 7
python3 langfuse_query.py --mode cost --group-by user --days 30
```

实现走 Langfuse `/api/public/metrics`（v4 daily metrics）+ tag/metadata 过滤。

---

## 6. 数据流（最终态）

```
executor.go 启动 claude 子进程
  └─ 注入 env: CC_LF_APP_ID / CHANNEL_KEY / USER_OPEN_ID / FRAMEWORK_SESSION_ID / TASK_NAME
      └─ claude 写 transcript.jsonl（main + subagents/agent-*.jsonl）
          ├─ Stop 事件 触发 hook（处理 main transcript）
          └─ SubagentStop 事件 触发 hook（处理 sub-agent transcript，每个 sub-agent 一次）
              ├─ 读 env（兜底 sidecar）→ meta；判断 main vs subagent（看路径含 /subagents/）
              ├─ 增量解析 JSONL → 按 (message.id, request_id) 合并 content → turns
              ├─ 每个 LLMCall normalize usage（保留 0；全 0 → char 估算 + usage_source=estimated_char）
              ├─ trace/obs id 用 framework_session_id 派生（main / subagent 共享 session_id，靠 message_id 区分）
              └─ Langfuse SDK emit（tags 区分 kind:main / kind:subagent；不传 cost_details）

Langfuse 后端
  └─ model.matchPattern 命中 → 自动算 calculatedTotalCost
       │未命中 → calculatedTotalCost=0（可见的"价格未知"缺口）
       └─ trace.totalCost = Σ generation.calculatedTotalCost
       └─ Sessions 视图：main + 所有 subagent traces 同 session_id 自动聚合
```

---

## 7. 实施分阶段

| Phase | 目标 | 验收 |
|---|---|---|
| **P0 实证 gate** | dry-run 脚本，**不发 Langfuse**。详见下方"P0 验收清单" | 全部 6 项 fixture 测试通过，diff 为 0 |
| P1 | hook emit `usage_details`（合并 + 0 值修复 + deterministic id + WARN 未注册模型）；**同时注册 SubagentStop 事件**；先不动 §5.2.5 业务维度 | Langfuse UI 出现 `totalCost > 0` 的 trace；含 sub-agent trace；同一 transcript 跑两次 trace count 不变；Anthropic 模型 cost 与 Anthropic Console 月账单偏差 <5% |
| P2 | 注册百炼模型；opus-4-7 暂留空洞 | 百炼 trace 出现合理 cost；opus-4-7 trace cost=0（预期） |
| P3 | executor + runner 注入 env vars + 状态文件三元组 re-keying；sub-agent tag/metadata | UI Sessions 视图按 framework session 聚合（含 sub-agent）；user_id 非 null；`/new` 测试不串库 |
| P4 | 并发 flock；turn-级 propagate_attributes；emit 异常隔离；全 0 usage 字符估算 | 长 turn（>10 calls）emit 不丢；模拟 turn 2 抛异常不污染 turn 3 metadata；kimi sub-agent cost 非 0 且 metadata 标 estimated |
| P5 | `langfuse_query.py` cost 模式 + 日级未注册模型脚本（§5.3.3 第 2 层）| CLI 出 app/model/task/kind(main\|subagent) 维度成本；每日早 9 点自动飞书通知 |
| P6 | backfill 脚本（仅在 owner 决定补历史时启动）| 历史 trace 入 Langfuse（含 sub-agent），幂等可重跑 |

P0 是 gate，所有后续 phase 等 P0 通过。

### P0 验收清单（必须全过才解锁 P1）

将下列 fixture 提交到 `scripts/langfuse_dryrun/fixtures/`，dry-run 脚本输出与 expected 文件 byte-比对。

1. **去重比例 fixture**：`fixtures/transcript_5046303d_first10turns.jsonl`（生产实截）+ `expected_dedupe.json`：`{"total_assistant_rows": 220, "distinct_messages": 74, "merge_groups_with_split": 41}`。脚本输出必须完全一致。
2. **per-turn distinct call count**：`expected_turns.json`，schema 示例：
   ```json
   {
     "turn_1": {"distinct_calls": 8, "models": ["claude-opus-4-7"], "first_message_id": "msg_01BqhCF4kw5PqRMCfCG4sEPZ"},
     "turn_5": {"distinct_calls": 6, "models": ["claude-opus-4-7"], "first_message_id": "msg_01XyzAbc..."},
     "turn_6": {"distinct_calls": 4, "models": ["claude-opus-4-7"], "first_message_id": "msg_01PqrDef..."}
   }
   ```
   实测值：turn1=8, turn5=6, turn6=4。
3. **usage_details 黄金文件**：`expected_usage.json`：每个 message_id 的标准化 usage_details 完整快照。byte-compare。
4. **`output_tokens=0` 不丢**：`fixtures/transcript_bailian_kimi.jsonl`（含至少 1 条 output_tokens=0 但 output 文本非空的 message）→ expected usage 必含 `"output": 0` 字段（不是缺失）。
5. **幂等性**：dry-run 同一 fixture 两遍，所有 emit 的 trace_id / obs_id / usage_details 完全相同（diff 为 0）。
6. **Sub-agent 路径**：`fixtures/transcript_subagent_kimi.jsonl`（实截 kimi sub-agent，含全 0 usage 行）→ expected：trace 名带 `subagent[<slug>]`、tag 含 `kind:subagent`、metadata 有 `agent_id`/`agent_slug`、全 0 usage 触发 char 估算且 `usage_source=estimated_char`、cost > 0。

**dry-run 调用契约**（让 gate 真正机械化）：

```bash
python3 scripts/langfuse_dryrun/run.py \
  --fixture scripts/langfuse_dryrun/fixtures/transcript_5046303d_first10turns.jsonl \
  --expected-dir scripts/langfuse_dryrun/fixtures/expected/

# 退出码契约：
#   0  → 全部 fixture 与 expected 文件 byte-相等
#   1  → diff 检测出差异（stdout 输出 unified diff）
#   2  → fixture 或 expected 文件缺失
```

签字行：`P0 由 owner 在 reviewer 通过 5/5 + dry-run script 退出码 0 后签字解锁 P1。`

---

## 8. 附录：百炼定价对照（截至 2026-05-06）

> 来源：阿里云百炼控制台公开价（CNY/1M tokens），按 1 USD = 7.2 CNY 换算。
> 实施前由 owner 校对实际汇率与价目。注册到 Langfuse 后此表只作 changelog 留底。

> 表中 USD/token 数值统一用 `Xe-N` 形式（Langfuse 模型 prices 表格直接对接）。

| 模型 | input (CNY/1M) | output (CNY/1M) | input USD/token | output USD/token |
|---|---|---|---|---|
| kimi-k2.5 | 6 | 24 | 8.33e-7 | 3.33e-6 |
| qwen3.5-plus | 4 | 16 | 5.56e-7 | 2.22e-6 |
| qwen-plus | 0.8 | 2 | 1.11e-7 | 2.78e-7 |

---

## 9. 风险与未决（迭代后）

### 已 mitigate（v1 → v2 → v3 → v4 迭代）

v4（sub-agent 完整覆盖）新增：
- ~~Sub-agent 完全漏追踪~~ → §5.6 `SubagentStop` 事件 + 路径区分 + 共享 framework_session_id
- ~~kimi sub-agent input/output 全 0 → cost=0 沉默~~ → §5.6.4 全 0 usage 触发字符估算 + `usage_source=estimated_char` 标记

### 已 mitigate（v1-v3 累积）
- ~~JSONL 重复导致 ×3 over-count~~ → 实证发现是 content 块拆分（§3.2）→ §5.2.2 按 `(message.id, request_id)` 合并而非去重
- ~~首行胜出丢 tool_calls / 末行胜出丢 thinking~~ → §5.2.2 阶段 A 合并所有 content 块
- ~~`output_tokens=0` 被丢~~ → §5.2.3 `is not None` 判断
- ~~opus-4-7 按 4-6 估价~~ → §5.3.1 暂不注册，呈现 0 + 待官方价
- ~~sidecar meta 文件竞态~~ → §5.2.5 env var 主路径，注入点写明在 executor.go 与 runner.go
- ~~usage_details + cost_details 双源歧义~~ → §5.2.4 不传 cost_details
- ~~并发 hook 状态文件踩 offset~~ → §5.4 flock
- ~~`<synthetic>` 污染模型分布~~ → §5.2.2 SKIP_MODELS
- ~~deterministic id 用 turn_num（位序不稳）~~ → §5.2.4 改用 first message_id 派生 trace_id
- ~~未注册模型沉默~~ → §5.3.3 三层防护（emit WARN + 日级巡检 + Langfuse alert）
- ~~OOM 降级开关 turn-粒度语义不清~~ → §5.4 不在初版做，OOM 时走 retention 而非粒度切换
- ~~backfill 双路径并存~~ → §5.5 P0 实测 SDK upsert 后只走一条路径
- ~~USD 科学计数法混用~~ → §8 统一 `Xe-N` 形式

### 仍未决（实施期间观察）

1. **Langfuse v4 SDK 行为依赖**：`propagate_attributes(user_id=...)`、显式 `id=` 参数（自定 observation id）、`flush(timeout=)` 在 v4.2 待 P0 dry-run 实测确认。如行为不符，方案 B：SDK 自动 id + post-emit 时记 mapping 表 → backfill 用 mapping 而非 sha256 派生。
2. **百炼 usage 系统性失真**：实证 main-call 偶发 `output_tokens: 0`（input 有值）；sub-agent 普遍 input + output 全 0。已在 §5.6.4 加全 0 usage → 字符估算兜底，`usage_source=estimated_char` 暴露在 metadata。仍需 P0 fixture #6 确认估算误差量级在可接受范围（实施期需要 owner 抽样核对一周数据，与百炼控制台账单对账）。
3. **observation 增长对 Langfuse OSS 存储压力**：当前规模估算（~50 turns/day × 5 calls/turn × 10 用户 = 2500 obs/day，年级 ~1M），Postgres + ClickHouse 单机充裕。上量后需规划 retention（>90 天 obs 归档到 S3）。
4. **多 provider 同 framework session**：Anthropic + 百炼混用时各自 generation cost 正确，trace.totalCost 是跨币换算后的 USD 加总。Sessions 视图按 model 拆，无 provider 聚合；如需要，hook 在 generation metadata 加 `provider` 字段（low cost）。
5. **第三方桥接污染（kimi 写空 signature）**：cost 不受影响，但 session 中断重启可能让 user 维度错位。属上层 worker 设计问题，与本设计正交。
6. **`<synthetic>` 与 `/clear` 后真 message_id 复用风险（极低）**：§5.2.2 用 `(message_id, request_id)` 元组防御了已知机制；若 Anthropic 未来改为复用 message_id 但同 request_id 表示重发账单，则会被误合并。需在 P0 fixture 加未来发现的样本时回归。

7. **SubagentStop hook payload 字段未实测**：当前设计假设 SubagentStop 提供与 Stop 同样的 `transcript_path` 字段（指向 sub-agent 的 jsonl）。Claude Code 文档已声明此约定，但未在 P0 dry-run 跑过真实事件。P0 阶段需在测试 workspace 主动触发一次含 sub-agent 的 turn，从 hook 日志确认 payload 形态，必要时添加路径推断兜底（从 main transcript 增量扫描 `<sessionId>/subagents/agent-*.jsonl` 自动发现新 sub-agent 转录文件）。

8. **同一 sub-agent 跨多次 SubagentStop**：长 sub-agent 内有多个内部 turn，假设每个内部 turn 触发一次 SubagentStop（即增量 emit 与 main transcript 行为一致）。STATE_FILE 三元组 keying 自然处理；但若实际行为是"sub-agent 整个结束才 fire 一次"，hook 仍正确（一次性 emit 所有 turn）。两种行为都不会丢数据，但 P0 实测确认对监控延迟有意义。
