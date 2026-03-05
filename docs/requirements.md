# 需求文档

> 状态: 已实现
> 最后更新: 2026-03-05

## 项目定位

企业级内部工具。通过飞书为每个团队或业务场景建立专属 AI 助理应用，每个应用背后对应一个 Claude Code workspace 目录，支持场景化的长记忆、技能和工具配置。

---

## 核心模块

### 1. 应用（Application）

- 每个飞书应用对应一个业务场景（如"产品助手"、"代码审查助手"）
- 每个应用绑定一个本地 **workspace 目录**，目录内包含：
  - `CLAUDE.md` — 场景指令和上下文
  - `skills/` — 该场景的自定义 skill 文件
  - `memory/` — 长期记忆文件
  - 工具配置、规范等
- 应用是消息路由的第一层：飞书群/单聊 → 路由到对应应用 → 路由到对应 workspace
- **实现**：`internal/config/config.go`（AppConfig），`cmd/server/main.go` 多 WS 客户端

### 2. 会话管理（Session Management）

#### 会话映射规则

| 飞书渠道 | channel_key 格式 | 支持 /new |
|---|---|---|
| 单聊（P2P） | `p2p:{open_id}:{app_id}` | ✅ |
| 普通群聊 | `group:{chat_id}:{app_id}` | ✅ |
| 话题群（Thread Group）| `thread:{chat_id}:{thread_id}:{app_id}` | ❌ |

- `/new` 命令：归档当前 session（`status = archived`），创建新 session，清空 `claude_session_id`
- **实现**：`internal/session/worker.go`（handleNew）、`internal/model/models.go`（Session）

#### 两层记录

| 层级 | 内容 | 实现 |
|---|---|---|
| Session 层 | 用户侧对话记录（Messages 表） | `internal/model/models.go` |
| Context 层 | Claude 执行的 message context | claude CLI `--resume <claude_session_id>` |

### 3. 消息路由与执行

**实现流程：**
```
飞书 WS 事件（P2MessageReceiveV1）
  → feishu.Receiver.handleMessage()
  → 解析消息类型（text / image / file / post）
  → 下载附件 → 保存 tmp → moveAttachments 移入 session/attachments/
  → session.Manager.Dispatch()
  → session.Worker 队列（channel_key 串行）
  → claude.Executor.Execute()（子进程 claude CLI，stream-json）
  → feishu.Sender.UpdateCard()
```

**关键约束（已实现）：**
- 同一 channel_key 的消息严格串行处理（Worker 队列 + goroutine）
- 不同 channel 并发处理（sync.Map）
- 飞书 **WebSocket 长连接模式**（larkws，免公网 IP）

### 4. 默认 Workspace 初始化配置

新建应用时，框架从 `workspaces/_template/` 自动复制：

- `CLAUDE.md` — 默认 AI 指令（群聊静默策略 / 绝对路径规范）
- `skills/feishu.md` — 飞书操作说明
- `skills/memory.md` — 长记忆读写规范（含 flock 指南）
- `skills/task.md` — 定时任务 YAML 格式规范

**实现**：`internal/workspace/init.go`（Init + copyTemplate，跳过 symlink）

### 5. 后台任务机制（Background Task）

- 用户通过对话创建定时任务（claude 调用 task skill 写 YAML）
- YAML 文件格式含：id / app_id / cron / target_type / target_id / prompt / enabled
- `fsnotify` 监听 `tasks/` 目录变更 → 同步 DB + 注册/注销 gocron Job
- Cron 表达式在 `LoadYAML` 时校验（`robfig/cron/v3`）
- 框架启动时全量扫描 DB 恢复 enabled 任务
- **实现**：`internal/task/`（watcher.go + scheduler.go + runner.go）

### 6. 消息队列与顺序执行

- 每个 channel_key 维护一个独立 goroutine（Worker）和缓冲队列（深度 64）
- 同一 channel 的消息严格串行处理
- Worker 空闲 30 分钟自动退出，session 归档，下次消息到达时重新创建
- 执行超时：通过 `context.WithTimeout` 控制（默认 5 分钟）
- **实现**：`internal/session/manager.go` + `internal/session/worker.go`

---

## 非功能需求（已确认实现）

| 需求 | 实现方案 |
|---|---|
| 部署方式 | 单机，单进程，SQLite WAL |
| 数据持久化 | SQLite（`github.com/glebarez/sqlite` CGO-free）|
| Claude 调用方式 | 子进程调用 `claude` CLI（`--output-format stream-json`）|
| 多应用权限隔离 | `allowed_chats` 白名单（每 app 独立配置）|
| 任务调度 | 单机 gocron/v2，非分布式 |
| 优雅关闭 | `sessionMgr.Wait()` 等待所有 worker 完成 |
| HTTP 安全 | ReadTimeout / WriteTimeout / IdleTimeout 均已设置 |
| 附件安全 | 100 MiB 写入上限（`io.LimitReader`）|
