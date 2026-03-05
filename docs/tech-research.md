# 技术调研报告

> 状态: 初稿
> 最后更新: 2026-03-05

## 一、飞书 SDK（larksuite/oapi-sdk-go v3）

### WebSocket 模式（larkws）

相比 Webhook 模式的优势：
- 本地开发无需公网 IP 或内网穿透
- 连接建立时一次鉴权，后续推送无需处理签名和解密
- 适合企业内部部署（只需能出公网）

核心用法：

```go
import (
    larkws   "github.com/larksuite/oapi-sdk-go/v3/ws"
    "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
    larkim   "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// 1. 创建事件分发器
eventHandler := dispatcher.NewEventDispatcher("", "").
    OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
        msg := event.Event.Message
        // ChatType: "p2p" | "group" | "topic"
        // ThreadId: 话题群的 thread id
        return nil
    })

// 2. 创建 WS 客户端并启动
wsClient := larkws.NewClient("APP_ID", "APP_SECRET",
    larkws.WithEventHandler(eventHandler),
    larkws.WithLogLevel(larkcore.LogLevelInfo),
)
wsClient.Start(context.Background()) // 阻塞
```

### 消息类型区分

| `ChatType` | 含义 | Session Key |
|---|---|---|
| `p2p` | 私聊 | `p2p:{open_id}` |
| `group` | 群聊（@Bot）| `group:{chat_id}` |
| `topic` | 话题群 | `thread:{thread_id}` |

### 消息发送 API

```go
// 发送文本消息
client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
    ReceiveIdType(larkim.ReceiveIdTypeChatId).
    Body(larkim.NewCreateMessageReqBodyBuilder().
        MsgType(larkim.MsgTypeText).
        ReceiveId(chatID).
        Content(`{"text":"hello"}`).
        Build()).
    Build())

// 回复消息（保持在同一 thread）
// ReplyToMessageId 字段指定被回复的消息 ID
```

### 卡片消息 + 流式更新（Typing 模拟）

飞书没有原生 Typing Indicator API，标准做法：
1. 收到消息后立即发送"思考中..."卡片（`MsgTypeInteractive`）
2. Claude 输出过程中定期 PATCH 更新卡片内容
3. 完成后最终 PATCH 写入完整结果

```go
// 更新已发送的卡片
client.Im.Message.Patch(ctx, larkim.NewPatchMessageReqBuilder().
    MessageId(messageID).
    Body(larkim.NewPatchMessageReqBodyBuilder().
        Content(newCardContent).
        Build()).
    Build())
```

---

## 二、技术选型

### 最终选型

| 组件 | 选型 | 理由 |
|---|---|---|
| 飞书连接 | `oapi-sdk-go/v3` + larkws | 官方 SDK，WS 模式免公网 |
| HTTP 框架 | `net/http` + `chi`（可选） | SDK 返回标准 Handler，chi 零依赖 |
| 定时任务 | `gocron/v2` | 积极维护，支持分布式锁，robfig/cron 已停更 |
| ORM | GORM | 数据模型简单，快速开发，支持 SQLite/PostgreSQL |
| 数据库 | SQLite（先行） | 单机部署够用，WAL 模式 30k TPS；预留 PostgreSQL 升级 |
| 配置 | Viper | 支持 YAML + ENV 覆盖，12-Factor |
| 日志 | `log/slog`（标准库） | Go 1.21+ 内置，零依赖 |
| Claude 集成 | `exec.Cmd` | 直接调用 claude CLI，控制最大 |

### SQLite 注意事项

使用 CGO-free 版本避免交叉编译问题：

```go
import _ "modernc.org/sqlite"
// 开启 WAL 模式提升并发读写
db, _ := gorm.Open(sqlite.Open("file:bot.db?_journal_mode=WAL"), &gorm.Config{})
```

---

## 三、Claude CLI 集成

### 关键标志

| 标志 | 说明 |
|---|---|
| `--cwd <path>` | 指定 workspace 目录（加载该目录的 CLAUDE.md、skills 等）|
| `--resume <session-id>` | 复用历史会话上下文 |
| `-p <prompt>` | 非交互模式，指定 prompt 并执行后退出 |
| `--output-format stream-json` | 流式 JSON 输出（NDJSON）|
| `--permission-mode acceptEdits` | 自动接受文件编辑操作（推荐，比 dangerously-skip-permissions 更精确）|
| `--allowedTools "Bash Read Edit Write mcp__xxx"` | 限制 claude 可用工具，空格分隔，支持 MCP 工具 |
| `--max-turns <n>` | 限制最大 agentic 轮次，防止失控 |
| `--debug` | 输出调试信息 |

### 推荐调用示例（参考实际用法）

```
claude \
  -p "Review this MR and implement the requested changes" \
  --cwd /workspaces/code-review \
  --permission-mode acceptEdits \
  --allowedTools "Bash Read Edit Write mcp__feishu" \
  --output-format stream-json \
  --resume <claude_session_id>
```

**权限模式对比**：

| 模式 | 说明 | 适用场景 |
|---|---|---|
| `--permission-mode acceptEdits` | 自动接受文件编辑，其余操作仍确认 | 大多数场景 |
| `--dangerously-skip-permissions` | 跳过所有权限确认 | 完全受控的自动化环境 |

**`--allowedTools` 的作用**：限制 claude 能调用的工具范围，有利于安全控制。每个应用可在配置中定义不同的 allowedTools。

### 会话复用

```
首次调用 → claude --cwd /ws/proj --print --output-format stream-json "问题"
           → 从 stream-json 的 system 事件中提取 session_id

后续调用 → claude --cwd /ws/proj --resume <session_id> --print --output-format stream-json "追问"
```

### stream-json 输出格式

输出为 NDJSON，每行一个事件：

```json
{"type":"system","session_id":"xxx","cwd":"/ws/proj","model":"claude-opus-4-6"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"..."}]}}
{"type":"result","subtype":"success","cost_usd":0.01,"duration_ms":3200}
```

### 子进程最佳实践

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()

cmd := exec.CommandContext(ctx, "claude", args...)
cmd.Dir = workspaceDir
cmd.WaitDelay = 30 * time.Second          // 强制关闭孤立 pipe
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // 进程组，防信号泄漏

pr, pw := io.Pipe()
cmd.Stdout = pw

go func() { defer pw.Close(); cmd.Run() }()

scanner := bufio.NewScanner(pr)
for scanner.Scan() {
    // 逐行解析 JSON 事件
}
```

### 注意事项

- `--cwd` 设置工作目录，但 claude 会向上查找 CLAUDE.md；若需完全隔离，在 workspace 根目录创建 `.git/HEAD` 阻止向上遍历
- 非 TTY 环境可能有输出缓冲，设置 `TERM=xterm-256color` 或 `FORCE_COLOR=0` 环境变量
- 单个 session 的 context 越长消耗 token 越多，需要定期 compact 或限制历史长度
