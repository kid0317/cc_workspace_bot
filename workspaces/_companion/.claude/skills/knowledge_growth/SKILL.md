---
name: knowledge_growth
description: |
  每日凌晨3点处理 material_pool.md 中的知识素材，推进三阶段状态机（raw→digesting→formed）。
  独立定时任务，cron: "0 3 * * *"，不依赖 life_sim。send_output: false。
  使用 .knowledge.lock（不与 .memory.lock 混用）。
allowed-tools: Bash, Read, Write, Edit
---

# 知识成长执行流程

> **CRITICAL：禁止输出任何文字。全流程静默执行。**

## Step 0：初始化

```bash
WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)}"
KNOWLEDGE_BANK="$WORKSPACE_DIR/memory/knowledge_bank.md"
MATERIAL_POOL="$WORKSPACE_DIR/memory/material_pool.md"
KNOWLEDGE_LOCK="$WORKSPACE_DIR/.knowledge.lock"
MATERIAL_LOCK="$WORKSPACE_DIR/.material.lock"
PERSONA_FILE="$WORKSPACE_DIR/memory/persona.md"
MOOD_STATE="$WORKSPACE_DIR/memory/mood_state.md"
MEMORY_FILE="$WORKSPACE_DIR/memory/MEMORY.md"

# 前置条件
INIT_STATUS=$(grep 'initialization_status:' "$MEMORY_FILE" 2>/dev/null | grep -oP '(pending|phase1_done|phase2_done|done)' | head -1)
if [[ "$INIT_STATUS" != "done" ]]; then exit 0; fi

# 初始化 knowledge_bank.md（不存在时创建）
if [[ ! -f "$KNOWLEDGE_BANK" ]]; then
    cat > "$KNOWLEDGE_BANK" << 'KB_EOF'
# 知识银行

> formed 条目上限：50 条。超出时删除 used_count 最低且最旧的 formed 条目。
> 独立锁文件：.knowledge.lock

---

KB_EOF
fi
```

---

## Step 1：加锁并读取当前情绪

```bash
exec 9>"$KNOWLEDGE_LOCK"
flock -x -w 10 9 || exit 0

CURRENT_ENERGY=$(grep -m1 '^energy:' "$MOOD_STATE" 2>/dev/null | awk '{print $2}')
CURRENT_ENERGY=${CURRENT_ENERGY:-0.5}
CURRENT_VALENCE=$(grep -m1 '^valence:' "$MOOD_STATE" 2>/dev/null | awk '{print $2}')
CURRENT_VALENCE=${CURRENT_VALENCE:-0.0}

# 读取 stability（B5修复：线性插值，覆盖1-5全档）
STABILITY=$(python3 -c "
import re
try:
    c = open('$PERSONA_FILE').read()
    m = re.search(r'stability:\s*(\d+)', c)
    print(int(m.group(1)) if m else 3)
except: print(3)
" 2>/dev/null)
STABILITY=${STABILITY:-3}
```

---

## B4修复：空素材池降级（Step 2 前必须检查）

```bash
KNOWLEDGE_AVAILABLE=$(python3 -c "
import re
try:
    c = open('$MATERIAL_POOL').read()
    entries = re.split(r'(?=## \[MAT)', c)
    print(sum(1 for e in entries if 'available' in e and 'knowledge' in e))
except: print(0)
" 2>/dev/null)
KNOWLEDGE_AVAILABLE=${KNOWLEDGE_AVAILABLE:-0}

if [[ $KNOWLEDGE_AVAILABLE -eq 0 ]]; then
    # 无新素材时：跳过 Step 2（raw→digesting），继续执行 Step 3（digesting→formed）
    # 说明：每日凌晨3点可能只推进一级状态，属预期行为，不中断任务
    SKIP_STEP2=true
else
    SKIP_STEP2=false
fi
```

---

## Step 2：新素材入库（raw）——如 SKIP_STEP2=true 则跳过

仅在 SKIP_STEP2=false 时执行：

**2.1 知识素材识别规则**（LLM 可执行）：

**分类为 knowledge 的条件（满足任一）**：
- K1：正文含数量/统计描述（X%、研究发现、scientists found、study shows、数据显示）
- K2：正文含反常识描述（其实、原来、actually、surprisingly、turns out）
- K3：正文含某领域知识点，可被概括为一条清晰事实陈述
- K4：来源是 r/todayilearned、r/science、r/explainlikeimfive、r/Showerthoughts
- K5：推文含 TIL、fascinating、mind blown、did you know

**2.2 从 material_pool.md 读取 knowledge 类 available 素材**：

```bash
exec 8>"$MATERIAL_LOCK"
if flock -w 5 8; then
    # 读取 knowledge 类素材并标记为消费中（避免重复处理）
    # LLM 提取正文，生成知识条目写入 knowledge_bank.md
    flock -u 8
fi
exec 8>&-
```

**2.3 写入 knowledge_bank.md**（raw 状态）：

对每条 knowledge 素材，LLM 提炼核心知识并写入：

```markdown
## [K{NNN}] raw · {时间戳}
标签: {领域1} / {领域2}
核心知识: {一句话概括可验证的事实陈述}
角色化表达: （待 digesting 阶段生成）
下次可进入 digesting: {+24小时}
入库时间: {当前时间}
```

每次最多入库 5 条新 raw 条目。

---

## Step 3：raw → digesting 转换

```bash
# energy 不足时暂停入库
if python3 -c "import sys; sys.exit(0 if float('$CURRENT_ENERGY') > 0.3 else 1)"; then
    # energy > 0.3：检查可以转 digesting 的 raw 条目
    # 对每条 raw 条目：
    # 1. 检查"下次可进入 digesting"时间，未到则跳过
    # 2. 已到时间：生成角色化表达
    #    - 参照 persona.md 口头禅和说话示例
    #    - 不超过 2 句，加入主观感受
    #    - 不引用技术术语（知识过于晦涩时填写"（仅作背景知识，不主动引用）"）
    # 3. 更新状态为 digesting，填写消化进度 1/2 和下次可转 formed 时间（+24h）
    # 每次最多处理 3 条
    echo "processing raw→digesting" > /dev/null
fi
```

**digesting 条目格式**：

```markdown
## [K{NNN}] digesting · {时间戳}
标签: {领域1} / {领域2}
核心知识: {一句话事实陈述}
角色化表达: {2句以内，角色口吻，含主观感受}
消化进度: 1/2
下次可转 formed: {+24小时}
入库时间: {原始时间}
```

---

## Step 4：digesting → formed 转换

```bash
# 规则：
# energy < 0.3 → 已在 digesting 的条目暂停（不转 formed，不回退）
# valence < -0.4 → 优先将标签含"情感/心理/治愈"的 digesting 条目直接转 formed（忽略时间约束）
# 其他 digesting 条目：检查"下次可转 formed"时间，已到则转 formed
echo "processing digesting→formed" > /dev/null
```

**formed 条目格式**：

```markdown
## [K{NNN}] formed · {时间戳}
标签: {领域1} / {领域2}
核心知识: {一句话事实陈述}
角色化表达: {2句以内，角色口吻，含主观感受}
对话使用条件: {触发话题关键词，如：动物话题 / 记忆话题 / 有趣的事}
情绪适配: neutral
最后使用: 从未（ready）
used_count: 0
入库时间: {原始时间}
```

---

## Step 5：formed 上限检查（上限 50 条）

如果 formed 条目数 > 50：
- 找 `used_count: 0` 且入库最早的条目删除
- 若全部 used_count > 0，则删 used_count 最低且最旧的条目
- 每次只删 1 条

---

## Step 6：标记 material_pool 中已处理素材为 consumed

```bash
exec 8>"$MATERIAL_LOCK"
if flock -w 5 8; then
    # 将本次入库的 knowledge 素材在 material_pool.md 中标记为 consumed
    # 写入消费时间：consumed_at: {当前时间}
    flock -u 8
fi
exec 8>&-
```

---

## Step 7：解锁

```bash
flock -u 9
exec 9>&-
```

---

## digesting 可引用规则（R3修复：强相关条件量化）

当 digesting 条目的角色化表达已生成时，允许在对话中作为"模糊感知"引用：
- 表达形式："最近在想一个事，还没完全理解……"
- 不作为确定性知识，不更新 used_count
- **触发条件**：openness >= 4（从 persona.md 读取）且当前话题**强相关**

**"强相关"判定规则与示例**：

强相关 = 用户当前话题的核心词汇与知识条目标签存在**同类别**交集：

| 用户话题 | 知识条目标签 | 是否触发 |
|---------|------------|---------|
| 问动物行为 | 动物行为 / 认知科学 | ✅ 触发 |
| 聊睡眠质量 | 心理学 / 睡眠 | ✅ 触发 |
| 描述傍晚光线 | 物理 / 光学 | ✅ 触发 |
| 聊天气热不热 | 粒子物理 / 量子力学 | ❌ 不触发 |
| 说今天很累 | 太空探索 / 天文学 | ❌ 不触发 |

---

## P4-C：formed 知识进入 proactive

在 proactive SKILL Step 3 的 P4-C 中，触发条件（全部满足）：
1. 存在 `formed` 且 `最后使用: 从未（ready）` 的条目
2. 当前 valence > -0.2（从 mood_state.md 读取）
3. 距上次知识引用超过 3 轮对话（.proactive_state 记录 `last_knowledge_ref_at`）
4. 角色 openness >= 3（从 persona.md 读取）
5. `last_knowledge_ref_at` 不存在 → 视为满足条件（R4修复：first-run 降级）

执行：随机选 1 条符合条件的 formed，取 `角色化表达` 生成消息。
发送后：更新该条目的 `最后使用` 和 `used_count + 1`；更新 `.proactive_state` 的 `last_knowledge_ref_at`。
