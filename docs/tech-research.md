# 技术调研报告

> 状态: 已落地
> 最后更新: 2026-03-05

## 一、飞书 SDK（larksuite/oapi-sdk-go v3）

### WebSocket 模式（larkws）

相比 Webhook 模式的优势：
- 本地开发无需公网 IP 或内网穿透
- 连接建立时一次鉴权，后续推送无需处理签名和解密
- 适合企业内部部署（只需能出公网）

核心用法（已实现于 `internal/feishu/receiver.go`）：

```go
import (
    larkws   "github.com/larksuite/oapi-sdk-go/v3/ws"
    "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
    larkim   "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// 1. 创建事件分发器
eventHandler := dispatcher.NewEventDispatcher(verificationToken, encryptKey).
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

| `ChatType` | 含义 | channel_key 格式 |
|---|---|---|
| `p2p` | 私聊 | `p2p:{open_id}:{app_id}` |
| `group` | 群聊 | `group:{chat_id}:{app_id}` |
| `topic` / `topic_group` | 话题群 | `thread:{chat_id}:{thread_id}:{app_id}` |

### 消息内容解析（已实现）

| `MessageType` | Content JSON | 处理方式 |
|---|---|---|
| `text` | `{"text":"..."}` | 直接提取 |
| `image` | `{"image_key":"..."}` | `MessageResource.Get` 下载 |
| `file` | `{"file_key":"...","file_name":"..."}` | `MessageResource.Get` 下载 |
| `post` | 富文本结构 | 提取 text 段落拼接 |

### 消息发送 API（已实现于 `internal/feishu/sender.go`）

```go
// 发送交互式卡片（思考中...）
client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
    ReceiveIdType("chat_id").
    Body(larkim.NewCreateMessageReqBodyBuilder().
        MsgType(larkim.MsgTypeInteractive).
        ReceiveId(chatID).
        Content(cardJSON).
        Build()).
    Build())

// PATCH 更新卡片内容（最终结果）
client.Im.Message.Patch(ctx, larkim.NewPatchMessageReqBuilder().
    MessageId(messageID).
    Body(larkim.NewPatchMessageReqBodyBuilder().
        Content(newCardJSON).
        Build()).
    Build())
```

---

## 二、技术选型（最终落地）

| 组件 | 选型 | 理由 |
|---|---|---|
| 飞书连接 | `oapi-sdk-go/v3` + larkws | 官方 SDK，WS 模式免公网 |
| 配置 | Viper | YAML + 结构体映射，支持默认值 |
| ORM | GORM | 快速开发，AutoMigrate |
| 数据库 | SQLite WAL（`glebarez/sqlite`）| CGO-free，单机够用，30k TPS |
| 定时任务 | `gocron/v2` | 积极维护，API 简洁 |
| 文件监听 | `fsnotify` | 跨平台 inotify 封装 |
| 日志 | `log/slog`（标准库）| Go 1.21+ 内置，零依赖 |
| Claude 集成 | `exec.Cmd` | 直接调用 claude CLI，控制最大 |
| Cron 解析 | `robfig/cron/v3` | gocron 间接依赖，复用于表达式校验 |
| UUID | `google/uuid` | Session / Task ID 生成 |

### SQLite 注意事项

使用 CGO-free 版本避免交叉编译问题：

```go
import (
    "github.com/glebarez/sqlite"   // 封装 modernc.org/sqlite
    "gorm.io/gorm"
)

db, _ := gorm.Open(sqlite.Open("file:bot.db?_journal_mode=WAL&_busy_timeout=5000"), &gorm.Config{})
```

---

## 三、Claude CLI 集成（已实现于 `internal/claude/executor.go`）

### 关键标志

| 标志 | 说明 |
|---|---|
| `--cwd <path>` | 指定工作目录（加载该目录的 CLAUDE.md、skills 等）|
| `--resume <session-id>` | 复用历史会话上下文 |
| `-p <prompt>` | 非交互模式，指定 prompt 并执行后退出 |
| `--output-format stream-json` | 流式 NDJSON 输出 |
| `--permission-mode acceptEdits` | 自动接受文件编辑操作 |
| `--allowedTools "..."` | 限制可用工具，空格分隔 |
| `--max-turns <n>` | 限制最大 agentic 轮次 |

### 推荐调用示例

```
claude \
  -p "Review this MR and implement the requested changes" \
  --cwd /workspaces/sessions/abc-123 \
  --permission-mode acceptEdits \
  --allowedTools "Bash Read Edit Write mcp__feishu" \
  --output-format stream-json \
  --max-turns 20 \
  --resume <claude_session_id>
```

### stream-json 输出格式（NDJSON）

```json
{"type":"system","session_id":"xxx","cwd":"/ws/proj","model":"claude-opus-4-6"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"..."}]}}
{"type":"result","subtype":"success","cost_usd":0.01,"duration_ms":3200}
```

解析策略：
- `system` 事件：提取 `session_id` 作为 `claude_session_id` 写入 DB
- `assistant` 事件：拼接 `content[].text`（用 `strings.Builder`）
- `result` 事件：记录 cost / duration

### 子进程安全实践（已实现）

```go
ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
defer cancel()

cmd := exec.CommandContext(ctx, "claude", args...)
cmd.Dir = sessionDir
cmd.WaitDelay = 30 * time.Second           // 强制关闭孤立 pipe
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // 进程组，防信号泄漏

// Scanner 设置 1 MiB 缓冲，防止大响应截断
scanner := bufio.NewScanner(stdout)
scanner.Buffer(make([]byte, 1<<20), 1<<20)

// stderr 用 sync.WaitGroup 追踪，cmd.Wait() 后 Join
```

### 注意事项

- `--cwd` 设置工作目录，claude 会向上查找 CLAUDE.md；session 目录在 workspace 子目录内，会自然继承 workspace 级配置
- 非 TTY 环境设置 `TERM=xterm-256color` + `FORCE_COLOR=0` 防止输出缓冲
- Scanner 默认 64 KiB 限制已通过 `scanner.Buffer` 扩展到 1 MiB

---

## 四、并发与安全设计

### 初始化循环依赖

`feishu.Receiver` 需要 `Dispatcher` 接口，`session.Manager` 需要 `feishu.Sender`。通过 `dispatchForwarder` 打破：

```go
type dispatchForwarder struct {
    target atomic.Pointer[session.Manager]
}
// 在所有 goroutine 启动前通过 atomic.Store 写入
fwd.target.Store(sessionMgr)
```

### 优雅关闭流程

```
SIGINT/SIGTERM
  → ctx cancel
  → taskScheduler.Stop()
  → sessionMgr.Wait()         ← 等待所有 worker 完成
  → httpServer.Shutdown(10s)
```

### 附件安全

- 下载限制：`io.LimitReader(body, 100 MiB)`
- `DownloadURL` 接受 `context.Context`，可被取消
- 附件移入 session/attachments/，7 天后归档清理（30 天强制）
