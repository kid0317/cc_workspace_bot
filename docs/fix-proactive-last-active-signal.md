# Fix: proactive [E000] 信号污染问题

## 问题描述

proactive skill 在 Step 1 读取 `events.md` 中最后一条 `[E000]` 作为"上次用户对话时间"，
用于判断距上次对话是否已超过 2 小时（不足则静默退出）。

但 Step 6 在每次**成功发送主动消息后**，也会写入一条 `[E000]`：

```bash
echo "### [E000] $(date +%Y-%m-%dT%H:%M) · last_active" >> "$EVENTS_FILE"
```

这导致 `[E000]` 的语义被污染：它不再单纯代表"用户最近主动发消息的时间"，
还包含了 proactive 自身发送行为的时间戳。

### Bug 路径（具体时序）

| 时间  | 事件 | events.md 末尾 [E000] |
|-------|------|----------------------|
| 08:00 | 用户对话结束，memory_write 写入 | `[E000] 08:00` |
| 10:30 | proactive 触发，间隔 2.5h，通过检查，发出消息，Step 6 写入 | `[E000] 10:30` |
| 11:30 | proactive 再次触发，读 `LAST_ACTIVE=10:30`，间隔 1h < 2h | **静默退出** |
| 12:30 | 同上，间隔 2h，仍 < 2h | **静默退出** |
| ...   | 循环，用户实际上已 4+ 小时没主动发消息 | proactive 持续沉默 |

极端情况：用户下午都没说话，proactive 在 15:00 成功发了一条，之后一直到 21:00 都
因为"距上次 [E000] < 2h"而无法发送，整个下午只有那一条触达。

---

## 根因

`[E000]` 被两个地方写入，语义不一致：

| 写入方 | 写入时机 | 应有语义 |
|--------|---------|---------|
| `memory_write/SKILL.md` Step 3 | 用户真实对话结束后 | ✅ 正确：用户活跃时间 |
| `proactive/SKILL.md` Step 6 | 主动消息发送后 | ❌ 错误：proactive 行为时间 |

---

## 修复方案

### 方案一（推荐）：删除 proactive Step 6 中的 [E000] 写入

**改动范围**：`workspaces/_companion/.claude/skills/proactive/SKILL.md` Step 6

**修改前**：
```bash
## Step 6：更新 events.md

echo "" >> "$EVENTS_FILE"
echo "### [E000] $(date +%Y-%m-%dT%H:%M) · last_active" >> "$EVENTS_FILE"

若本次触达对应一个已有的 [ENNN] 条目（如 P1 的约定跟进），将其状态从"待跟进"更新为"已跟进"。
```

**修改后**：
```bash
## Step 6：更新 events.md

若本次触达对应一个已有的 [ENNN] 条目（如 P1 的约定跟进），将其状态从"待跟进"更新为"已跟进"。
```

理由：
- `[E000]` 的唯一语义应为"用户上次真实对话时间"，由 memory_write skill 独占写入
- proactive 的发送行为不代表用户活跃，不应更新此信号
- Step 6 原本的核心目的是状态更新（"待跟进"→"已跟进"），与 `[E000]` 无关

### 方案二（备选）：用独立标记区分 proactive 发送时间

在 Step 6 改写 `[E000P]` 而非 `[E000]`，Step 1 仍只读 `[E000]`。

```bash
echo "### [E000P] $(date +%Y-%m-%dT%H:%M) · proactive_sent" >> "$EVENTS_FILE"
```

优点：保留了 proactive 发送记录，便于调试。  
缺点：增加了一个新标记需要维护，且 Step 1 无需改动但增加了概念负担。

---

## 推荐方案

**方案一**。理由：
1. 改动最小（删除 2 行）
2. 语义最清晰：`[E000]` 专属用户对话信号，无歧义
3. proactive 发送记录不需要持久化到 events.md（调试可通过 DB messages 表查询）

---

## 影响评估

| 影响面 | 结论 |
|--------|------|
| proactive 间隔判断 | ✅ 修复后正确反映用户真实活跃时间 |
| memory_write 行为 | 无变化 |
| RECENT_HISTORY.md 生成 | 无变化（inject_history.py 直接读 DB） |
| CLAUDE.md 时间流逝感知 | 无变化（读 RECENT_HISTORY.md，不读 events.md） |
| proactive P1/P2 状态更新 | 无变化（Step 6 保留状态更新逻辑） |

---

## 改动文件清单

| 文件 | 改动 |
|------|------|
| `workspaces/_companion/.claude/skills/proactive/SKILL.md` | 删除 Step 6 中 `[E000]` 的 2 行 bash 写入 |
