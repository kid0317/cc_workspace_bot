# 产品需求文档

> 状态：开源版整理
> 最后更新：2026-07-18

## 1. 产品定位

CC Workspace Bot 是一个飞书入口的 Claude Code 多工作空间运行框架。它不试图替代 Claude Code，而是把 Claude Code 的本地执行能力封装成可在飞书中使用的团队助理。

一句话：一个飞书应用对应一个 workspace，一个聊天渠道对应一个串行 session。

## 2. 用户价值

| 用户任务 | 产品价值 |
|---|---|
| 在飞书里让 AI 读代码、改文件、跑命令 | 不需要远程登录服务器或打开 IDE |
| 为不同团队配置不同助理 | workspace 级提示词、技能、记忆相互隔离 |
| 群聊中沉淀任务和上下文 | 同一群聊复用 Claude context，必要时 `/new` 重置 |
| 让助理定时推送报告 | 对话创建 YAML 任务，后台自动调度 |
| 让助理读写飞书文档/表格 | workspace 内置 `feishu_ops` 技能脚本 |

## 3. 产品形态

### 3.1 服务端

Go 单进程服务，负责：

- 加载 `config.yaml`
- 初始化各 workspace
- 建立多个飞书 WebSocket receiver
- 维护 per-channel Worker
- 调用 Claude Code CLI
- 管理 SQLite 和定时任务

### 3.2 Workspace

每个 workspace 是一个完整 AI 助手：

- `CLAUDE.md`：角色和协作规则
- `.claude/skills/`：可调用技能
- `memory/`：长期记忆
- `tasks/`：定时任务 YAML
- `sessions/`：每个会话的执行目录和附件
- `bot.db`：该 workspace 的运行时数据库

### 3.3 回复模式

| 模式 | 适用场景 | 体验 |
|---|---|---|
| `work` | 任务、开发、报告、调研 | 先发“思考中”卡片，再 PATCH 成最终结果 |
| `companion` | 陪伴、日常、短对话 | 不发卡片，直接发送文本，可按段落分批输出 |

## 4. 核心用户流程

### 4.1 首次部署

1. 开发者复制 `config.yaml.template` 为 `config.yaml`。
2. 填写飞书应用凭证和 workspace 路径。
3. 执行 `init_workspace.sh` 生成 workspace。
4. 启动服务。
5. 在飞书中给机器人发消息。

### 4.2 普通对话

1. 用户发送文本、图片或文件。
2. Receiver 标准化消息并下载附件。
3. Manager 按 `channel_key` 找到或创建 Worker。
4. Worker 获取当前 active session。
5. Executor 在 session 目录调用 Claude。
6. Sender 把结果发回飞书。

### 4.3 新建会话

1. 用户发送 `/new`。
2. Worker 归档当前 active session。
3. 系统创建新 session。
4. 下一次执行不带旧 `claude_session_id`。

### 4.4 创建定时任务

1. 用户用自然语言要求定时提醒或报告。
2. Claude 通过 task skill 写入 `tasks/<slug>.yaml`。
3. Watcher 解析 YAML 并同步 DB。
4. Scheduler 按 cron 触发 Runner。
5. Runner 调用 Claude 并按任务配置发送或静默执行。

## 5. 配置需求

必须提供公开模板，但真实配置必须由使用者本机创建：

- `config.yaml.template`：主服务配置模板
- `config.bailian.yaml.template`：第三方兼容 provider 示例
- `workspaces/_template/.claude/skills/feishu_ops/feishu.json.template`：飞书脚本凭证模板

真实文件必须被 `.gitignore` 排除。

## 6. 权限与安全

- Claude 只在 session 目录作为 cwd 执行。
- `SessionDirOverride` 必须校验仍位于 workspace 内。
- 不把真实飞书凭证传给 LLM 文本；`feishu_ops` 脚本从本地 `feishu.json` 读取。
- 运行时 memory、session、attachment、task、DB 不作为公开样例提交。
- 每个 app 可用 `allowed_chats` 做聊天白名单。

## 7. MVP 范围

| 模块 | 必须支持 |
|---|---|
| 配置 | 多 app、workspace、provider、model、allowed tools |
| 飞书 | WebSocket 收消息、发文本、发卡片、更新卡片、下载附件 |
| 会话 | active/archived session、`/new`、per-channel 串行队列 |
| 执行 | Claude CLI stream-json、超时、resume、第三方 provider 注入 |
| 任务 | YAML 监听、cron 调度、启动恢复、系统任务 |
| 文档 | 中文 README、需求、PRD、概要设计、详细设计、开源审计 |

## 8. 验收指标

- 5 分钟内可按 README 完成本地配置文件准备。
- 新 app 初始化后自动拥有标准 workspace 目录结构。
- 单聊、群聊和话题群产生稳定 `channel_key`。
- 同一聊天连续消息不会并发执行。
- 发布前 secret scan 不发现真实密钥形态内容。
