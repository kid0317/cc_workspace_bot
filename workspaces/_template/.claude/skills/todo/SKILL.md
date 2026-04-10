---
name: todo
description: "当用户查看/添加/完成待办、说「收工」需要生成日志、或回顾历史工作记录时使用"
type: procedure
version: "1.0"
---

# Todo Skill — 工作待办管理

本 skill 规范工作待办的**提醒、添加、标记完成、每日日志、历史回顾**五项功能。

---

## 数据文件结构

```
memory/
├── todos_today.md              # 今日待办（每天覆盖/清空）
├── todos_ongoing.md            # 持续跟进（长期任务，不自动清空）
└── daily_logs/
    ├── YYYY-MM-DD.md           # 每日工作日志（按日期一文件）
    ├── todos_YYYY-MM-DD.md     # 未来某日待办（非今天的日期待办）
    └── ...
```

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

读取当天待办和持续跟进，格式化输出。

### 操作步骤

```bash
# 1. 读取今日待办
cat <memory_dir>/todos_today.md

# 2. 读取持续跟进
cat <memory_dir>/todos_ongoing.md
```

### 输出格式

```
📋 **今日待办**（<today>）

**今天**
- [ ] 参加 10 点站会
- [x] 完成冒烟测试报告

**持续跟进**
- [ ] 开发 mock 功能（持续跟进中）

共 N 项未完成，加油！
```

### 注意

- 若 `todos_today.md` 的待办列表为空，提示「今天还没有添加待办，要加几条吗？」
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

文件不存在时自动创建：

```bash
flock -x <memory_lock_path> -c "
  mkdir -p <memory_dir>/daily_logs
  # 若文件不存在，先写入文件头
  if [ ! -f <memory_dir>/daily_logs/todos_YYYY-MM-DD.md ]; then
    cat > <memory_dir>/daily_logs/todos_YYYY-MM-DD.md << 'EOF'
# 待办 — YYYY-MM-DD

## 待办列表

EOF
  fi
  echo "- [ ] <任务描述>（录入于 <today>）" >> <memory_dir>/daily_logs/todos_YYYY-MM-DD.md
"
# 注意：<today> 替换为 SESSION_CONTEXT.md 中的当前日期（YYYY-MM-DD 格式）
```

### 今日待办写入示例

```bash
flock -x <memory_lock_path> -c "
  echo \"- [ ] <任务描述>（录入于 <today>）\" >> <memory_dir>/todos_today.md
"
# 注意：<today> 替换为 SESSION_CONTEXT.md 中的当前日期（YYYY-MM-DD 格式）
```

### 每日提醒时合并当天待办

当天 10:00 推送或用户查看今日待办时，若 `daily_logs/todos_YYYY-MM-DD.md`（今天日期）存在，将其内容合并展示到今日待办中，并提示用户可将其归入 `todos_today.md`。

---

## 功能三：标记完成 / 取消完成（Toggle）

将指定任务的 `[ ]` 改为 `[x]`，或反向操作。

### 识别用户意图

- 「完成了 XXX」「XXX 做完了」→ 标记 `[x]`
- 「XXX 没做完」「取消完成」→ 恢复 `[ ]`
- 若描述模糊，列出当前未完成项让用户选择编号

### 操作步骤

```bash
# 使用 sed 替换（精确匹配任务描述）
flock -x <memory_lock_path> -c "
  sed -i 's/- \[ \] <任务描述>/- [x] <任务描述>/' <memory_dir>/todos_today.md
"
```

### 注意

- 在今日待办和持续跟进中同时查找匹配项
- 标记完成后回复确认：「✅ 已标记完成：XXX」

---

## 功能四：记录每日日志（Daily Log）

在用户「收工」或主动要求时，将当天工作汇总写入日志文件，供日后回顾。

### 触发时机

- 用户说「收工」「下班了」「今天做了什么」「生成日志」
- 每天 10:00 定时推送后，若当天已有待办，晚间可自动提示（可选）

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
| ✅ 完成 | 参加 10 点站会 |
| ❌ 未完成 | 完成冒烟测试报告 |

**完成率**：N/M（N%）

## 持续跟进状态

- [ ] 开发 mock 功能（进行中）
- [x] 整理季度测试总结报告（已完成）

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

生成日志后：
1. 将 `todos_today.md` 的已完成条目保留（不清空），供第二天对比参考
2. **不自动清空** `todos_today.md`；次日用户明确说「新的一天开始」或「清空今日待办」时再清空

---

## 功能五：历史回顾（Review）

查询并展示过去某天或某段时间的工作日志。

### 触发识别

| 用户说 | 解析 |
|--------|------|
| 「昨天做了什么」 | 读取 `daily_logs/YYYY-MM-DD.md`（昨天日期） |
| 「上周做了什么」 | 读取上周 5 个工作日的日志，汇总 |
| 「3月10日做了什么」 | 读取 `daily_logs/YYYY-MM-DD.md`（对应日期） |
| 「最近一周工作回顾」 | 读取最近 7 天日志，生成总结 |

### 操作示例

```bash
# 读取昨天日志
YESTERDAY=$(date -d "yesterday" +%Y-%m-%d)
cat <memory_dir>/daily_logs/${YESTERDAY}.md 2>/dev/null || echo "暂无昨天的日志记录"

# 读取某日志（TARGET_DATE 为解析后的具体日期，如 2026-03-10）
cat <memory_dir>/daily_logs/${TARGET_DATE}.md 2>/dev/null

# 列出所有日志文件（用于概览）
ls <memory_dir>/daily_logs/ 2>/dev/null | sort -r | head -20
```

### 输出格式（周回顾示例）

```
📊 **上周工作回顾**（YYYY-MM-DD ~ YYYY-MM-DD）

| 日期 | 完成 | 未完成 |
|------|------|--------|
| 周一 | 3 项 | 1 项 |
| 周二 | 2 项 | 0 项 |
| ...  | ...  | ...  |

**本周完成率**：XX%

**持续跟进项进展**：
- 开发 mock 功能：进行中

**本周小结**：[AI 生成 3 句话总结]
```

---

## 常见对话处理

### 场景：用户说「收工」

1. 读取今日待办，统计完成/未完成
2. 读取持续跟进，列出未完成项
3. 生成日志文件（`daily_logs/YYYY-MM-DD.md`）
4. 回复：

```
🌙 今天辛苦了！

**今日完成**：X 项 / Y 项（完成率 N%）
- ✅ 参加 10 点站会
- ❌ 冒烟测试报告（未完成）

**日志已保存** → `daily_logs/<today>.md`

有什么想补充的小结吗？（直接说，我帮你加进去）
```

### 场景：用户说「清空今日待办」

```bash
flock -x <memory_lock_path> -c "
cat > <memory_dir>/todos_today.md << 'EOF'
# 今日待办

> 每天早上在此记录当天需要完成的事项，完成后标记 [x]。

## 待办列表

EOF
"
```

---

## 注意事项

- 所有文件读写使用 `flock -x` 加锁，防止并发冲突
- `daily_logs/` 目录按需创建（`mkdir -p`）
- 日志文件写入后不得修改核心内容，只允许追加「补充说明」段落
- 持续跟进的条目不受「清空今日待办」影响
- 日志中涉及同事姓名等隐私信息，建议用角色代称（如「开发同学」「测试 PM」）
