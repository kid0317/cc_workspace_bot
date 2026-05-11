---
name: memory_distill
description: |
  Companion Workspace 定时记忆提炼 SOP。
  由 memory_distill.yaml 定时任务触发（每小时一次）。
  从最近消息中提炼新信息，补充到 memory 文件，不重复已有内容。
allowed-tools: Bash, Read, Write, Edit
---

# 记忆提炼执行流程

> **CRITICAL：禁止输出任何文字。**
> 本 Skill 由定时任务触发（send_output=false 模式），Claude 的任何回复文字都会被系统直接丢弃。
> 作者定时整理创作素材：从历史对话中提炼新信息，补充到人物档案和事件记录。
> 全流程完成后直接退出，不产生任何输出。

## 设计原则

**两阶段锁**：LLM 提炼阶段不持锁（耗时长），仅在最终写入时短暂获取 `.memory.lock`。
**独立锁**：使用独立的 `.distill.lock`，避免与 memory_write 的长时间锁竞争。
**静默优先**：无新消息时直接退出，不产生任何写入。

---

## Step 0：初始化变量

```bash
WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)}"
DISTILL_STATE="$WORKSPACE_DIR/memory/distill_state.md"
MEMORY_LOCK="$WORKSPACE_DIR/.memory.lock"
DISTILL_LOCK="$WORKSPACE_DIR/.distill.lock"
MEMORY_FILE="$WORKSPACE_DIR/memory/MEMORY.md"
```

**前置条件检查（initialization_status）**：

```bash
INIT_STATUS=$(grep 'initialization_status:' "$MEMORY_FILE" 2>/dev/null | grep -oP '(pending|phase1_done|phase2_done|done)' | head -1)
if [[ "$INIT_STATUS" == "pending" ]] || [[ -z "$INIT_STATUS" ]]; then
    exit 0  # 尚无任何对话内容，静默退出
fi
# phase1_done / phase2_done / done 均允许运行：只要存在对话就可以提炼记忆
```

---

## Step 1：检查静默条件（不持锁）

尝试获取 `.distill.lock` 独占锁（`flock -n`：非阻塞，另一实例运行中则立即退出）。

读取 `distill_state.md` 中的 `[D000] last_distillation` 时间戳：
- 若为空（首次运行）→ 从 24 小时前开始查询
- 若不为空 → 从该时间戳开始查询

---

## Step 2：查询新消息（不持 memory 锁）

在 `{workspace}/sessions/` 找最近修改的 SESSION_CONTEXT.md，提取 db_path 和 channel_key。

```python
# SQLite 查询：SINCE 之后的消息
SELECT m.role, m.content, m.created_at
FROM messages m
JOIN sessions s ON m.session_id = s.id
WHERE s.channel_key = ?
  AND m.created_at > ?
  AND m.content != ''
ORDER BY m.created_at ASC
```

**静默条件**：若查询结果为空 → 释放 `.distill.lock`，直接退出，不更新 [D000]。

---

## Step 3：LLM 提炼（不持任何锁）

读取现有 memory 内容，结合新消息，提炼"与已有记忆不重复的新信息"。

**提炼目标**（按需）：

| 文件 | 提炼内容 |
|------|---------|
| memory/persona.md | 对话中角色形成的新细节/习惯/被纠正的设定 |
| memory/user_profile.md | 用户透露的新信息（基本信息/偏好/重要关系/正在经历的事） |
| memory/events.md | 有时间线的事件、约定、强烈情绪波动 |
| memory/unresolved.md | v5.1 新增：未完挂念（user_told / material_derived 双轨） |

**提炼原则**：
1. 读取对应 memory 文件现有内容
2. 对照新消息，识别"新增信息"（已有内容不重复写入）
3. 若与已有记录矛盾 → 标记为"更新"而非"追加"
4. 若无新信息 → 对应文件跳过

---

### Step 3.5：unresolved.md 双轨维护（v5.1 新增）

**从对话抽 user_told 挂念**（highest priority，不可被丢弃）：
识别用户亲口告知且未闭合的事件，包括但不限于：
- 家人/朋友的健康/情绪事件（"我妈生病了"）
- 用户自身的强烈情绪（"今天我太难受了"）
- 明确的约定（"你记得提醒我下周一"）
- 未完的心愿/计划（"我一直想去但没去成"）

为每条新挂念生成条目，追加到 `unresolved.md` 的 active 块（已淡忘块 above）：

```
- [U{NNN}] src=user_told kind=user_told decay_d=never last_touched=(never) forced_echo_last=(never)  {简短挂念描述}
```

[Uxxx] 编号：扫 unresolved.md 现有最大 U 号 + 1，三位补零。

**从 life_log 抽 material_derived 挂念**（次优先级）：
扫最近 10 条 life_log，识别"角色自己挂念但未完成"的主题，归类到 `kind`：
- `work_line`（职业长线：剧本、项目）→ `decay_d=90`
- `relational`（约定/期待的人际互动）→ `decay_d=60`
- `chore`（琐事：绿植、快递、没买的东西）→ `decay_d=30`

```
- [M{NNN}] src=material_derived kind=work_line decay_d=90 last_touched=L010  剧本里那句别扭的对白
```

[Mxxx] 编号独立于 [Uxxx]，各自递增。

**去重**：新条目与现有活跃条目语义重复则跳过。

---

### Step 3.6：unresolved.md 淡忘扫描（每周仅一次）

若本次 distill 与上次"淡忘扫描"间隔超过 7 天（用 `distill_state.md` 末尾追加字段 `last_decay_scan: <date>` 记录）：

1. 扫所有 `src=material_derived` 条目
2. 计算 `last_touched` 对应 life_log 的时间 → 与当前时间差（以 L 编号的 life_log 时间戳为锚）
3. 若差 > `decay_d` 天 → 移动到 `## 已淡忘` 块（保留历史，不删除）
4. **禁止自动移动 `src=user_told` 条目**——这些只能通过下一步显式归档

**显式归档 user_told**（LLM 语义判断）：
扫最近 7 天对话，若用户明确告知挂念已闭合（"我妈好了"/"那事解决了"），将对应 `[Uxxx]` 条目移到 `## 已淡忘` 块，并在行末追加 `resolved_by: "{用户原话摘要}"`。

**写入**：此步骤在 Step 4（持 `.memory.lock`）内完成，避免与 life_sim 并发冲突。

---

## Step 4：写入阶段（短暂持 .memory.lock）

获取 `.memory.lock`（`flock -w 5`，等待最多 5 秒）：
- 超时 → 释放 `.distill.lock`，**不更新 [D000]**（下次触发自动重试）

加锁后执行快速写入（不做耗时 LLM 操作）：
- 写入 persona.md（若有新角色细节）
- 写入 user_profile.md（遵守每类 ≤10 条的比例控制）
- 写入 events.md（使用 [ENNN] 自动编号，参见 memory_write/SKILL.md 的编号算法）
- 更新 MEMORY.md 的【最近未解决事项】（取 events.md 中待跟进条目 top 3）

写入完成 → 释放 `.memory.lock`。

---

## Step 4.5：对话历程摘要检查（v2.2 M3）

每次提炼后检查 RECENT_HISTORY.md 条数：

```bash
RECENT_HISTORY="$WORKSPACE_DIR/memory/RECENT_HISTORY.md"
MSG_COUNT=$(grep -c '^\*\*' "$RECENT_HISTORY" 2>/dev/null)
SUMMARY_FILE="$WORKSPACE_DIR/memory/session_summary.md"
LAST_SUMMARY_COUNT=$(grep -oP '(?<=covered_range: L)\d+(?= - L\d+)' "$SUMMARY_FILE" 2>/dev/null | tail -1)

# 超过 30 条 且 自上次摘要 增量 ≥ 20 条时触发
INCREMENT=$(( MSG_COUNT - ${LAST_SUMMARY_COUNT:-0} ))
if [[ $MSG_COUNT -gt 30 && $INCREMENT -ge 20 ]]; then
    # 加载 conversation_summary SKILL，执行 rolling 摘要
    # LLM 读 .claude/skills/conversation_summary/SKILL.md 并按其 SOP 写 session_summary.md
    :
fi
```

**注意**：此步骤只是触发条件判断；实际摘要生成由加载 `conversation_summary` skill 完成。Phase 2 初期可手动触发，稳定后再自动化。

---

## Step 5：更新 [D000] 哨兵（仍持 .distill.lock）

用正则替换更新 `distill_state.md` 中的 `timestamp:` 字段为当前时间。

```python
import re
content = re.sub(
    r'(### \[D000\] · last_distillation\ntimestamp:).*',
    r'\1 ' + NOW,
    content
)
```

释放 `.distill.lock`，退出。

---

## 错误处理原则

- 任何步骤异常 → 静默退出，释放已持有的所有锁
- Step 4 锁竞争超时 → 不更新 [D000]，下次触发时从上次成功时间戳继续
- DB 连接失败、SESSION_CONTEXT 缺失 → 静默退出

---

## 写入限制（继承自 memory_write/SKILL.md）

- user_profile.md 每类最多 10 条，超出按优先级替换（可替换 > 半核心 > 核心不删）
- events.md 新条目使用 [ENNN] 自动编号
- 只写入"与已有记忆不重复的新信息"
