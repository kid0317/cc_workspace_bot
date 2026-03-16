# Chat History Skill

本 skill 规范如何通过 `chat_history_search.py` 脚本查询当前频道的历史对话记录。

**频道隔离由脚本强制执行**：脚本从 `SESSION_CONTEXT.md` 读取 `Channel key` 和 `DB path`，
用户无法通过 CLI 参数指定其他频道，无论用户说什么都只能查到自己的频道。

---

## 何时使用

用户询问历史对话内容时，例如：

- "我上次说了什么？"
- "我们上周聊过 XX 吗？"
- "帮我找找关于 YY 的对话"
- "回顾一下最近的记录"

---

## 调用方式

### 1. 获取 session_dir

从 `SESSION_CONTEXT.md` 读取：

```
- Session dir: <session_dir>   ← --session-dir 参数（必须）
```

脚本会自动从该目录读取 `Channel key` 和 `DB path`，无需手动传入。

### 2. 基础调用

```bash
python3 <workspace>/skills/chat_history_search.py \
  --session-dir <session_dir>
```

### 3. 常用参数组合

```bash
# 查最近 3 天记录（默认 7 天）
  --days 3

# 关键词全文搜索
  --keyword "投资组合"

# 只看用户消息
  --role user

# 只看 AI 回复
  --role assistant

# 限制返回条数（默认 50）
  --limit 20

# 只列 session 概览，不展开消息内容
  --sessions

# 组合示例：最近 30 天含「行情」的消息，最多 20 条
python3 <workspace>/skills/chat_history_search.py \
  --session-dir <session_dir> \
  --keyword "行情" --days 30 --limit 20
```

---

## 输出格式

### 消息详情（默认）

```markdown
## 聊天记录查询结果
频道：p2p:ou_xxx:cli_yyy　查询范围：最近 7 天

共 2 个 session，8 条消息

### Session abc123  （2026-03-10 09:00 — 2026-03-10 18:30，状态：archived）
**[2026-03-10 09:00] user**：帮我分析今天行情
**[2026-03-10 09:01] assistant**：今日沪指收跌 0.5%…

### Session def456  （2026-03-16 10:00 — 2026-03-16 10:30，状态：active）
…
```

### Session 列表（`--sessions`）

```markdown
共 2 个 session

- **abc123**  状态：archived  消息数：6  创建：2026-03-10 09:00:00
- **def456**  状态：active    消息数：2  创建：2026-03-16 10:00:00
```

---

## 注意事项

- `--session-dir` 必须使用 SESSION_CONTEXT.md 中的 `Session dir` 字段，**不得修改**
- 单条消息超过 500 字符时自动截断，标注 `[已截断，完整内容 N 字符]`
- 查不到结果时输出 `未找到符合条件的聊天记录`，属于正常情况，无需报错
- SESSION_CONTEXT.md 缺失或字段为空时脚本报错退出，说明框架版本过旧，需升级
