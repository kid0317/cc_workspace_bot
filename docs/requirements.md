# 需求文档

> 状态: 初稿（待讨论确认）
> 最后更新: 2026-03-05

## 项目定位

企业级内部工具。通过飞书为每个团队或业务场景建立专属 AI 助理应用，每个应用背后对应一个 Claude Code workspace 目录，支持场景化的长记忆、技能和工具配置。

---

## 核心模块

### 1. 应用（Application）

- 每个飞书应用对应一个业务场景（如"产品助手"、"代码审查助手"）
- 每个应用绑定一个本地 **workspace 目录**，目录内包含：
  - `CLAUDE.md` — 场景指令和上下文
  - `skills/` — 该场景的自定义 skill 文件
  - `memory/` — 长期记忆文件
  - 工具配置、规范等
- 应用是消息路由的第一层：飞书群/单聊 → 路由到对应应用 → 路由到对应 workspace

### 2. 会话管理（Session Management）

#### 会话映射规则

| 飞书渠道 | 会话映射 | 支持 /new |
|---|---|---|
| 单聊（P2P） | 每个用户一个活跃 session | ✅ |
| 普通群聊 | 每个群一个活跃 session | ✅ |
| 话题群（Thread Group）| 每个 thread_id → 一个 session | ❌ |

- `/new` 命令：在支持的渠道新建 session，旧 session 归档
- Session ID 是框架维护的逻辑 ID，与 Claude 的 conversation context 分离

#### 两层记录

| 层级 | 内容 | 说明 |
|---|---|---|
| Session 层 | 用户侧对话记录 | 记录飞书消息原文、发送者、时间戳等 |
| Context 层 | Claude 执行的 message context | 传给 Claude CLI 的完整 messages 历史 |

### 3. 消息路由与执行

**流程：**
```
飞书 WS 事件
  → 解析消息（应用ID、群ID/用户ID、thread_id）
  → 路由应用 → 找到对应 workspace 目录
  → 路由 session → 进入消息队列
  → 顺序执行：在 workspace 目录执行 claude CLI
  → 返回结果给飞书
```

**关键约束：**
- 同一渠道（同一 session）的消息必须**排队顺序执行**，不并发
- 不同渠道的消息可并发处理
- 使用飞书 **WebSocket 长连接模式**监听事件（非 webhook 轮询）

### 4. 默认 Workspace 初始化配置

新建应用时，框架提供一套默认配置模板，包括：

- **飞书操作 skill**：让 Claude 能调用飞书 API（发消息、查日历、搜索文档等）
- **长记忆 skill**：对话结束后自动将关键信息写入 `memory/` 目录
- 默认 `CLAUDE.md` 模板
- 标准工具规范

### 5. 后台任务机制（Background Task）

- 用户通过 skill 或对话创建定时/事件任务（如"每天 9:00 提醒我 XXX"）
- 任务持久化为配置文件（YAML/JSON），包含：
  - 触发时机（cron 表达式 / 事件触发）
  - 所属应用（对应 workspace）
  - 目标用户 & 渠道（发送到哪个飞书群/单聊）
  - 执行内容（prompt 或 skill 调用）
- 框架调度器定期扫描任务配置，到时触发执行
- 执行逻辑与普通消息执行一致（在 workspace 目录跑 claude CLI）

### 6. 消息队列与顺序执行

- 每个 session 维护一个独立的消息队列（channel）
- 同一 session 的消息严格串行处理
- 超时机制：单条消息执行超时后记录错误，继续处理队列下一条
- 支持在飞书侧显示"处理中"状态（typing indicator）

---

## 非功能需求（待确认）

- 部署方式：单机 / 容器化？
- 数据持久化：SQLite（轻量）还是 PostgreSQL？
- Claude CLI 调用方式：子进程调用 `claude` 命令还是 API 直调？
- 多应用之间是否需要权限隔离（哪些群可以访问哪个应用）？
- 任务调度是否需要分布式（单机 cron 够用？）
