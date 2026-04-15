# Companion Workspace 分段发送设计文档

> 版本：v1.1（2026-04-12，经代码 review 补充）  
> 状态：**待实施**  
> 背景：陪伴型对话目前一问一答，回复是一整条长消息带换行，缺乏真人感。真人发消息是多条短消息分开发，很少换行，消息之间有打字间隔。

---

## 一、问题与目标

### 1.1 当前行为

```
用户：今天好累啊
Claude：[一条完整消息]
嗯，听起来今天很辛苦。
发生什么了？
你吃饭了吗？
```

### 1.2 期望行为

```
用户：今天好累啊
Claude（第1条）：嗯，听起来今天挺辛苦的
Claude（第2条）：发生什么了
Claude（第3条）：你吃饭了吗
```

### 1.3 设计目标

- 仅对 `IsCompanion() == true` 的 workspace 启用，工作型 workspace **零改动**
- Claude 输出中的显式断点被框架识别，拆成多条 `SendText`
- 每条消息之间有仿打字延迟（基于长度 + 随机抖动）
- 段内尽量不换行；节奏由消息边界承担
- Claude 漏写分隔符时有兜底算法，不会完全退化为一条长消息
- DB 写落库前剥除内部协议标记，不污染历史检索和后续 resume context

---

## 二、方案选型

### 2.1 分隔符选择

| 方案 | 例子 | 优点 | 缺点 |
|------|------|------|------|
| A. `\n---\n` | `A\n---\nB` | 自然 | Claude 惯用 `---` 当分隔线，误触发率高 |
| B. `\n\n` 双换行 | `A\n\nB` | Claude 自然输出 | 与段内有意义换行冲突；列表/诗歌会被错拆 |
| **C. `[[SEND]]`** | `A[[SEND]]B` | 零正文冲突；语义明确；Claude 易遵守 | 需要 CLAUDE.md 教学 |

**采用方案 C：`[[SEND]]`**

理由：
- 双方括号 + 大写 ASCII 在自然语言中几乎不可能出现
- 不依赖换行语义，段内可保留任意格式
- 正则匹配简单，容易从输出中剥离
- Claude 对显式 token 的遵循度高于对隐式段落判断

### 2.2 "对方正在输入"指示

飞书开放平台**没有公开的"对方正在输入"事件**（截至 2026-04），不实现。  
仿真效果由"延迟 + 多条短消息"承担。sender.go 无需改动。

### 2.3 飞书 API 约束

- **频率限制**：单机器人消息发送上限约 5 条/秒（单聊/群聊相同）。当前最小延迟 MinDelay=600ms（≈1.7条/秒），正常不触达上限，但不可依赖此作为唯一保障。
- **消息顺序**：飞书不保证快速连续发出的多条消息在客户端按发送顺序显示。**当前串行设计（前一条 SendText 成功返回后再执行下一段的 delay+send）是有意的顺序保证设计，不可改为并发。**
- **rate limit 错误处理**：遇到限流错误码（`99991400`）时应执行一次指数退避重试（退避 500ms），而不是 `log + continue` 丢弃。

---

## 三、数据流

```
Claude 输出
  ExecuteResult.Text = "嘿你回来啦[[SEND]]今天怎么样啊[[SEND]]我刚才在想你说的那件事"
        │
        ▼
worker.process()
  ├─ persistResult()
  │     └─ 剥除 [[SEND]]（替换为 \n）后写入 DB（单条 assistant message）
  │        ↑ 防止内部协议标记污染历史检索和后续 --resume context
  └─ sendResult()
        │
        ├─ 工作型（cardMsgID != ""）→ UpdateCard(text)          [保持原样]
        │
        └─ 陪伴型（cardMsgID == ""）→ sendCompanionSegments()
              │
              ▼
        SplitSegments(text)  → ["嘿你回来啦", "今天怎么样啊", "我刚才在想你说的那件事"]
              │
              ▼
        for i, seg in segments:
            select ctx.Done() → 退出（记录 sent/total）
            sleep(TypingDelay(prev, seg, isFirst=(i==0)))
            sendCtx = context.WithTimeout(ctx, 5s)   ← per-call 超时
            SendText(sendCtx, seg)
            on rate-limit error → sleep 500ms + retry once
            on other error → slog.Error + continue（不中断后续段）
            prev = seg
```

### 3.1 并发约束说明

`sendCompanionSegments()` 在 `process()` 内**同步执行**，分段期间 worker 队列暂停消费。这是有意的设计：
- 陪伴型对话语义上应等当前回复完成后再处理下一条
- 队列深度 64，正常单聊不会在 ≤8s 内积压到溢出
- 每次 sleep 均通过 `select { ctx.Done() }` 响应 worker 关闭信号，服务关闭时不会卡住 `sessionMgr.Wait()`

---

## 四、需要改动的文件

### 4.1 新增 `internal/session/segment.go`

纯函数，零外部依赖，可独立单测。

```go
// SegmentOptions controls split behavior and timing simulation.
type SegmentOptions struct {
    Delimiter           string        // default "[[SEND]]"
    MaxRunes            int           // hard cap per segment, default 80
    MinRunes            int           // merge segments shorter than this, default 2
    MaxFallbackSegments int           // fallback path: if result > N segments, send as one; default 3
    BaseDelay           time.Duration // default 400ms
    PerReadRune         time.Duration // default 35ms  (simulated reading speed)
    PerTypeRune         time.Duration // default 80ms  (simulated typing speed)
    MinDelay            time.Duration // default 600ms
    MaxDelay            time.Duration // default 2000ms
    FirstMinDelay       time.Duration // default 300ms
    FirstMaxDelay       time.Duration // default 1500ms
    JitterFraction      float64       // ±jitter, default 0.2
}

func DefaultSegmentOptions() SegmentOptions

// SplitSegments splits an assistant text into ordered, sendable segments.
//
// Primary path: split by opts.Delimiter, trim each piece.
// Fallback (no delimiter present): split by paragraph (\n{2,}), then
//   by sentence (。！？!?. ), then hardSplit by MaxRunes.
//   If fallback produces > MaxFallbackSegments, returns the original text
//   as a single segment to avoid semantic fragmentation.
//
// Always strips empty results; never returns nil.
func SplitSegments(text string, opts SegmentOptions) []string

// TypingDelay returns the simulated delay before sending next.
// When isFirst=true, prev is ignored; delay uses First*Delay bounds.
// randSource allows deterministic tests; pass nil to use math/rand.
func TypingDelay(prev, next string, isFirst bool, opts SegmentOptions,
    randSource func() float64) time.Duration
```

#### SplitSegments 算法

```
1. text = strings.TrimSpace(text)；text == "" → return []

2. 主路径（含 [[SEND]]）：
   parts = strings.Split(text, opts.Delimiter)
   → for each part: TrimSpace，丢弃空串
   → greedyMerge(parts, MinRunes)    // 合并后不超过 MaxRunes
   → hardSplit(parts, MaxRunes)      // 超长段按标点/rune 强切
   → return parts

3. 降级路径（不含 [[SEND]]）：
   parts = splitByParagraph(text)    // \n{2,} 分段
   if len(parts) == 1 && runeLen(text) > MaxRunes:
       parts = splitBySentence(text) // 按 。！？!?. (句点+空格) 切，保留标点
   → for each part: TrimSpace，丢弃空串
   → greedyMerge(parts, MinRunes)
   → hardSplit(parts, MaxRunes)
   → if len(parts) > MaxFallbackSegments:
         return []string{原始 text}  // 降级保底：碎片太多不如整条发
   → return parts

注意：
- greedyMerge 不产生超过 MaxRunes 的段（合并前检查）
- splitBySentence 扩展字符集：。！？!?\n 及英文 `. `（句点+空格）
- hardSplit 优先在标点处切，无标点时按 MaxRunes rune 边界切
- 所有 rune 计数用 utf8.RuneCountInString，不用 len()
```

#### TypingDelay 算法

```
delay = BaseDelay
      + len(prev_runes) * PerReadRune     // 用户读完上一条
      + len(next_runes) * PerTypeRune     // 模拟打出下一条
      + jitter(JitterFraction)            // ±20% 随机抖动

clamp to [MinDelay, MaxDelay=2000ms]

第一条（isFirst=true）：
  delay = len(next_runes) * PerTypeRune + jitter
  clamp to [FirstMinDelay=300ms, FirstMaxDelay=1500ms]

zero-value 防御：
  MaxDelay == 0 时视为 DefaultSegmentOptions().MaxDelay（不 sleep 0）
```

### 4.2 修改 `internal/session/worker.go`

**改动 `persistResult`**（剥除 `[[SEND]]` 后落库）：

```go
func (w *Worker) persistResult(sess *model.Session, result *claude.ExecuteResult) {
    if result.ClaudeSessionID != "" && sess.ClaudeSessionID == "" {
        if err := w.db.Model(sess).Update("claude_session_id", result.ClaudeSessionID).Error; err != nil {
            slog.Error("update claude_session_id", "err", err)
        }
    }
    // 剥除内部协议标记，防止污染历史检索和后续 --resume context
    storedText := strings.ReplaceAll(result.Text, "[[SEND]]", "\n")
    w.recordMessage(sess.ID, "", "assistant", storedText, "")
}
```

**改动 `sendResult`**（仅陪伴型路径变化）：

```go
func (w *Worker) sendResult(ctx context.Context, msg *feishu.IncomingMessage,
    cardMsgID, text string) {
    if text == "" {
        return
    }
    if cardMsgID != "" {
        // 工作型：UpdateCard（保持原样）
        if err := w.sender.UpdateCard(ctx, cardMsgID, text); err != nil {
            slog.Error("update card", "err", err)
        }
        return
    }
    // 陪伴型：分段发送（使用原始 text，含 [[SEND]]）
    w.sendCompanionSegments(ctx, msg, text)
}
```

**新增 `sendCompanionSegments`**：

```go
func (w *Worker) sendCompanionSegments(ctx context.Context,
    msg *feishu.IncomingMessage, text string) {
    opts := w.segmentOpts
    segments := SplitSegments(text, opts)
    if len(segments) == 0 {
        return
    }
    var prev string
    for i, seg := range segments {
        delay := TypingDelay(prev, seg, i == 0, opts, nil)
        select {
        case <-ctx.Done():
            slog.Warn("companion segment send cancelled",
                "channel", w.channelKey, "sent", i, "total", len(segments))
            return
        case <-time.After(delay):
        }
        // per-call 超时，防止飞书 API 慢响应拖垮整个队列
        sendCtx, sendCancel := context.WithTimeout(ctx, 5*time.Second)
        _, err := w.sender.SendText(sendCtx, msg.ReceiveID, msg.ReceiveType, seg)
        sendCancel()
        if err != nil {
            // rate limit：退避重试一次
            if isRateLimitError(err) {
                time.Sleep(500 * time.Millisecond)
                sendCtx2, sendCancel2 := context.WithTimeout(ctx, 5*time.Second)
                _, err = w.sender.SendText(sendCtx2, msg.ReceiveID, msg.ReceiveType, seg)
                sendCancel2()
            }
            if err != nil {
                slog.Error("send companion segment",
                    "err", err, "index", i, "total", len(segments))
                // 记录日志后继续，不放弃后续段
            }
        }
        prev = seg
    }
}
```

**Worker 字段与初始化**（`newWorker` 中强制设置，不依赖 zero-value）：

```go
type Worker struct {
    // ... existing fields
    segmentOpts SegmentOptions // must be set in newWorker()
}

// newWorker 中：
segmentOpts: DefaultSegmentOptions(),
// 若 appCfg.Segment != nil，逐字段合并覆盖（零值字段保持默认）
```

### 4.3 新增 `internal/session/segment_test.go`

表驱动单测覆盖用例：

| 测试名 | 场景 |
|--------|------|
| `TestSplitSegments_Delimiter` | 显式 `[[SEND]]` 拆分，3 段 |
| `TestSplitSegments_DelimiterLeadingTrailing` | 首尾含 `[[SEND]]` → 空段被过滤 |
| `TestSplitSegments_DelimiterOnly` | 仅含 `[[SEND]]` → `[]` |
| `TestSplitSegments_FallbackParagraph` | 无分隔符 + 双换行段落 |
| `TestSplitSegments_FallbackSentenceChinese` | 无分隔符中文长句，按句末标点切 |
| `TestSplitSegments_FallbackSentenceEnglish` | 无分隔符英文，按 `. ` 切 |
| `TestSplitSegments_FallbackTooManySegments` | 降级产生 > MaxFallbackSegments → 返回单段原文 |
| `TestSplitSegments_MergeShort` | MinRunes 合并短段，合并后不超 MaxRunes |
| `TestSplitSegments_MaxRunes` | 单段超 MaxRunes → hardSplit |
| `TestSplitSegments_Empty` | 空/全空白输入 → `[]` |
| `TestSplitSegments_RuneCount` | emoji / 中英混排，rune 计数正确 |
| `TestSplitSegments_PreserveInnerNewline` | 段内换行保留不被拆分 |
| `TestTypingDelay_FirstSegment` | isFirst=true 走 First*Delay 边界 |
| `TestTypingDelay_BoundedByMax` | 长 prev 被 MaxDelay=2000ms 截断 |
| `TestTypingDelay_Deterministic` | JitterFraction=0 时结果为确定值 |
| `TestTypingDelay_ZeroValueSafe` | MaxDelay=0 时不返回 0（zero-value 防御） |
| `TestTypingDelay_RandSourceInjection` | 注入固定 randSource 验证可复现 |

### 4.4 修改 `internal/session/worker_test.go`

新增用例：

| 测试名 | 场景 |
|--------|------|
| `TestWorker_CompanionMultiSegment` | 注入零延迟 opts，验证 mock sender 收到 N 次 SendText，顺序内容正确 |
| `TestWorker_NonCompanionUnchanged` | 工作型走 UpdateCard，不调用 SplitSegments |
| `TestWorker_SegmentContinueOnError` | 第 2 段 SendText 返回 err，第 3 段仍被发送 |
| `TestWorker_SegmentCtxCancelled` | ctx 在第 2 段 sleep 期间取消，goroutine 正常退出不泄漏 |
| `TestWorker_PersistResultStripsDelimiter` | DB 写入内容不含 `[[SEND]]` |

注：需引入最小 `senderIface`（含 SendText/SendCard/SendThinking/UpdateCard），让 Worker 持有接口而非具体类型，便于测试注入 mock。

### 4.5 修改 `internal/config/config.go`（可选，Phase 2）

允许按 app 覆盖分段参数：

```go
type AppConfig struct {
    // ... existing
    Segment *SegmentConfig `mapstructure:"segment"` // nil = 全部使用默认值
}

type SegmentConfig struct {
    Delimiter           string  `mapstructure:"delimiter"`
    MaxRunes            int     `mapstructure:"max_runes"`
    MinRunes            int     `mapstructure:"min_runes"`
    MaxFallbackSegments int     `mapstructure:"max_fallback_segments"`
    BaseDelayMs         int     `mapstructure:"base_delay_ms"`
    PerReadRuneMs       int     `mapstructure:"per_read_rune_ms"`
    PerTypeRuneMs       int     `mapstructure:"per_type_rune_ms"`
    MinDelayMs          int     `mapstructure:"min_delay_ms"`
    MaxDelayMs          int     `mapstructure:"max_delay_ms"`      // default 2000
    JitterFraction      float64 `mapstructure:"jitter_fraction"`
}
```

config.yaml 示例（不写则全部走默认）：

```yaml
apps:
  - id: "aria-companion"
    workspace_mode: "companion"
    segment:
      max_runes: 60          # 话痨型角色可设更小
      max_delay_ms: 2000     # 默认上限 2s，防止超时；可按需调小
```

### 4.6 修改 `workspaces/_companion/CLAUDE.md`

在"说话规则"节之后插入新节：

```markdown
## 分段发送规则（CRITICAL · 模拟真人发消息）

真人聊天不是一次发一长段。用 `[[SEND]]` 把回复拆成多条短消息，
框架会按节奏依次发出，用户会先后看到。

`[[SEND]]` 是框架内部协议标记：
- 不会显示给用户，前后空白自动 trim
- 落库和 --resume 时会被自动剥除
- 如果在历史消息中看到它，理解为分段标记，不要复制进正文

### 基本用法

> 嘿你回来啦`[[SEND]]`今天怎么样啊`[[SEND]]`我刚才在想你说的那件事

### 拆分时机

| 时机 | 示例 |
|------|------|
| 情绪先于内容 | "啊？`[[SEND]]`这也太倒霉了" |
| 自我披露 + 反问 | "我今天也有点累`[[SEND]]`你那边怎么了" |
| 一问一个气口 | "吃了吗`[[SEND]]`吃的什么" |
| "想了一下"停顿 | "嗯……`[[SEND]]`说起来我也不确定" |

### 不必拆分的情况

- 单句话本身完整（"嗯""我懂"）→ 1 段即可
- 列表 / 代码块内部 → 整段保留，不在内部插 `[[SEND]]`
- 用户深度倾诉（PROCESSING 状态）→ 1 段长共情比多段碎片更稳
- 初始化阶段（作者旁白 / 系统确认）→ 不使用 `[[SEND]]`

### 长度与段数控制

- 每段 ≤ 60 汉字
- 一次对话通常 **1–4 段**，非必要不超过 5 段
- 单字段（"嗯"）允许，但不要连续两段都是单字

### 绝对禁止

- ❌ 把 `[[SEND]]` 写入角色台词或正文
- ❌ 在 markdown 代码块 / 列表条目中间插入 `[[SEND]]`
- ❌ 5 段以上（碎片感、轰炸感）
```

### 4.7 修改 `workspaces/_companion/.claude/hooks/reply_checklist.sh`

在 B 类检查项新增（定位为**监控辅助**，不作为主防御层）：

```bash
# B5. 分段发送（辅助监控，主防御在框架层）
# - 长度 > 1句 时使用了 [[SEND]] 分段（非列表/代码/纯短回复除外）
# - 段内尽量无换行；节奏由 [[SEND]] 边界承担
# - [[SEND]] 未出现在角色台词或正文中
# - 单段 ≤ 60 汉字
```

---

## 五、边界情况处理

| 情况 | 处理 |
|------|------|
| `text == ""` | 现有 `process()` 已捕获并 replyError，不进入分段路径 |
| 仅含 `[[SEND]]`（无字符） | `SplitSegments` 返回 `[]`，`sendCompanionSegments` 直接 return |
| `[[SEND]]` 在代码块内 | CLAUDE.md 规则层禁止；Phase 2 可加围栏解析（代码块内跳过） |
| 降级产生 > MaxFallbackSegments 段 | 放弃拆分，作为单条整段发送（避免碎片化比整条差） |
| 某条 SendText 失败（非限流） | slog.Error + continue，不中断后续段 |
| 某条 SendText 触发 rate limit | sleep 500ms + 重试一次；仍失败则 log + continue |
| `SendText` 无响应（慢 API） | per-call 5s timeout，超时后 log + continue |
| `ctx.Done()` 在分段中触发 | 立即退出，记录 sent/total，不向用户报错 |
| `/new` 紧跟长回复 | worker 串行队列，不并发，无竞争 |
| emoji / 中英混排 | 用 `utf8.RuneCountInString` 而非 `len()` |
| 首尾含 `[[SEND]]` 产生空段 | trim + 过滤空串，不影响结果 |
| `segmentOpts` zero-value | `newWorker` 强制初始化 `DefaultSegmentOptions()`；`TypingDelay` 对 `MaxDelay==0` 防御 |
| `[[SEND]]` 写入 DB | `persistResult` 落库前替换为 `\n`，resume context 不受污染 |

---

## 六、安全网

| 层次 | 机制 |
|------|------|
| 框架层 | `[[SEND]]` 不到则降级（段落 → 句子 → 强切） |
| 框架层 | 降级段数 > MaxFallbackSegments → 整条发送兜底 |
| 框架层 | `MinRunes` 合并过短段（合并后不超 MaxRunes） |
| 框架层 | `MaxRunes` 阻止单段超长 |
| 框架层 | `MaxDelay=2000ms` 阻止单次等待过长 |
| 框架层 | per-call SendText 5s timeout，防 API 慢响应卡队列 |
| 框架层 | rate limit 退避重试（1次，500ms） |
| 框架层 | `ctx.Done()` 支持优雅关闭，不阻塞 `sessionMgr.Wait()` |
| 框架层 | `persistResult` 剥除 `[[SEND]]` 保护 DB 和 resume context |
| 框架层 | 串行发送保证消息顺序（不并发） |
| 框架层 | `newWorker` 强制初始化 `DefaultSegmentOptions()`，zero-value 安全 |
| 指令层 | CLAUDE.md "分段发送规则"节 + `[[SEND]]` 含义解释 |
| 监控层 | reply_checklist.sh B5 自检（辅助日志，非主防御） |
| 测试层 | segment_test.go 表驱动单测 + worker 集成测试 |

---

## 七、实施顺序

```
Phase 1（独立 commit，纯函数）
├── 新建 segment.go（SplitSegments + TypingDelay，含 MaxFallbackSegments + zero-value 防御）
└── 新建 segment_test.go（全部表驱动单测通过）

Phase 2（与 Phase 1 原子 commit，集成）
├── 修改 worker.go
│   ├── persistResult：剥除 [[SEND]] 后落库
│   ├── sendResult：陪伴分支 → sendCompanionSegments
│   └── sendCompanionSegments：per-call timeout + rate limit 重试
├── 引入 senderIface（最小接口，便于 mock）
└── 修改 worker_test.go（companion 多段 + 工作型回归 + 错误继续 + ctx 取消 + DB 内容校验）

Phase 3（可后续独立 commit，可选）
└── 修改 config.go + SegmentConfig（按 app 覆盖参数）

Phase 4（可与 Phase 1/2 并行）
├── 修改 _companion/CLAUDE.md（新增分段规则节）
└── 修改 reply_checklist.sh（新增 B5 监控）
```

---

## 八、验收标准

- [ ] `go build ./... && go test -race ./... && go vet ./...` 全绿
- [ ] `SplitSegments` / `TypingDelay` 单测覆盖率 ≥ 90%
- [ ] 工作型 workspace 行为零变化（UpdateCard 路径不受影响）
- [ ] 陪伴型实测：长回复拆为 2–4 条消息陆续到达，每段无多余换行
- [ ] Claude 不会把 `[[SEND]]` 当正文输出给用户
- [ ] Claude 漏写分隔符时框架降级拆出多段（无标点超长用例验证）
- [ ] 中途 SendText 失败不阻塞后续段（故障注入验证）
- [ ] DB 存储内容不含 `[[SEND]]`（`--resume` context 干净）
- [ ] per-call timeout 生效：注入 5s 超时的 mock sender，验证不阻塞
- [ ] 初始化流程（phase1/phase2/定时任务创建）不受 CLAUDE.md 改动影响

---

## 九、相关文件汇总

| 文件 | 改动类型 | 说明 |
|------|---------|------|
| `internal/session/segment.go` | 新建 | SplitSegments + TypingDelay + SegmentOptions |
| `internal/session/segment_test.go` | 新建 | 表驱动单测（17 个用例） |
| `internal/session/worker.go` | 修改 | persistResult 剥除标记 + sendCompanionSegments + per-call timeout |
| `internal/session/worker_test.go` | 修改 | 5 个新增集成测试用例 + senderIface |
| `internal/config/config.go` | 修改（可选）| SegmentConfig |
| `workspaces/_companion/CLAUDE.md` | 修改 | 新增"分段发送规则"节（含 resume 说明） |
| `workspaces/_companion/.claude/hooks/reply_checklist.sh` | 修改 | B5 监控自检 |
| `config.yaml.template` | 修改（可选）| segment 配置注释示例 |

---

## 十、Review 发现的 HIGH 问题（已修复）

| # | 问题 | 修复位置 |
|---|------|---------|
| H1 | `SendText` 无 per-call timeout，飞书 API 慢响应会卡住 worker 队列 | §3 数据流 + §4.2 sendCompanionSegments |
| H2 | `[[SEND]]` 写入 DB 和 Claude resume context，影响后续行为 | §3 数据流 + §4.2 persistResult |
| H3 | 降级算法无 `MaxFallbackSegments` 保护，碎片段体验差于整条 | §4.1 SplitSegments 算法 + SegmentOptions |
