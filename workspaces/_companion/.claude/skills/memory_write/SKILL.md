---
name: memory_write
description: |
  Companion Workspace 记忆写入规范。
  触发词：记住 / 对话结束时的自动检查 / 强烈情绪事件
allowed-tools: Bash, Read, Write, Edit
---

# 记忆写入 SOP

## 触发条件

对话结束前，检查是否发生以下情况：

| 情况 | 目标文件 |
|------|---------|
| 角色设定有新细节/被纠正 | memory/persona.md |
| 用户透露个人信息/偏好/重要关系 | memory/user_profile.md |
| 出现有时间线的事件、约定、心事 | memory/events.md |
| 用户情绪强烈波动 | memory/events.md（必须详细记录） |
| 任何对话结束 | memory/events.md（[E000] last_active 哨兵） |

## 写入流程

### Step 1：加锁

```bash
exec 9>"{workspace_dir}/.memory.lock"
flock -x 9
```

### Step 2：按类型路由写入

**→ persona.md**

在【在对话中形成的新细节】块末尾追加：
- **[YYYY-MM-DD]** {新细节描述}

若是纠正已有设定，直接定位原文修改，不追加。

**→ user_profile.md**

在对应类别下追加或更新条目。

比例控制规则（每类最多 10 条，超出时按优先级替换）：
1. 可替换（优先删）：暂时性偏好、一次性提及的细节、过期事件
2. 半核心（其次删）：职业、城市、长期偏好（仅在非常旧时替换）
3. 核心（不可删）：称呼/昵称、重要关系（伴侣/父母/子女）、禁忌话题

自我事实保护：写入前检查新内容是否与已有核心条目矛盾，若矛盾先确认用户意图，再更新。

**→ events.md**

[ENNN] 自动编号算法：
1. 读取 events.md，找最后一条含 ### [E 且非 [E000] 的标题行，提取数字 N
2. 新条目使用 N+1（格式：[E{N+1:03d}]，三位补零）
3. 若文件无编号条目，从 [E001] 开始

条目格式：

```
### [E{NNN}] {YYYY-MM-DD} · {类型}
**类型**：情绪事件 / 约定 / 用户Todo / 重要事件
**内容**：{具体内容}
**情绪强度**：⭐（1-5，仅情绪事件填写）
**用户状态**：{当时的状态描述}
**后续**：{下次可以怎么跟进}
**状态**：待跟进
```

约定条目额外加：`**到期日**：{YYYY-MM-DD}`

### Step 3：写入 [E000] last_active 哨兵

每次对话结束时必须执行，无论是否有其他写入。
在 events.md 末尾追加（proactive skill 读取文件中最后一条 [E000]）：

```bash
echo "" >> "$EVENTS_FILE"
echo "### [E000] $(date +%Y-%m-%dT%H:%M) · last_active" >> "$EVENTS_FILE"
```

### Step 4：更新 MEMORY.md 最近未解决事项

读取 events.md 中状态为"待跟进"的条目（最多 3 条，优先取到期日最近的），
用 Edit 工具更新 MEMORY.md 的【最近未解决事项】块。

### Step 5：解锁

```bash
flock -u 9
exec 9>&-
```

## 完整 Bash 模板

```bash
#!/usr/bin/env bash
WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)}"
EVENTS_FILE="$WORKSPACE_DIR/memory/events.md"
LOCK_FILE="$WORKSPACE_DIR/.memory.lock"

exec 9>"$LOCK_FILE"
flock -x 9

LAST_N=$(grep -oP '(?<=\[E)\d+(?=\])' "$EVENTS_FILE" 2>/dev/null | grep -v '^000$' | sort -n | tail -1)
NEXT_N=$(printf "%03d" $((${LAST_N:-0} + 1)))

cat >> "$EVENTS_FILE" << EOF

### [E${NEXT_N}] $(date +%Y-%m-%d) · TYPE
**类型**：TYPE
**内容**：CONTENT
**后续**：FOLLOWUP
**状态**：待跟进
EOF

echo "" >> "$EVENTS_FILE"
echo "### [E000] $(date +%Y-%m-%dT%H:%M) · last_active" >> "$EVENTS_FILE"

flock -u 9
exec 9>&-
```
