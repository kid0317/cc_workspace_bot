---
name: material_fetch
description: |
  v5.1 关键词模板驱动 + 硬筛规则 + LLM 二审（锁外）+ 失败状态追踪。
  由 material_fetch.yaml 定时任务触发（每 6 小时）。send_output: false。
  读 memory/keyword_templates.yaml 生成查询，经 filters.yaml 硬筛后 LLM 二审打 fit_score 入库。
allowed-tools: Bash, Read, Write, Edit
---

# 素材抓取执行流程 v5.1

> **CRITICAL：禁止输出任何文字。全流程静默执行。**

## 设计原则

- **模板驱动关键词**：不再硬编码，所有 query 从 `keyword_templates.yaml` 填槽生成
- **能力位图路由**：按 capabilities 查 `platform_rules` 决定目标平台
- **硬筛 → LLM 二审**：filters.yaml 第一层硬 reject 走 bash/regex，第二层 LLM 打 fit_score（**锁外执行**）
- **失败状态追踪**：`.material_fetch_state.json` 记连续失败次数、last_success_ts，供 life_sim 判断降级
- **锁用来保护写入**：LLM 二审耗时，必须在锁外完成，仅 append 到 pool 时短暂持锁

---

## Step 0：初始化

```bash
WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)}"
MATERIAL_POOL="$WORKSPACE_DIR/memory/material_pool.md"
MATERIAL_LOCK="$WORKSPACE_DIR/.material.lock"
FETCH_STATE="$WORKSPACE_DIR/.material_fetch_state.json"
PERSONA_FILE="$WORKSPACE_DIR/memory/persona.md"
MEMORY_FILE="$WORKSPACE_DIR/memory/MEMORY.md"
KW_TEMPLATES="$WORKSPACE_DIR/memory/keyword_templates.yaml"
UNRESOLVED="$WORKSPACE_DIR/memory/unresolved.md"
RECENT_HISTORY="$WORKSPACE_DIR/memory/RECENT_HISTORY.md"
FILTERS="$WORKSPACE_DIR/.claude/skills/material_fetch/filters.yaml"
CALIBRATE_STATE="$WORKSPACE_DIR/.calibrate_state"
EVENTS_JSONL="$WORKSPACE_DIR/.life_sim_events.$(date +%Y%m%d).jsonl"

INIT_STATUS=$(grep 'initialization_status:' "$MEMORY_FILE" 2>/dev/null | grep -oP '(pending|phase1_done|phase2_done|done)' | head -1)
[[ "$INIT_STATUS" != "done" ]] && exit 0

# material_pool.md 不存在则创建
if [[ ! -f "$MATERIAL_POOL" ]]; then
    cat > "$MATERIAL_POOL" << 'POOL_EOF'
# 素材缓冲池

> 状态：available / consumed / expired
> 类型：life_scene / knowledge
> 满 200 条或 consumed/expired 超 30 天时自动归档。

---

POOL_EOF
fi

# 事实流辅助（同 life_sim）
_emit_event() {
    local payload="$1"
    local ts; ts=$(python3 -c "from datetime import datetime; print(datetime.now().astimezone().isoformat(timespec='seconds'))" 2>/dev/null)
    local ws; ws=$(basename "$WORKSPACE_DIR")
    printf '{"v":1,"ts":"%s","ws":"%s",%s\n' "$ts" "$ws" "${payload:1}" >> "$EVENTS_JSONL"
}
```

---

## Step 1：template drift 检测（persona.md mtime）

```bash
PERSONA_MTIME=$(stat -c %Y "$PERSONA_FILE" 2>/dev/null || echo 0)
LAST_SEEN_MTIME=$(grep '^last_persona_mtime:' "$CALIBRATE_STATE" 2>/dev/null | awk '{print $2}')
LAST_SEEN_MTIME=${LAST_SEEN_MTIME:-0}

CALIBRATE_TEMPLATES="$WORKSPACE_DIR/.claude/skills/calibrate_params/calibrate_templates.py"
KW_INSTANCE="$WORKSPACE_DIR/memory/keyword_templates.yaml"

# 触发条件：(1) keyword_templates.yaml 不存在（首次运行）(2) persona.md mtime 漂移
NEEDS_CALIBRATE=false
if [[ ! -f "$KW_INSTANCE" ]]; then
    NEEDS_CALIBRATE=true
elif [[ $PERSONA_MTIME -gt $LAST_SEEN_MTIME ]]; then
    NEEDS_CALIBRATE=true
fi

if [[ "$NEEDS_CALIBRATE" == "true" ]] && [[ -f "$CALIBRATE_TEMPLATES" ]]; then
    python3 "$CALIBRATE_TEMPLATES" "$WORKSPACE_DIR" 2>/dev/null || true
    _emit_event "{\"event\":\"calibrate_triggered\",\"reason\":\"$([[ ! -f "$KW_INSTANCE" ]] && echo first_run || echo persona_mtime_drift)\"}"
fi

# 若仍无 keyword_templates.yaml（persona 空/calibrate 失败）→ 静默退出
if [[ ! -f "$KW_INSTANCE" ]]; then
    _emit_event "{\"event\":\"fetch_skip\",\"reason\":\"no_keyword_templates\"}"
    exit 0
fi
```

---

## Step 2：归档检查（每次开头自理，不要框架级 cron）

```bash
# material_pool 大小检查
POOL_TOTAL=$(grep -c '^## \[MAT' "$MATERIAL_POOL" 2>/dev/null || echo 0)
POOL_CONSUMED_OLD=$(python3 -c "
import re
from datetime import datetime, timezone
try:
    c = open('$MATERIAL_POOL').read()
    entries = re.split(r'(?=## \[MAT)', c)
    n = 0
    for e in entries:
        if 'consumed' not in e and 'expired' not in e: continue
        m = re.search(r'## \[MAT\d+\] (\d{4}-\d{2}-\d{2}T\d{2}:\d{2})', e)
        if m:
            ts = datetime.fromisoformat(m.group(1)).replace(tzinfo=timezone.utc)
            if (datetime.now(timezone.utc) - ts).days > 30: n += 1
    print(n)
except: print(0)
" 2>/dev/null)

if [[ $POOL_TOTAL -gt 200 ]] || [[ ${POOL_CONSUMED_OLD:-0} -gt 20 ]]; then
    # 归档：把 >30 天的 consumed/expired 移到 material_pool_archive_YYYYMM.md
    ARCHIVE_FILE="$WORKSPACE_DIR/memory/material_pool_archive_$(date +%Y%m).md"
    # LLM 或 python 执行：切分条目 → 老旧 consumed/expired append 到 archive → 从 pool 删除
    _emit_event "{\"event\":\"pool_archived\",\"file\":\"$(basename "$ARCHIVE_FILE")\"}"
fi
```

---

## Step 3：关键词生成（填槽）

读 `keyword_templates.yaml` 的 `dimensions`，对每个 `enabled_when` 为 true 的维度：

1. 读 `slots_source` 指向的数据源
   - `persona.xxx_anchors` → 从 persona.md 对应块读取
   - `memory/unresolved.md` → 读活跃块的 keyword（关键词从挂念文字提取）
   - `memory/RECENT_HISTORY.md#recent_24h` → LLM 从最近 24h 对话抽情绪词
2. 填入 `templates` 产出候选 query，每维度最多 3 条
3. 应用 `priority_boost`（emotion 维度 × 1.5）

**输出**：一个 query 列表，每条带 `dimension`、`priority`、`origin_slot`。

---

## Step 4：平台路由 + 抓取

读 `keyword_templates.yaml` 的 `platform_rules`，按 `capabilities` 匹配第一条 `when` 为 true 的规则。
得到 `primary` + `secondary` 平台列表。

**本次运行只抓一个平台**（减少超时；轮换从 `.material_fetch_state.json` 的 `next_platform_rotation` 读）：

```bash
NEXT_PLATFORM=$(python3 -c "
import json
try: print(json.load(open('$FETCH_STATE')).get('next_platform_rotation','xreach'))
except: print('xreach')
" 2>/dev/null)
THIS_PLATFORM="${NEXT_PLATFORM:-xreach}"

# 屏蔽小红书（防账号被封）
if [[ "$THIS_PLATFORM" == "xiaohongshu" ]]; then
    THIS_PLATFORM="xreach"
fi
```

### 平台执行分支

- ~~xiaohongshu~~ **已废弃（账号易封，2026-04-21 起禁用）** `mcporter call "xiaohongshu.search_feeds(keyword: \"$KW\")"` （同 v3）
  - **get_feed_detail 兜底**：`get_feed_detail` 对 ~40% 老帖返回 `"feed xxx not found in noteDetailMap"`。
    硬筛层需加防御：若详情抓取失败，**用 search_feeds 返回的 title/summary 判定 fit**；仍不足时 skip 该条不入库（避免假数据污染 pool）。
- **xreach (X/Twitter)**：`xreach search "$KW_EN" -n 10 --json`
- **reddit (arctic_shift)**：`curl https://arctic-shift.photon-reddit.com/api/posts/search?subreddit=X&title=Y&after=TS&limit=15&sort=desc`
  - **失败 fallback**：若 title 过滤返回 0 条，retry 不带 `title=` 参数（按 subreddit + after 宽抓），再做关键词二层匹配
- **weibo / exa_web**：按需扩展

**每次失败**（超时/空返回/HTTP错误）：
```bash
FAILED_PLATFORMS_THIS_TICK="$FAILED_PLATFORMS_THIS_TICK $THIS_PLATFORM"
```

---

## Step 5：硬筛 + LLM 二审（**锁外完成**）

### 5.1 硬筛（bash/regex 按 filters.yaml）

对每条素材应用 `hard_reject.by_capability` 和 `hard_reject.always` 规则，不匹配者进入 5.2。

```bash
# 伪代码：LLM 读取 filters.yaml + capabilities + 素材列表，应用规则过滤
# 输出：PASSED_MATERIALS = [素材1, 素材2, ...]
```

### 5.2 LLM 二审（锁外）

对每条通过硬筛的素材，用 `filters.yaml.llm_review_prompt` 让 LLM 产出 JSON：

```json
{
  "fit_score": 0.82,
  "reason": "宝可梦视角可平移",
  "suggested_form": "暗夜态",
  "suggested_verb": "看见",
  "valence": -0.2,
  "energy": 0.3
}
```

`fit_score < 0.6` → 丢弃。保留的素材进入 Step 6 写入。

**重要**：此步骤完全在锁外完成，不要持有 `.material.lock`。

---

## Step 6：临门加锁写入 material_pool.md

```bash
exec 8>"$MATERIAL_LOCK"
if ! flock -w 10 8; then
    _emit_event "{\"event\":\"write_lock_timeout\"}"
    exit 0
fi

LAST_MAT=$(grep -oP '(?<=\[MAT)\d+(?=\])' "$MATERIAL_POOL" 2>/dev/null | sort -n | tail -1)
# 10# 前缀强制十进制解析，避免 "012" 被 bash 当八进制
NEXT_MAT=$(( 10#${LAST_MAT:-0} + 1 ))

# LLM 对每条保留的素材，按以下格式 append 到 $MATERIAL_POOL（编号递增）：
```

**写入格式**：

```markdown
## [MAT{NNN}] YYYY-MM-DDTHH:MM · available
src_platform: reddit/r/Showerthoughts
kind: life_scene                    # life_scene | knowledge
fit_score: 0.82
suggested_form: 暗夜态               # 或 null
suggested_verb: 看见
valence: -0.2
energy: 0.3
dimension: emotion                   # 触发本条抓取的 dimension（profession/place/interest/unresolved/emotion）
query_used: "深夜 怎么办"
content: |
  there are bad ideas that come to you at night, and good ones that die when you wake up
```

```bash
flock -u 8
exec 8>&-
```

---

## Step 7：更新 .material_fetch_state.json

```bash
NOW_TS=$(python3 -c "from datetime import datetime; print(datetime.now().astimezone().isoformat(timespec='seconds'))")

# 计算本次成功 / 失败
if [[ ${#NEW_ENTRIES_COUNT} -gt 0 ]]; then
    # 至少入库了一条 → 重置连续失败
    _PLATFORM_TICK_STATUS="success"
else
    _PLATFORM_TICK_STATUS="fail"
fi

# 决定下次轮换平台
case "$THIS_PLATFORM" in
    xreach) NEXT_NEXT="reddit" ;;
    reddit) NEXT_NEXT="xreach" ;;
    *) NEXT_NEXT="xreach" ;;    # 跳过 xiaohongshu（已屏蔽）
esac

# 写入 JSON（LLM 保证字段完整）：
_FS="$FETCH_STATE" _NOW="$NOW_TS" _NEXT="$NEXT_NEXT" _STATUS="$_PLATFORM_TICK_STATUS" _PLATFORM="$THIS_PLATFORM" \
python3 << 'PYEOF' 2>/dev/null
import json, os
path = os.environ['_FS']
try: state = json.load(open(path))
except: state = {"last_success_ts":"","consecutive_failures":0,"platform_failures":{}}

if os.environ['_STATUS'] == 'success':
    state['last_success_ts'] = os.environ['_NOW']
    state['consecutive_failures'] = 0
else:
    state['consecutive_failures'] = state.get('consecutive_failures',0) + 1
    p = os.environ['_PLATFORM']
    state.setdefault('platform_failures',{})
    state['platform_failures'][p] = state['platform_failures'].get(p,0) + 1

state['last_run_ts'] = os.environ['_NOW']
state['next_platform_rotation'] = os.environ['_NEXT']

with open(path,'w') as f:
    json.dump(state, f, ensure_ascii=False, indent=2)
PYEOF
```

---

## Step 8：失败告警事件

```bash
CONSEC=$(python3 -c "
import json
try: print(json.load(open('$FETCH_STATE')).get('consecutive_failures',0))
except: print(0)
" 2>/dev/null)

if [[ ${CONSEC:-0} -ge 3 ]]; then
    _emit_event "{\"event\":\"fetch_alert\",\"severity\":\"warn\",\"consecutive\":${CONSEC}}"
fi
if [[ ${CONSEC:-0} -ge 10 ]]; then
    _emit_event "{\"event\":\"fetch_alert\",\"severity\":\"critical\",\"consecutive\":${CONSEC}}"
fi
```

---

## 错误处理

- 平台调用失败/超时 → 记录到 `FAILED_PLATFORMS_THIS_TICK`，下次轮换跳过
- arctic_shift 返回空 → 正常退出（不算失败）
- material_pool.md 损坏 → Step 0 重建
- 加锁超时 → `_emit_event write_lock_timeout` + 退出
