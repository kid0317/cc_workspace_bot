# 需求分析文档

> 状态：开源版整理
> 最后更新：2026-07-18

## 1. 背景

很多团队已经把 Claude Code 用作本地开发和知识工作工具，但默认交互入口仍然是终端。终端适合开发者，不适合团队成员在移动端、群聊、定时提醒和知识沉淀场景中使用。

本项目把飞书作为统一入口，把每个飞书应用映射到一个独立 Claude Code workspace。用户在飞书发消息，服务在本机或内网服务器中调用 `claude` CLI，结果再回到飞书。

## 2. 目标用户

| 用户 | 诉求 |
|---|---|
| 开发者 | 在手机或群聊中触发代码阅读、修改、测试、文档生成 |
| 团队负责人 | 为不同业务线配置不同 AI 助理，并控制可访问群聊 |
| 运营 / 产品人员 | 使用飞书文档、表格、定时任务和长期记忆完成日常工作 |
| 个人用户 | 构建学习、研究、生活记录或陪伴型助手 |

## 3. 核心问题

1. 飞书消息如何稳定路由到正确 workspace。
2. 同一聊天里的多条消息如何串行处理，避免 Claude 并发写同一目录。
3. 每个应用如何隔离配置、记忆、任务、附件和数据库。
4. 如何让任务型助手用卡片反馈进度，让陪伴型助手保持自然文本体验。
5. 私有凭证、运行时数据、用户记忆如何与可公开源码分离。

## 4. 功能需求

### 4.1 多应用配置

- 系统从 `config.yaml` 读取多个 app。
- 每个 app 至少包含 `id`、飞书 App ID / Secret、`workspace_dir`。
- 每个 app 可配置 `allowed_chats` 白名单。
- 每个 app 可覆盖 Claude provider、model、effort 和 allowed tools。

### 4.2 飞书消息接入

- 使用飞书 WebSocket 长连接接收事件。
- 支持单聊、普通群聊、话题群。
- 支持文本、图片、文件、富文本消息。
- 机器人入群、用户入群、首次打开单聊时可发送欢迎消息。

### 4.3 会话管理

- `channel_key` 是队列和 session 的稳定键。
- 同一 `channel_key` 串行执行。
- 不同 `channel_key` 可并发执行。
- `/new` 归档当前 session 并创建新 session。
- Worker 空闲超时后退出，但保留 session 数据。

### 4.4 Claude 执行

- 每次执行都在 `workspace/sessions/<session-id>/` 下运行。
- work 模式复用 `claude_session_id` 以恢复上下文。
- companion 模式每轮新建 Claude 会话，通过 hooks 注入最近历史。
- 子进程超时、输出解析、stderr 收集和进程组清理必须可控。
- 对第三方 provider 污染的 resume JSONL 做一次自动清理和重试。

### 4.5 附件处理

- 图片和文件先下载到临时目录，再移动到 session 的 `attachments/`。
- 传给 Claude 的 prompt 中只包含本地绝对路径引用。
- 纯附件消息先缓存，下一条文字消息到达后合并处理。
- 附件写入大小上限为 100 MiB。

### 4.6 定时任务

- `tasks/*.yaml` 是任务 source of truth。
- 文件创建、修改、删除后自动同步 DB 和 gocron job。
- 启动时恢复 enabled 任务。
- 定时任务默认不复用旧 Claude context。
- 支持系统任务、用户回复任务和借用用户 channel 的后台任务。

### 4.7 数据持久化

- 每个 app 使用独立 SQLite `bot.db`，路径位于对应 workspace 下。
- DB 保存 channel、session、message、task 的运行时镜像。
- YAML 任务文件仍是任务配置真源。

## 5. 非功能需求

| 类别 | 需求 |
|---|---|
| 安全 | 私有配置和运行时数据不得进入 git；workspace cwd 不允许逃逸 |
| 可运维 | 支持健康检查、日志、优雅关闭、后台脚本启动 |
| 可测试 | 核心纯函数和边界逻辑有 Go 单测 |
| 可迁移 | 模板和配置文件有公开 `.template` 版本 |
| 易用性 | README 提供从克隆到启动的完整中文教程 |

## 6. 非目标

- 不提供公网 SaaS 托管。
- 不实现多节点分布式调度。
- 不内置用户管理后台。
- 不把生产密钥、真实 workspace 记忆或真实聊天数据作为开源样例。

## 7. 验收标准

- 新开发者能按 README 复制模板、填写飞书配置、启动服务。
- `go build ./...`、`go test ./... -cover`、`go vet ./...` 可通过。
- `git ls-files` 中不包含私有 `config.yaml`、真实 `feishu.json`、DB、日志、session、memory、tasks。
- docs 目录下存在需求分析、PRD、概要设计、详细设计和开源审计文档。
