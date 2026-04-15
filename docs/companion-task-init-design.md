# 陪伴型 Workspace 定时任务初始化设计

> ⚠️ **已过期（DEPRECATED）**  
> 本文档为任务初始化专项设计文档（v1.0–v1.2），其中的设计方案已全部实施完成。  
> 当前权威文档为 [`companion-workspace-spec.md`](./companion-workspace-spec.md)。  
> 本文档保留供历史参考（根因分析 + 实施决策过程）。
>
> ---
>
> 版本：v1.2（2026-04-11 二轮架构师 Review 修复：upsertTask 缺字段 + CLAUDE.md 模板实例化 + `__WORKSPACE_DIR__` 占位符未替换）  
> v1.1：补充 review 发现 + 后台任务消息泄漏问题  
> v1.0：诊断 xh_yibu 从未收到主动消息，暴露的系统性设计问题

---

## 一、问题背景

### 1.1 原始问题

**现象**：xh_yibu 陪伴机器人自部署以来从未发出过任何主动消息。

**诊断结果**（服务器日志）：
```
restored tasks app_id=xh_yibu count=0
```

三个 task YAML 文件存在于 workspace，但没有任何任务被注册进 scheduler。

### 1.2 附加问题（v1.1 新增）

**现象**：
- memory_distill / life_sim 执行后向用户发了"记忆整理完成"之类的后台消息，严重打断沉浸感
- proactive_reach 决定"本次不发"时，向用户发出了"当前静默"的说明，非常出戏

---

## 二、根因分析

### 根因 1：`app_id` 字段语义歧义（已修复 D1）

Task YAML 文件里有 `app_id` 字段，init 脚本用 `__FEISHU_APP_ID__` 占位符替换，填入飞书 App ID（`cli_a95d2eb715391bdf`）。

但 `restoreEnabledTasks()` 和 `runner.go` 中 `appRegistry` 的查找键是 workspace ID（`xh_yibu`），两者不一致，导致 DB 里的任务永远查不到。

**本质**：`AppConfig.ID`（workspace ID）和 `AppConfig.FeishuAppID`（飞书 App ID）是两个不同字段，字段名 `app_id` 存在歧义，init 脚本做了"正确的错误替换"。

### 根因 2：`target_type`/`target_id` 占位符从未被填写（设计时序问题）

Task YAML 模板有 `__TARGET_TYPE__` 和 `__TARGET_ID__` 占位符，设计意图是由 Claude 在阶段二完成后填写。

**时序问题**：
```
T1: init 脚本运行
    └─ 用户 Feishu ID 此时不存在，无法填写

T2: 用户首次发消息
    └─ 此时才有 routing_key / channel_key

T3: 阶段二完成
    └─ CLAUDE.md 指令模糊：只提了 proactive_reach，未说怎么拿 target 值
    └─ Claude 没有收到明确指令，占位符从未被替换
```

**本质**：是设计时序问题，不是模型能力问题。模型在该执行操作的时间点没有正确的信息和指令。

### 根因 3：0 任务启动无可见信号（已修复 D3）

`restoreEnabledTasks` 返回 0 时以 INFO 级别记录，淹没在启动日志里，运维无感知。

### 根因 4：runner 不区分任务类型，所有输出都发给用户（v1.1 新增）

**代码位置**：`internal/task/runner.go`

```go
if result.Text != "" {
    receiveID, receiveType := receiveTarget(task.TargetType, task.TargetID)
    sender.SendText(ctx, receiveID, receiveType, result.Text)
}
```

runner 设计假设：所有任务执行后的文字输出都应发给用户。对 `proactive_reach` 成立，对 `memory_distill`（后台提炼）和 `life_sim`（后台日志生成）根本不成立。

**两个次级原因**：

1. **SKILL.md 的"静默退出"约束的是 bash 脚本，不约束 Claude 的文字输出**  
   `exit 0` 是 bash 层面的退出，Claude 在执行脚本前已经可能输出了执行摘要。runner 拿到这段文字就发给了用户。

2. **proactive 决定"不发"时的文字输出同样被发出**  
   proactive SKILL.md 里有多个 `exit 0` 路径表示"本次静默"，但 Claude 可能先输出"当前距上次对话不足4小时，静默"，这段说明被 runner 当消息发出。

### 根因 5：`removeTask` 用文件名查 DB，与实际存储的 task ID 不一致（v1.1 新增）

`watcher.go` 的 Remove/Rename 分支：
```go
base := filepath.Base(event.Name) // "proactive_reach.yaml"
id := strings.TrimSuffix(base, ".yaml") // "proactive_reach"
w.removeTask(id) // 按此 id 查 DB
```

但 DB 里的 `task.ID` 来自 YAML 的 `id` 字段，模板写的是 `__APP_ID__-proactive`，替换后为 `xh_yibu-proactive`。两者不一致，删除文件时 scheduler 里的 job 永远无法被正确移除。

---

## 三、已实施的代码改动

### D1：路径推导 workspace ID（✅ 已完成）

**文件**：`internal/task/watcher.go`、`internal/task/runner.go`、`internal/model/models.go`、`cmd/server/main.go`

**变更**：
- `Watcher` 新增 `dirAppIDs map[string]string`（`tasks/ 目录 → workspace appID` 映射）
- `AddDir(dir)` → `AddDir(dir, appID)`
- `LoadYAML(path)` → `LoadYAML(path, appID)`，强制用路径推导的 appID 覆盖 YAML 的 `app_id` 字段
- `TaskYAML.AppID` 标记 Deprecated

**效果**：彻底消除"YAML 里的 app_id 与 workspace ID 不一致"这一类 bug。

### D2：占位符检测 + 必填字段校验（✅ 已完成）

**文件**：`internal/task/runner.go`

**变更**：
- `LoadYAML` 对 `target_type`、`target_id`、`prompt` 检测 `__[A-Z_]+__` 占位符，命中则返回 ERROR
- `LoadYAML` 对 `target_type`、`target_id`、`cron`、`prompt` 进行非空校验

**注意**：D2 对所有任务生效，不区分 `enabled` 状态。模板文件如果落入被监听的 `tasks/` 目录，D2 会拒绝它，任务不进 DB。这正是设计意图（见第四节）。

### D3：0 任务启动升级为 WARN（✅ 已完成）

**文件**：`cmd/server/main.go`

**变更**：
- `restoreEnabledTasks` 新增 `tasksDir` 参数
- `count == 0` 时从 `slog.Info` 升级为 `slog.Warn`，同时输出 `tasks_dir`

---

## 四、待实施的设计方案

### 4.0 优先级与实施顺序（v1.1 修正）

**原文档建议**的顺序：D1 日志 → 模板重构 → init 脚本 → CLAUDE.md

**正确顺序**（由依赖关系决定）：

```
第一优先级（原子完成，不可拆分）
├── 模板目录重构（4.1）
└── init 脚本更新（4.2）

第二优先级
├── removeTask id 统一（4.5，独立 bug）
└── send_output 字段 + SKILL.md 更新（4.6）

第三优先级
└── CLAUDE.md phase2_done 指令精确化（4.3）

第四优先级
└── D1 覆盖时 slog.Warn（4.4，低风险）
```

> 注意：模板目录重构和 init 脚本必须同一 commit 完成。若先重构目录，init 脚本找不到模板文件会失败；若先改 init 脚本，此时模板还在 tasks/ 里，新实例仍会把带占位符的文件复制到被监听目录。

### 4.1 任务模板目录重构

**新目录结构**：

```
workspaces/_companion/
├── tasks/                          ← 空（实例化后才有文件）
└── .claude/
    └── task_templates/             ← 模板存放位置（init 脚本复制此目录）
        ├── proactive_reach.yaml
        ├── life_sim.yaml
        └── memory_distill.yaml
```

实例 workspace：
```
/root/xh_yibu/
├── tasks/                          ← fsnotify 监听，只放完整可用的文件
└── .claude/
    └── task_templates/             ← init 脚本复制（含占位符），Claude 阶段二读取
```

**fsnotify 仅监听 `tasks/`**，`.claude/task_templates/` 不在监听范围内（无需代码改动）。

**模板文件变更**：
- 移除 `app_id` 字段（D1 后忽略）
- 移除 `id: __APP_ID__-proactive` 格式（见 4.5 统一为文件名）
- 保留 `target_type: __TARGET_TYPE__` 和 `target_id: __TARGET_ID__`
- memory_distill / life_sim 新增 `send_output: false`（见 4.6）

**可维护性**：
- 模板是唯一权威来源，可 git diff / review
- 修改模板 → 所有新建实例生效
- 存量实例：指令 Claude "重新从 `.claude/task_templates/` 实例化到 `tasks/`"

### 4.2 init_companion_workspace.sh 更新

**当前**：把 `_companion/` 下所有文件（含 `tasks/*.yaml`）复制到实例，包括被监听目录

**改为**：
- 在复制文件前，将 `tasks/*.yaml` 文件**排除**在外（或从 4.1 的模板目录重构后自然消失）
- 复制 `.claude/task_templates/` 到实例（含占位符，供 Claude 阶段二使用）
- `tasks/` 保持空目录

### 4.3 CLAUDE.md phase2_done 指令精确化

**当前指令（错误）**：只提创建 `tasks/proactive_reach.yaml` 一个文件。

**新指令（精确）**：

```markdown
## phase2_done → done 流程

1. 读取当前会话目录下的 SESSION_CONTEXT.md：
   - 找到 `Channel key` 字段，格式为 `type:id:app_id`
   - target_type = 第一段（如 `p2p`）
   - target_id = 第二段（如 `oc_98f5f925b82266ee81716ed91b519917`）

2. 读取 {workspace_dir}/.claude/task_templates/ 目录下所有 yaml 文件。
   对每个文件：将 __TARGET_TYPE__ 替换为 target_type，__TARGET_ID__ 替换为 target_id。
   写入 {workspace_dir}/tasks/ 目录（同名文件）。

3. 验证：tasks/ 下有 3 个 yaml 文件，且无 __ 开头的占位符残留。
   如有残留 → 停止，向用户（管理员）报告："初始化任务配置失败，请检查 SESSION_CONTEXT.md"。

4. 更新 MEMORY.md 的 initialization_status 为 done。

5. 以角色身份继续对话。
```

**数据来源统一**：唯一使用 `SESSION_CONTEXT.md` 的 `Channel key` 字段（Go server 写入，格式稳定）。废弃 user_profile.md routing_key 作为数据来源（可保留用于运行时备用，但不作为初始化数据源）。

### 4.4 D1 覆盖时加 slog.Warn（低优先级）

当 YAML 里的 `app_id` 与路径推导的 appID 不一致时，记录警告，便于发现文件被跨 workspace 移动的场景：

```go
// runner.go - LoadYAML 内
if ty.AppID != "" && ty.AppID != appID {
    slog.Warn("task yaml app_id overridden by workspace path",
        "yaml_app_id", ty.AppID, "effective_app_id", appID, "file", path)
}
```

### 4.5 统一 task ID 为文件名（修复 removeTask bug）

**问题**：`removeTask` 用文件名（`proactive_reach`）查 DB，但 DB 里存的是 YAML `id` 字段的值（`xh_yibu-proactive`），永远找不到。

**修复方案**：`LoadYAML` 中 task ID 始终从文件名派生，不使用 YAML 里的 `id` 字段：

```go
// runner.go - LoadYAML
// Task ID 统一用文件名（去掉 .yaml），与 watcher.go 的 removeTask 保持一致
ty.ID = strings.TrimSuffix(filepath.Base(path), ".yaml")
```

这样：
- `proactive_reach.yaml` → `id: proactive_reach`
- `life_sim.yaml` → `id: life_sim`
- `memory_distill.yaml` → `id: memory_distill`

同时从模板文件中移除 `id: __APP_ID__-proactive` 等字段（该字段不再被读取）。

### 4.6 send_output 字段：后台任务不发飞书消息（v1.1 新增）

**设计原则**：任务类型分两类：
- **用户可见任务**（`send_output: true`，默认）：执行结果发给用户
- **后台静默任务**（`send_output: false`）：执行结果仅写日志，不发飞书

**模型变更**：

```go
// internal/model/models.go

type Task struct {
    // ...
    SendOutput bool `gorm:"default:true"` // 是否将 Claude 输出发给用户
}

type TaskYAML struct {
    // ...
    SendOutput bool `yaml:"send_output"` // false = 后台静默任务
}
```

**runner.go 变更**：

```go
if task.SendOutput && result.Text != "" {
    receiveID, receiveType := receiveTarget(task.TargetType, task.TargetID)
    if _, err := sender.SendText(ctx, receiveID, receiveType, result.Text); err != nil {
        slog.Error("task runner: send text", "err", err)
    }
} else if !task.SendOutput && result.Text != "" {
    slog.Info("task output suppressed (send_output=false)",
        "task_id", task.ID, "text_len", len(result.Text))
}
```

**模板文件更新**：

```yaml
# memory_distill.yaml
send_output: false

# life_sim.yaml
send_output: false

# proactive_reach.yaml
# send_output 省略（默认 true）
```

**SKILL.md 指令层补充**（与结构层叠加，防御深度）：

memory_distill/SKILL.md 和 life_sim/SKILL.md 顶部加：
```
> ⚠️ CRITICAL（后台静默任务）：执行完成后，Claude 的最终输出必须为空字符串。
> 禁止输出任何执行摘要、完成说明、步骤描述。所有操作结果写入文件，不输出到回复。
```

proactive/SKILL.md 所有 `exit 0` 静默路径前加：
```
# 当选择静默时，Claude 回复内容必须完全为空——不得输出"本次静默"或任何说明。
```

**两层保障效果**：

| 场景 | 结构层 | 指令层 | 用户感知 |
|------|--------|--------|---------|
| memory_distill 正常 | send_output=false 拦截 | 禁止输出 | 无 ✅ |
| memory_distill Claude 意外输出 | send_output=false 拦截 | 失效 | 无 ✅（日志记录）|
| proactive 发消息 | send_output=true 通过 | 正常输出 | 收到消息 ✅ |
| proactive 静默且无输出 | send_output=true 通过 | 输出为空 | 无 ✅ |
| proactive 静默但意外输出 | 无法拦截 | 指令层约束 | ⚠️ 依赖指令层 |

---

## 五、整体初始化架构图

```
T1：init 脚本运行（用户 ID 未知）
    ↓
复制 .claude/task_templates/ 到实例（含占位符）
tasks/ 保持空目录

T2：用户首次发消息
    ↓
Go server 建立 Channel 记录（DB）
SESSION_CONTEXT.md 写入 channel_key

T2-T3：阶段一（确立角色人设）
T3-T4：阶段二（收集用户信息）
    ↓ initialization_status = phase2_done

T4：阶段二完成（channel_key 已知）
    ↓
Claude 读 SESSION_CONTEXT.md → 解析 channel_key（target_type / target_id）
Claude 读 .claude/task_templates/*.yaml → 实例化 → 写入 tasks/
    ↓
fsnotify watcher 检测 tasks/ 变化
LoadYAML(path, appID)       [D1: appID 从路径推导]
D2: 占位符检测 + 必填字段校验
    ↓ 通过
upsertTask → scheduler.Add
initialization_status = done
定时任务开始运行 ✅

运行阶段：
09:00/21:00  → proactive_reach（判断是否主动联系用户）
每小时       → memory_distill（后台提炼，send_output=false）
每4小时      → life_sim（后台生成，send_output=false）
```

---

## 六、安全网机制

| 机制 | 覆盖场景 |
|------|---------|
| D1：路径推导 appID | 防止 YAML 里 app_id 填错 |
| D2：占位符检测 | 防止未实例化模板进 scheduler |
| D2：必填字段校验 | 防止空字段导致执行失败 |
| D3：0 任务 WARN | 快速定位任务未注册 |
| send_output=false | 防止后台任务输出发给用户 |
| SKILL.md 指令层 | 防止 Claude 在静默路径输出文字 |
| D1 覆盖日志（待加）| 发现文件跨 workspace 移动 |
| 阶段二失败反馈 | Claude 报告配置失败，不静默挂在 phase2_done |

---

## 七、待办清单（v1.1 更新）

| 项 | 优先级 | 状态 | 文件 |
|----|--------|------|------|
| D1：路径推导 appID | — | ✅ 已完成 | `watcher.go`, `runner.go`, `models.go`, `main.go` |
| D2：占位符检测 + 必填校验 | — | ✅ 已完成 | `runner.go` |
| D3：0 任务 WARN | — | ✅ 已完成 | `main.go` |
| 修复现有实例 xh_yibu 等的 app_id + target | — | ✅ 已完成（手动修复）| yaml 文件 |
| **模板目录重构 + init 脚本（原子）** | **P1** | ⬜ 待实施 | `_companion/`, init 脚本 |
| **removeTask id 统一为文件名** | **P1** | ⬜ 待实施 | `runner.go`, 模板文件 |
| **send_output 字段 + runner.go** | **P1** | ⬜ 待实施 | `models.go`, `runner.go`, 模板文件 |
| **SKILL.md 后台任务禁止输出** | **P1** | ⬜ 待实施 | `memory_distill/SKILL.md`, `life_sim/SKILL.md` |
| **proactive SKILL.md 静默路径禁止输出** | **P1** | ⬜ 待实施 | `proactive/SKILL.md` |
| CLAUDE.md phase2_done 指令精确化 | P2 | ⬜ 待实施 | `_companion/CLAUDE.md` |
| D1 覆盖时 slog.Warn | P3 | ⬜ 待实施 | `runner.go` |
