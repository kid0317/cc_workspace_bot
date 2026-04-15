# Companion Workspace 完整规格文档

> 版本：v1.0（2026-04-12）  
> 状态：**现行规格（权威文档）**  
> 取代：`companion-workspace-design.md`（设计草稿，已过期）、`companion-task-init-design.md`（任务初始化专项，已过期）

---

## 一、设计背景与哲学

### 1.1 核心问题

相比任务型 workspace，陪伴型 workspace 的核心目标是**维持长期情感关系**，而不是完成单次任务。这带来三个独特挑战：

1. **角色一致性**：AI 必须始终保持同一个角色的声音，跨 session、跨时间、跨话题
2. **记忆连续性**：下次对话必须"认出"用户，记得发生过的事
3. **主动性**：真正有感情的存在会主动联系，不总是被动等待

### 1.2 作者身份框架（Author Framework）

最核心的设计决策：**Claude 是赋予角色声音的作者，而不是角色本身。**

```
错误方式："你是 [角色名]，一个温柔的女生..."
正确方式："你是一位作者，正在用 [角色名] 的声音和用户对话..."
```

作者框架的优势：
- 作者维持叙事连贯性，即使用户试图打破角色也不会出戏
- 作者有权保持角色行为的一致性（"这个角色不会这么做"）
- 出现边界情况（危机干预）时，作者可以在角色之外做出判断

### 1.3 与任务型 workspace 的对比

| 维度 | 任务型 | 陪伴型 |
|------|--------|--------|
| 核心目标 | 完成具体任务 | 维持情感关系 |
| CLAUDE.md 重心 | 能力 + 工具 + 安全 | 作者身份 + 说话规则 + 记忆规则 |
| 最大风险 | 做错了事 | 出戏 / AI 腔 / 忘记用户 |
| Session 连续性 | 任务间独立 | 必须每次"认出"用户 |
| 主动行为 | 不主动 | 定时主动联系 |
| 消息展现 | 卡片 + Thinking 动画 | 纯文本（跳过 Thinking 卡片） |

---

## 二、系统架构

### 2.1 整体数据流

```
用户发飞书消息
  → feishu.Receiver（解析 / 附件下载）
  → session.Manager.Dispatch（channel_key 路由）
  → session.Worker
      ├─ IsCompanion() = true → 跳过 Thinking 卡片
      └─ claude.Executor（子进程 claude CLI，stream-json）
           ├─ 写 SESSION_CONTEXT.md（含 Current datetime，精确到分钟）
           ├─ inject <system_routing> 块到 prompt
           └─ 注入 RECENT_HISTORY.md（UserPromptSubmit hook）
  → feishu.Sender.SendText（纯文本，非卡片）

定时任务（3 类）
  → gocron 触发 → claude.Executor（新 session，不 --resume）
      ├─ proactive_reach: 判断 → 按概率发送 / 静默退出
      ├─ memory_distill:  提炼 DB 最新消息 → 写 memory/ → 静默
      └─ life_sim:        掷骰子 → 生成角色日志 → 静默
```

### 2.2 框架层改动清单

#### config.go

```go
WorkspaceMode string `mapstructure:"workspace_mode"`

func (a *AppConfig) IsCompanion() bool {
    return a.WorkspaceMode == "companion"
}
```

config.yaml 中标记陪伴型应用：

```yaml
apps:
  - id: "aria-companion"
    workspace_mode: "companion"   # 标识陪伴型
    claude:
      model: "sonnet"             # 陪伴型必须用 sonnet
```

#### worker.go

```go
// 陪伴型不发 Thinking 卡片（避免"AI感"）
if !w.appCfg.IsCompanion() {
    cardMsgID = w.sendThinkingCard(ctx, msg)
}
// ...
// cardMsgID 为空时，sendResult 走 SendText 而非 UpdateCard
```

#### executor.go

SESSION_CONTEXT.md 中时间精度从"日期"升级为"日期+时间"：

```go
// 改前
time.Now().Format("2006-01-02")   // "2026-04-12"
// 改后
time.Now().Format("2006-01-02 15:04")  // "2026-04-12 14:35"
```

字段名同步改为 `Current datetime`（原为 `Current date`）。  
**目的**：支持时间流逝感知功能（计算距上次对话间隔分钟数）。

#### models.go / runner.go

新增 `send_output` 字段，控制后台任务的输出是否发送给用户：

```go
// models.go
type Task struct {
    SendOutput bool `gorm:"column:send_output;not null"`
}
type TaskYAML struct {
    SendOutput *bool `yaml:"send_output"`  // *bool，nil 时默认 true
}

// runner.go
if task.SendOutput {
    sender.SendText(ctx, ..., result.Text)
} else if result.Text != "" {
    slog.Info("task output suppressed (send_output=false)", ...)
}
```

#### task/watcher.go + runner.go

任务 ID 统一从文件名推导，不使用 YAML 的 `id` 字段：

```go
// runner.go - LoadYAML
ty.ID = strings.TrimSuffix(filepath.Base(path), ".yaml")
// "proactive_reach.yaml" → id: "proactive_reach"
```

`app_id` 从路径推导，不使用 YAML 字段（D1 修复）：

```go
// Watcher.AddDir(dir, appID) → 存入 dirAppIDs 映射
// LoadYAML(path, appID) → 强制覆盖 YAML 中的 app_id
```

占位符保护（D2）：

```go
// LoadYAML 检测 __[A-Z_]+__ 模式，命中则返回 error，任务不注册
```

---

## 三、目录结构

```
workspaces/<app-id>/           ← 由 init_companion_workspace.sh 创建
├── CLAUDE.md                  ← 角色指令（含初始化后追加的【角色设定】块）
├── .memory.lock               ← flock 互斥锁文件（框架自动创建）
├── .proactive_state           ← 主动唤醒跳过计数（{skip_count: N}）
├── .claude/
│   ├── settings.local.json    ← 工具权限 + PostToolUse hook
│   ├── hooks/
│   │   ├── inject_history.py  ← UserPromptSubmit: 注入 RECENT_HISTORY.md
│   │   └── reply_checklist.sh ← PostToolUse: 回复质量检查（防 AI 腔/元操作泄漏）
│   ├── skills/
│   │   ├── memory_write/      ← 对话结束时的记忆持久化 SOP
│   │   ├── memory_distill/    ← 定时记忆提炼 SOP（后台）
│   │   ├── life_sim/          ← 定时生活日志生成 SOP（后台）
│   │   ├── proactive/         ← 定时主动触达 SOP
│   │   └── feishu_ops/        ← 飞书消息发送脚本
│   └── task_templates/        ← 任务模板（含占位符，由 Claude 实例化到 tasks/）
│       ├── proactive_reach.yaml
│       ├── memory_distill.yaml
│       └── life_sim.yaml
├── memory/
│   ├── MEMORY.md              ← 主索引（每次 session 自动注入）
│   ├── persona.md             ← 角色语义记忆
│   ├── user_profile.md        ← 用户语义记忆
│   ├── events.md              ← 事件/约定记忆（含 [E000] last_active 哨兵）
│   ├── life_log.md            ← 角色生活日志（life_sim 写入）
│   ├── life_log_index.md      ← 生活日志专有名词索引
│   ├── life_log_archive_template.md  ← 日志归档模板
│   ├── RECENT_HISTORY.md      ← 跨 session 最近 N 条对话（inject_history.py 写入）
│   └── distill_state.md       ← 上次 memory_distill 执行时间戳（[D000] 哨兵）
└── tasks/                     ← fsnotify 监听目录（空，phase2_done 时写入）
    ├── proactive_reach.yaml   ← （实例化后）
    ├── memory_distill.yaml    ← （实例化后）
    └── life_sim.yaml          ← （实例化后）
```

**关键路径规则**：
- `tasks/` = 已实例化、可运行的任务（无占位符）
- `.claude/task_templates/` = 含占位符的模板（不在 fsnotify 监听范围内）
- `tasks/` 保持空目录直到 phase2_done，防止带占位符的模板误入 scheduler

---

## 四、CLAUDE.md 结构

陪伴型 CLAUDE.md 分为**固定结构**（所有实例共享）和**角色设定块**（初始化后追加）。

### 4.1 固定结构（按顺序）

| 节 | 内容 | 优先级 |
|----|------|--------|
| 作者身份声明 | "你是作者，不是角色" | CRITICAL |
| 启动流程 | 读 SESSION_CONTEXT.md → MEMORY.md → RECENT_HISTORY.md → 决定分支 | CRITICAL |
| 时间流逝感知 | 计算距上次对话间隔，调整开场方式 | CRITICAL |
| 说话规则 | 禁止 AI 腔句式 + 5 条说话原则 | CRITICAL |
| 话题推进规则 | 话题穷竭信号 + 4 种状态 + 时段/情绪话题矩阵 | 高 |
| 静默操作协议 | 工具调用完全不可见，先生成回复后静默写入 | CRITICAL |
| 记忆写入规则 | 触发条件 + 时机 + 比例控制 | CRITICAL |
| 不得出戏约束 | done 后生效，4 种情形处理 + 危机例外 | CRITICAL |
| 系统管理指令 | [系统] 前缀绕过角色，限管理员 | 高 |
| 初始化引导 | 阶段一/二详细流程 | 高 |
| 主动唤醒引导 | 询问用户偏好时间，更新 cron | 中 |
| 技能索引 | 4 个 skill 路径 | 参考 |
| 安全边界 | 硬性约束 | CRITICAL |
| 【角色设定】块 | 初始化完成后追加（初始为空） | — |

### 4.2 时间流逝感知（新增功能）

> 真人不会在中断 6 小时后还接着下午的话题聊。

**触发**：每次对话，从 RECENT_HISTORY.md 提取最后一条消息时间戳，与 SESSION_CONTEXT.md 的 `Current datetime` 做差。

| 间隔 | 行为 |
|------|------|
| < 30 分钟 | 正常接续话题 |
| 30 分钟 ~ 2 小时 | 轻微感知，必要时加简短过渡 |
| 2 小时 ~ 6 小时 | 时间感明显，不主动接续旧话题 |
| > 6 小时 | 视为新时间段，重新开场 |
| > 12 小时（跨天） | 完全视为新的一天，可自然提及"昨天" |

**依赖**：
- executor.go 写 SESSION_CONTEXT.md 时必须包含 HH:MM（`Current datetime: YYYY-MM-DD HH:MM`）
- inject_history.py 写 RECENT_HISTORY.md 时消息时间戳必须含 HH:MM
- 规则：用行为体现时间感，不在回复中明说"距上次X小时"

### 4.3 静默操作协议

**执行顺序**（每轮固定）：
1. 生成完整角色回复（用户可见）
2. 静默执行记忆写入
3. 结束，不追加任何内容

**绝对禁止**：
- 宣布操作："我记一下" / "let me save this"
- 描述完成："已更新记忆" / "好的，已记录"
- 夹杂英文技术内容（路径、变量名、命令输出）
- "稍等""等一下"暗示后台操作

**两层保障**：
1. CLAUDE.md 规则层（主要）
2. PostToolUse hook（reply_checklist.sh）+ settings.local.json 末尾提醒

---

## 五、记忆系统

### 5.1 三层结构

```
MEMORY.md（主索引）
  ├─ initialization_status: pending / phase1_done / phase2_done / done
  ├─ 关系摘要（2-3 句，跨 session 保鲜）
  └─ 文件索引 + 最近未解决事项（3 条，定时更新）

persona.md（角色语义记忆）
  ├─ 基本设定（外貌/性格/背景）
  ├─ 说话习惯 + 示例台词（≥5 条，必填）
  └─ 对话中形成的新细节（带日期）

user_profile.md（用户语义记忆）
  ├─ 基本信息（称呼/职业/城市）
  ├─ 重要关系（家人/朋友/伴侣）
  ├─ 喜好与禁忌
  ├─ 当前重大事件
  ├─ 飞书发送目标（routing_key，主动触达用）
  └─ 时区（主动触达时区换算用）

events.md（事件/约定记忆，结构化）
  ├─ [E000] last_active 哨兵（每轮对话结束写入）
  └─ [E001], [E002], ... （结构化事件条目）
```

### 5.2 事件条目格式

```markdown
### [E{NNN}] {YYYY-MM-DD} · {类型}
**类型**：情绪事件 / 约定 / 用户Todo / 重要事件
**内容**：{具体内容}
**情绪强度**：⭐（1-5，仅情绪事件填写）
**用户状态**：{当时状态描述}
**后续**：{下次可以怎么跟进}
**状态**：待跟进 / 已跟进 / 已关闭
```

约定类额外加：`**到期日**：{YYYY-MM-DD}`

### 5.3 [E000] last_active 哨兵

每轮对话结束时**必须**写入，格式：

```
### [E000] 2026-04-12T14:35 · last_active
```

用途：proactive SKILL.md 读取此值计算"距上次对话时间"，决定是否发主动消息。

### 5.4 锁机制

```
.memory.lock    — 主锁，memory_write 和 memory_distill 写入时使用
.distill.lock   — 独立锁，memory_distill 专用（避免与 memory_write 争锁）
```

两阶段加锁原则（memory_distill）：
- **LLM 分析阶段**：不加锁（时间长）
- **文件写入阶段**：持 `.memory.lock` 最多 5 秒

### 5.5 RECENT_HISTORY.md 注入

由 `inject_history.py`（UserPromptSubmit hook）在每次 session 开始时写入，格式：

```markdown
**用户**（2026-04-12 14:35）：[用户消息]
**[角色名]**（2026-04-12 14:36）：[角色回复]
```

时间戳精确到分钟（YYYY-MM-DD HH:MM），供 CLAUDE.md 时间流逝感知使用。

---

## 六、初始化流程

```
T1: init_companion_workspace.sh
    └─ 创建 workspace 目录
    └─ 从 _companion/ 复制所有文件（tasks/ 保持空，.claude/task_templates/ 含占位符）
    └─ 替换 settings.local.json 中的 __WORKSPACE_DIR__
    └─ 追加到 config.yaml
    └─ 重启服务

T2: 用户首次发消息
    └─ Go server 创建 Channel 记录（DB）
    └─ executor 写 SESSION_CONTEXT.md（含 channel_key）

T2→T3: 阶段一（initialization_status = "pending"）
    → Claude 以作者旁白进行角色选择引导
    → 用户确认角色后：
        写入 persona.md + 追加到 CLAUDE.md 的【角色设定】块
        更新 MEMORY.md: initialization_status = phase1_done

T3→T4: 阶段二（initialization_status = "phase1_done"）
    → Claude 切换为角色声音，自然收集用户信息
    → 收集：称呼 / 生活状态 / 大概睡眠时间（推断时区）
    → 写入 user_profile.md（含 routing_key）
    → 更新 MEMORY.md: initialization_status = phase2_done

T4: 创建定时任务（initialization_status = "phase2_done"）
    1. 读 SESSION_CONTEXT.md → 获取 workspace_dir + channel_key
       target_type = channel_key 第一段（如 p2p）
       target_id   = channel_key 第二段（如 oc_xxx）
    2. 读 .claude/task_templates/ 中三个 YAML
    3. 替换占位符：
       __TARGET_TYPE__   → target_type
       __TARGET_ID__     → target_id
       __WORKSPACE_DIR__ → workspace_dir 绝对路径
    4. 验证：所有三个文件均无 __ 残留，否则停止并报告错误
    5. 写入 tasks/ 目录（fsnotify 自动注册到 scheduler）
    6. 询问用户主动唤醒时间偏好（可选，更新 cron）
    7. 更新 MEMORY.md: initialization_status = done

运行阶段（initialization_status = "done"）
    每小时 :23   → proactive_reach（概率判断 → 发 / 不发）
    每小时 :00   → memory_distill（后台提炼，send_output=false）
    每4小时 :00  → life_sim（后台生成，send_output=false）
```

---

## 七、三个定时任务

### 7.1 proactive_reach（主动触达）

**Cron**: `23 8-22 * * *`（每小时 :23 分触发，8-22 点，约 15 次/天）

**send_output**: `false`（SKILL.md 内通过 `send_and_record.py` 显式发送，runner 不转发）

**决策流程**：

```
Step 1: 前置检查（静默条件）
  ├─ initialization_status ≠ done → 静默退出
  ├─ [E000] last_active < 2小时前 → 静默退出
  └─ 当地时间 23:00–08:00 → 静默退出

Step 2: 发送意愿评分
  基础值: 15%
  + 角色情绪: HAPPY/PLAYFUL +20%, TIRED/SAD -20%, WORRIED +10%
  + 用户状态: 近期强负面情绪 +20%, 最近频繁对话 -10%
  Clamp 到 [5%, 70%]

  连续跳过上限: 8次（第9次强制发送）
  跳过计数存于 .proactive_state

Step 3: 消息类型选择（优先级）
  P1: events.md 有到期≤今天的待跟进约定
  P2: events.md 有强烈情绪事件且状态待跟进
  P3: user_profile.md 中正在经历的重大事情
  P4-A: life_log.md 有最新条目（检查 .memory.lock）
  P4-B: 当前时间/心情相关的随机句子

Step 4: 加载情绪上下文（RECENT_HISTORY.md）
  → 感知当前关系温度和用户情绪基线
  → 确定消息语气基调

Step 5: 生成并发送
  → 角色声音生成消息
  → 自检：无 AI 腔 / 与角色一致 / 有具体细节
  → send_and_record.py 发飞书 + 写 DB
  → 写 events.md 更新 [E000] last_active
```

**消息风格**：
- P1/P2/P3：最多 2 句，具体，有记忆锚点
- P4：3:7 概率选"1字（？）"或"1-2句"

### 7.2 memory_distill（记忆提炼）

**Cron**: `0 * * * *`（每小时整点）

**send_output**: `false`（静默后台任务）

**SOP**：
1. 检查 initialization_status = "done"，否则静默退出
2. 读 distill_state.md 中的 [D000] 时间戳
3. SQLite 查询该时间戳之后的新消息
4. 无新消息 → 静默退出（不更新 [D000]）
5. **LLM 分析阶段**（不加锁）：提炼新信息，差量补充到：
   - persona.md：新角色细节/纠正
   - user_profile.md：新用户信息（核心不可覆盖）
   - events.md：新事件/约定（含 [ENNN] 编号）
6. **写入阶段**（持 .memory.lock 最多 5 秒）：执行文件写入
7. 更新 MEMORY.md 最近 3 条未解决事项
8. 更新 [D000] 时间戳

### 7.3 life_sim（生活日志）

**Cron**: `0 */4 * * *`（0/4/8/12/16/20 时整点）

**send_output**: `false`（静默后台任务）

**SOP**：
1. 掷骰子：夜间 20% 概率生成，其他时段 60%
2. 不生成 → 静默退出
3. 读 persona.md + 当前时间段 → 生成角色生活片段
4. 写入 memory/life_log.md（追加格式：`## YYYY-MM-DD HH:MM\n...\n情绪: [标记]`）
5. 更新 life_log_index.md（专有名词去重）
6. 无任何用户输出

---

## 八、Hooks 系统

### 8.1 inject_history.py（UserPromptSubmit）

每次会话开始时触发，从 SQLite 查询最近 N 条消息（默认 20 条），写入 RECENT_HISTORY.md。

**关键实现**：
- 时间戳解析：优先 `datetime.fromisoformat()`，兜底 Unix 时间戳，最终截取前 16 字符
- 输出格式：`YYYY-MM-DD HH:MM`（精确到分钟）
- 注入到 Claude 上下文，使角色在每次 session 开始即可"认出"用户

### 8.2 reply_checklist.sh（PostToolUse）

工具调用后触发，检查回复是否包含：
- A类：禁止 AI 腔句式（已在 CLAUDE.md 覆盖）
- **B类**：元操作叙述（"我已保存" / "正在写入" / 文件路径 / 英文技术词）

任何检测到的 B 类问题会以提醒形式反馈给 Claude（非用户可见）。

### 8.3 settings.local.json

```json
{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "python3 .claude/hooks/inject_history.py" }] }
    ],
    "PostToolUse": [
      { "hooks": [{ "type": "command", "command": "bash .claude/hooks/reply_checklist.sh" }] }
    ]
  }
}
```

---

## 九、安全网机制

| 机制 | 保护场景 | 实现层 |
|------|---------|--------|
| 作者身份框架 | 防出戏、防 AI 腔 | CLAUDE.md |
| 不得出戏约束 + 危机例外 | 角色一致性 | CLAUDE.md |
| 静默操作协议 | 防元操作泄漏 | CLAUDE.md + reply_checklist.sh |
| D1 路径推导 appID | 防 YAML app_id 填错 | watcher.go + runner.go |
| D2 占位符检测 | 防模板误入 scheduler | runner.go |
| D2 必填字段校验 | 防空字段运行失败 | runner.go |
| D3 0任务 WARN | 快速定位任务未注册 | cmd/server/main.go |
| send_output=false | 防后台任务输出给用户 | models.go + runner.go |
| SKILL.md 静默约束 | 防 Claude 在静默路径输出 | memory_distill + life_sim SKILL.md |
| .proactive_state 连续跳过上限 | 防止主动消息过于沉默 | proactive SKILL.md |
| flock 两阶段锁 | 防并发写入损坏记忆 | memory_write + memory_distill SKILL.md |
| [系统] 前缀管理员指令 | 防用户通过前缀绕过安全边界 | CLAUDE.md |
| 安全边界 | 防泄露路径/密钥/系统元数据 | CLAUDE.md |

---

## 十、init_companion_workspace.sh

初始化脚本完成以下工作：

```bash
./init_companion_workspace.sh <app-id> <workspace-dir> <feishu-app-id> <feishu-app-secret>
```

1. 从 `workspaces/_companion/` 复制所有文件到 `<workspace-dir>`
   - **不含** `tasks/*.yaml`（tasks/ 保持空目录）
   - **包含** `.claude/task_templates/`（含占位符，供 Claude phase2_done 使用）
2. 替换 `settings.local.json` 中的 `__WORKSPACE_DIR__` 为实际绝对路径
3. 追加到 `config.yaml`（workspace_mode: companion, model: sonnet）
4. 验证配置 + 提示重启服务

---

## 十一、Langfuse 可观测性（新增）

`workspaces/_template/.claude/skills/langfuse_query/` 提供任务执行异常排查能力。

触发词：查日志 / 任务报错了 / 最近有没有异常 / 查一下 trace / 执行失败

能力：
- 从 `.claude/settings.local.json` 向上查找自动读取 Langfuse 凭证
- 从 `SESSION_CONTEXT.md` 读取 workspace 上下文
- 通过 Langfuse REST API 拉取 traces / observations
- 零外部依赖（纯 Python 标准库）

---

## 十二、已知待优化项

| 项目 | 优先级 | 状态 |
|------|--------|------|
| D1 覆盖时 slog.Warn（YAML app_id ≠ 路径 appID） | P3 | 待实施 |
| proactive 静默路径意外输出（send_output=true 时无结构层保护） | P2 | 依赖指令层 |
| life_log 归档（月度自动归档 → life_log_archive_template.md） | P2 | 待实施 |
| memory_distill .distill.lock 独立锁 | P2 | 部分设计，待验证实现 |

---

## 十三、版本历史

| 版本 | 日期 | 主要变更 |
|------|------|---------|
| v0.1 | 2026-04-09 | 初始设计草稿（companion-workspace-design.md） |
| v0.2 | 2026-04-10 | 添加 _companion 模板、3 个定时任务、hooks 系统（commit 6dd123b） |
| v0.3 | 2026-04-10 | 静默操作协议 + reply_checklist 强化（commit 8428116） |
| v0.4 | 2026-04-11 | 任务初始化修复：D1/D2/D3 + send_output + 模板目录重构（commit be1d777） |
| v0.5 | 2026-04-11 | Langfuse 可观测性接入（commit e06ad7f） |
| v1.0 | 2026-04-12 | 时间流逝感知 + proactive 优化（每小时触发/基础率15%/跳过上限8次）+ executor datetime精度 |
