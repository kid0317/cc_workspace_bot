# 陪伴型 Workspace 定时任务初始化设计

> 版本：v1.0  
> 日期：2026-04-11  
> 背景：诊断 xh_yibu 从未收到主动消息，暴露的系统性设计问题

---

## 一、问题背景

**现象**：xh_yibu 陪伴机器人自部署以来从未发出过任何主动消息。

**诊断结果**（服务器日志）：
```
restored tasks app_id=xh_yibu count=0
```

三个 task YAML 文件存在于 workspace，但没有任何任务被注册进 scheduler。

---

## 二、根因分析

### 根因 1：`app_id` 字段语义歧义（已修复 D1）

Task YAML 文件里有 `app_id` 字段，init 脚本用 `__FEISHU_APP_ID__` 占位符替换，填入飞书 App ID（`cli_a95d2eb715391bdf`）。

但 `restoreEnabledTasks()` 和 `runner.go` 中 `appRegistry` 的查找键是 workspace ID（`xh_yibu`），两者不一致，导致 DB 里的任务永远查不到。

**根本原因**：`AppConfig.ID`（workspace ID）和 `AppConfig.FeishuAppID`（飞书 App ID）是两个不同字段，字段名 `app_id` 存在歧义，init 脚本做了"正确的错误替换"。

### 根因 2：`target_type`/`target_id` 占位符从未被填写

Task YAML 模板有 `__TARGET_TYPE__` 和 `__TARGET_ID__` 占位符，设计意图是由 Claude 在阶段二完成后填写。

**但存在时序问题**：
```
T1: init 脚本运行
    └─ 用户 Feishu ID 此时不存在，无法填写

T2: 用户首次发消息
    └─ 此时才有 routing_key / channel_key

T3: 阶段二完成
    └─ CLAUDE.md 指令模糊（只提了 proactive_reach，未说怎么拿 target 值）
    └─ Claude 没有收到明确指令，占位符从未被替换
```

**本质**：是设计时序问题，不是模型能力问题。模型在该执行操作的时间点没有正确的信息和指令。

### 根因 3：0 任务启动无可见信号（已修复 D3）

`restoreEnabledTasks` 返回 0 时以 INFO 级别记录，淹没在启动日志里，运维无感知。

---

## 三、已实施的代码改动

### D1：路径推导 workspace ID（✅ 已完成）

**文件**：`internal/task/watcher.go`、`internal/task/runner.go`、`internal/model/models.go`、`cmd/server/main.go`

**变更**：
- `Watcher` 新增 `dirAppIDs map[string]string`，存储 `tasks/ 目录路径 → workspace appID` 映射
- `AddDir(dir string)` → `AddDir(dir string, appID string)`
- `LoadYAML(path string)` → `LoadYAML(path string, appID string)`，强制用路径推导的 appID 覆盖 YAML 里的 `app_id` 字段
- `TaskYAML.AppID` 标记 Deprecated，说明该字段被忽略

**效果**：彻底消除"YAML 里的 app_id 与 workspace ID 不一致"这一类 bug，来源从可写错的字段变为服务器直接观测的文件路径。

**遗留改进**：覆盖时如果 yaml 里的值与 appID 不同，应加一条 `slog.Warn`，便于发现文件被跨 workspace 移动的场景。

```go
if ty.AppID != "" && ty.AppID != appID {
    slog.Warn("task yaml app_id overridden by workspace path",
        "yaml_app_id", ty.AppID, "effective_app_id", appID, "file", path)
}
```

### D2：占位符检测 + 必填字段校验（✅ 已完成）

**文件**：`internal/task/runner.go`

**变更**：
- 新增 `placeholderRe = regexp.MustCompile(`__[A-Z_]+__`)`
- `LoadYAML` 对 `target_type`、`target_id`、`prompt` 进行占位符检测，命中则返回 ERROR
- `LoadYAML` 对 `target_type`、`target_id`、`cron`、`prompt` 进行非空校验

**效果**：将"任务注册进 scheduler 但执行时静默失败"变为"加载时立即 ERROR、任务不进 DB"。

**注意**：D2 对所有任务生效，不区分 `enabled` 状态。这与下文设计方案的关系见第四节。

### D3：0 任务启动升级为 WARN（✅ 已完成）

**文件**：`cmd/server/main.go`

**变更**：
- `restoreEnabledTasks` 新增 `tasksDir` 参数
- `count == 0` 时从 `slog.Info` 升级为 `slog.Warn`，同时输出 `tasks_dir`

---

## 四、待实施的设计方案

### 4.1 任务模板目录重构

**当前问题**：task YAML 模板文件放在 `_companion/tasks/`，init 脚本复制到实例 `tasks/`。带占位符的模板进入被 fsnotify 监听的目录，D2 会拒绝它们（或在未来调整 D2 后才能存入 DB）。设计边界模糊。

**新目录结构**：

```
workspaces/_companion/
├── tasks/                          ← 空（实例初始化后才有文件）
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
│   ├── proactive_reach.yaml        ← 由 Claude 在阶段二实例化写入
│   ├── life_sim.yaml
│   └── memory_distill.yaml
└── .claude/
    └── task_templates/             ← init 脚本从模板复制（含占位符）
        ├── proactive_reach.yaml
        ├── life_sim.yaml
        └── memory_distill.yaml
```

**fsnotify 仅监听 `tasks/`**，`.claude/task_templates/` 不在监听范围内（无需代码改动）。

**模板文件变更**：
- 移除 `app_id: __FEISHU_APP_ID__`（D1 后该字段被忽略，不再需要）
- 保留 `target_type: __TARGET_TYPE__` 和 `target_id: __TARGET_ID__`
- `enabled: true`（模板的预期状态；实例化时值已确定，直接可用）

**可维护性**：
- 模板是唯一权威来源，可 git diff、可 review
- 修改模板 → 所有新建实例生效
- 存量实例：可通过指令让 Claude 重新从 `.claude/task_templates/` 实例化到 `tasks/`

### 4.2 init_companion_workspace.sh 更新

**当前**：复制 `_companion/tasks/*.yaml` → 实例 `tasks/`

**改为**：
- 复制 `_companion/.claude/task_templates/` → 实例 `.claude/task_templates/`
- 不再复制任何文件到实例 `tasks/`（`tasks/` 保持空目录）
- 替换 `.claude/task_templates/` 中的 `__APP_ID__`、`__WORKSPACE_DIR__`（`app_id` 字段已移除，只需替换其他占位符）

### 4.3 CLAUDE.md 阶段二初始化指令更新

**当前 phase2_done 指令**（模糊）：
> "创建定时任务 tasks/proactive_reach.yaml，询问主动唤醒时间，更新为 done"

**问题**：
- 只提了一个文件
- 没有说 target 值从哪里来
- 没有说所有 task 文件都需要创建

**新指令**（精确）：

```markdown
## phase2_done → done 流程

1. 读取 SESSION_CONTEXT.md（当前会话目录下），找到：
   - `Channel key` 字段，格式为 `type:id:app_id`
   - target_type = 第一段（如 `p2p`）
   - target_id = 第二段（如 `oc_98f5f925b82266ee81716ed91b519917`）

2. 读取 `.claude/task_templates/` 目录下所有 yaml 文件，
   将每个文件中的 `__TARGET_TYPE__` 替换为 target_type，
   `__TARGET_ID__` 替换为 target_id，
   写入 `tasks/` 目录（同名文件）。

3. 确认 `tasks/` 下有 3 个 yaml 文件且无占位符残留。

4. 将 MEMORY.md 中的 `initialization_status` 更新为 `done`。

5. 继续对话，以角色身份继续。
```

**触发机制**：写入 `tasks/*.yaml` 后，fsnotify watcher 自动检测文件变化 → `LoadYAML(path, appID)` → D2 校验（已填充，通过）→ `upsertTask` → `scheduler.Add`。无需任何额外工程机制。

---

## 五、整体架构图

```
初始化阶段
──────────────────────────────────────────────────────────
init 脚本运行（T1，用户 ID 未知）
    ↓
复制 task_templates/ 到实例 .claude/task_templates/
tasks/ 保持空目录

用户首次发消息（T2）
    ↓
Go server 建立 Channel 记录（DB）
SESSION_CONTEXT.md 写入 channel_key

阶段一：确立角色人设（T2-T3）
阶段二：收集用户信息（T3-T4）
    ↓ initialization_status = phase2_done

阶段二完成（T4，channel_key 已知）
    ↓
Claude 读 SESSION_CONTEXT.md → 解析 channel_key
Claude 读 .claude/task_templates/*.yaml → 实例化 → 写入 tasks/
    ↓
fsnotify watcher 检测 tasks/ 变化
    ↓
LoadYAML(path, appID)  [D1: appID 从路径推导]
    ↓
D2: 占位符检测 + 必填字段校验
    ↓ 通过
upsertTask → scheduler.Add
    ↓
initialization_status = done
定时任务开始运行 ✅

运行阶段
──────────────────────────────────────────────────────────
09:00 / 21:00 → proactive_reach 触发
每小时       → memory_distill 触发
每4小时      → life_sim 触发
```

---

## 六、安全网机制

| 机制 | 覆盖场景 |
|------|---------|
| D1：路径推导 appID | 防止 YAML 里 app_id 填错，永久消除该类 bug |
| D2：占位符检测 | 防止未实例化的模板文件进入 scheduler |
| D2：必填字段校验 | 防止 target_type/target_id/cron/prompt 为空 |
| D3：0 任务 WARN | 快速定位"任务从未注册"的运维问题 |
| D1 覆盖日志（待加）| 发现文件跨 workspace 移动的场景 |

---

## 七、待办清单

| 项 | 状态 | 文件 |
|----|------|------|
| D1：路径推导 appID | ✅ 已完成 | `watcher.go`, `runner.go`, `models.go`, `main.go` |
| D2：占位符检测 + 必填校验 | ✅ 已完成 | `runner.go` |
| D3：0 任务 WARN | ✅ 已完成 | `main.go` |
| D1 覆盖时加 slog.Warn | ⬜ 待实施 | `runner.go` |
| 模板目录重构（tasks/ → .claude/task_templates/）| ⬜ 待实施 | `_companion/` |
| init 脚本更新 | ⬜ 待实施 | `init_companion_workspace.sh` |
| CLAUDE.md phase2_done 指令精确化 | ⬜ 待实施 | `_companion/CLAUDE.md` |
| 修复现有实例（xh_yibu 等）已损坏的 task YAML | ✅ 已完成（手动修复 app_id + target） |
