# Todo Skill — 待办管理

本 skill 规范待办事项的**提醒、添加、标记完成、每日日志、历史回顾**五项功能。

---

## 数据文件结构

```
memory/
├── todos_today.md              # 今日待办（每天覆盖/清空）
├── todos_ongoing.md            # 持续跟进（长期任务，不自动清空）
└── daily_logs/
    ├── YYYY-MM-DD.md           # 每日工作日志（收工时生成）
    ├── todos_YYYY-MM-DD.md     # 未来某日待办（非今天的日期安排）
    └── ...
```

> `daily_logs/` 中存在两种文件：
> - `YYYY-MM-DD.md`：当日收工时生成的工作日志
> - `todos_YYYY-MM-DD.md`：提前记录的未来某日待办

所有路径从 `SESSION_CONTEXT.md` 读取 `Memory dir`，拼接子路径，**不使用硬编码**。

---

## 触发识别

| 用户说 | 触发动作 |
|--------|---------|
| 「今天要做什么」「我的待办」「提醒我」「今日任务」 | → **提醒** |
| 「加一条」「记下来」「添加」「新增待办」 | → **添加** |
| 「完成了」「做完了」「标记完成」「✓ XXX」 | → **标记完成** |
| 「收工」「下班了」「今天做了什么」「生成日志」 | → **记录日志** |
| 「上周」「昨天」「回顾」「X月X日做了什么」 | → **回顾历史** |

---

## 功能一：提醒（Reminder）

读取当天待办和持续跟进，格式化输出。**调用前先检查跨日**，若日期不符则归档昨天并初始化今天。

### 操作步骤

```bash
# 0. 跨日检查（从 SESSION_CONTEXT.md 读取 Current date）
TODAY=<current_date_from_session_context>
HEADER=$(head -1 <memory_dir>/todos_today.md 2>/dev/null)
# todos_today.md 第一行格式为：# 今日待办 — YYYY-MM-DD
if [ -n "$HEADER" ] && echo "$HEADER" | grep -q "$TODAY"; then
  : # 日期匹配，无需处理
else
  # 日期不匹配（跨天）或文件不存在：先执行昨日日志归档（见功能四），再重置 todos_today.md
  YESTERDAY=$(echo "$HEADER" | grep -o '[0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}')
  # 归档后初始化新文件头
  flock -x <memory_lock_path> -c "
    echo '# 今日待办 — $TODAY' > <memory_dir>/todos_today.md
  "
fi

# 1. 读取今日待办
cat <memory_dir>/todos_today.md

# 2. 读取持续跟进
cat <memory_dir>/todos_ongoing.md
```

### 输出格式

```
📋 **今日待办**（YYYY-MM-DD）

**今天**
- [ ] 任务 A
- [x] 任务 B（已完成）

**持续跟进**
- [ ] 长期事项 C（持续跟进中）

共 N 项未完成，加油！
```

### 注意

- 若 `todos_today.md` 为空，提示「今天还没有添加待办，要加几条吗？」
- 已完成项（`[x]`）正常显示，让用户感受进度感

---

## 功能二：添加（Add）

向今日待办、未来某日待办或持续跟进中追加新条目。

### 判断规则

| 用户描述 | 写入目标 |
|---------|---------|
| 含「今天」或无日期 | `todos_today.md` |
| 含具体日期（「下周二」「3月17日」「明天」等） | `daily_logs/todos_YYYY-MM-DD.md`（对应日期文件） |
| 含「持续跟进」「长期」「一段时间」 | `todos_ongoing.md` |
| 无法判断 | 询问「是今天的任务、某天的安排，还是需要持续跟进的事项？」 |

### 日期解析

- 「明天」→ 当前日期 +1 天
- 「下周二」→ 计算下一个周二的日期
- 「3月17日」「3/17」→ 当年对应日期，若已过则取次年
- 解析后在回复中确认具体日期，避免歧义

### 写入格式

```
- [ ] <任务描述>（录入于 YYYY-MM-DD）
```

### 未来日期待办文件

文件路径：`<memory_dir>/daily_logs/todos_YYYY-MM-DD.md`

```bash
flock -x <memory_lock_path> -c "
  mkdir -p <memory_dir>/daily_logs
  if [ ! -f <memory_dir>/daily_logs/todos_YYYY-MM-DD.md ]; then
    cat > <memory_dir>/daily_logs/todos_YYYY-MM-DD.md << 'EOF'
# 待办 — YYYY-MM-DD

## 待办列表

EOF
  fi
  echo '- [ ] <任务描述>（录入于 $(date +%Y-%m-%d)）' >> <memory_dir>/daily_logs/todos_YYYY-MM-DD.md
"
```

### 今日待办写入示例

```bash
flock -x <memory_lock_path> -c "
  # 若文件不存在或无日期头，先写入头部
  if [ ! -f <memory_dir>/todos_today.md ] || ! head -1 <memory_dir>/todos_today.md | grep -q '[0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}'; then
    echo '# 今日待办 — $(date +%Y-%m-%d)' > <memory_dir>/todos_today.md
  fi
  echo '- [ ] <任务描述>（录入于 $(date +%Y-%m-%d)）' >> <memory_dir>/todos_today.md
"
```

### 每日提醒时合并当天待办

若 `daily_logs/todos_YYYY-MM-DD.md`（今天日期）存在，将其内容合并展示到今日待办中，提示用户可归入 `todos_today.md`。

---

## 功能三：标记完成 / 取消完成（Toggle）

将指定任务的 `[ ]` 改为 `[x]`，或反向操作。

### 识别用户意图

- 「完成了 XXX」「XXX 做完了」→ 标记 `[x]`
- 「XXX 没做完」「取消完成」→ 恢复 `[ ]`
- 若描述模糊，列出当前未完成项让用户选择编号

### 操作步骤

```bash
flock -x <memory_lock_path> -c "
  sed -i 's/- \[ \] <任务描述>/- [x] <任务描述>/' <memory_dir>/todos_today.md
"
```

### 注意

- 在今日待办和持续跟进中同时查找匹配项
- 标记完成后回复确认：「✅ 已标记完成：XXX」

---

## 功能四：记录每日日志（Daily Log）

在用户「收工」或主动要求时，将当天工作汇总写入日志文件。

### 日志文件路径

```
<memory_dir>/daily_logs/YYYY-MM-DD.md
```

### 日志模板

```markdown
# 工作日志 — YYYY-MM-DD（周X）

## 今日待办完成情况

| 状态 | 事项 |
|------|------|
| ✅ 完成 | 事项 A |
| ❌ 未完成 | 事项 B |

**完成率**：N/M（N%）

## 持续跟进状态

- [ ] 长期事项 C（进行中）

## 今日小结

> [3-5 句话，由 AI 根据完成情况生成，或由用户补充]

---
_记录时间：HH:MM_
```

### 写入流程

```bash
# 1. 读取今日待办
cat <memory_dir>/todos_today.md

# 2. 读取持续跟进
cat <memory_dir>/todos_ongoing.md

# 3. 生成日志内容（AI 汇总）

# 4. 写入日志文件（flock 加锁）
flock -x <memory_lock_path> -c "
  mkdir -p <memory_dir>/daily_logs
  cat > <memory_dir>/daily_logs/$(date +%Y-%m-%d).md << 'EOF'
  ...日志内容...
  EOF
"
```

### 收工后处理

1. 将 `todos_today.md` 的已完成条目保留，供第二天对比参考
2. **不自动清空** `todos_today.md`；次日用户明确说「新的一天开始」或「清空今日待办」时再清空

---

## 功能五：历史回顾（Review）

查询并展示过去某天或某段时间的工作日志。

### 触发识别

| 用户说 | 解析 |
|--------|------|
| 「昨天做了什么」 | 读取 `daily_logs/YYYY-MM-DD.md`（昨天日期） |
| 「上周做了什么」 | 读取上周各工作日日志，汇总 |
| 「3月10日做了什么」 | 读取 `daily_logs/2026-03-10.md` |
| 「最近一周工作回顾」 | 读取最近 7 天日志，生成总结 |

### 操作示例

```bash
# 读取昨天日志
YESTERDAY=$(date -d "yesterday" +%Y-%m-%d)
cat <memory_dir>/daily_logs/${YESTERDAY}.md 2>/dev/null || echo "暂无昨天的日志记录"

# 列出所有日志文件（用于概览）
ls <memory_dir>/daily_logs/ 2>/dev/null | sort -r | head -20
```

### 输出格式（周回顾示例）

```
📊 **上周工作回顾**（YYYY-MM-DD ~ YYYY-MM-DD）

| 日期 | 完成 | 未完成 |
|------|------|--------|
| 周一 | 3 项 | 1 项 |
| ...  | ...  | ...  |

**本周完成率**：XX%
**本周小结**：[AI 生成 3 句话总结]
```

---

## 注意事项

- 所有文件读写使用 `flock -x` 加锁，防止并发冲突
- `daily_logs/` 目录按需创建（`mkdir -p`）
- 日志文件写入后不得修改核心内容，只允许追加「补充说明」段落
- 持续跟进的条目不受「清空今日待办」影响
