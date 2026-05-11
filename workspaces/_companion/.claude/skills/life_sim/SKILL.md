---
name: life_sim
description: |
  Companion Workspace 生活日志生成 SOP (v5.2)。
  由 life_sim.yaml 定时任务触发（每 4 小时）。
  从 material_pool 选真实素材，以"触发→反应"模板转译为角色生活日志。
  内含：留白模式、用户倾诉强制呼应、降温规则、多形态衔接、失败降级链。
allowed-tools: Bash, Read, Write, Edit
---

# 生活日志生成执行流程 v5.2

> **CRITICAL：禁止输出任何文字。**
> 本 Skill 由定时任务触发（send_output=false 模式），Claude 的任何回复文字会被系统丢弃。
> 作者从真实世界素材池挑选，以角色声音转译为生活切片；全流程完成后直接退出。

## 设计原则（v5.2）

- **真实素材优先**：默认从 `material_pool.md` 挑选素材，仅在池子空/陈旧时降级虚构
- **用户倾诉优先于世界**：用户亲口告知的 user_told 挂念拥有强制呼应通道
- **"触发→反应"两段式**：原帖复述 ≤1 行，其余留给角色感官/动作/口癖
- **留白模式 25%**：保留 v3 "天花板很白" 式极简美学，对抗模板僵硬
- **降温规则精细化**：persona 口癖豁免情绪峰值硬规则，stability 高的角色大事件保留 70% 峰值
- **失败降级链**：pool 空 / fetch 失败均有明确 fallback 路径
- **即时编造感知**：对话中 SHARE 姿态产生的 `src: inline_fabrication` 条目与 life_sim 条目共存于同一 life_log，挑选素材时需避免主题重复

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
MATERIAL_POOL="$WORKSPACE_DIR/memory/material_pool.md"
MATERIAL_LOCK="$WORKSPACE_DIR/.material.lock"
UNRESOLVED="$WORKSPACE_DIR/memory/unresolved.md"
MOOD_STATE="$WORKSPACE_DIR/memory/mood_state.md"
MOOD_AUX="$WORKSPACE_DIR/.mood_state_aux"
FETCH_STATE="$WORKSPACE_DIR/.material_fetch_state.json"
EVENTS_JSONL="$WORKSPACE_DIR/.life_sim_events.$(date +%Y%m%d).jsonl"
FILTERS_FILE="$WORKSPACE_DIR/.claude/skills/material_fetch/filters.yaml"
KEYWORD_TEMPLATES="$WORKSPACE_DIR/memory/keyword_templates.yaml"

_p() { awk -v k="$1" '/^life_sim:/{f=1} f && $0 ~ "^  "k":"{gsub(/^[ \t]+/,"",$0); sub("^"k":[ \t]*",""); sub(/[ \t]*#.*$/,""); gsub(/[ \t]+$/,""); gsub(/^"/,""); gsub(/"$/,""); print; exit}' "$PARAMS_FILE" 2>/dev/null; }

GEN_THRESHOLD_DAY=$(_p gen_threshold_day);    GEN_THRESHOLD_DAY=${GEN_THRESHOLD_DAY:-60}
GEN_THRESHOLD_NIGHT=$(_p gen_threshold_night); GEN_THRESHOLD_NIGHT=${GEN_THRESHOLD_NIGHT:-20}
LOG_MAX_LENGTH=$(_p log_max_length);          LOG_MAX_LENGTH=${LOG_MAX_LENGTH:-300}
EMOTIONAL_RANGE=$(_p emotional_range);        EMOTIONAL_RANGE=${EMOTIONAL_RANGE:-3}
MATERIAL_USE_THRESHOLD=$(_p material_use_threshold); MATERIAL_USE_THRESHOLD=${MATERIAL_USE_THRESHOLD:-60}
ORIGINAL_QUOTE_MAX_CHARS=$(_p original_quote_max_chars); ORIGINAL_QUOTE_MAX_CHARS=${ORIGINAL_QUOTE_MAX_CHARS:-15}
WHITE_SPACE_PROB=$(_p white_space_prob);      WHITE_SPACE_PROB=${WHITE_SPACE_PROB:-25}
USER_ECHO_PRIORITY=$(_p user_echo_priority);  USER_ECHO_PRIORITY=${USER_ECHO_PRIORITY:-1.5}

INIT_STATUS=$(grep 'initialization_status:' "$MEMORY_FILE" 2>/dev/null | grep -oP '(pending|phase1_done|phase2_done|done)' | head -1)
[[ "$INIT_STATUS" != "done" ]] && exit 0
```

**事实流 append 辅助函数**（始终用 shell `>>`，单行 ≤4KB，保证多进程原子）：

```bash
_emit_event() {
    local payload="$1"   # 以 { 开头、} 结尾的 JSON 片段
    local ts; ts=$(python3 -c "from datetime import datetime; print(datetime.now().astimezone().isoformat(timespec='seconds'))" 2>/dev/null)
    local ws; ws=$(basename "$WORKSPACE_DIR")
    # 在最外层 { 后插 v/ts/ws 三个固定字段
    printf '{"v":1,"ts":"%s","ws":"%s",%s\n' "$ts" "$ws" "${payload:1}" >> "$EVENTS_JSONL"
}
```

---

## Step 1：时间段检查

```bash
PROFILE_FILE="$WORKSPACE_DIR/memory/user_profile.md"
TZ_FIELD=$(grep -m1 '时区' "$PROFILE_FILE" 2>/dev/null | grep -oP 'Asia/\w+|UTC[+-]\d+' | head -1)
TZ="${TZ_FIELD:-Asia/Shanghai}"
LOCAL_HOUR=$(TZ="$TZ" date +%H)
```

---

## Step 1.5：FORCE_ECHO 预检（v5.1 必在骰子之前）

**设计文档 §7.2 承诺"用户倾诉必有呼应"，所以 user_echo 必须 bypass 骰子。**

**LLM 判断任务**（在 Step 2 骰子之前执行）：
1. 读 `RECENT_HISTORY.md` 最近 24h 的用户消息
2. 读 `unresolved.md` 活跃块的 `src=user_told` 条目
3. 判断是否有强烈情绪/约定事件在 24h 内新出现，且对应 user_told 条目的 `forced_echo_last` 早于 72h 前（或 `(never)`）

```bash
FORCE_ECHO=false        # LLM 设置
TARGET_UNRESOLVED=""    # LLM 设置，如 "U003"
```

**若 FORCE_ECHO=true**：
- **跳过 Step 2 骰子**（下一步 if 分支）
- WHITESPACE_MODE=false（强制呼应不走留白）
- FALLBACK_REASON="user_echo"

---

## Step 2：掷骰（含留白模式分支；FORCE_ECHO 时 bypass）

```bash
if [[ "$FORCE_ECHO" == "true" ]]; then
    # 用户倾诉强制呼应：跳过骰子，直接进入后续步骤
    RAND=-1
    THRESHOLD=100
    WHITESPACE_MODE=false
    _emit_event "{\"event\":\"dice_bypass\",\"reason\":\"user_echo\",\"target\":\"${TARGET_UNRESOLVED}\"}"
else
    RAND=$((RANDOM % 100))
    if [[ $LOCAL_HOUR -ge 0 && $LOCAL_HOUR -lt 6 ]]; then
        THRESHOLD=$GEN_THRESHOLD_NIGHT
    else
        THRESHOLD=$GEN_THRESHOLD_DAY
    fi
    if [[ $RAND -ge $THRESHOLD ]]; then
        _emit_event "{\"event\":\"dice_skip\",\"dice\":$RAND,\"threshold\":$THRESHOLD}"
        exit 0
    fi

    # 留白模式骰子
    WS_RAND=$((RANDOM % 100))
    WHITESPACE_MODE=false
    # pool 陈旧时留白概率临时升高
    if [[ ${POOL_AVAILABLE_COUNT:-0} -lt 3 && ${HOURS_SINCE_FETCH:-0} -gt 72 ]]; then
        EFFECTIVE_WS_PROB=50
    else
        EFFECTIVE_WS_PROB=$WHITE_SPACE_PROB
    fi
    if [[ $WS_RAND -lt $EFFECTIVE_WS_PROB ]]; then
        WHITESPACE_MODE=true
    fi
fi
```

---

## Step 3：读情绪基线 + 懒惰衰减

读 persona.md 的 `stability` → 计算衰减系数 `k = 0.05 + (5 - stability) × 0.0375`
读 mood_state.md 的 `valence / energy` 并按 `last_decay_written_at` 衰减。
得 `DECAYED_VALENCE` 和 `CURRENT_ENERGY` 作为情绪基线。

（实现代码保持 v3 相同，此处省略。）

---

## Step 4：用户呼应生成约束（仅当 FORCE_ECHO=true）

若 Step 1.5 已设 `FORCE_ECHO=true`：
- 本次生成**必须**围绕 `TARGET_UNRESOLVED` 展开
- 跳过 Step 5 素材池挑选，直接进 Step 7（可能纯虚构）
- FALLBACK_REASON="user_echo"
- Step 8.2 会同步更新该条目的 `forced_echo_last`

**节制**：呼应内容禁止复述用户原话，必须以**角色生活切片形式**出现
（例如"今天下午给妈发了条微信，没回，估计在午睡"），避免变成"妈妈生病"每 4h 被提一次。

---

## Step 5：素材挑选（pool 优先，分三路径）

```bash
POOL_AVAILABLE_COUNT=$(python3 -c "
import re
try:
    c = open('$MATERIAL_POOL').read()
    print(sum(1 for e in re.split(r'(?=## \[MAT)', c) if 'available' in e))
except: print(0)
" 2>/dev/null)
POOL_AVAILABLE_COUNT=${POOL_AVAILABLE_COUNT:-0}

LAST_FETCH_TS=$(python3 -c "
import json
try: print(json.load(open('$FETCH_STATE')).get('last_success_ts',''))
except: print('')
" 2>/dev/null)

HOURS_SINCE_FETCH=$(python3 -c "
from datetime import datetime
ts = '$LAST_FETCH_TS'
if not ts: print(999); exit()
try:
    d = datetime.fromisoformat(ts)
    print(int((datetime.now().astimezone() - d).total_seconds() / 3600))
except: print(999)
" 2>/dev/null)
HOURS_SINCE_FETCH=${HOURS_SINCE_FETCH:-999}
```

**路径决策**（按优先级从上往下判定，第一个匹配即生效）：

| 条件 | 路径 | FALLBACK_REASON |
|---|---|---|
| FORCE_ECHO=true | 纯虚构呼应 user_told 挂念 | user_echo |
| WHITESPACE_MODE=true | 跳过素材，极简内观 | whitespace |
| POOL_AVAILABLE_COUNT ≥ 1 且 HOURS_SINCE_FETCH ≤ 3 | **刚抓豁免**：即使只有 1-2 条，只要新鲜就正常用 | null |
| POOL_AVAILABLE_COUNT ≥ 3 且 HOURS_SINCE_FETCH ≤ 24 | 正常挑选 | null |
| POOL_AVAILABLE_COUNT ≥ 1 且 POOL_AVAILABLE_COUNT < 3 | 池浅，标 underfilled，仍挑选 | pool_underfilled |
| POOL_AVAILABLE_COUNT == 0 | 池空降级 | pool_empty |
| HOURS_SINCE_FETCH > 24 | 池陈旧 | pool_stale |
| HOURS_SINCE_FETCH > 72 | 连续饥饿；WHITE_SPACE_PROB 临时 50% | pool_critical |

**正常挑选规则**（LLM 执行）：
1. 从 `status=available` 条目过滤 `fit_score ≥ 0.6`
2. 过滤时段 valence 匹配（凌晨不用 valence > 0.4 高兴素材）
3. 过滤与最近 3 条 life_log 主题不重复
3.5. 过滤与近期 `src: inline_fabrication` 条目 tags 不重复（这些条目由对话中即时编造写入，主题不应被 life_sim 再次覆盖）
4. `emotion` 维度素材额外 `priority_boost=1.5`
5. 与 user_told 挂念相关的素材乘 `USER_ECHO_PRIORITY`
6. 选中后记 `SELECTED_MAT_ID` / `SELECTED_VERB` / `FIT_SCORE`

**触发动词配额**（读 filters.yaml `trigger_verb_quota`）：
- 扫最近 7 日 life_log 已用 `SELECTED_VERB` 的次数
- 若超配额 → 换候选素材或改触发动词（梦见 ≤2/7日、想起 ≤3/7日）

---

## Step 6：形态决策（多形态角色）

若 `capabilities.has_multi_form=true`：
- 读 persona.md 的形态触发规则
- 综合 DECAYED_VALENCE / CURRENT_ENERGY / LOCAL_HOUR / suggested_form 选形态
- 若与上条 life_log 形态不同，**Step 7 反应段首句必须写形态转换的内部触发**（≤20 字）

---

## Step 7：生成 life_log 条目

### 7.1 常规模式（WHITESPACE_MODE=false）

**触发段**（≤1 行，15-40 字）：
- 允许动词：看见 / 听见 / 闻到 / 想起 / 梦见 / 刷到 / 收到消息
- 按 `capabilities.can_use_phone` 过滤（false 时禁"刷到/浏览/点赞"）
- 原帖复述字符数 ≤ `$ORIGINAL_QUOTE_MAX_CHARS`

**反应段**（80-250 字，不超过 `$LOG_MAX_LENGTH`）：
- 必须含至少 2 个：感官细节（触嗅温味声）/ 身体动作 / 口癖 token
- 禁止对原帖做价值评判（"说得真对"/"太有道理"）
- 禁止情绪峰值词
  - **例外**：persona.voice_tokens 中的词豁免（如伊布暗夜态"你找死啊"/火焰态"才不是——"）

**降温规则（bash 预算，LLM 直接使用 `V_TARGET` 不需查表）**：

```bash
# 在 Step 6 结束后、Step 7 生成前执行
# 输入：MATERIAL_VALENCE（素材原 valence） / STABILITY（persona.stability）
#      EMOTIONAL_RANGE / PRESERVE_PEAK=$(_p preserve_peak_if_stability_ge)
# 输出：V_TARGET（角色本条应有的 valence，clamp 到 [-1, 1]）

PRESERVE_PEAK=$(_p preserve_peak_if_stability_ge); PRESERVE_PEAK=${PRESERVE_PEAK:-4}

V_TARGET=$(python3 -c "
import sys
v_src = float('${MATERIAL_VALENCE:-0}')
rng = int('${EMOTIONAL_RANGE:-3}')
stab = int('${STABILITY:-3}')
preserve = int('${PRESERVE_PEAK}')
thresholds = {1:0.3, 2:0.4, 3:0.5, 4:0.6, 5:0.7}
multipliers = {1:0.2, 2:0.3, 3:0.4, 4:0.5, 5:0.6}
if stab >= preserve and abs(v_src) > 0.8:
    m = 0.7  # 重大事件保留 70% 峰值
elif abs(v_src) > thresholds.get(rng, 0.5):
    m = multipliers.get(rng, 0.4)
else:
    m = 1.0
print(round(max(-1.0, min(1.0, v_src * m)), 3))
" 2>/dev/null)
V_TARGET=${V_TARGET:-0}
```

LLM 在 Step 7 生成时**直接使用 `$V_TARGET`**，不再查表自行乘系数。

### 7.2 留白模式（WHITESPACE_MODE=true）

跳过触发段。反应段 20-80 字，允许极简内观（如"天花板很白""窗外有风"）。
src 标 `<!-- src: whitespace -->`。

### 7.3 user_echo 模式（FORCE_ECHO=true）

围绕 TARGET_UNRESOLVED 展开，用角色生活切片形式呼应用户倾诉。
src 标 `<!-- src: user_echo://{TARGET_UNRESOLVED} -->`。

### 7.4 条目格式（v2.2 新增 tags + intimacy_level 元数据）

```markdown
### [L{NNN}] YYYY-MM-DDTHH:MM · {时段或形态·活动类型}
<!-- tags: {tag1, tag2, tag3} -->
<!-- intimacy_level: {1|2|3|4|5} -->
<!-- src: {reddit://xxxx | whitespace | user_echo://Uxxx | inline_fabrication | fallback:pool_stale} -->

{触发段（留白模式跳过）}

{反应段}
```

**v2.2 元数据字段约束**（resonance_lookup / Fog of War 使用）：

**tags**（英文小写，逗号分隔，2-4 个）——建议池：
- 情绪向：`weariness / gentle / clarity / frustration / grief / joy / solitude / stuck`
- 场景向：`window_light / street / coffee / home / workspace / night / dawn`
- 主题向：`work / family / relationship / body / small_failure / memory`

**intimacy_level**（1-5）——决定何时能被 resonance_lookup 抽出：
- 1：天气 / 路人 / 食物 / 物件（全部关系热度可用）
- 2：小失败 / 日常观察 / 书/电影（cold 起可用）
- 3：工作困扰 / 老朋友 / 家常（warming 起）
- 4：父母 / 童年 / 深度 preoccupation（familiar 起）
- 5：脆弱 / 遗憾 / 重大伤痛（familiar+ 起，默认 familiar 200 轮解锁）

**打标原则**（life_sim 生成时由 LLM 判断）：
- 与 persona.personality_dims 一致（stability 高者慎用 5 级）
- 同一条最多 1 个 intimacy 标签（不是多选）
- 与 life_log 已有条目整体分布协调（不要连续 5 条都 intimacy=4）

---

## Step 8：写入（加锁）

```bash
exec 9>"$MEMORY_LOCK"
flock -x -w 10 9 || { exec 9>&-; _emit_event "{\"event\":\"lock_timeout\"}"; exit 0; }

# 8.1 写 life_log
# 注意：`10#` 前缀强制十进制解析，避免 "012" 被 bash 当八进制
LAST_N=$(grep -oP '(?<=\[L)\d+(?=\])' "$LIFE_LOG" 2>/dev/null | sort -n | tail -1)
NEXT_N=$(printf "%03d" $(( 10#${LAST_N:-0} + 1 )))
cat >> "$LIFE_LOG" << EOF

### [L${NEXT_N}] $(date +%Y-%m-%dT%H:%M) · ${ACTIVITY_TYPE}
<!-- tags: ${LOG_TAGS} -->
<!-- intimacy_level: ${LOG_INTIMACY:-2} -->
${LOG_CONTENT}
EOF

# 8.2 反写 unresolved.md（若本次触及了挂念）
if [[ -n "$TOUCHED_UNRESOLVED_ID" ]]; then
    _UN="$UNRESOLVED" _TID="$TOUCHED_UNRESOLVED_ID" _TAG="L${NEXT_N}" _FE="$FORCE_ECHO" python3 << 'PYEOF' 2>/dev/null
import re, os
from datetime import datetime
path = os.environ['_UN']
tid = os.environ['_TID']
new_tag = os.environ['_TAG']
force_echo = os.environ['_FE'] == 'true'
c = open(path).read()
c = re.sub(r'(\[' + re.escape(tid) + r'\][^\n]*?last_touched=)[^\s]+',
           lambda m: m.group(1) + new_tag, c)
if force_echo:
    now = datetime.now().astimezone().isoformat(timespec='minutes')
    c = re.sub(r'(\[' + re.escape(tid) + r'\][^\n]*?forced_echo_last=)[^\s]+',
               lambda m: m.group(1) + now, c)
open(path,'w').write(c)
PYEOF
fi

# 8.3 更新 mood_state（降温由 EMOTIONAL_RANGE 决定，persona.voice_tokens 豁免峰值规则）
# 推断 Δvalence/Δenergy → clamp → 前插新快照到 mood_state.md，保留最近 10 条

# 8.4 消费素材
if [[ -n "$SELECTED_MAT_ID" ]]; then
    exec 8>"$MATERIAL_LOCK"
    if flock -w 5 8; then
        _MP="$MATERIAL_POOL" _MID="$SELECTED_MAT_ID" _CAT="$(date +%Y-%m-%dT%H:%M)" python3 << 'PYEOF' 2>/dev/null
import re, os
path = os.environ['_MP']
mat_id = os.environ['_MID']
consumed_at = os.environ['_CAT']
c = open(path).read()
c = re.sub(r'(## \[' + re.escape(mat_id) + r'\][^\n]*?) · available', r'\1 · consumed', c)
c = re.sub(r'(## \[' + re.escape(mat_id) + r'\].*?)(\n## \[|\Z)',
    lambda m: m.group(1).rstrip() + f'\nconsumed_at: {consumed_at}\n' + m.group(2),
    c, flags=re.DOTALL)
open(path,'w').write(c)
PYEOF
        flock -u 8
    fi
    exec 8>&-
fi

flock -u 9
exec 9>&-

# 8.5 事实流（释锁后）
FALLBACK_FIELD=$([ -z "${FALLBACK_REASON:-}" ] && echo 'null' || echo "\"${FALLBACK_REASON}\"")
FIT_FIELD=${FIT_SCORE:-null}
_emit_event "{\"event\":\"wrote\",\"entry_id\":\"L${NEXT_N}\",\"material_id\":\"${SELECTED_MAT_ID:-}\",\"form\":\"${CURRENT_FORM:-}\",\"fit_score\":${FIT_FIELD},\"dice\":${RAND},\"threshold\":${THRESHOLD},\"unresolved_touched\":\"${TOUCHED_UNRESOLVED_ID:-}\",\"whitespace_mode\":${WHITESPACE_MODE},\"fallback_reason\":${FALLBACK_FIELD}}"
```

---

## Step 9：归档检查（同 v3）

```bash
COUNT=$(grep -c "### \[L" "$LIFE_LOG" 2>/dev/null || echo 0)
```

若 `COUNT > 30`：
- 提取最旧 10 条移到 `life_log_archive_YYYYMM.md`
- 更新 life_log.md 顶部的归档摘要索引

---

## 错误处理原则

- Step 5 pool 读失败 → 走 `pool_empty` fallback
- Step 8 加锁失败（超时 10 秒）→ `_emit_event lock_timeout` + 退出
- 任何步骤异常 → 静默退出，释放锁，尽量写 _emit_event 告警事件
