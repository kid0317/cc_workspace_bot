---
name: calibrate_params
description: |
  基于 persona.md 的 personality_dims，按映射公式计算行为参数，写入 character_params.yaml。
  由 calibrate_params 定时任务触发（每7天），或初始化完成时立即触发一次。
  其他 SKILL/hook 在检测到 persona_checksum 不一致时，也会直接调用 recalculate.sh 同步重算。
allowed-tools: Bash, Read, Write
---

# 参数校准执行流程

> **CRITICAL：禁止任何前台输出。全程静默。**
> 本 Skill 由定时任务触发（send_output=false），任何 Claude 输出都会被丢弃。

## Step 0：初始化

```bash
WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)}"
PERSONA_FILE="$WORKSPACE_DIR/memory/persona.md"
PARAMS_FILE="$WORKSPACE_DIR/character_params.yaml"
MEMORY_LOCK="$WORKSPACE_DIR/.memory.lock"
MEMORY_FILE="$WORKSPACE_DIR/memory/MEMORY.md"

# 前置条件：initialization_status 必须为 done
INIT_STATUS=$(grep 'initialization_status:' "$MEMORY_FILE" 2>/dev/null \
    | grep -oP '(pending|phase1_done|phase2_done|done)' | head -1)
if [[ "$INIT_STATUS" != "done" ]]; then
    exit 0
fi
```

## Step 1：调用 recalculate.sh 完成计算和写入

本 SKILL 的核心逻辑封装在 `recalculate.sh`（纯 bash，无 LLM 依赖），
供定时任务和同步触发两条路径复用：

```bash
bash "$WORKSPACE_DIR/.claude/skills/calibrate_params/recalculate.sh" "$WORKSPACE_DIR"
```

recalculate.sh 的职责：
1. 读取 persona.md 中 `personality_dims:` 块的 6 个维度值
2. 按映射公式计算 response_mode_weights / proactive / conversation / life_sim 参数
3. 检查 persona_checksum，若与已有 character_params.yaml 一致则跳过（无变化时幂等）
4. 写入 character_params.yaml（flock 加锁，原子写入）
5. 静默退出

## 参数映射规则（参考文档）

### 性格维度（persona.md 中定义）

| 维度 | 含义 | 范围 |
|------|------|------|
| extraversion | 外向性：话多爱分享 vs 安静内敛 | 1–5 |
| empathy | 共情性：情绪敏感 vs 理性冷静 | 1–5 |
| initiative | 主动性：频繁主动触达 vs 等待用户 | 1–5 |
| verbosity | 话量：长段回复 vs 惜字如金 | 1–5 |
| stability | 情绪稳定性：很少起伏 vs 情绪丰富 | 1–5 |
| openness | 开放性：分享想法/脑洞 vs 守口如瓶 | 1–5 |

### 响应模式权重公式（MIN_FLOOR=8 保护，归一化到总和=100）

```
LISTEN  = max(30 + empathy*2 - extraversion,                   8)
SHARE   = max(15 + extraversion*2 + openness*2 - (6-initiative), 8)
OBSERVE = max(25 + stability - empathy,                         8)
SILENCE = max(20 - extraversion*2 + (6-verbosity)*2,            8)
```

### proactive 参数

```
base_prob = 10 + initiative*2     # 范围 12%–20%
max_skip  = 14 - initiative*2     # 范围 4–12（initiative=1→12，initiative=5→4）
```

### 对话参数

```
question_interval = 2 + (empathy-1) // 2   # 范围 2–4（empathy=5→4，empathy=1→2）
```

### life_sim 参数

```
gen_threshold_day   = clamp(40 + (verbosity-3)*10, 20, 80)
gen_threshold_night = 10 + (verbosity-1)*3
log_max_length      = 100 + verbosity*40
```
