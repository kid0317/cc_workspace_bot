---
name: life_sim
description: |
  Companion Workspace 生活日志生成 SOP。
  由 life_sim.yaml 定时任务触发（每4小时一次）。
  模拟角色的日常生活片段，写入 memory/life_log.md。
allowed-tools: Bash, Read, Write, Edit
---

# 生活日志生成执行流程

> **CRITICAL：禁止输出任何文字。**
> 本 Skill 由定时任务触发（send_output=false 模式），Claude 的任何回复文字都会被系统直接丢弃。
> 作者创作角色生活片段：基于角色性格、当前时间和心情状态，为角色虚构一段生活日志，丰富角色的真实感和内在世界。
> 全流程完成后直接退出，写入 memory/life_log.md，不产生用户可见输出。

## 设计原则

- **真实性优先**：专有名词（电影/游戏/书籍）必须验证真实存在，不杜撰
- **时间合理性**：生成内容必须符合当前时间段（凌晨不看电影）
- **情绪连续性**：新日志的情绪基调参考上一条 life_log + 最近对话
- **克制生成**：先掷骰子决定是否生成，不每次都写

---

## Step 0：初始化

```bash
WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)}"
LIFE_LOG="$WORKSPACE_DIR/memory/life_log.md"
LIFE_LOG_INDEX="$WORKSPACE_DIR/memory/life_log_index.md"
MEMORY_LOCK="$WORKSPACE_DIR/.memory.lock"
PERSONA_FILE="$WORKSPACE_DIR/memory/persona.md"
RECENT_HISTORY="$WORKSPACE_DIR/memory/RECENT_HISTORY.md"
MEMORY_FILE="$WORKSPACE_DIR/memory/MEMORY.md"
PARAMS_FILE="$WORKSPACE_DIR/character_params.yaml"

# 读取性格驱动参数（带降级默认值）
GEN_THRESHOLD_DAY=$(awk '/^life_sim:/{f=1} f && /^  gen_threshold_day:/{print $2; exit}' \
    "$PARAMS_FILE" 2>/dev/null)
GEN_THRESHOLD_DAY=${GEN_THRESHOLD_DAY:-60}

GEN_THRESHOLD_NIGHT=$(awk '/^life_sim:/{f=1} f && /^  gen_threshold_night:/{print $2; exit}' \
    "$PARAMS_FILE" 2>/dev/null)
GEN_THRESHOLD_NIGHT=${GEN_THRESHOLD_NIGHT:-20}

LOG_MAX_LENGTH=$(awk '/^life_sim:/{f=1} f && /^  log_max_length:/{print $2; exit}' \
    "$PARAMS_FILE" 2>/dev/null)
LOG_MAX_LENGTH=${LOG_MAX_LENGTH:-300}
```

**前置条件检查（initialization_status）**：

```bash
INIT_STATUS=$(grep 'initialization_status:' "$MEMORY_FILE" 2>/dev/null | grep -oP '(pending|phase1_done|phase2_done|done)' | head -1)
if [[ "$INIT_STATUS" != "done" ]]; then
    exit 0  # 初始化未完成，静默退出
fi
```

---

## Step 1：时间段检查

读取 persona.md 或 user_profile.md 中的时区字段（默认 Asia/Shanghai）。

```bash
PROFILE_FILE="$WORKSPACE_DIR/memory/user_profile.md"
TZ_FIELD=$(grep -m1 '时区' "$PROFILE_FILE" 2>/dev/null | grep -oP 'Asia/\w+|UTC[+-]\d+' | head -1)
TZ="${TZ_FIELD:-Asia/Shanghai}"
LOCAL_HOUR=$(TZ="$TZ" date +%H)
```

**凌晨约束**（LOCAL_HOUR 在 00-05 之间）：
- 不生成涉及外出、电影、购物等活动
- 仅允许生成：失眠/做梦/半夜想事情 等夜间活动

---

## Step 2：生成概率掷骰

```bash
RAND=$((RANDOM % 100))
# 阈值从 character_params.yaml 读取（verbosity 维度驱动，话少型角色生成频率更低）
if [[ $LOCAL_HOUR -ge 0 ]] && [[ $LOCAL_HOUR -lt 6 ]]; then
    THRESHOLD=$GEN_THRESHOLD_NIGHT   # 夜间：由 verbosity 决定，默认 20%
else
    THRESHOLD=$GEN_THRESHOLD_DAY     # 其他时段：由 verbosity 决定，默认 60%
fi

if [[ $RAND -ge $THRESHOLD ]]; then
    exit 0  # 本次不生成，静默退出
fi
```

---

## Step 2.5：素材来源决策（G1-G6 检查）

在骰子通过后、内容生成前，检查是否有 life_scene 类素材可用作生成灵感。

```bash
MATERIAL_POOL="$WORKSPACE_DIR/memory/material_pool.md"
MATERIAL_LOCK="$WORKSPACE_DIR/.material.lock"
SELECTED_MATERIAL=""
SELECTED_MAT_ID=""
USE_MATERIAL_SEED=false

if [[ -f "$MATERIAL_POOL" ]]; then
    LIFE_SCENE_COUNT=$(python3 -c "
import re
try:
    c = open('$MATERIAL_POOL').read()
    entries = re.split(r'(?=## \[MAT)', c)
    print(sum(1 for e in entries if 'available' in e and 'life_scene' in e))
except: print(0)
" 2>/dev/null)
    LIFE_SCENE_COUNT=${LIFE_SCENE_COUNT:-0}
fi
```

**G1-G6 检查清单**（LLM 执行）：

| # | 检查项 | 否 → 降级 |
|---|--------|---------|
| G1 | material_pool.md 存在 available + life_scene 类素材 | 跳过素材，纯虚构生成 |
| G2 | 素材情绪向量（valence）与当前时间段适配（凌晨不使用 valence > 0.4 的高兴素材） | 换一条或降级 |
| G3 | 素材内容与 life_log.md 最近 3 条记录无重复（主题/专有名词） | 换一条或降级 |
| G4 | 随机决策通过（有素材时使用概率 60%） | 降级为纯虚构 |
| G5 | 素材文字量合理（不超过 LOG_MAX_LENGTH 的 50%） | 换一条或降级 |
| G6 | 全部通过 → USE_MATERIAL_SEED=true，选定素材 ID 存入 SELECTED_MAT_ID | 降级 |

若 G1-G6 全部通过：从 material_pool.md 随机选取 1 条 life_scene available 素材，
以该素材的情感/场景描述作为本条日志的生成灵感（不直接复制，角色化创作）。

---

## Step 3：读取情绪上下文 + 情绪状态（mood_state.md）

1. 读取 `life_log.md` 最后一条 [LNNN] 条目，提取情绪状态
2. 读取 `RECENT_HISTORY.md`（若存在），了解用户近期互动情绪
3. 综合推断角色当前情绪基调（不必强行延续，可合理变化）

**从 mood_state.md 读取当前情绪基线 + 懒惰衰减（B5修复）**：

```bash
MOOD_STATE="$WORKSPACE_DIR/memory/mood_state.md"
MOOD_AUX="$WORKSPACE_DIR/.mood_state_aux"

CURRENT_ENERGY=$(grep -m1 '^energy:' "$MOOD_STATE" 2>/dev/null | awk '{print $2}')
CURRENT_ENERGY=${CURRENT_ENERGY:-0.5}
CURRENT_VALENCE=$(grep -m1 '^valence:' "$MOOD_STATE" 2>/dev/null | awk '{print $2}')
CURRENT_VALENCE=${CURRENT_VALENCE:-0.0}

# 懒惰衰减：k = 0.05 + (5 - stability) × 0.0375（线性插值，覆盖1-5全档）
LAST_DECAY_TS=$(grep '^last_decay_written_at:' "$MOOD_AUX" 2>/dev/null | awk '{print $2}')
DECAYED_VALENCE=$(python3 -c "
from datetime import datetime, timezone
try:
    stability = int('$STABILITY')
    k = 0.05 + (5 - stability) * 0.0375
    valence = float('$CURRENT_VALENCE')
    last_ts = '$LAST_DECAY_TS'
    if last_ts:
        last = datetime.fromisoformat(last_ts)
        now = datetime.now().astimezone()
        if last.tzinfo is None:
            last = last.replace(tzinfo=timezone.utc)
        hours = (now - last).total_seconds() / 3600
        decayed = valence * (1 - k) ** hours
    else:
        decayed = valence
    print(round(max(-1.0, min(1.0, decayed)), 3))
except: print('$CURRENT_VALENCE')
" 2>/dev/null)
DECAYED_VALENCE=${DECAYED_VALENCE:-$CURRENT_VALENCE}
```

生成内容时以 `DECAYED_VALENCE` 和 `CURRENT_ENERGY` 作为角色情绪基线参考。

---

## Step 4：生成候选日志内容

基于以下维度生成 1 条日志草稿：
- 当前时间段（早晨/下午/傍晚/夜间）
- 角色性格（参考 persona.md）
- 情绪基调（Step 3 推断）
- **字数约束**：日志正文不超过 `$LOG_MAX_LENGTH` 字（话少型角色不写长篇，默认 300 字上限）
- 活动类型选择（按时间段合理选择）：
  - 早晨：起床、早餐、看手机、出门准备
  - 下午：工作/学习、闲逛、咖啡、朋友
  - 傍晚：买菜、做饭、散步、健身
  - 夜间：追剧、游戏、读书、发呆、睡前

---

## Step 5：专有名词核查（真实性 Checklist）

若日志中涉及以下类型的具体名称，**必须**通过 agent-reach 验证其真实存在：

| 类型 | 需要验证 | 降级方案 |
|------|---------|---------|
| 电影/剧集 | 验证片名真实存在 | 改为"一部不记得名字的电影" |
| 游戏 | 验证游戏名真实存在 | 改为"手机游戏" |
| 书籍 | 验证书名+作者真实 | 改为"一本小说" |
| 音乐/歌手 | 验证真实存在 | 改为"一首老歌" |

**agent-reach 超时处理**：
- 超时上限：30 秒，用 `timeout` 命令包裹
- 超时后直接采用降级方案，不中断生成流程

```bash
# 调用 agent-reach 验证专有名词，超时 30 秒自动降级
VERIFY_RESULT=$(timeout 30 python3 -c "
import subprocess, sys
result = subprocess.run(
    ['agent-reach', '--query', sys.argv[1]],
    capture_output=True, text=True, timeout=25
)
print(result.stdout)
" "$NOUN_TO_VERIFY" 2>/dev/null) || VERIFY_RESULT=""
# 若 VERIFY_RESULT 为空或 timeout 退出码为 124，采用降级方案
if [[ -z "$VERIFY_RESULT" ]]; then
    USE_FALLBACK=true
fi
```

**真实性 Checklist（A-F 六项，生成前逐项核查）**：
- [ ] A. 活动与当前时间段是否符合（凌晨不购物）
- [ ] B. 活动与角色性格是否自洽（persona.md 中的设定）
- [ ] C. 所有专有名词是否经过验证或已降级处理
- [ ] D. 情绪基调是否与上一条 life_log 合理衔接
- [ ] E. 内容是否与 life_log_index.md 中已有记录重复（避免角色反复看同一部电影）
- [ ] F. 日志文字是否自然口语化（不像 AI 生成的活动日报）

---

## Step 6：去重检查（life_log_index.md）

读取 `life_log_index.md`，对照候选内容中的专有名词：
- 若该名词已出现过 → 换一个不同的专有名词，或改为无专有名词的描述
- 若候选内容无专有名词 → 跳过此步

---

## Step 7：写入 life_log.md（加锁）

```bash
exec 9>"$MEMORY_LOCK"
flock -x 9

# 计算新条目编号
LAST_N=$(grep -oP '(?<=\[L)\d+(?=\])' "$LIFE_LOG" 2>/dev/null | sort -n | tail -1)
NEXT_N=$(printf "%03d" $((${LAST_N:-0} + 1)))

# 写入条目
cat >> "$LIFE_LOG" << EOF

### [L${NEXT_N}] $(date +%Y-%m-%dT%H:%M) · ${ACTIVITY_TYPE}
${LOG_CONTENT}
EOF


# --- Step 7.5：情绪状态写入（写入点 B）---
# 在生活日志写入后，根据日志内容推断情绪变化，更新 mood_state.md。
# 注意：此处不更新 last_decay_written_at（写入点 B 设计约束：decay timer 继续运行）。
# 注意：此处在同一个 .memory.lock 锁内执行，无需额外加锁 mood_state.md。

# LLM 推断 Δvalence/Δenergy（基于本次生成的 ACTIVITY_TYPE + LOG_CONTENT）：
# - 积极活动（开心/放松/聚会/美食）→ Δvalence +0.05 ~ +0.15, Δenergy 0 ~ +0.10
# - 消极活动（失眠/难过/担心）→ Δvalence -0.05 ~ -0.15, Δenergy -0.05 ~ -0.10
# - 高能量活动（健身/外出/聚会）→ Δenergy +0.05 ~ +0.10
# - 低能量活动（睡觉/发呆）→ Δenergy -0.05 ~ 0
# - 单次 Δ 约束：|Δvalence| ≤ 0.20，|Δenergy| ≤ 0.15
# 以 DECAYED_VALENCE 为基线计算新 valence；以 CURRENT_ENERGY 为基线计算新 energy。
# 计算后 clamp：valence ∈ [-1.0, 1.0]，energy ∈ [0.0, 1.0]。

NOW_TS=$(python3 -c "from datetime import datetime; print(datetime.now().astimezone().isoformat(timespec='minutes'))" 2>/dev/null)

# 将新快照前插到 mood_state.md，同时更新顶部 valence/energy 字段
# 快照格式：
# ### {NOW_TS} · source=life_sim
# valence: {NEW_VALENCE}
# energy: {NEW_ENERGY}
# note: {ACTIVITY_TYPE}
#
# 保留最近 10 条快照，超出时删除最旧的。

flock -u 9
exec 9>&-
```

---

## Step 8：更新 life_log_index.md

若本条日志包含新的专有名词（在 index 中未出现），追加到对应类别表格中：

```markdown
| 名称 | 首次日期 | 条目ID |
| 电影名 | YYYY-MM-DD | L{NNN} |
```

（不加锁：life_log_index.md 只由 life_sim 写，无竞争风险）

---

## Step 9：归档检查

```bash
COUNT=$(grep -c "### \[L" "$LIFE_LOG" 2>/dev/null || echo 0)
```

若 COUNT > 30，触发归档流程：

**9.1 确定归档文件路径**
```
ARCHIVE_FILE = {workspace_dir}/memory/life_log_archive_{YYYYMM}.md
```
若文件不存在，从 `memory/life_log_archive_template.md` 复制并替换头部占位符（YYYY-MM、归档周期、条目数量）。

**9.2 提取最旧的 10 条**
从 life_log.md 提取最早的 10 个 `[LNNN]` 完整条目块（从 `### [L` 到下一个 `### [L` 之前）。

**9.3 扫描待关注事项**
对被归档的 10 条扫描：
- 角色想做但未做的事（关键词：想去、打算、下次、改天）
- 强烈情绪词（关键词：崩溃、特别低落、特别开心、一直想着）

写入归档文件的【待关注事项】块。

**9.4 追加写入归档文件**
将 10 条追加到归档文件的【归档条目】区域（保持 `[LNNN]` 原编号不变）。

**9.5 从 life_log.md 删除这 10 条**
重写 life_log.md，保留剩余条目和顶部的【归档摘要索引】块。

**9.6 更新 life_log.md 顶部归档摘要索引**
在文件最顶部维护索引块（不存在则新建）：
```markdown
## 归档摘要索引

### 来自 life_log_archive_YYYYMM.md（N条，YYYY-MM-DD ~ YYYY-MM-DD）
**待关注**：{9.3 提取的摘要}
→ 归档文件：memory/life_log_archive_YYYYMM.md
```

---

## 错误处理原则

- Step 5 agent-reach 超时 → 降级处理，不阻塞后续步骤
- Step 7 加锁失败（flock 等待10秒超时）→ 本次跳过，不写入
- 任何步骤异常 → 静默退出，释放锁
