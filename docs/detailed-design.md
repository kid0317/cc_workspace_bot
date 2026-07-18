# 详细设计

> 状态：开源版整理
> 最后更新：2026-07-18

## 1. 配置模型

入口：`internal/config/config.go`

```go
type Config struct {
    Apps    []AppConfig
    Server  ServerConfig
    Claude  ClaudeConfig
    Session SessionConfig
    Cleanup CleanupConfig
}
```

关键规则：

- `apps` 至少一个。
- `app.id` 不能为空，且不能包含 `/`。
- `feishu_app_id`、`feishu_app_secret`、`workspace_dir` 必填。
- `server.port` 默认 `8080`。
- `claude.timeout_minutes` 默认 `5`。
- `claude.max_turns` 默认 `20`。
- `session.worker_idle_timeout_minutes` 默认 `30`。
- `cleanup.schedule` 默认 `0 2 * * *`。

## 2. Channel Key

生成逻辑位于 `internal/feishu/receiver.go`。

| 飞书场景 | channel_key |
|---|---|
| 单聊 | `p2p:{chat_id}:{app_id}` |
| 普通群聊 | `group:{chat_id}:{app_id}` |
| 话题群 | `thread:{chat_id}:{thread_id}:{app_id}` |

`app_id` 使用本项目配置中的 workspace app ID，不是飞书 App ID。

## 3. 数据模型

入口：`internal/model/models.go`

| 表 | 主键 | 说明 |
|---|---|---|
| `channels` | `channel_key` | 飞书渠道镜像 |
| `sessions` | `id` | 一个 channel 内的对话 session |
| `messages` | `id` | 用户和助手消息记录 |
| `tasks` | `id` | `tasks/*.yaml` 的运行时镜像 |

每个 app 打开独立 DB：

```text
workspaces/<app-id>/bot.db
```

DB registry 在启动时创建，并把每个 app 的绝对 DB 路径写入 `AppConfig.DBPath`，供 `SESSION_CONTEXT.md` 注入。

## 4. Workspace 初始化

入口：`internal/workspace/init.go`

初始化动作：

1. 创建 workspace 根目录。
2. 创建 `.claude/skills/`、`memory/`、`tasks/`、`sessions/`。
3. 创建 `.memory.lock`。
4. 如果提供飞书 App ID / Secret，则写入 `.claude/skills/feishu_ops/feishu.json`，权限 `0600`。
5. 从 `workspaces/_template/` 复制缺失文件，跳过 symlink。

安全约束：

- `feishu.json` 是私有运行时文件，必须被 `.gitignore` 排除。
- 模板中只允许提交 `feishu.json.template`。

## 5. Receiver

入口：`internal/feishu/receiver.go`

职责：

- 创建飞书 SDK client。
- 注册 WebSocket event handler。
- 校验 `allowed_chats`。
- 解析文本、图片、文件、富文本。
- 下载附件到临时目录。
- 生成 `IncomingMessage` 并交给 dispatcher。

附件写入使用 `io.LimitReader` 限制最大 100 MiB。

## 6. Worker

入口：`internal/session/worker.go`

Worker 挂在 `channel_key` 上，队列深度 64。

处理流程：

1. 收到 `/new` 时归档当前 session，创建新 session。
2. 获取或创建 active session。
3. 移动附件到 `sessions/<session-id>/attachments/`。
4. 记录用户消息。
5. 纯附件消息先缓存并要求用户补充意图。
6. work 模式发送 thinking card。
7. 调用 Executor。
8. companion 模式读取 `FINAL_REPLY.md` 作为 hook 过滤后的结果。
9. 保存 assistant 消息。
10. 根据模式发送结果。

## 7. Executor

入口：`internal/claude/executor.go`

核心参数：

```text
claude
  -p <prompt>
  --cwd <workspace>/sessions/<session-id>
  --output-format stream-json
  --permission-mode <mode>
  --allowedTools <tools>
  --max-turns <n>
  --model <model>
  --effort <effort>
  --settings <json>
  --resume <claude_session_id>
```

关键实现：

- `SessionDirOverride` 必须位于 workspace 内。
- 过滤父进程的 `ANTHROPIC_*`、`CLAUDECODE*`、`WORKSPACE_DIR`、`CC_LF_*` 后再注入新环境变量。
- 第三方 provider 通过 `ANTHROPIC_BASE_URL`、`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_MODEL` 注入。
- 默认 Anthropic 且无自定义配置时不注入认证变量，使用 Claude CLI 自身登录态。
- `stream-json` 中的 system event 提取 `claude_session_id`，assistant text 拼接为最终输出。

## 8. Task

入口：`internal/task/`

YAML 示例：

```yaml
name: "每日总结"
cron: "0 18 * * 1-5"
target_type: "p2p"
target_id: "ou_xxx"
prompt: "整理今天的工作进展并发送摘要。"
send_output: true
created_by: "ou_xxx"
enabled: true
```

任务类型：

| 类型 | 条件 | 行为 |
|---|---|---|
| 用户回复任务 | `send_output=true` 且 target 完整 | Claude 输出直接发送给目标 |
| 借用 channel 后台任务 | `send_output=false` 且 target 完整 | 运行在目标 channel 上，但结果不自动发送 |
| 系统任务 | `send_output=false` 且无 target | 运行在 `sessions/_system/<slug>/`，不触达用户 |

`id` 和 `app_id` 由文件名和 watcher 注入，YAML 内同名字段不作为真源。

## 9. Companion 输出过滤

companion workspace 可通过 Stop hook 写入：

```text
sessions/<session-id>/FINAL_REPLY.md
```

Worker 在 Claude 退出后读取该文件。如果文件新鲜，则用其内容替换原始输出；如果缺失或为空，则 fail-open 使用原始输出。canary 在过滤器曾经成功后连续缺失多次时写日志告警。

## 10. 测试策略

| 范围 | 测试 |
|---|---|
| 配置 | 必填项、默认值、provider 覆盖 |
| DB | SQLite 打开、迁移、registry |
| Receiver | channel key、消息解析、allowed chat |
| Session | `/new`、附件缓存、分段、输出过滤 |
| Task | YAML 加载、校验、scheduler、runner |
| Claude | stream-json 解析、resume sanitize、provider env |

发布前必须运行：

```bash
go build ./...
go test ./... -cover
go vet ./...
```
