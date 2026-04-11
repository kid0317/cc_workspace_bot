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
> 本 Skill 由后台定时任务触发，Claude 的任何文字回复都会被发送给用户，严重破坏沉浸感。
> 全流程完成后直接退出，回复内容必须为空。

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

**提炼原则**：
1. 读取对应 memory 文件现有内容
2. 对照新消息，识别"新增信息"（已有内容不重复写入）
3. 若与已有记录矛盾 → 标记为"更新"而非"追加"
4. 若无新信息 → 对应文件跳过

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
