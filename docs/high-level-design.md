# 概要设计

> 状态：开源版整理
> 最后更新：2026-07-18

## 1. 总体架构

```text
Feishu WebSocket
  -> Receiver
  -> Manager
  -> Worker
  -> Executor
  -> Claude CLI
  -> Sender

Workspace tasks/
  -> Watcher
  -> Scheduler
  -> Runner
  -> Executor
```

系统采用单进程多 app 架构。一个进程加载所有 app 配置，为每个 app 创建飞书 receiver 和 sender，为每个 app 打开独立 SQLite 数据库，并共享一个 Claude executor。

## 2. 模块职责

| 模块 | 目录 | 职责 |
|---|---|---|
| 配置 | `internal/config` | 读取 YAML、设置默认值、校验必填项 |
| 数据库 | `internal/db` | 打开 SQLite WAL，按 app 管理 DB registry |
| 飞书接入 | `internal/feishu` | WebSocket 事件处理、消息解析、附件下载、消息发送 |
| 会话 | `internal/session` | Worker 队列、session 生命周期、消息持久化、长消息分段 |
| Claude 执行 | `internal/claude` | 构造 CLI 参数、注入 provider/env、解析 stream-json、resume 修复 |
| 任务 | `internal/task` | YAML 加载、fsnotify 监听、cron 调度、定时执行 |
| Workspace | `internal/workspace` | 初始化目录、复制模板、写入本地飞书凭证 |

## 3. 数据边界

| 数据 | 位置 | 是否公开 |
|---|---|---|
| 源码 | `cmd/`、`internal/`、`scripts/` | 是 |
| 公共模板 | `config*.template`、`workspaces/_template/`、`workspaces/_companion/` | 是 |
| 主配置 | `config.yaml` | 否 |
| 飞书脚本凭证 | `workspaces/<app>/.claude/skills/feishu_ops/feishu.json` | 否 |
| 会话数据 | `workspaces/<app>/sessions/` | 否 |
| 长期记忆 | `workspaces/<app>/memory/` | 否 |
| 任务数据 | `workspaces/<app>/tasks/` | 否，除模板任务 |
| SQLite | `workspaces/<app>/bot.db` | 否 |

## 4. 部署视图

```text
host
  cc-workspace-bot process
  claude CLI
  config.yaml
  workspaces/
    app-a/
      bot.db
      memory/
      tasks/
      sessions/
    app-b/
      bot.db
      memory/
      tasks/
      sessions/
```

主服务不依赖外部数据库服务。SQLite 文件跟随 workspace 存放。可选 Langfuse、Redis 或其他工具依赖由 workspace hooks/skills 自行决定，不是核心启动路径。

## 5. 运行时数据流

### 5.1 消息处理

1. 飞书 WebSocket 推送消息事件。
2. Receiver 校验 allowed chat，解析消息类型。
3. 图片和文件下载到临时路径。
4. Receiver 构造 `IncomingMessage`。
5. Manager 根据 `channel_key` 找到 Worker。
6. Worker 获取 active session，移动附件到 session 目录。
7. Executor 调用 Claude。
8. Worker 保存消息和结果。
9. Sender 发文本或更新卡片。

### 5.2 任务处理

1. Watcher 监听每个 workspace 的 `tasks/`。
2. YAML 文件被创建或修改后解析为 Task。
3. Task 写入该 app 的 SQLite DB。
4. Scheduler 注册 cron job。
5. 到点后 Runner 新开 Claude 执行。
6. 根据 `send_output` 和 target 配置决定是否发送结果。

## 6. 关键设计决策

| 决策 | 说明 |
|---|---|
| WebSocket 优先 | 避免公网 Webhook 和反向代理配置 |
| per-workspace DB | 运行时数据随 workspace 隔离，减少跨 app 数据泄漏风险 |
| per-channel Worker | 保证同一聊天内串行执行 |
| YAML 为任务真源 | 文件可审计、可由 Claude 写入、可被 watcher 恢复 |
| Claude CLI 子进程 | 复用 Claude Code 的工具生态和登录态 |
| 模板公开、实例私有 | 开源仓库只提交模板和源码 |

## 7. 失败处理

- 配置缺失：启动失败并输出明确错误。
- 飞书附件下载失败：返回失败占位文本，不阻断消息。
- Claude 超时：终止子进程并回复错误。
- Claude 空响应：提示用户 `/new`。
- Watcher 丢失目录监听：周期 rescan 重建 watch。
- 第三方 provider 污染 resume：sanitize 后重试一次。
