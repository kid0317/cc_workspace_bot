# xh_yibu 主动触达 Bad Case 排查报告

**日期**：2026-04-12  
**涉及 workspace**：`/root/xh_yibu`  
**时间线**：08:25–15:49 CST

---

## 事件时间线

| 时间 (CST) | 事件 | 预期 | 实际 |
|-----------|------|------|------|
| 08:25–09:35 | 用户正常聊天，最后一条 09:35 | — | ✅ |
| 12:25 | proactive 触发，发送"昨晚睡得好不好？" | 9:35→12:25 = 2.8h > 2h，允许发送 | ✅（旧 [E000] 机制） |
| 13:25 | proactive 触发，发送"两只小精灵抢石头" | 12:25→13:25 = 1h < 2h，**应静默** | ❌ 发出 |
| 14:25 | proactive 触发，发送 POKAPIA 消息（内容近似重复） | 应静默或不重复 | ❌ 发出 |
| 15:25 | proactive 触发，发送"两只小精灵"（再次重复） | 同上 | ❌ 发出 |
| 15:49 | 用户回复"嗯嗯，好好笑" | 模型认出自己讲了石头故事 | ❌ 回复"什么什么什么，好笑的事要说出来！" |

---

## 问题清单与根因分析

### 问题一：LAST_ACTIVE 永远为空，2小时间隔门失效

**现象**（来自 13:25 运行日志）：
```
LAST_ACTIVE: []
No last_active found, will allow sending
```

**排查**：

proactive SKILL.md（本次修复后的版本）用以下 bash 查询用户最近一条消息时间：
```bash
LAST_ACTIVE=$(sqlite3 "$DB_PATH" \
    "SELECT MAX(m.created_at) FROM messages m ..." \
    2>/dev/null)
```

验证结果：
```
sqlite3 NOT FOUND
```

**根因**：`sqlite3` CLI 二进制在本系统上未安装。`2>/dev/null` 将报错静默，`LAST_ACTIVE=""` → 代码进入 "首次运行，允许发送" 分支 → **2小时时间门永久失效**。

DB 中实际存在用户消息（经 Python sqlite3 模块验证）：
```
role=user  count=49  last=2026-04-12 09:35:26.452...+08:00
```

---

### 问题二：proactive 消息连续发送（13:25 / 14:25 / 15:25）

**现象**：在 12:25 已发送的情况下，13:25、14:25、15:25 继续发送。

**根因**：由问题一导致——LAST_ACTIVE 永远为空，唯一的频率限制只剩 skip_count/随机概率门（15% 基础概率，skip_count 上限 8）。随机门在三次运行中均通过，造成连续发送。

**次要根因**：设计上缺少"上次主动触达时间"冷却检查。当前 Step 1 只检查"距上次用户消息 < 2h"，但没有检查"距上次主动触达 < Xh"。即使用户一直不回复，proactive 理论上也可以在每个通过概率门的小时里都发送。

---

### 问题三：内容高度重复（石头故事三次）

**现象**：13:25、14:25、15:25 发送的消息内容几乎一致，都在讲"两只小精灵抢石头"。

**排查**：

proactive SKILL.md Step 4 读取 `memory/RECENT_HISTORY.md` 作为上下文。但 `RECENT_HISTORY.md` 由 `inject_history.py` 写入，该脚本是 `UserPromptSubmit` hook，**仅在用户发消息前触发**。

- 12:25 proactive 发出后，RECENT_HISTORY.md 仍反映 09:35 时的状态（用户最后一条消息）
- 13:25、14:25、15:25 的 proactive 运行时，RECENT_HISTORY.md 没有包含 12:25 的主动触达内容
- 每次 proactive 生成消息时，都"以为"自己还没讲过石头故事

**根因**：proactive 任务不经过 `UserPromptSubmit` hook，无法自动刷新 RECENT_HISTORY.md；缺乏机制让 proactive 在执行前感知自己的发送历史。

验证：实际 DB 中已有 4 条 proactive assistant 消息（send_and_record.py 写入成功），但 proactive 任务本身无法读取。

---

### 问题四：用户回复"嗯嗯，好好笑"后模型不认自己讲的故事

**现象**：15:49 用户回复，模型回应"什么什么什么，好笑的事要说出来！"，完全不知道石头故事是自己讲的。

**排查**：

`inject_history.py` 查询逻辑（Python sqlite3，无问题），能找到 4 条 proactive 消息。

时间戳格式不一致：

| 来源 | 写入方 | 时间格式 | 示例 |
|------|-------|---------|------|
| 用户消息 | Go GORM | `+08:00` CST | `2026-04-12 09:35:26.452+08:00` |
| proactive 消息 | send_and_record.py | UTC `+00:00` | `2026-04-12T07:26:20+00:00` |

`inject_history.py` 的 `parse_timestamp` 使用 `datetime.fromisoformat()` + `strftime("%Y-%m-%d %H:%M")`，**没有将 UTC 转换为本地时间**：

```python
dt = datetime.fromisoformat("2026-04-12T07:26:20+00:00")
dt.strftime("%Y-%m-%d %H:%M")  # → "2026-04-12 07:26" ← UTC时间!
# 正确应为 CST: "2026-04-12 15:26"
```

后果：
- RECENT_HISTORY.md 中最后一条 proactive 消息显示时间 `07:26`（UTC），而 SESSION_CONTEXT.md 的 `Current datetime` 是 `15:49`（CST）
- CLAUDE.md 的【时间流逝感知】规则：`07:26 → 15:49 = 8小时23分 > 6小时` → 触发"视为新的时间段，不接续之前话题"
- 模型不知道自己刚才讲了石头故事，把用户的"好好笑"当成全新信息处理

---

### 问题五：WORKSPACE_DIR 被设为 session 目录，记忆全部读取失败

**现象**（来自日志）：
```bash
WORKSPACE_DIR="/root/xh_yibu/sessions/2377802c-510b-422e-9502-981fc19c278a"
cat "$WORKSPACE_DIR/memory/MEMORY.md"  → MEMORY.md not found
```

RECENT_HISTORY.md、MEMORY.md、events.md 等全部读取失败，导致：
- `INIT_STATUS` 为空 → proactive 可能因初始化检查误判
- 所有记忆文件路径错误

**排查**：

SKILL.md 使用 `WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)}"`:
- 若 `$WORKSPACE_DIR` 环境变量正确设置为 `/root/xh_yibu`，则使用正确路径
- 若未设置或被覆盖，则 fallback 到 `$(pwd)` = session 目录（executor 设置 `cmd.Dir = sessionDir`）

`executor.go` 通过以下方式设置 WORKSPACE_DIR：
```go
cmd.Env = append(baseEnv,
    "WORKSPACE_DIR="+req.WorkspaceDir,  // "/root/xh_yibu"
)
```

但 `baseEnv` 是从当前进程环境继承的。若服务器进程本身运行在 Claude Code session 中（服务器被 Claude Code 启动），则会**继承** `WORKSPACE_DIR=/root/cc_workspace_bot`。

Linux 下当 `cmd.Env` 中同一 key 出现两次时，`getenv()` 返回**第一个**匹配项。

```
baseEnv 中: WORKSPACE_DIR=/root/cc_workspace_bot  ← 第一个，优先返回
append 后:  WORKSPACE_DIR=/root/xh_yibu           ← 第二个，被忽略
```

结果：Claude 子进程的 `$WORKSPACE_DIR` = `/root/cc_workspace_bot`（服务器目录），而非 `/root/xh_yibu`。

SKILL.md 的 `${WORKSPACE_DIR:-$(pwd)}` 得到非空值 `/root/cc_workspace_bot`，而非 fallback 到 pwd，所以问题比预想更隐蔽：路径是一个存在的目录，但 memory/ 等子目录在那里不存在（除非 cc_workspace_bot 里恰好有同名目录）。

当服务器通过 `start.sh` 以守护进程方式运行时，**不在 Claude Code 会话内启动**，所以不会继承 WORKSPACE_DIR，问题不出现。当用户在 Claude Code 中手动启动服务器时，会继承 Claude Code 当前工作区的 WORKSPACE_DIR，问题出现。

---

## 问题汇总

| # | 问题 | 根因 | 影响 |
|---|------|------|------|
| P1 | `sqlite3` CLI 不存在 → LAST_ACTIVE 永远空 | 系统未安装 sqlite3 CLI | 2小时间隔门完全失效 |
| P2 | proactive 连续发送，无冷却 | P1 导致 + 缺少"上次主动触达"冷却 | 用户被密集打扰 |
| P3 | 内容高度重复 | proactive 无法感知自己的发送历史（RECENT_HISTORY.md 未刷新）| 同样内容发 3 次 |
| P4 | 模型不认自己讲的故事 | inject_history.py UTC时间未转本地 → 时间间隔计算错误 → 错误"新对话"行为 | 对话连续性断裂 |
| P5 | WORKSPACE_DIR 指向 session 目录 | executor.go 未过滤继承的 WORKSPACE_DIR，Linux 重复 key 取第一个 | 所有记忆文件读取失败 |

---

## 修复方案

### Fix 1：用 `python3` 替代 `sqlite3` CLI（修复 P1 / P2）

**改动文件**：所有 companion workspace 的 `proactive/SKILL.md`

将 Step 1 中的 sqlite3 调用替换为 python3：

```bash
# 从 SQLite 查询用户最近一条消息时间（使用 python3 内置模块，避免依赖 sqlite3 CLI）
LAST_ACTIVE=""
if [[ -n "$DB_PATH" && -n "$CHANNEL_KEY" ]]; then
    LAST_ACTIVE=$(python3 -c "
import sqlite3, sys
try:
    conn = sqlite3.connect('$DB_PATH')
    row = conn.execute('''
        SELECT MAX(m.created_at) FROM messages m
        JOIN sessions s ON m.session_id = s.id
        WHERE s.channel_key = '$CHANNEL_KEY' AND m.role = 'user'
    ''').fetchone()
    conn.close()
    if row and row[0]:
        print(row[0])
except Exception:
    pass
" 2>/dev/null)
fi
```

### Fix 2：`.proactive_state` 增加 `last_sent_ts` 冷却字段（修复 P2）

**改动文件**：所有 companion workspace 的 `proactive/SKILL.md`

Step 1 增加主动触达冷却检查（紧接时区检查之后）：
```bash
# 检查距上次主动触达时间（不依赖用户活跃，防止密集触达）
LAST_SENT_TS=$(grep 'last_sent_ts:' "$PROACTIVE_STATE" 2>/dev/null | grep -oP '\d+' | tail -1)
if [[ -n "$LAST_SENT_TS" ]]; then
    NOW_TS=$(date +%s)
    SINCE_LAST_SENT=$(( (NOW_TS - LAST_SENT_TS) / 3600 ))
    if [[ $SINCE_LAST_SENT -lt 2 ]]; then
        exit 0  # 上次主动触达不足2小时，静默退出
    fi
fi
```

发送成功后（Step 5 之后），更新 `.proactive_state`：
```bash
echo "skip_count: 0" > "$PROACTIVE_STATE"
echo "last_sent_ts: $(date +%s)" >> "$PROACTIVE_STATE"
```

### Fix 3：proactive 执行前刷新对话历史（修复 P3）

**改动文件**：所有 companion workspace 的 `proactive/SKILL.md`

在 Step 1 读取完变量后，主动用 python3 刷新 `RECENT_HISTORY.md`，逻辑与 `inject_history.py` 相同：

```bash
# 刷新 RECENT_HISTORY.md（proactive 任务不经过 UserPromptSubmit hook，需手动触发）
if [[ -n "$DB_PATH" && -n "$CHANNEL_KEY" ]]; then
    python3 -c "
import sqlite3
from pathlib import Path
WORKSPACE = '$WORKSPACE_DIR'
DB = '$DB_PATH'
CK = '$CHANNEL_KEY'
N = 20
try:
    conn = sqlite3.connect(DB)
    rows = conn.execute('''
        SELECT m.role, m.content, m.created_at
        FROM messages m JOIN sessions s ON m.session_id = s.id
        WHERE s.channel_key = ? AND m.content != ''
        ORDER BY m.created_at DESC LIMIT ?
    ''', (CK, N)).fetchall()
    conn.close()
    if not rows:
        exit(0)
    from datetime import datetime, timezone
    def fmt_ts(s):
        s = str(s or '').strip()
        for cand in (s, s.replace('T', ' ')):
            try:
                dt = datetime.fromisoformat(cand)
                return dt.astimezone().strftime('%Y-%m-%d %H:%M')
            except:
                pass
        return s[:16].replace('T', ' ') if len(s) >= 16 else s
    lines = ['# 最近对话记录（跨 session）\n', f'> 自动注入，最近 {len(rows)} 条\n\n']
    for role, content, ts in reversed(rows):
        tag = '**用户**' if role == 'user' else '**角色**'
        body = content[:500] + ('…' if len(content) > 500 else '')
        lines.append(f'{tag}（{fmt_ts(ts)}）：{body}\n\n')
    Path(WORKSPACE + '/memory/RECENT_HISTORY.md').write_text(''.join(lines))
except Exception as e:
    pass
" 2>/dev/null
fi
```

### Fix 4：inject_history.py UTC 转本地时间（修复 P4）

**改动文件**：所有 companion workspace 的 `.claude/hooks/inject_history.py`

在 `parse_timestamp` 函数中，将 UTC datetime 转换为本地时间后再格式化：

```python
# 修改前
return dt.strftime("%Y-%m-%d %H:%M")

# 修改后
if dt.tzinfo is not None:
    dt = dt.astimezone()  # 转为本地时区（CST +08:00）
return dt.strftime("%Y-%m-%d %H:%M")
```

同时修复 `send_and_record.py`，用本地时间写入（避免今后混入 UTC 时间戳）：

```python
# 修改前
now_iso = time.strftime("%Y-%m-%dT%H:%M:%S+00:00", time.gmtime())

# 修改后
from datetime import datetime, timezone, timedelta
tz_local = datetime.now().astimezone().tzinfo
now_iso = datetime.now(tz_local).isoformat(timespec='seconds')
```

### Fix 5：executor.go 过滤继承的 WORKSPACE_DIR（修复 P5）

**改动文件**：`internal/claude/executor.go`

在设置 WORKSPACE_DIR 前，先从继承环境中过滤掉已有的同名变量：

```go
// 过滤继承的 WORKSPACE_DIR，防止父进程（如 Claude Code）的值覆盖正确值
// Linux 下 getenv() 返回 environ 数组中第一个匹配项，若不过滤，继承值会胜出
baseEnv = filterEnv(baseEnv, "WORKSPACE_DIR")

cmd.Env = append(baseEnv,
    "TERM=xterm-256color",
    "FORCE_COLOR=0",
    "WORKSPACE_DIR="+req.WorkspaceDir,
)
```

**额外防御**（SKILL.md 层面）：用 SESSION_CONTEXT.md 的 `Workspace:` 字段作为 WORKSPACE_DIR 的可信来源：

```bash
# 先从环境变量取，若不存在再从自身所在的 SESSION_CONTEXT.md 推导
if [[ -z "$WORKSPACE_DIR" ]] || [[ "$WORKSPACE_DIR" == *"/sessions/"* ]]; then
    # SESSION_CONTEXT.md 在 cwd（session dir）下
    _WS=$(grep "^- Workspace:" "./SESSION_CONTEXT.md" 2>/dev/null | sed 's/^- Workspace: //')
    [[ -n "$_WS" ]] && WORKSPACE_DIR="$_WS"
fi
WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)}"
```

---

## 优先级与实施顺序

| 优先级 | Fix | 紧急程度 | 说明 |
|--------|-----|---------|------|
| P0 | Fix 5（executor.go） | 🔴 最高 | 影响所有 companion workspace 的所有操作，包括对话、记忆、任务 |
| P0 | Fix 1（sqlite3 → python3） | 🔴 最高 | 主动触达间隔门完全失效 |
| P1 | Fix 4（inject_history.py 时区） | 🟠 高 | 时间流逝感知系统性错误，影响对话连续性 |
| P1 | Fix 3（proactive 刷新历史） | 🟠 高 | 防止重复内容触达，保障对话质量 |
| P2 | Fix 2（last_sent_ts 冷却） | 🟡 中 | Fix 1 修复后 P2 主要问题消失，此 fix 作为额外安全层 |
