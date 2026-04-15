# Task ID 全局唯一性重构设计

**状态**：待 Review  
**日期**：2026-04-14  
**背景**：多 workspace 同名任务文件导致 DB 记录互相覆盖，定时任务静默失效

---

## 一、问题陈述

### 根本缺陷

tasks 表使用文件名（去掉 `.yaml` 后缀）作为主键 ID。多个 workspace 可以有同名文件，导致 DB 中只保留最后一个写入者的记录，前面 workspace 的任务从调度器和 DB 中消失。

### 真实事故

2026-04-14，xh_yibu 和 ycm_mate 两个陪伴空间均有：

```
tasks/proactive_reach.yaml
tasks/life_sim.yaml
tasks/memory_distill.yaml
```

ycm_mate 注册时覆盖了 xh_yibu 的三条 DB 记录，xh_yibu 的所有定时任务停止执行，且无任何报错。

### 为什么不能靠模型规避

任务文件由模型（Claude）在对话中写入 `tasks/` 目录，框架通过 fsnotify 监听并自动注册。模型可以使用任意文件名（语义名、UUID、自定义名），框架无法对模型的命名行为做约束假设。因此，**唯一性保证必须在框架层实现**。

---

## 二、现状分析

### 当前 ID 生成逻辑（`LoadYAML`）

```go
ty.ID = strings.TrimSuffix(filepath.Base(path), ".yaml")
// "tasks/proactive_reach.yaml" → ID = "proactive_reach"
// "tasks/1ff20d20-....yaml"    → ID = "1ff20d20-...."
```

ID 不携带 workspace 信息，跨空间文件名重复即冲突。

### 临时补丁现状（2026-04-14 已上线）

当前代码中有一个临时 `TaskFileID` 函数，仅对非 UUID 文件名加 `app_id` 前缀：

```go
func TaskFileID(base, appID string) string {
    if _, err := uuid.Parse(base); err == nil {
        return base           // UUID 文件名不加前缀
    }
    return appID + "." + base  // 非 UUID 加前缀
}
```

**临时补丁的缺陷**：
1. 需要区分 UUID / 非 UUID，逻辑脆弱
2. DB 中存在两种 ID 格式（纯 UUID vs `app_id.name`），混乱
3. UUID 文件名理论上仍可冲突（极低概率，但非零）
4. 存量 DB 记录格式不一致，部分是旧裸名格式

### 受影响空间统计

| 空间 | 非 UUID 文件名 | 当前状态 |
|---|---|---|
| xh_yibu | proactive_reach, life_sim, memory_distill | 有旧裸名 DB 记录 |
| ycm_mate | proactive_reach, life_sim, memory_distill | 有 `ycm_mate.*` 临时记录 |
| ycm_life | morning-todo-8am, evening-fatigue-log-6pm 等4个 | 暂无冲突，但同类空间出现即冲突 |
| 其他空间 | 全部 UUID 文件名 | 理论安全，实际无冲突 |

---

## 三、设计方案

### 核心原则

**task ID = `{app_id}/{filename_slug}`，完全由框架计算，不依赖模型行为。**

框架已知每个 YAML 文件的归属 workspace（通过 `dirAppIDs` 映射），可在读取文件时无条件构造全局唯一 ID。

### ID 生成规则

```
ID = app_id + "/" + filename_without_ext

示例：
  workspace: xh_yibu
  文件: tasks/proactive_reach.yaml
  → ID: "xh_yibu/proactive_reach"

  workspace: investment
  文件: tasks/1ff20d20-4469-4346-8e96-3dda5d71c123.yaml
  → ID: "investment/1ff20d20-4469-4346-8e96-3dda5d71c123"
```

分隔符选 `/`：语义清晰（namespace/slug），不与现有 UUID 格式（含 `-`）混淆，在日志中一眼可识别归属空间。

### 代码变更（最小化）

**`internal/task/runner.go`**：

```go
// LoadYAML 中，将
ty.ID = strings.TrimSuffix(filepath.Base(path), ".yaml")
// 改为
ty.ID = appID + "/" + strings.TrimSuffix(filepath.Base(path), ".yaml")
```

同时删除 `TaskFileID` 函数及 `uuid` import（如不再需要）。

**`internal/task/watcher.go`**：Remove / Rename 分支同步使用新格式：

```go
case event.Op&fsnotify.Remove != 0:
    base := strings.TrimSuffix(filepath.Base(event.Name), ".yaml")
    w.removeTask(appID + "/" + base)
```

**变更范围**：仅 `runner.go` 和 `watcher.go` 各 1~3 行，影响面极小。

---

## 四、迁移策略

### 时机：server 启动时自动迁移

在 `cmd/server/main.go` 的任务子系统初始化阶段，scheduler 启动**之前**，执行一次幂等迁移。

```
启动流程（含迁移）:
  1. 连接 DB
  2. 初始化 Watcher / Scheduler / Runner
  3. ← 在此插入：migrateTaskIDs(db, apps)
  4. 为每个 workspace 执行 AddDir + restoreEnabledTasks
  5. 启动 Scheduler、Watcher
```

### 迁移逻辑（`migrateTaskIDs`）

```
对 tasks 表中每条记录：
  如果 ID 中不包含 "/"：
    新 ID = app_id + "/" + ID
    UPDATE tasks SET id = 新ID WHERE id = 旧ID
    记录日志：migrated task ID old → new
```

**幂等保证**：已迁移的记录 ID 含 `/`，不会被二次处理。

**边界情况处理**：

| 情况 | 处理 |
|---|---|
| 新旧 ID 冲突（同一空间有 `foo` 和 `xh_yibu/foo` 同时存在） | 以新格式为准，旧裸名记录 soft delete |
| app_id 为空（历史脏数据） | 跳过，记录 WARN |
| 迁移失败（DB 错误） | 记录 ERROR，不阻塞启动（降级：旧记录继续用旧 ID 工作） |

### 迁移后数据状态

| 记录 | 迁移前 ID | 迁移后 ID |
|---|---|---|
| xh_yibu proactive_reach（旧裸名） | `proactive_reach` | `xh_yibu/proactive_reach` |
| xh_yibu life_sim（旧裸名） | `life_sim` | `xh_yibu/life_sim` |
| ycm_mate.proactive_reach（临时） | `ycm_mate.proactive_reach` | `ycm_mate/proactive_reach` |
| investment UUID 任务 | `1ff20d20-...` | `investment/1ff20d20-...` |
| ycm_life UUID 任务 | `52486d63-...` | `ycm_life/52486d63-...` |

---

## 五、受影响组件清单

| 组件 | 变更类型 | 说明 |
|---|---|---|
| `internal/task/runner.go` | 逻辑修改 | ID 生成规则，删除 TaskFileID |
| `internal/task/watcher.go` | 逻辑修改 | Remove/Rename 分支 ID 构建 |
| `cmd/server/main.go` | 新增函数 | `migrateTaskIDs`，启动时调用 |
| `internal/task/runner_test.go` | 测试更新 | LoadYAML 期望 ID 格式变更 |
| `internal/task/watcher_test.go` | 测试更新 | removeTask 期望 ID 格式变更 |
| DB `tasks` 表 | 数据迁移 | 所有记录 ID 加 `app_id/` 前缀 |
| YAML 文件本身 | 不变 | 文件名不需要改，ID 字段本已废弃 |

---

## 六、不在本次范围内

- **YAML 文件重命名**：文件名不影响正确性，不需要变更
- **陪伴空间初始化协议**：模型仍可用任意文件名，框架保证不冲突
- **task_templates 模板内容**：注释里关于 ID 推断的说明需要同步更新（文档工作，低优先级）

---

## 七、风险评估

| 风险 | 概率 | 影响 | 缓解措施 |
|---|---|---|---|
| 迁移期间 ID 变更导致调度器找不到 job | 低（迁移在 Scheduler.Start 前） | 重启后自动恢复 | 迁移顺序保证在 Scheduler 启动前完成 |
| 迁移失败（DB 写错误）阻塞启动 | 极低 | 无（ERROR + 降级继续） | 迁移失败不 os.Exit，仅记录 |
| 存量 YAML 里的 `id` 字段与新 ID 不一致 | 已存在（id 字段本已废弃） | 无 | LoadYAML 已忽略 YAML 中的 id 字段 |
| 将来某个工具直接构造裸名 ID 查询 | 低 | DB 查询不到 | `app_id` 列上有索引，按 app_id 查 tasks 的路径不受影响 |
