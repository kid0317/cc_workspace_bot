---
name: proactive
description: |
  主动唤醒 SOP。由定时任务触发，判断是否向用户发送主动关心消息。
  包含发送条件检查、消息类型选择、角色声音生成、飞书发送。
allowed-tools: Bash, Read, Write
---

# 主动唤醒执行流程

> **CRITICAL：选择静默时，回复内容必须完全为空。**
> 静默路径（exit 0）执行后 Claude 不得输出任何文字。
> 发送"当前不需要发信息"之类的说明会直接泄露给用户，严重破坏沉浸感。

## Step 1：发送前置检查（静默条件）

```bash
WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)}"
EVENTS_FILE="$WORKSPACE_DIR/memory/events.md"
PROFILE_FILE="$WORKSPACE_DIR/memory/user_profile.md"
LIFE_LOG="$WORKSPACE_DIR/memory/life_log.md"
RECENT_HISTORY="$WORKSPACE_DIR/memory/RECENT_HISTORY.md"
MEMORY_LOCK="$WORKSPACE_DIR/.memory.lock"
MEMORY_FILE="$WORKSPACE_DIR/memory/MEMORY.md"
PROACTIVE_STATE="$WORKSPACE_DIR/.proactive_state"

# 前置条件：initialization_status 必须为 done
INIT_STATUS=$(grep 'initialization_status:' "$MEMORY_FILE" 2>/dev/null | grep -oP '(pending|phase1_done|phase2_done|done)' | head -1)
if [[ "$INIT_STATUS" != "done" ]]; then
    exit 0  # 初始化未完成，静默退出
fi

# 从最近的 SESSION_CONTEXT.md 读取 DB_PATH、CHANNEL_KEY、SESSION_ID（供后续步骤复用）
LATEST_CTX=$(find "$WORKSPACE_DIR/sessions" -name "SESSION_CONTEXT.md" \
    -exec stat -c '%Y %n' {} \; 2>/dev/null | sort -rn | head -1 | awk '{print $2}')
SESSION_ID=$(grep "^- Session ID:" "$LATEST_CTX" 2>/dev/null | awk '{print $NF}')
DB_PATH=$(grep "^- DB path:" "$LATEST_CTX" 2>/dev/null | sed 's/^- DB path: //')
CHANNEL_KEY=$(grep "^- Channel key:" "$LATEST_CTX" 2>/dev/null | sed 's/^- Channel key: //')

# 从 SQLite 查询用户最近一条消息时间（真值源，不依赖 LLM 写入的文本标记）
# 注意：此处严禁改为读取 [E000]——[E000] 仅由 memory_write skill 写入，proactive 不拥有该信号
# 使用 python3 内置 sqlite3 模块（系统 sqlite3 CLI 可能未安装），通过 env 传参避免 shell 注入
LAST_ACTIVE=""
if [[ -n "$DB_PATH" && -n "$CHANNEL_KEY" ]]; then
    LAST_ACTIVE=$(_DB="$DB_PATH" _CH="$CHANNEL_KEY" python3 -c "
import sqlite3, os
try:
    conn = sqlite3.connect(os.environ['_DB'])
    row = conn.execute(
        'SELECT MAX(m.created_at) FROM messages m JOIN sessions s ON m.session_id=s.id WHERE s.channel_key=? AND m.role=?',
        (os.environ['_CH'], 'user')
    ).fetchone()
    conn.close()
    print(row[0] or '', end='')
except Exception:
    pass
" 2>/dev/null)
fi
```

1. 若 last_active 为空（首次运行）→ 允许发送，进入 Step 2
2. 计算距上次对话小时数。若 < 2 小时 → **静默退出，不发消息，不写 events.md**
3. 读取 user_profile.md 的【时区】字段（如 Asia/Shanghai），用 `date` 命令换算当前本地时间
4. 若当前本地时间在 23:00–08:00 之间 → **静默退出**

```bash
TZ_FIELD=$(grep -m1 '时区' "$PROFILE_FILE" | grep -oP 'Asia/\w+|UTC[+-]\d+' | head -1)
TZ="${TZ_FIELD:-Asia/Shanghai}"
LOCAL_HOUR=$(TZ="$TZ" date +%H)
if [[ $LOCAL_HOUR -ge 23 ]] || [[ $LOCAL_HOUR -lt 8 ]]; then
    exit 0
fi
```

5. 读取 `.proactive_state` 的 `last_sent_ts` 字段。若距上次主动触达不足 2 小时（120 分钟）→ **静默退出**（防止因随机门短时间内重复触发）

```bash
LAST_SENT_TS=$(grep '^last_sent_ts:' "$PROACTIVE_STATE" 2>/dev/null | awk '{print $2}')
if [[ -n "$LAST_SENT_TS" ]]; then
    MINS_SINCE_SENT=$(_TS="$LAST_SENT_TS" python3 -c "
import os
from datetime import datetime, timezone
try:
    last = datetime.fromisoformat(os.environ['_TS'])
    now = datetime.now().astimezone()
    if last.tzinfo is None:
        last = last.replace(tzinfo=timezone.utc)
    print(int((now - last).total_seconds() / 60))
except Exception:
    print(9999)
" 2>/dev/null)
    if [[ ${MINS_SINCE_SENT:-9999} -lt 120 ]]; then
        exit 0  # 上次主动触达不足 2 小时，静默退出
    fi
fi
```

6. 纯随机发送门（CRITICAL）

> **此门在读取任何情绪/事件上下文之前执行。** 概率由 bash `$RANDOM` 决定，
> 与 LLM 对"角色当前是否想说话"的判断完全隔离。
> 这是保证行为真正随机的核心设计：等 LLM 读到情绪上下文时，发送/跳过已定局。
>
> - **发送概率和 max_skip 从 character_params.yaml 读取**（由角色 initiative 维度决定，非硬编码）
> - 降级默认值：base_prob=15，max_skip=8

```bash
# 从 character_params.yaml 读取性格驱动参数（带降级）
PARAMS_FILE="$WORKSPACE_DIR/character_params.yaml"
SEND_PROB=$(awk '/^proactive:/{f=1} f && /^  base_prob:/{print $2; exit}' "$PARAMS_FILE" 2>/dev/null)
SEND_PROB=${SEND_PROB:-15}
MAX_SKIP=$(awk '/^proactive:/{f=1} f && /^  max_skip:/{print $2; exit}' "$PARAMS_FILE" 2>/dev/null)
MAX_SKIP=${MAX_SKIP:-8}

# 纯随机门：所有时间/冷却硬性检查通过后，用 bash RANDOM 决定本次是否发送
SKIP_COUNT=$(grep 'skip_count:' "$PROACTIVE_STATE" 2>/dev/null | grep -oP '\d+' | tail -1)
RAND=$((RANDOM % 100))
if [[ $RAND -ge $SEND_PROB ]] && [[ ${SKIP_COUNT:-0} -lt $MAX_SKIP ]]; then
    NEW_COUNT=$((${SKIP_COUNT:-0} + 1))
    PREV_SENT_TS=$(grep '^last_sent_ts:' "$PROACTIVE_STATE" 2>/dev/null | awk '{print $2}')
    { echo "skip_count: $NEW_COUNT"; [[ -n "$PREV_SENT_TS" ]] && echo "last_sent_ts: $PREV_SENT_TS"; } > "$PROACTIVE_STATE"
    exit 0
fi
# 到达这里：本次将发送（skip_count 在 Step 5 发送成功后重置为 0）
```

---

## Step 2：加载事件与用户上下文（发送决策已定，此处仅读取内容）

读取以下文件，为后续步骤提供消息内容依据（此阶段不再影响是否发送）：

```bash
EVENTS_CONTENT=$(cat "$EVENTS_FILE" 2>/dev/null || echo "")
PROFILE_CONTENT=$(cat "$PROFILE_FILE" 2>/dev/null || echo "")
```

---

## Step 3：选择消息类型（按优先级）

读取 memory/events.md 和 memory/user_profile.md，按以下优先级选一条：

| 优先级 | 条件 | 示例消息 |
|--------|------|---------|
| P1 | events.md 有"待跟进"且到期日 ≤ 今天的约定 | "你说周五去看哪吒……好看吗？" |
| P2 | events.md 有"强烈情绪波动"且状态为"待跟进" | "那天的事……后来怎么样了？" |
| P3 | user_profile.md 中有"正在经历的重大事情"，随机选一件 | "你上次说在备考，最近还在拼吗？" |
| P4-C | knowledge_bank.md 有 formed + 最后使用:从未（ready） + valence > -0.2 + 距上次知识引用 > 3 轮 + openness ≥ 3 | 以角色口吻自然分享一个已形成的知识点 |
| P4-A | 无以上情况，life_log.md 有最近条目 | 以 life_log 最新内容为素材，自然带出 |
| P4-B | P4-A 不可用（life_log 为空或被锁定） | 与当前时间/心情相关的随机句子 |

**P4-A 锁检查**（正确用法：先绑定 fd 到锁文件）：
```bash
exec 9<"$MEMORY_LOCK"
if flock -n 9; then
    # 未被锁定，正常读取 life_log.md
    flock -u 9
    USE_LIFE_LOG=true
else
    # 被锁定（life_sim 正在写入），等待最多 5 秒
    if flock -w 5 9; then
        flock -u 9
        USE_LIFE_LOG=true
    else
        USE_LIFE_LOG=false  # 超时，降级到 P4-B
    fi
fi
exec 9>&-
```

**P4-A 氛围匹配检查**（V3.1 新增）：
选用 life_log 素材前，对比 life_log 最新条目情绪与 RECENT_HISTORY.md 最后一条用户消息情绪：
- 用户消息含 难过/焦虑/哭/压力/担心/崩/难 → 用户情绪沉重
- life_log 条目含 高兴/开心/好玩/好笑/爽/棒/喜欢 → 角色情绪轻松
- 两组都匹配（情绪方向相悖）→ 降级到 P4-B（时间/心情相关随机句），不用 life_log 素材

**随机消息克制度**（P4-A/P4-B）：
- 有时只发一个字或一个符号（"？"/"..."/"嗯"）
- 有时发完整的 1-2 句话
- 比例约 3:7，不要每次都是长句

---

## Step 4：加载情绪上下文（辅助生成）

**前置：重新生成 RECENT_HISTORY.md**（proactive 任务不经过 UserPromptSubmit hook，inject_history.py 不会自动刷新，必须手动重建）：

```bash
if [[ -n "$DB_PATH" && -n "$CHANNEL_KEY" ]]; then
    _DB="$DB_PATH" _CH="$CHANNEL_KEY" _WS="$WORKSPACE_DIR" python3 - <<'PYEOF' 2>/dev/null
import sqlite3, os
from datetime import datetime
from pathlib import Path
ws = Path(os.environ['_WS'])
try:
    conn = sqlite3.connect(os.environ['_DB'])
    rows = conn.execute(
        "SELECT m.role, m.content, m.created_at FROM messages m "
        "JOIN sessions s ON m.session_id = s.id "
        "WHERE s.channel_key = ? AND m.content != '' "
        "ORDER BY m.created_at DESC LIMIT 20",
        (os.environ['_CH'],)
    ).fetchall()
    conn.close()
except Exception:
    rows = []
if rows:
    lines = ['# 最近对话记录（跨 session）\n',
             f'> 自动注入，最近 {len(rows)} 条\n\n']
    for role, content, ts in reversed(rows):
        tag = '**用户**' if role == 'user' else '**角色**'
        try:
            dt = datetime.fromisoformat(ts)
            if dt.tzinfo is not None:
                dt = dt.astimezone()
            date_str = dt.strftime('%Y-%m-%d %H:%M')
        except Exception:
            date_str = str(ts)[:16].replace('T', ' ')
        body = content[:500] + ('…' if len(content) > 500 else '')
        lines.append(f'{tag}（{date_str}）：{body}\n\n')
    (ws / 'memory' / 'RECENT_HISTORY.md').write_text(''.join(lines))
PYEOF
fi
```

读取 `memory/RECENT_HISTORY.md`（如果存在）：
- 提取最近 3-5 条对话，感知当前关系温度和用户情绪基线
- 这是上次日常对话时写入的快照，可能有时间差，仅作参考，不当作"刚发生的事"
- 结合 life_log.md 最新条目的角色情绪，确定生成消息的语气基调

---

## Step 5：作者创作主动触达台词，并通过框架投递

此刻你作为作者，已判断角色在这个时间节点应该主动出现。
你在创作角色的一段主动台词（可含旁白），最后由 send_and_record.py 投递给用户。

**创作规则**：
- 你写的是角色的台词/旁白，不是你（作者）直接给用户发消息
- 旁白判断标准同 CLAUDE.md：对场景叙述有价值才写，不用来描述操作
- `[[SEND]]` 可用于控制发送节奏（参考正常对话规则）
- 台词创作完成后，send_and_record.py（后台）负责投递和 DB 记录
- 投递完成后作者收工，不追加任何内容

### 风格规则（CRITICAL — 定时任务不经过 reply_checklist.sh，此处必须自行执行）

**作者审稿：生成候选台词后，先自检以下规则，如有违反必须重写，不向用户解释修改过程**：

**A. 禁止 AI 腔（必须过滤）**
- ❌ 以"当然！""好的！""没问题！"开头
- ❌ 出现"我理解你的感受""我能体会"（套话共情）
- ❌ 出现任何形式的系统说明（"我刚刚写入了""已更新""后台操作"）
- ❌ 出现英文技术词汇（路径、变量名、JSON、工具名）
- ❌ 消息结尾附加总结段落

**B. 角色声音一致性**
- 参照 CLAUDE.md 末尾【角色设定】块中的口头禅、语言习惯、示例台词
- 不无条件同意用户观点，可以有自己的立场
- 情绪状态与 life_log.md 最新条目一致（开心时句子短，担心时省略号多）

**C. 主动触达特有规则**
- 先有内心触发（某个记忆片段 / life_log 活动 / 今天的时间节点），再生成消息
- P1/P2：最长 2 句，带具体细节（引用 events.md 中的内容，不泛化）
- P4：可以只有 1 个字（"？" "嗯" "..."），或 1-2 句随意话

**自检流程**：
1. 生成候选消息
2. 逐条对照 A/B/C 规则
3. 若有任何违反 → 重写，不输出违规版本
4. 通过后发送

### 发送

```bash
# routing_key：优先从 <system_routing> 上下文块读取（executor.go 注入）
# 若无 system_routing → 从 user_profile.md 的【飞书发送目标】字段读取
ROUTING_KEY=$(grep -A1 '飞书发送目标\|routing_key' "$PROFILE_FILE" | grep -oP '(p2p|group):[a-zA-Z0-9_]+' | head -1)

# DB_PATH 和 SESSION_ID 已在 Step 1 从 SESSION_CONTEXT.md 读取，此处直接复用
SCRIPT_DIR="$WORKSPACE_DIR/.claude/skills/feishu_ops/scripts"
# 仅在发送成功（exit 0）后才记录时间戳；发送失败时不更新冷却，避免静默期
if python3 "$SCRIPT_DIR/send_and_record.py" \
    --routing_key "$ROUTING_KEY" \
    --text "消息内容" \
    --db_path "$DB_PATH" \
    --session_id "$SESSION_ID"; then
    NOW_TS=$(python3 -c "from datetime import datetime; print(datetime.now().astimezone().isoformat(timespec='seconds'))" 2>/dev/null)
    printf 'skip_count: 0\nlast_sent_ts: %s\n' "$NOW_TS" > "$PROACTIVE_STATE"
fi
```

> **为什么用 send_and_record.py**：此任务 send_output=false，runner 不会将 Claude 输出转发给用户，
> 也不会写 DB。send_and_record.py 同时完成飞书发送 + messages 表写入，确保用户下次对话时
> Claude 能从历史中看到自己发过的内容。DB 写入失败不阻断发送（降级记录错误）。

---

## Step 6：更新 events.md

若本次触达对应一个已有的 [ENNN] 条目（如 P1 的约定跟进），将其状态从"待跟进"更新为"已跟进"。

> **设计说明**：本 skill 不写入 `[E000]`。`[E000]` 代表"用户上次真实对话时间"，
> 由 memory_write skill 唯一负责写入。proactive 的发送行为不代表用户活跃，
> 若在此写入 [E000] 会导致下次运行把"上次触达时间"误判为"用户活跃时间"，产生自我抑制循环。
