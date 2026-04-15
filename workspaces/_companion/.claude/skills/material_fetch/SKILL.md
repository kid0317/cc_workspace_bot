---
name: material_fetch
description: |
  定时从小红书 / X.com / Reddit 抓取角色兴趣相关的素材，写入 material_pool.md。
  由 material_fetch.yaml 定时任务触发（每6小时一次）。send_output: false。
  入库时为每条素材赋值情绪向量（valence/energy），G4C 直接读取此字段。
allowed-tools: Bash, Read, Write, Edit
---

# 素材抓取执行流程

> **CRITICAL：禁止输出任何文字。全流程静默执行。**

## Step 0：初始化

```bash
WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)}"
MATERIAL_POOL="$WORKSPACE_DIR/memory/material_pool.md"
MATERIAL_LOCK="$WORKSPACE_DIR/.material.lock"
FETCH_STATE="$WORKSPACE_DIR/.material_fetch_state"
PERSONA_FILE="$WORKSPACE_DIR/memory/persona.md"
MEMORY_FILE="$WORKSPACE_DIR/memory/MEMORY.md"

# 前置条件：initialization_status 必须为 done
INIT_STATUS=$(grep 'initialization_status:' "$MEMORY_FILE" 2>/dev/null | grep -oP '(pending|phase1_done|phase2_done|done)' | head -1)
if [[ "$INIT_STATUS" != "done" ]]; then exit 0; fi

# 初始化 material_pool.md（不存在时创建）
if [[ ! -f "$MATERIAL_POOL" ]]; then
    cat > "$MATERIAL_POOL" << 'POOL_EOF'
# 素材缓冲池

> 状态枚举：available / consumed / expired
> 类型枚举：life_scene / knowledge
> 最多保留 60 条，consumed/expired 超过 30 天后删除

---

POOL_EOF
fi
```

---

## Step 1：读取角色素材配置（从 persona.md）

从 persona.md 的【素材来源配置】区块读取：

```bash
# 读取配置（降级默认值）
KEYWORDS_ZH=$(python3 -c "
import re
try:
    c = open('$PERSONA_FILE').read()
    m = re.search(r'生活场景关键词（中文）[^\n]*:\s*([^\n]+)', c)
    print(m.group(1).strip() if m else '咖啡馆、独处,傍晚散步,一个人下午')
except: print('咖啡馆、独处,傍晚散步,一个人下午')
" 2>/dev/null)

KEYWORDS_EN=$(python3 -c "
import re
try:
    c = open('$PERSONA_FILE').read()
    m = re.search(r'生活场景关键词（英文）[^\n]*:\s*([^\n]+)', c)
    print(m.group(1).strip() if m else 'quiet afternoon,morning coffee alone,bookshop')
except: print('quiet afternoon,morning coffee alone,bookshop')
" 2>/dev/null)

KD_EN=$(python3 -c "
import re
try:
    c = open('$PERSONA_FILE').read()
    m = re.search(r'knowledge_domain_keywords_en[^\n]*:\s*([^\n]+)', c)
    print(m.group(1).strip() if m else 'psychology,interesting facts,science')
except: print('psychology,interesting facts,science')
" 2>/dev/null)

SUBREDDITS=$(python3 -c "
import re
try:
    c = open('$PERSONA_FILE').read()
    m = re.search(r'Reddit subreddits[^\n]*:\s*([^\n]+)', c)
    print(m.group(1).strip() if m else 'CasualConversation,TrueOffMyChest,mildlyinteresting')
except: print('CasualConversation,TrueOffMyChest,mildlyinteresting')
" 2>/dev/null)
```

---

## Step 2：读取上次状态，决定本次平台轮换

```bash
NEXT_PLATFORM=$(grep '^next_platform_rotation:' "$FETCH_STATE" 2>/dev/null | awk '{print $2}')
NEXT_PLATFORM=${NEXT_PLATFORM:-xiaohongshu}
LAST_KEYWORDS=$(grep '^last_keywords:' "$FETCH_STATE" 2>/dev/null | sed 's/^last_keywords: //')

# 统计当前 life_scene 库存
LIFE_COUNT=$(python3 -c "
import re
try:
    c = open('$MATERIAL_POOL').read()
    entries = re.split(r'(?=## \[MAT)', c)
    print(sum(1 for e in entries if 'available' in e and 'life_scene' in e))
except: print(0)
" 2>/dev/null)
LIFE_COUNT=${LIFE_COUNT:-0}

# 库存不足5条时优先小红书；否则按轮换顺序
if [[ $LIFE_COUNT -lt 5 ]]; then
    THIS_PLATFORM="xiaohongshu"
else
    THIS_PLATFORM="$NEXT_PLATFORM"
fi
```

---

## Step 3：按平台抓取

一次运行只抓一个平台，减少超时风险。

### 小红书（xiaohongshu）

```bash
# 从 KEYWORDS_ZH 选一个未在 LAST_KEYWORDS 中使用的关键词
KW_ZH=$(python3 -c "
kws = '$KEYWORDS_ZH'.replace('、',',').split(',')
last = '$LAST_KEYWORDS'
for kw in kws:
    kw = kw.strip()
    if kw and kw not in last:
        print(kw); break
else:
    print(kws[0].strip() if kws else '咖啡馆')
" 2>/dev/null)
KW_ZH=${KW_ZH:-咖啡馆}

# 故障诊断（如失败先检查）：
# docker ps --filter name=xiaohongshu → 容器未运行则 docker start xiaohongshu-mcp
# mcporter config add xiaohongshu http://localhost:18060/mcp → 重新注册（按目录存储）
RESULT=$(mcporter call "xiaohongshu.search_feeds(keyword: \"$KW_ZH\")" 2>/dev/null)

# LLM 对结果执行质量过滤后写入 material_pool.md：
# 丢弃：含 推荐/种草/测评/好物/购买/下单/优惠/广告/合作/带货/店铺/直播/产品/品牌/￥ 的内容
# 丢弃：正文长度 < 30 字
# 通过：记录为 life_scene 类素材，赋值情绪向量（见 Step 4）
USED_KW="$KW_ZH"
```

### X.com（xreach）

```bash
KW_EN=$(python3 -c "
kws = '$KEYWORDS_EN'.split(',')
last = '$LAST_KEYWORDS'
for kw in kws:
    kw = kw.strip()
    if kw and kw not in last:
        print(kw); break
else:
    print(kws[0].strip() if kws else 'quiet afternoon')
" 2>/dev/null)
KW_EN=${KW_EN:-quiet afternoon}

RESULT=$(timeout 30 xreach search "$KW_EN" -n 10 --json 2>/dev/null) || RESULT=""

python3 << PYEOF 2>/dev/null
import json, re
raw = '''$RESULT'''
try:
    data = json.loads(raw)
except:
    exit(0)
items = data if isinstance(data, list) else data.get('items', [])
filtered = []
for item in items:
    if item.get('isRetweet') or item.get('isQuote'):
        continue
    if item.get('likeCount', 0) < 50 and not item.get('user', {}).get('isBlueVerified', False):
        continue
    text = re.sub(r'https?://t\.co/\S+', '', item.get('text', '')).strip()
    text = re.sub(r'@\w+', '', text).strip()
    text = re.sub(r'#\w+', '', text).strip()
    if len(text) < 30:
        continue
    neg_words = ['hate', 'angry', 'worst', 'terrible', 'disgusting', 'awful']
    fact_markers = ['research', 'study', 'found', 'actually', 'TIL', 'scientists', 'data']
    if any(w in text.lower() for w in neg_words) and not any(w in text.lower() for w in fact_markers):
        continue
    filtered.append({'text': text, 'likes': item.get('likeCount', 0)})

# LLM 对 filtered 列表执行情绪向量赋值后写入 material_pool.md
for f in filtered[:5]:
    print(f"TEXT:{f['text'][:300]}")
    print(f"LIKES:{f['likes']}")
    print("---")
PYEOF
USED_KW="$KW_EN"
```

### Reddit（arctic_shift，无需 agent-reach 直接 curl）

```bash
AFTER_TS=$(python3 -c "import time; print(int(time.time()-7*86400))")
SUBREDDIT=$(echo "$SUBREDDITS" | python3 -c "
import sys, random
subs = sys.stdin.read().strip().split(',')
last = '$LAST_KEYWORDS'
unused = [s.strip() for s in subs if s.strip() not in last]
print(random.choice(unused) if unused else random.choice([s.strip() for s in subs]))
" 2>/dev/null)
SUBREDDIT=${SUBREDDIT:-CasualConversation}

KW_REDDIT=$(python3 -c "
kws = '$KD_EN'.split(',')
last = '$LAST_KEYWORDS'
for kw in kws:
    kw = kw.strip()
    if kw and kw not in last:
        print(kw); break
else:
    print('morning routine')
" 2>/dev/null)
KW_REDDIT=${KW_REDDIT:-morning}
KW_ENCODED=$(python3 -c "import urllib.parse; print(urllib.parse.quote('$KW_REDDIT'))")

RESULT=$(curl -s --max-time 15 \
    "https://arctic-shift.photon-reddit.com/api/posts/search?subreddit=${SUBREDDIT}&title=${KW_ENCODED}&after=${AFTER_TS}&limit=15&sort=desc" \
    2>/dev/null)

python3 << PYEOF 2>/dev/null
import json
raw = '''$RESULT'''
try:
    data = json.loads(raw)
except:
    exit(0)
for post in data.get('data', []):
    selftext = post.get('selftext', '')
    if selftext in ('[removed]', '[deleted]', ''):
        continue
    if not post.get('is_self', True):
        continue
    if post.get('score', 0) < 2:
        continue
    if len(selftext) < 50:
        continue
    print(f"TITLE:{post.get('title', '')}")
    print(f"BODY:{selftext[:500]}")
    print(f"SCORE:{post.get('score', 0)}")
    print(f"SUBREDDIT:{post.get('subreddit', '')}")
    print("---")
PYEOF
USED_KW="${SUBREDDIT}:${KW_REDDIT}"
```

---

## Step 4：情绪向量赋值规则（LLM 执行，入库时一次性赋值）

对每条通过质量过滤的素材，LLM 判断并赋值：

**valence 赋值**：
- 含 高兴/美好/温暖/满足/开心/喜欢/好玩/快乐/happy/warm/lovely/wonderful 等正向词 → `valence: +0.1` ~ `+0.3`
- 含 失落/疲惫/无聊/沮丧/难过/失望/tired/bored/lonely/sad 等负向词 → `valence: -0.1` ~ `-0.3`
- 中性场景（日常叙述，无明显情绪倾向）→ `valence: 0.0`

**energy 赋值**：
- outdoor/active（外出/运动/社交/热闹/城市漫步/逛街）→ `energy: 0.5` ~ `0.7`
- indoor/quiet（室内/安静/休息/独处/发呆/冥想/reading/sitting）→ `energy: 0.3` ~ `0.5`
- 知识类素材（无明显活动描述）→ `energy: 0.0`

**说明**：情绪向量入库后固定，life_sim Step 2.5 的 G4C 检查直接读取此字段，无需 LLM 现场推断。

---

## Step 5：knowledge vs life_scene 识别（LLM 可执行）

**分类为 knowledge 的条件（满足任一）**：
- K1：正文含数量/统计描述（X%、研究发现、scientists found、study shows、数据显示）
- K2：正文含反常识描述（其实、原来、actually、surprisingly、turns out）
- K3：正文含某领域知识点，可被概括为一条清晰事实陈述
- K4：来源是 r/todayilearned、r/science、r/explainlikeimfive、r/Showerthoughts
- K5：推文含 TIL、fascinating、mind blown、did you know

**分类为 life_scene**：其余情况（保守策略，两者都不明确时归 life_scene）

---

## Step 6：写入 material_pool.md（加 .material.lock 保护）

```bash
exec 8>"$MATERIAL_LOCK"
if ! flock -w 10 8; then
    exit 0  # 获取锁失败，本次静默跳过
fi

# 清理超过30天的 consumed/expired 条目（R2修复：统一清理规则）
python3 << 'CLEAN_EOF' 2>/dev/null
import re
from datetime import datetime, timezone, timedelta
try:
    with open("$MATERIAL_POOL") as f:
        content = f.read()
    entries = re.split(r'(?=## \[MAT)', content)
    header = [e for e in entries if not e.strip().startswith('## [MAT')]
    data = [e for e in entries if e.strip().startswith('## [MAT')]
    cutoff = datetime.now(timezone.utc) - timedelta(days=30)
    kept = []
    for e in data:
        # available 条目保留
        if 'available' in e:
            kept.append(e)
            continue
        # consumed/expired：检查时间戳
        m = re.search(r'## \[MAT\d+\] (\d{4}-\d{2}-\d{2}T\d{2}:\d{2})', e)
        if m:
            try:
                ts = datetime.fromisoformat(m.group(1)).replace(tzinfo=timezone.utc)
                if (datetime.now(timezone.utc) - ts).days <= 30:
                    kept.append(e)
            except:
                kept.append(e)
        else:
            kept.append(e)
    with open("$MATERIAL_POOL", 'w') as f:
        f.write(''.join(header) + ''.join(kept))
except:
    pass
CLEAN_EOF

# 计算下一个 MAT 编号
LAST_MAT=$(grep -oP '(?<=\[MAT)\d+(?=\])' "$MATERIAL_POOL" 2>/dev/null | sort -n | tail -1)
NEXT_MAT=$(printf "%03d" $(( ${LAST_MAT:-0} + 1 )))
NOW_FETCH=$(date +%Y-%m-%dT%H:%M)

# LLM 将步骤3抓取、步骤4赋值情绪向量后的素材，按以下格式追加到 material_pool.md：
# 
# ## [MAT${NEXT_MAT}] ${NOW_FETCH} · available
# 类型: life_scene
# 平台: xiaohongshu
# 标题: [素材标题]
# 正文摘要: [前200字，已清洗平台标记词]
# 情绪向量: valence=+0.1, energy=0.4
# 角色适配: ✅
# 搜索关键词: [使用的关键词]
#
# （knowledge 类素材同格式，类型字段填 knowledge，情绪向量 energy=0.0）

flock -u 8
exec 8>&-
```

---

## Step 7：更新 .material_fetch_state

```bash
NOW_TS=$(python3 -c "from datetime import datetime; print(datetime.now().astimezone().isoformat(timespec='seconds'))")

# 确定下次轮换平台
case "$THIS_PLATFORM" in
    xiaohongshu) NEXT_NEXT="xreach" ;;
    xreach) NEXT_NEXT="reddit" ;;
    reddit) NEXT_NEXT="xiaohongshu" ;;
    *) NEXT_NEXT="xreach" ;;
esac

NEW_LIFE=$(python3 -c "
import re
try:
    c = open('$MATERIAL_POOL').read()
    entries = re.split(r'(?=## \[MAT)', c)
    print(sum(1 for e in entries if 'available' in e and 'life_scene' in e))
except: print(0)
" 2>/dev/null)
NEW_KWD=$(python3 -c "
import re
try:
    c = open('$MATERIAL_POOL').read()
    entries = re.split(r'(?=## \[MAT)', c)
    print(sum(1 for e in entries if 'available' in e and 'knowledge' in e))
except: print(0)
" 2>/dev/null)

cat > "$FETCH_STATE" << EOF
last_run: ${NOW_TS}
last_keywords: ${USED_KW:-}
last_failed_platform:
next_platform_rotation: ${NEXT_NEXT}
material_pool_life_count: ${NEW_LIFE:-0}
material_pool_knowledge_count: ${NEW_KWD:-0}
EOF
```

---

## 错误处理

- 小红书调用失败 → 记录 `last_failed_platform: xiaohongshu`，自动切换到 Reddit（arctic_shift 直接 curl，无需 agent-reach）
- X.com 超时（30秒）→ 跳过，不阻断，更新 NEXT_PLATFORM 跳过 xreach
- arctic_shift 返回空 → 正常退出，不报错
- material_pool.md 不存在 → Step 0 自动创建
