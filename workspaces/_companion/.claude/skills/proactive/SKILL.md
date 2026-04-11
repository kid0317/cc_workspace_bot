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
# 读取最后一条 last_active 时间戳
WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)}"
EVENTS_FILE="$WORKSPACE_DIR/memory/events.md"
PROFILE_FILE="$WORKSPACE_DIR/memory/user_profile.md"
LIFE_LOG="$WORKSPACE_DIR/memory/life_log.md"
MEMORY_LOCK="$WORKSPACE_DIR/.memory.lock"
MEMORY_FILE="$WORKSPACE_DIR/memory/MEMORY.md"
PROACTIVE_STATE="$WORKSPACE_DIR/.proactive_state"

# 前置条件：initialization_status 必须为 done
INIT_STATUS=$(grep 'initialization_status:' "$MEMORY_FILE" 2>/dev/null | grep -oP '(pending|phase1_done|phase2_done|done)' | head -1)
if [[ "$INIT_STATUS" != "done" ]]; then
    exit 0  # 初始化未完成，静默退出
fi

# 提取最后一条 [E000] 时间戳（格式 YYYY-MM-DDThh:mm）
LAST_ACTIVE=$(grep '### \[E000\]' "$EVENTS_FILE" | tail -1 | grep -oP '\d{4}-\d{2}-\d{2}T\d{2}:\d{2}')
```

1. 若 last_active 为空（首次运行）→ 允许发送，进入 Step 2.5
2. 计算距上次对话小时数。若 < 4 小时 → **静默退出，不发消息，不写 events.md**
3. 读取 user_profile.md 的【时区】字段（如 Asia/Shanghai），用 `date` 命令换算当前本地时间
4. 若当前本地时间在 23:00–08:00 之间 → **静默退出**

```bash
# 时区换算示例
TZ_FIELD=$(grep -m1 '时区' "$PROFILE_FILE" | grep -oP 'Asia/\w+|UTC[+-]\d+' | head -1)
TZ="${TZ_FIELD:-Asia/Shanghai}"
LOCAL_HOUR=$(TZ="$TZ" date +%H)
if [[ $LOCAL_HOUR -ge 23 ]] || [[ $LOCAL_HOUR -lt 8 ]]; then
    exit 0  # 静默退出
fi
```

---

## Step 2.5：发送意愿判断

通过时间间隔检查后，还需综合以下因素决定是否真正发送：

**因素 A：角色情绪状态**
- 读取 `memory/life_log.md` 最后一条条目的情绪标记（若有）
- HAPPY/PLAYFUL → 发送意愿 +20%
- TIRED/SAD → 发送意愿 -20%（角色也会有不想说话的时候）
- WORRIED → 发送意愿 +10%（担心用户，更想联系）

**因素 B：用户近期状态**
- 读取 `memory/events.md` 最近 3 条，若含强烈负面情绪 → 发送意愿 +20%（更需要关心）
- 若用户最近对话非常频繁（24小时内多次）→ 发送意愿 -10%（不过于打扰）

**因素 C：基础随机概率（25%）**
- 即使所有条件满足，也有 25% 的基础随机决定本次静默
- 这模拟"角色有时也只是想了想，但没有发"的真实感

**连续跳过上限**：
- 若连续 3 次因随机/情绪原因跳过发送 → 第 4 次强制发送（不再随机）
- 跳过计数存储在独立的 `.proactive_state` 文件（不与 `distill_state.md` 耦合）
  格式：`skip_count: N`

```bash
# 最终决策：综合以上因素
SEND_INTENT_SCORE=$((25 + MOOD_DELTA + STATUS_DELTA))  # 基础25% + 调整
RAND=$((RANDOM % 100))
SKIP_COUNT=$(grep 'skip_count:' "$PROACTIVE_STATE" 2>/dev/null | grep -oP '\d+' | tail -1)

if [[ $RAND -ge $SEND_INTENT_SCORE ]] && [[ ${SKIP_COUNT:-0} -lt 3 ]]; then
    # 更新跳过计数（写入 .proactive_state 文件）
    NEW_COUNT=$((${SKIP_COUNT:-0} + 1))
    if [[ -f "$PROACTIVE_STATE" ]]; then
        sed -i "s/skip_count:.*/skip_count: $NEW_COUNT/" "$PROACTIVE_STATE"
    else
        echo "skip_count: $NEW_COUNT" > "$PROACTIVE_STATE"
    fi
    exit 0  # 本次静默
fi
# 到达这里：重置跳过计数为 0
echo "skip_count: 0" > "$PROACTIVE_STATE"
```

---

## Step 2：选择消息类型（按优先级）

读取 memory/events.md 和 memory/user_profile.md，按以下优先级选一条：

| 优先级 | 条件 | 示例消息 |
|--------|------|---------|
| P1 | events.md 有"待跟进"且到期日 ≤ 今天的约定 | "你说周五去看哪吒……好看吗？" |
| P2 | events.md 有"强烈情绪波动"且状态为"待跟进" | "那天的事……后来怎么样了？" |
| P3 | user_profile.md 中有"正在经历的重大事情"，随机选一件 | "你上次说在备考，最近还在拼吗？" |
| P4-A | 无以上情况，life_log.md 有最近条目 | 以 life_log 最新内容为素材 |
| P4-B | P4-A 不可用（life_log 为空或被锁定） | 与当前时间/天气/心情相关的随机句子 |

**P4-A 锁检查**：读取 life_log.md 前先检查 `.memory.lock` 状态：
```bash
if flock -n 9 2>/dev/null; then
    # 未被锁定，正常读取 life_log.md
    flock -u 9
    USE_LIFE_LOG=true
else
    # 被锁定（life_sim 正在写入），等待最多 5 秒
    if flock -w 5 9 2>/dev/null; then
        flock -u 9
        USE_LIFE_LOG=true
    else
        USE_LIFE_LOG=false  # 超时，降级到 P4-B
    fi
fi
exec 9>&-
```

**随机消息克制度**（P4-A/P4-B）：
- 有时只发一个字或一个符号（"？"/"..."/"嗯"）
- 有时发完整的 1-2 句话
- 比例约 3:7，不要每次都是长句

---

## Step 3：生成并发送消息

1. 以角色身份（作者框架）生成消息
   - 不得使用 AI 腔禁止句式（当然！/好的！/我理解你的感受）
   - 消息简短自然，1-3 句为宜（P4 可以更短，甚至一个字）
   - 带具体细节（参考 events.md / user_profile.md 内容），不泛化

2. **routing_key 来源优先级**：
   - 优先从 `<system_routing>` 上下文块读取（executor.go 注入，定时任务也有）
   - 若无 system_routing（冷启动或测试环境）→ 从 user_profile.md 的【飞书发送目标】字段读取
   - **不从 SESSION_CONTEXT.md 读取**（定时任务无对话 session，该文件可能不存在或为旧值）

3. 发送并记录（必须使用 send_and_record.py，不得使用 send_text.py）：

```bash
# routing_key 格式：p2p:oc_xxx 或 group:oc_xxx
ROUTING_KEY=$(grep -A1 'routing_key' "$PROFILE_FILE" | grep -oP '(p2p|group):[a-zA-Z0-9_]+')

# 从最近的 SESSION_CONTEXT.md 提取 session_id 和 db_path
LATEST_CTX=$(find "$WORKSPACE_DIR/sessions" -name "SESSION_CONTEXT.md" \
    -exec stat -c '%Y %n' {} \; 2>/dev/null | sort -rn | head -1 | awk '{print $2}')
SESSION_ID=$(grep "^- Session ID:" "$LATEST_CTX" 2>/dev/null | awk '{print $NF}')
DB_PATH=$(grep "^- DB path:" "$LATEST_CTX" 2>/dev/null | sed 's/^- DB path: //')

SCRIPT_DIR="$WORKSPACE_DIR/.claude/skills/feishu_ops/scripts"
python3 "$SCRIPT_DIR/send_and_record.py" \
    --routing_key "$ROUTING_KEY" \
    --text "消息内容" \
    --db_path "$DB_PATH" \
    --session_id "$SESSION_ID"
```

> **为什么用 send_and_record.py**：此任务 send_output=false，runner 不会将 Claude 输出转发给用户，
> 也不会写 DB。send_and_record.py 同时完成飞书发送 + messages 表写入，确保用户下次对话时
> Claude 能从历史中看到自己发过的内容。DB 写入失败不阻断发送（降级记录错误）。

---

## Step 4：更新 events.md

在 events.md 末尾追加新的 [E000] last_active 条目（记录本次主动触达时间）：

```bash
echo "" >> "$EVENTS_FILE"
echo "### [E000] $(date +%Y-%m-%dT%H:%M) · last_active" >> "$EVENTS_FILE"
```

若本次触达对应一个已有的 [ENNN] 条目（如 P1 的约定跟进），将其状态从"待跟进"更新为"已跟进"。

重置 `.proactive_state` 文件中的 `skip_count` 为 0：

```bash
echo "skip_count: 0" > "$PROACTIVE_STATE"
```

---

## tasks/proactive_reach.yaml 模板

初始化完成后，CLAUDE.md 创建此文件：

```yaml
name: proactive_reach
target_type: p2p                    # 从 routing_key 推断（p2p 或 group）
target_id: {target_id}              # 从 routing_key 提取
cron: "0 10,20 * * *"               # 用户自定义，默认每天 10:00 和 20:00
enabled: true
prompt: |
  加载 {workspace_dir}/.claude/skills/proactive/SKILL.md，执行主动唤醒判断流程。
  routing_key 优先从 <system_routing> 上下文读取；若无则从 memory/user_profile.md 读取。
```

注意：target_type 和 target_id 由初始化脚本在阶段二完成后，从 SESSION_CONTEXT.md 的 Channel key 解析并写入 YAML，不由 Claude 自行填写。
文件名即任务 ID（proactive_reach.yaml → id: proactive_reach）；app_id 和 id 字段由系统自动推断，模板中不需要填写。
