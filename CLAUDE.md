# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**cc-workspace-bot** — 企业内部飞书 AI 助理平台。

每个飞书应用对应一个 workspace 目录，用户发消息后由框架路由到对应 workspace 执行 `claude` CLI，将结果通过交互式卡片回复给用户。

- 设计文档：`docs/design.md`
- 需求文档：`docs/requirements.md`
- 技术调研：`docs/tech-research.md`

## Common Commands

```bash
# Build
go build ./...

# Run (需先配置 config.yaml)
go run ./cmd/server

# Run with custom config
go run ./cmd/server -config /path/to/config.yaml

# Test
go test ./...

# Test with coverage
go test ./... -cover

# Test with race detector
go test -race ./...

# Build with race detector (CI 推荐)
go build -race ./...

# Vet
go vet ./...

# Lint (requires golangci-lint)
golangci-lint run

# Format
gofmt -w .

# Tidy dependencies
go mod tidy

# DB 调试：查询 session 和 message 数据
# 固定读取当前目录的 bot.db，过滤 channel_key 含 "yzk_worker" 的 session
# 输出：session 列表（ID/channel/status/claude_sid/时间）+ 每个 session 的消息明细
go run ./cmd/dbcheck
```

## Directory Structure

```
cc-workspace-bot/
├── cmd/
│   ├── server/main.go          # 入口：加载配置、连线各组件、启动
│   └── dbcheck/main.go         # DB 调试工具：查询 session / message 数据
├── internal/
│   ├── config/
│   │   └── config.go           # Viper YAML 配置结构 + Validate()
│   ├── model/
│   │   └── models.go           # GORM 数据模型（Channel / Session / Message / Task）
│   ├── db/
│   │   └── db.go               # SQLite WAL 连接 + AutoMigrate
│   ├── claude/
│   │   └── executor.go         # 子进程调用 claude CLI，stream-json 解析
│   ├── feishu/
│   │   ├── receiver.go         # WS 事件解析、附件下载、欢迎事件处理、Dispatcher 接口
│   │   └── sender.go           # 发送卡片（SendCard/SendThinking/UpdateCard/SendText）
│   ├── session/
│   │   ├── manager.go          # channel_key → Worker 懒启动映射（sync.Map + WaitGroup）
│   │   └── worker.go           # 单 channel 串行队列、/new 命令、空闲超时归档
│   ├── task/
│   │   ├── watcher.go          # fsnotify 监听 tasks/ 目录变更
│   │   ├── scheduler.go        # gocron/v2 调度器管理
│   │   └── runner.go           # 定时任务执行（YAML 加载 + claude 调用）
│   └── workspace/
│       └── init.go             # workspace 目录初始化 + 模板复制（跳过 symlink）
├── workspaces/
│   └── _template/              # 新 workspace 默认模板
│       ├── CLAUDE.md           # workspace 级 AI 指令
│       └── .claude/
│           └── skills/
│               ├── feishu_ops/     # 飞书操作（SKILL.md + scripts/）
│               ├── memory/         # 长记忆读写规范（SKILL.md）
│               ├── task/           # 定时任务规范（SKILL.md）
│               ├── cases/          # 事件记录（SKILL.md）
│               ├── todo/           # 待办管理（SKILL.md）
│               ├── insights/       # 感悟记录（SKILL.md）
│               └── chat_history/   # 历史对话检索（SKILL.md + 脚本）
├── config.yaml                 # 应用配置示例
├── go.mod / go.sum
└── bot.db                      # SQLite 数据库（运行时生成）
```

## Architecture

### 整体数据流

```
飞书用户
  → 飞书 WS 推送（P2MessageReceiveV1 / ChatMemberBotAdded / ChatMemberUserAdded / BotP2pChatEntered）
  → feishu.Receiver（解析消息 / 下载附件 / 欢迎事件直接回复）
  → session.Manager.Dispatch()
  → session.Worker（按 channel_key 串行队列）
  → claude.Executor（子进程 claude CLI，stream-json）
  → feishu.Sender（PATCH 卡片展示结果）
```

### channel_key 格式

| 飞书渠道 | channel_key |
|---|---|
| 单聊 | `p2p:{chat_id}:{app_id}` |
| 群聊 | `group:{chat_id}:{app_id}` |
| 话题群 | `thread:{chat_id}:{thread_id}:{app_id}` |

### 关键设计决策

| 决策 | 方案 |
|---|---|
| 初始化循环依赖 | `dispatchForwarder`（`atomic.Pointer[session.Manager]`）|
| 并发隔离 | `--cwd sessions/<id>/`，session 目录级隔离 |
| Context 管理 | `--resume <claude_session_id>` 复用，`/new` 时清空 |
| 任务创建 | claude 写 `tasks/<uuid>.yaml`，fsnotify 自动注册 |
| 内存共享写 | workspace `memory/` 目录用 flock 加锁（skill 层实现）|
| 优雅关闭 | `sessionMgr.Wait()` 等待所有 worker 完成后再退出 |

## Development Workflow

每次新功能开发必须按以下顺序进行，不可跳步：

### 1. 设计（Design）
- 阅读 `docs/design.md` 了解现有架构
- 在 `docs/design.md` 中写明新功能的数据流、接口、决策点（或确认与现有架构无冲突）

### 2. 开发（Implement）
- 优先扩展已有函数/方法，避免新建不必要的文件
- 将核心逻辑提取为纯函数，便于测试

### 3. 测试（Test）
```bash
go build ./...          # 必须编译通过
go test ./... -cover    # 所有现有测试必须通过；新代码需补单测
go vet ./...            # 无警告
```
- 针对新增纯函数写表驱动单测
- handler 类代码需测试边界（nil、空值、AllowedChat 过滤）

### 4. 更新文档（Update Docs）
- `docs/design.md`：补充或修订数据流时序图、设计决策汇总
- `README.md`：如有用户可见的新特性，更新核心特性表
- `CLAUDE.md`（本文件）：如有架构变化，同步更新 Architecture / Key Concepts

---

所有配置通过 `config.yaml` 加载（Viper），**不使用环境变量**：

```yaml
apps:
  - id: "product-assistant"
    feishu_app_id: "cli_xxx"
    feishu_app_secret: "xxx"
    feishu_verification_token: "xxx"
    feishu_encrypt_key: ""          # 可选
    workspace_dir: "./workspaces/product-assistant"
    allowed_chats: []               # 空 = 不限制
    claude:
      permission_mode: "acceptEdits"
      allowed_tools:
        - "Bash"
        - "Read"
        - "Edit"
        - "Write"
      # provider: "bailian"        # 覆盖默认供应商（可选）
      # model: "qwen-plus"         # 覆盖该供应商的默认模型（可选）

server:
  port: 8080

claude:
  timeout_minutes: 5
  max_turns: 20
  default_provider: "anthropic"     # 默认供应商，对应 providers 中的 key
  providers:
    anthropic:
      model: "sonnet"               # 默认模型
    # bailian:
    #   base_url: "https://coding.dashscope.aliyuncs.com/apps/anthropic"
    #   auth_token: "sk-xxx"
    #   model: "qwen-plus"

session:
  worker_idle_timeout_minutes: 30   # Worker 空闲超时

cleanup:
  attachments_retention_days: 7
  attachments_max_days: 30
  schedule: "0 2 * * *"
```

## Key Concepts

### claude.Executor

通过 `exec.Cmd` 调用 `claude` CLI，关键参数：

```
claude \
  -p "<prompt>" \
  --cwd <session_dir> \
  --output-format stream-json \
  --permission-mode acceptEdits \
  --allowedTools "Bash Read Edit Write" \
  --max-turns 20 \
  --model <expanded_model>           # 来自 resolveProvider()
  --settings '{"env":{...}}'         # 覆盖 ~/.claude/settings.json（非默认 provider 时）
  --resume <claude_session_id>       # 省略 = 新 context
```

#### 模型 / 供应商注入机制（三层覆盖）

1. **进程环境层**：`filterEnv()` 先过滤 `CLAUDECODE`/`CLAUDE_CODE_*`（防止嵌套会话检测），再过滤 `ANTHROPIC_*` 变量后 append 新值
2. **CLI `--settings` 层**：`buildSettingsJSON()` 生成 `{"env":{"ANTHROPIC_BASE_URL":"...","ANTHROPIC_AUTH_TOKEN":"...","ANTHROPIC_MODEL":"..."}}` 传给 `--settings` 参数，优先级高于 `~/.claude/settings.json`
3. **CLI `--model` 层**：直接通过 `--model` 参数指定，最高优先级

环境变量（由 `buildClaudeEnvVars()` 构造）：

```
ANTHROPIC_BASE_URL=<mapped from provider or base_url>   # 百炼等第三方供应商
ANTHROPIC_AUTH_TOKEN=<from auth_token>                   # 供应商 API Key
ANTHROPIC_MODEL=<from model>                             # 模型名称
ANTHROPIC_DEFAULT_HAIKU_MODEL=<from model>
ANTHROPIC_DEFAULT_SONNET_MODEL=<from model>
ANTHROPIC_DEFAULT_OPUS_MODEL=<from model>
```

对于默认 anthropic provider 且无自定义配置时，不注入任何 env / --settings，让 claude CLI 使用自身认证。

### session.Worker

- 每个 `channel_key` 对应一个常驻 goroutine（懒启动）
- 消息队列深度 64，串行处理
- 空闲 30 分钟后自动退出并归档 session
- `/new` 命令：归档当前 session，创建新 session 目录
- **附件缓存**：纯附件消息（无文字）自动缓存，提示用户描述意图；下条文字消息合并附件引用后一起发给 claude
- **空结果守护**：claude 返回空文本时（如 context 过长），提示用户 `/new` 开新会话

### 附件处理

飞书图片/文件 → 下载到 `tmp/` → `moveAttachments` 移入 `sessions/<id>/attachments/` → prompt 中路径替换为绝对路径 → claude 通过 `Read` 工具访问。

### 定时任务

claude 通过 `task` skill 写入 `<workspace>/tasks/<uuid>.yaml` → fsnotify 触发 → DB 更新 + gocron Job 注册。YAML 文件为 source of truth。每次任务执行使用全新 claude 会话（不传 `--resume`），避免过期/过长 context 导致失败。

## Dependencies

| 库 | 用途 |
|---|---|
| `github.com/larksuite/oapi-sdk-go/v3` | 飞书 SDK（WS 模式 + IM API）|
| `github.com/glebarez/sqlite` | CGO-free SQLite GORM 驱动 |
| `gorm.io/gorm` | ORM（SQLite WAL 模式）|
| `github.com/go-co-op/gocron/v2` | 定时任务调度器 |
| `github.com/fsnotify/fsnotify` | 文件系统事件监听 |
| `github.com/spf13/viper` | YAML 配置加载 |
| `github.com/robfig/cron/v3` | Cron 表达式解析（gocron 间接依赖，直接用于校验）|
| `gopkg.in/yaml.v3` | task YAML 解析 |
| `github.com/google/uuid` | Session / Task ID 生成 |

## Workspace Template

新应用自动从 `workspaces/_template/` 复制初始配置。每个 workspace 结构：

```
workspaces/<app-id>/
├── CLAUDE.md              # app 级 AI 指令（从模板复制，可按场景自定义）
├── .memory.lock           # flock 锁文件（框架自动创建）
├── .claude/
│   └── skills/
│       ├── feishu_ops/    # 飞书操作（SKILL.md + scripts/）
│       ├── memory/        # 长记忆读写规范（SKILL.md）
│       ├── task/          # 定时任务规范（SKILL.md）
│       ├── cases/         # 事件记录（SKILL.md）
│       ├── todo/          # 待办管理（SKILL.md）
│       ├── insights/      # 感悟记录（SKILL.md）
│       └── chat_history/  # 历史对话检索（SKILL.md + 脚本）
├── memory/                # 长期记忆（跨 session 共享，flock 保护）
├── tasks/                 # 定时任务 YAML（claude 写，watcher 监听）
└── sessions/
    └── <session-id>/
        ├── SESSION_CONTEXT.md  # 框架注入的绝对路径上下文
        └── attachments/        # 附件（定期清理）
```

claude 启动时 `--cwd` 指向 `sessions/<session-id>/`，向上查找 `CLAUDE.md` 和 `skills/`。
