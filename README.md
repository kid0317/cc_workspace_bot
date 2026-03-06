# CC Workspace Bot

企业级飞书 AI 助理平台，基于 Claude Code 构建的多场景智能助手框架。

## 项目简介

CC Workspace Bot 是一个企业内部工具，通过飞书为不同团队或业务场景提供专属 AI 助理。每个飞书应用对应一个独立的 Claude Code workspace 目录，支持场景化的长记忆、自定义技能和工具配置。

**核心特性：**

- 🎯 **多应用隔离**：每个飞书应用对应独立 workspace，支持不同场景的 AI 助理
- 💬 **智能会话管理**：支持单聊、群聊、话题群，自动维护上下文
- 🔄 **后台任务调度**：通过对话创建定时任务，支持 cron 表达式
- 📁 **长期记忆**：跨会话共享记忆，支持并发安全的文件锁机制
- 🛠️ **自定义技能**：每个 workspace 可配置专属 skills 和工具
- 🔌 **WebSocket 连接**：无需公网 IP，适合企业内网部署

## 架构设计

### 整体架构

```
飞书用户
  ↓ 发送消息
飞书 WebSocket 推送
  ↓ P2MessageReceiveV1
消息路由器（解析 + 附件下载）
  ↓ 按 channel_key 分发
Session Worker（串行队列）
  ↓ 执行
Claude Executor（子进程调用 claude CLI）
  ↓ stream-json 输出
飞书 Sender（交互式卡片回复）
```

### Channel Key 映射

| 飞书渠道 | channel_key 格式 | 支持 /new |
|---------|-----------------|----------|
| 单聊 | `p2p:{open_id}:{app_id}` | ✅ |
| 群聊 | `group:{chat_id}:{app_id}` | ✅ |
| 话题群 | `thread:{chat_id}:{thread_id}:{app_id}` | ❌ |

### 核心概念

- **channel_key**：飞书渠道的稳定标识，对应一个常驻 Worker goroutine
- **session_id**：当前活跃会话，`/new` 命令时归档旧会话并创建新会话
- **claude_session_id**：Claude CLI 的 context ID，通过 `--resume` 参数复用

## 快速开始

### 前置要求

- Go 1.23+
- Claude CLI（已安装并配置）
- 飞书企业账号及应用凭证

### 安装

```bash
# 克隆仓库
git clone https://github.com/kid0317/cc-workspace-bot.git
cd cc-workspace-bot

# 安装依赖
go mod download

# 构建
go build ./cmd/server
```

### 配置

复制配置模板并编辑：

```bash
cp config.yaml.example config.yaml
```

配置示例：

```yaml
apps:
  - id: "product-assistant"
    feishu_app_id: "cli_xxx"
    feishu_app_secret: "xxx"
    feishu_verification_token: "xxx"
    feishu_encrypt_key: ""
    workspace_dir: "./workspaces/product-assistant"
    allowed_chats: []  # 空表示不限制
    claude:
      permission_mode: "acceptEdits"
      allowed_tools:
        - "Bash"
        - "Read"
        - "Edit"
        - "Write"

server:
  port: 8080

claude:
  timeout_minutes: 5
  max_turns: 20

session:
  worker_idle_timeout_minutes: 30
```

### 运行

```bash
# 使用默认配置
./server

# 指定配置文件
./server -config /path/to/config.yaml
```

## 使用方法

### 基本对话

在飞书中直接向机器人发送消息，支持：
- 文本消息
- 图片（自动下载并提供给 Claude）
- 文件（自动下载并提供给 Claude）
- 富文本（自动提取文本内容）

### 命令

- `/new` - 开启新会话（归档当前会话，清空上下文）

### 创建定时任务

通过自然语言对话创建：

```
用户：每天早上 9 点提醒我查看待办事项
AI：已创建定时任务，将在每天 9:00 向您发送提醒
```

Claude 会自动调用 task skill 创建 YAML 配置文件。

## 项目结构

```
cc-workspace-bot/
├── cmd/
│   └── server/main.go          # 入口：配置加载、组件连线、启动
├── internal/
│   ├── config/                 # Viper YAML 配置
│   ├── model/                  # GORM 数据模型
│   ├── db/                     # SQLite 连接 + AutoMigrate
│   ├── claude/                 # Claude CLI 执行器
│   ├── feishu/                 # 飞书 SDK 封装
│   │   ├── receiver.go         # WS 事件解析、附件下载
│   │   └── sender.go           # 消息/卡片发送
│   ├── session/                # 会话管理
│   │   ├── manager.go          # Worker 映射管理
│   │   └── worker.go           # 单 channel 串行队列
│   ├── task/                   # 定时任务
│   │   ├── watcher.go          # fsnotify 监听
│   │   ├── scheduler.go        # gocron 调度器
│   │   └── runner.go           # 任务执行
│   └── workspace/              # Workspace 初始化
├── workspaces/
│   └── _template/              # 新 workspace 模板
│       ├── CLAUDE.md           # AI 指令
│       └── skills/             # 技能定义
├── config.yaml                 # 应用配置
├── go.mod / go.sum
└── bot.db                      # SQLite 数据库（运行时生成）
```

### Workspace 目录结构

```
workspaces/<app-id>/
├── CLAUDE.md                   # 场景级 AI 指令
├── .memory.lock                # flock 锁文件
├── skills/
│   ├── feishu.md               # 飞书操作说明
│   ├── memory.md               # 长记忆读写规范
│   └── task.md                 # 定时任务格式规范
├── memory/                     # 长期记忆（跨 session 共享）
├── tasks/                      # 定时任务 YAML
└── sessions/
    └── <session-id>/
        ├── SESSION_CONTEXT.md  # 框架注入的上下文
        └── attachments/        # 附件（定期清理）
```

## 技术栈

| 组件 | 技术选型 | 说明 |
|-----|---------|------|
| 语言 | Go 1.23 | 高性能、并发友好 |
| 飞书 SDK | oapi-sdk-go/v3 | 官方 SDK，WebSocket 模式 |
| 数据库 | SQLite WAL | CGO-free，单机 30k TPS |
| ORM | GORM | 快速开发，AutoMigrate |
| 配置 | Viper | YAML 配置管理 |
| 定时任务 | gocron/v2 | 简洁的 cron 调度器 |
| 文件监听 | fsnotify | 跨平台 inotify 封装 |
| Claude 集成 | exec.Cmd | 子进程调用 claude CLI |

### 核心依赖

```go
github.com/larksuite/oapi-sdk-go/v3  // 飞书 SDK
github.com/glebarez/sqlite           // CGO-free SQLite
gorm.io/gorm                         // ORM
github.com/go-co-op/gocron/v2        // 定时任务
github.com/fsnotify/fsnotify         // 文件监听
github.com/spf13/viper               // 配置管理
github.com/robfig/cron/v3            // Cron 表达式解析
github.com/google/uuid               // UUID 生成
```

## 开发指南

### 构建与测试

```bash
# 构建
go build ./...

# 运行测试
go test ./...

# 测试覆盖率
go test ./... -cover

# 竞态检测
go test -race ./...

# 代码检查
go vet ./...

# 格式化
gofmt -w .

# 依赖整理
go mod tidy
```

### 关键设计决策

| 决策点 | 方案 | 原因 |
|-------|------|------|
| 并发隔离 | `--cwd` 指向 session 目录 | 每个会话独立工作目录 |
| Context 管理 | `--resume` 复用 claude session | Claude CLI 自身管理历史 |
| 群聊触发 | 所有消息触发，AI 判断是否回复 | 灵活性，由 CLAUDE.md 定义策略 |
| 附件处理 | 下载到本地，替换为绝对路径 | Claude 通过 Read 工具访问 |
| 任务创建 | Claude 写 YAML，fsnotify 监听 | YAML 为 source of truth |
| 内存共享写 | flock 文件锁 | 防止并发写冲突 |
| 优雅关闭 | `sessionMgr.Wait()` | 等待所有 worker 完成 |

### 并发安全

- **Session 级隔离**：每个 session 独立目录，`--cwd` 隔离写操作
- **Memory 共享写**：通过 flock 加锁（`.memory.lock`）
- **Worker 串行**：同一 channel_key 的消息严格串行处理
- **附件限制**：单文件 100 MiB 上限（`io.LimitReader`）

## 文档

- [需求文档](docs/requirements.md) - 功能需求和模块说明
- [设计文档](docs/design.md) - 架构设计和时序图
- [技术调研](docs/tech-research.md) - 技术选型和实现细节

## 许可证

MIT License

## 作者

kid0317

## 贡献

欢迎提交 Issue 和 Pull Request！
