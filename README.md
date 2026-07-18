# CC Workspace Bot

CC Workspace Bot 是一个面向企业内部和个人团队的飞书 AI 助理框架。它把飞书消息路由到不同的 Claude Code workspace，让每个飞书应用拥有独立的提示词、技能、长期记忆、定时任务和会话数据。

适合这些场景：

- 在飞书里使用自己的编程、研究、运营、学习助理
- 为不同业务线部署相互隔离的 AI 助手
- 在内网机器上运行，无需公网 Webhook
- 让 Claude Code 通过受控 workspace 读写文件、执行脚本、调用飞书文档和表格能力

## 核心能力

| 能力 | 说明 |
|---|---|
| 多应用隔离 | 一个 `config.yaml` 可配置多个飞书应用，每个应用绑定独立 workspace |
| 飞书 WebSocket 接入 | 使用飞书长连接模式接收消息，本机只需能访问公网 |
| 会话队列 | 每个 `channel_key` 一个串行 Worker，避免同一聊天内并发互相污染 |
| Claude Code 执行 | 子进程调用 `claude -p`，支持 `--cwd`、`--resume`、模型供应商覆盖和工具白名单 |
| 工作 / 陪伴双模式 | `work` 模式使用交互卡片，`companion` 模式直接发送文本 |
| 附件处理 | 飞书图片和文件下载到当前 session 的 `attachments/` 后交给 Claude 读取 |
| 长消息分段 | 超过飞书消息限制时自动切分发送 |
| 定时任务 | workspace 的 `tasks/*.yaml` 是 source of truth，fsnotify + gocron 自动注册 |
| 长期记忆 | workspace 的 `memory/` 目录由技能和 flock 约束管理 |
| 可选观测 | 可通过 Claude Code hooks 接入自托管 Langfuse 记录成本和 trace |

## 架构概览

```text
飞书用户
  -> 飞书 WebSocket 事件
  -> feishu.Receiver 解析消息、下载附件、构造 channel_key
  -> session.Manager 按 channel_key 懒启动 Worker
  -> session.Worker 串行处理、管理 /new、记录消息
  -> claude.Executor 在 workspace/sessions/<session-id>/ 调用 claude CLI
  -> feishu.Sender 发送文本或更新交互卡片

tasks/*.yaml
  -> task.Watcher 监听变更
  -> task.Scheduler 注册 cron
  -> task.Runner 定时调用 claude CLI 并按配置发送结果
```

详细设计见 [概要设计](docs/high-level-design.md) 和 [详细设计](docs/detailed-design.md)。

## 开源安全边界

仓库可以公开的内容包括 Go 源码、workspace 模板、脚本源码、测试 fixture、中文产品文档和安全模板。

不要提交这些内容：

- `config.yaml`、`config.bailian.yaml`、`config.yaml.bak*`、`config.yaml.no_sonnet`
- `bot.db`、`bot.db.*`、workspace 下的 `bot.db`
- `workspaces/<app>/memory/`、`sessions/`、`tasks/`、`tmp/`、`tmp_file/`
- 任意 `.claude/skills/feishu_ops/feishu.json`
- `.claude/settings.local.json` 等本机权限配置
- 运行日志、pid 文件、编译产物

已提供可公开模板：

- [config.yaml.template](config.yaml.template)
- [config.bailian.yaml.template](config.bailian.yaml.template)
- [feishu.json.template](workspaces/_template/.claude/skills/feishu_ops/feishu.json.template)

完整审计清单见 [开源发布审计](docs/open-source-audit.md)。

## 前置要求

必需：

- Linux / macOS / WSL
- Go 1.24 或更高版本
- Claude Code CLI，且运行用户已经完成登录
- 一个或多个飞书自建应用，开启机器人能力和 WebSocket 长连接

按需安装：

- Python 3：仅当使用 `feishu_ops`、Langfuse dry-run 或 workspace 内 Python 技能时需要
- `lark-cli`：仅当使用 `feishu_ops` 脚本读写飞书文档、表格、多维表格时需要
- Docker：主服务不强依赖 Docker；如果你要本机启动 Langfuse、Redis 等外部观测或技能依赖，建议用 Docker Compose 管理

内置 SQLite 使用本地文件，不需要单独安装数据库服务。当前核心服务也不依赖 Redis；只有自定义 workspace 技能或外部观测组件需要 Redis 时才需要启动。

## 快速开始

1. 克隆仓库并进入目录：

```bash
git clone git@github.com:kid0317/cc_workspace_bot.git
cd cc_workspace_bot
```

2. 准备配置：

```bash
cp config.yaml.template config.yaml
```

编辑 `config.yaml`，至少填入：

```yaml
apps:
  - id: "product-assistant"
    feishu_app_id: "cli_your_feishu_app_id"
    feishu_app_secret: "your_feishu_app_secret"
    feishu_verification_token: ""
    feishu_encrypt_key: ""
    workspace_dir: "./workspaces/product-assistant"
    workspace_mode: "work"
    allowed_chats: []
    claude:
      permission_mode: "acceptEdits"
      allowed_tools:
        - "Bash"
        - "Read"
        - "Edit"
        - "Write"
```

3. 初始化一个 workspace：

```bash
./init_workspace.sh product-assistant ./workspaces/product-assistant cli_your_feishu_app_id your_feishu_app_secret
```

该脚本会创建 workspace 目录、复制默认模板、写入私有 `feishu_ops/feishu.json`，并尝试注册 `lark-cli` profile。`feishu.json` 已被 `.gitignore` 排除，不能提交。

4. 编译和测试：

```bash
go mod download
go build ./...
go test ./...
go vet ./...
```

5. 启动服务：

```bash
go run ./cmd/server -config config.yaml
```

也可以构建二进制后用脚本后台运行：

```bash
go build -o server ./cmd/server
./start.sh start
./start.sh status
```

服务启动后会监听本地健康检查：

```bash
curl http://127.0.0.1:8080/health
```

返回 `ok` 表示 HTTP 进程正常。飞书 WebSocket 是否连通以服务日志中的 receiver 启动和飞书平台连接状态为准。

## 飞书应用配置

在飞书开放平台中完成以下配置：

1. 创建企业自建应用。
2. 启用机器人能力，并把机器人加入目标单聊或群聊。
3. 开启事件订阅，选择 WebSocket 长连接模式。
4. 订阅消息接收事件，以及按需订阅入群、用户进群、首次单聊事件。
5. 为机器人开通发送消息、读取消息资源、文档/表格等所需权限。
6. 将 App ID、App Secret、Verification Token、Encrypt Key 写入本机 `config.yaml`。

如果只想允许指定群使用某个应用，在 `allowed_chats` 中填入飞书 `chat_id`。空数组表示不限制。

## 配置说明

### 应用配置

| 字段 | 必填 | 说明 |
|---|---:|---|
| `id` | 是 | workspace ID，也是内部 app 标识，不能包含 `/` |
| `feishu_app_id` | 是 | 飞书应用 App ID |
| `feishu_app_secret` | 是 | 飞书应用 App Secret |
| `feishu_verification_token` | 否 | 飞书事件订阅校验 token，WebSocket 模式可为空 |
| `feishu_encrypt_key` | 否 | 飞书事件加密 key，不启用加密可为空 |
| `workspace_dir` | 是 | 应用 workspace 目录 |
| `workspace_mode` | 否 | `work` 或 `companion`，默认按 `work` 处理 |
| `allowed_chats` | 否 | 允许访问的 chat_id 白名单 |
| `claude.permission_mode` | 否 | 传给 Claude CLI 的权限模式 |
| `claude.allowed_tools` | 否 | 传给 Claude CLI 的工具白名单 |
| `claude.provider` | 否 | 覆盖全局默认模型供应商 |
| `claude.model` | 否 | 覆盖供应商默认模型 |
| `claude.effort` | 否 | Anthropic provider 下传给 Claude CLI 的 effort |

### 模型供应商

默认使用 Claude Code CLI 自身登录态：

```yaml
claude:
  default_provider: "anthropic"
  providers:
    anthropic:
      model: "sonnet"
```

使用兼容 Anthropic 协议的第三方供应商时，在私有 `config.yaml` 中配置：

```yaml
claude:
  default_provider: "bailian"
  providers:
    bailian:
      base_url: "https://coding.dashscope.aliyuncs.com/apps/anthropic"
      auth_token: "your_dashscope_api_key"
      model: "qwen-plus"
```

`auth_token` 是密钥，只能写在本机私有配置里。

## Workspace 目录

```text
workspaces/<app-id>/
  CLAUDE.md
  .memory.lock
  .claude/
    skills/
      feishu_ops/
      memory/
      task/
      cases/
      todo/
      insights/
      chat_history/
  memory/
  tasks/
  sessions/
```

公开仓库只保留 `_template` 和 `_companion` 模板。实际业务 workspace 的 `memory/`、`tasks/`、`sessions/`、附件、DB 都属于运行时数据，默认不提交。

## 本地依赖和 Docker

主程序是单进程 Go 服务：

- 数据库：每个 workspace 一个 SQLite 文件 `workspaces/<app>/bot.db`
- 缓存：核心服务不需要 Redis
- 消息入口：飞书 WebSocket 长连接
- AI 执行：本机 `claude` CLI

如果启用可选 Langfuse 观测，建议在本机 Docker 中部署 Langfuse 及其依赖服务，并通过 Claude Code hooks 上报。该能力属于可选观测面，不影响主服务启动。

## 开发

常用命令：

```bash
go build ./...
go test ./... -cover
go vet ./...
gofmt -w .
```

调试数据库：

```bash
go run ./cmd/dbcheck
```

构建 filelock 辅助工具：

```bash
go build -o filelock ./cmd/filelock
```

## 文档

| 文档 | 说明 |
|---|---|
| [需求分析](docs/requirements.md) | 项目目标、用户、场景和需求边界 |
| [PRD](docs/prd.md) | 产品能力、用户流程和验收标准 |
| [概要设计](docs/high-level-design.md) | 模块划分、数据流和部署结构 |
| [详细设计](docs/detailed-design.md) | 关键接口、数据模型、错误处理和测试策略 |
| [技术调研](docs/tech-research.md) | 主要技术选型依据 |
| [开源发布审计](docs/open-source-audit.md) | 可公开/不可公开文件边界 |
| [历史设计归档](docs/archive/README.md) | 已归档的阶段性设计和问题分析 |

## 许可证

建议使用 MIT License。发布前请确认 [LICENSE](LICENSE) 是否已经存在并与仓库策略一致。
