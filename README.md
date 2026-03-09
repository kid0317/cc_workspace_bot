# CC Workspace Bot

> 企业级飞书 AI 助理平台，基于 Claude Code CLI 构建的多场景智能助手框架。
> 个人微信: bmagician ,欢迎添加讨论.

[![Go Version](https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue)](LICENSE)
[![SQLite](https://img.shields.io/badge/Database-SQLite%20WAL-003B57?logo=sqlite)](https://www.sqlite.org/)

---

## 目录

- [项目简介](#项目简介)
- [核心特性](#核心特性)
- [架构概览](#架构概览)
- [实战案例](#实战案例)
  - [投资助理：持仓分析 + 交易纪律执行](#案例一投资助理持仓分析--交易纪律执行)
  - [个人学习助理：交互式学习 + 知识沉淀](#案例二个人学习助理交互式学习--知识沉淀)
  - [随身编程助手：从电脑前解放](#案例三随身编程助手从电脑前解放)
  - [饮食管理：热量追踪 + 营养分析 + 定期报告](#案例四饮食管理热量追踪--营养分析--定期报告)
- [前置要求](#前置要求)
- [快速开始](#快速开始)
  - [安装](#安装)
  - [配置](#配置)
  - [初始化 Workspace](#初始化-workspace)
  - [启动服务](#启动服务)
- [使用指南](#使用指南)
  - [基本对话](#基本对话)
  - [命令参考](#命令参考)
  - [定时任务](#定时任务)
  - [自定义 Workspace](#自定义-workspace)
- [配置参考](#配置参考)
- [项目结构](#项目结构)
- [技术栈](#技术栈)
- [开发指南](#开发指南)
- [文档](#文档)
- [许可证](#许可证)

---

## 项目简介

**CC Workspace Bot** 是一套企业内部飞书 AI 助理平台。每个业务场景（如投资助理、产品助手、代码评审机器人）对应一个飞书应用和一个独立的 Claude Code workspace 目录。用户在飞书发送消息，框架路由到对应 workspace 执行 `claude` CLI，通过交互式卡片返回结果。

**解决的核心问题**：

- 同一套代码支撑多个完全隔离的 AI 助理场景，互不干扰
- 每个助理拥有独立的长期记忆、自定义技能、工作模式和定时任务
- 无需公网 IP，通过飞书 WebSocket 长连接实现企业内网安全部署
- Claude Code 的工具能力（读写文件、执行命令、调用 API）与飞书完整集成

---

## 核心特性

| 特性 | 说明 |
|------|------|
| **多应用隔离** | 每个飞书应用对应独立 workspace，session 级目录隔离并发写操作 |
| **智能会话管理** | 单聊 / 群聊 / 话题群，自动维护 Claude context，`/new` 开启新会话 |
| **长期记忆** | 跨 session 共享记忆，flock 文件锁保障并发安全 |
| **定时任务** | 对话式创建 cron 任务，fsnotify 自动注册，YAML 为 source of truth |
| **附件支持** | 图片、文件自动下载至 session 目录，替换为绝对路径供 Claude 读取 |
| **飞书全集成** | 发消息、读写文档/表格/多维表格、管理日历、查询群成员（18 个 Python 脚本）|
| **事件记录** | 结构化记录具体事件（cases），支持按时间检索历史案例 |
| **WS 长连接** | 无需公网 IP，企业内网直接部署 |

---

## 架构概览

```
飞书用户
  ↓ 发送消息（文本 / 图片 / 文件）
飞书 WebSocket 长连接
  ↓ P2MessageReceiveV1 事件推送
消息路由器（解析消息类型 + 下载附件到 TempDir）
  ↓ 按 channel_key 分发到对应队列
Session Worker（每个 channel_key 对应一个串行队列）
  ↓ moveAttachments() → 发送「思考中」卡片
Claude Executor
  ↓ 写入 SESSION_CONTEXT.md → prepend routing_key/sender_id → 启动子进程
claude CLI（cmd.Dir = sessions/<session-id>/，--resume 复用 context）
  ↓ stream-json 输出
飞书 Sender（PATCH 卡片为最终结果）
```

### channel_key 格式

| 飞书渠道 | channel_key 格式 | 支持 /new |
|---------|-----------------|----------|
| 单聊（P2P） | `p2p:{chat_id}:{app_id}` | ✅ |
| 群聊 | `group:{chat_id}:{app_id}` | ✅ |
| 话题群 | `thread:{chat_id}:{thread_id}:{app_id}` | ❌ |

> P2P 的 `chat_id` 是飞书的会话 ID，不是用户的 `open_id`。

更多架构细节见 [设计文档](docs/design.md)。

---

## 实战案例

### 案例一：投资助理——持仓分析 + 交易纪律执行

**场景**：A 股 / 港股散户投资者，持有多只个股，经常在冲动下想追涨杀跌，希望有一个客观的「第二意见」来帮助执行纪律。

**配置思路**：

1. 创建一个专属 workspace（`investment-assistant`），在 `CLAUDE.md` 中定义角色为「个人投资顾问，严格客观，不迎合情绪」
2. 让助手自行安装 [akshare](https://github.com/akfamily/akshare) 及行情工具：

```
用户：帮我安装 akshare，并写一个 skill，能查询股票实时行情、K线数据和资金流向
助手：好的，我来安装依赖并创建 skill...
     （Claude 执行 pip install akshare，在 skills/ 下创建 stock_data.md 和对应脚本）
助手：✅ 已安装完成。现在可以通过「查询 600519 的行情」直接获取数据。
```

3. 创建定时任务，每个交易日收盘后自动推送持仓分析：

```
用户：每个工作日 15:30 帮我分析一下持仓情况，重点看今日涨跌幅、资金流向和技术形态，有没有需要操作的建议发到我私聊
助手：好的，我来创建这个定时任务...
     （Claude 写入 tasks/<uuid>.yaml，cron: "30 15 * * 1-5"）
助手：✅ 已创建「每日收盘持仓分析」任务，将在每个工作日 15:30 自动推送。
```

4. **交易纪律执行**——在任何想操作的冲动来临时，先找助手聊：

```
用户：比亚迪今天跌了 5%，我想趁低加仓，你觉得呢？
助手：我来看一下当前数据...
     （调用 akshare 查询 K 线、资金流向、大盘情绪）
     从技术面看，比亚迪目前处于下降通道，今日主力资金净流出 3.2 亿，
     跌停板附近有支撑但尚未确认。结合你当前仓位已达 35%，
     建议等待量能缩减后的反弹信号再考虑加仓，而非追跌。
     你是否想看一下近 30 日的资金流向趋势？
```

**核心价值**：助手的角色记忆（`memory/user_profile.md`）中记录了你的持仓成本、风险偏好和历史操作复盘，每次对话都基于完整背景做判断，而不是给通用建议。

---

### 案例二：个人学习助理——交互式学习 + 知识沉淀

**场景**：技术人员平时收藏大量文章、论文、视频，却很少真正消化。希望把「阅读」变成「对话式学习」，并让学习记录真正沉淀为可检索的知识库。

**基本流程**：

```
用户：https://arxiv.org/abs/2501.12345  帮我学习这篇关于 Agent 架构的论文
助手：我来读取这篇论文...
     （WebFetch 抓取内容，解析摘要、方法、实验结果）

     这篇论文提出了 XXX 架构，核心创新点是...
     【核心概念】...
     【与现有方法对比】...
     【实际应用启示】...

     你想深入哪个部分？还是我出几道理解题来验证一下？
```

**应对无法直接抓取的页面**（付费内容、需登录的网站、动态渲染页面）：

> 先在飞书桌面端或移动端浏览器中打开该页面，使用**飞书剪存**（浏览器插件）将网页保存为飞书文档，再把文档链接发给助手。助手通过 `feishu_ops` skill 直接读取飞书文档全文，无障碍处理。

```
用户：这个极客时间专栏要付费，我已经用飞书剪存存成文档了：
     https://xxx.feishu.cn/docx/AbCd1234
助手：我来读取这篇文档...
     （调用 feishu_ops/scripts/read_doc.py 读取全文）
     好的，这是关于「分布式事务」的内容，我来帮你梳理...
```

**知识沉淀**——学习记录自动写入 memory：

```
用户：把今天学到的 Agent 架构要点记下来
助手：好的，我来整理今天的学习笔记...
     （使用 flock 写入 memory/learning_notes.md）
     ✅ 已记录到长期记忆。下次问起 Agent 架构时我会从这里引用。
```

**核心价值**：助手的知识库随着每次学习对话不断积累，它「知道你学过什么、哪里还有疑问」，逐渐成为真正懂你知识体系的学习伙伴，而不是每次都要从零开始。

---

### 案例三：随身编程助手——从电脑前解放

**场景**：周末外出或开会途中，在手机上用飞书就能进行代码编写、架构设计、项目推进，真正摆脱「必须坐在电脑前才能工作」的限制。

**代码编写**：

```
用户：帮我在 ~/projects/myapp 里新建一个 Go HTTP handler，
     处理 POST /api/upload，接收文件并保存到 ./uploads/ 目录，
     返回文件 URL，注意做 MIME 类型校验只允许图片
助手：我来创建这个 handler...
     （Read 现有代码结构 → Write 新文件 → 符合项目风格）
     ✅ 已创建 internal/handler/upload.go，主要逻辑：
     - 限制 MIME 类型：image/jpeg, image/png, image/webp
     - 文件大小上限 10 MiB（io.LimitReader）
     - 使用 UUID 命名防止冲突
     需要我顺手写一个单测吗？
```

**架构设计讨论**：

```
用户：我在考虑给现有的单体服务加消息队列，业务量不大，
     主要是为了解耦异步通知（邮件 + webhook），你觉得上 Kafka 还是直接 Redis List？
助手：对于你描述的场景，Kafka 明显过重...
     （结合项目 memory 中记录的技术栈和团队规模给出具体建议）
     建议用 Redis List + BLPOP 方案，理由是：
     1. 你们已有 Redis 实例，零增量运维成本
     2. 消息量级（<1k/day）不需要 Kafka 的分区机制
     3. 单次 BLPOP 超时 30s，天然支持优雅停止
     我可以直接帮你写 consumer goroutine 的骨架代码。
```

**远程执行与验证**：

```
用户：帮我跑一下项目的测试，看看刚才那个改动有没有问题
助手：好的，我来执行测试...
     （Bash: cd ~/projects/myapp && go test ./internal/handler/... -v）
     ✅ 全部通过（3/3），覆盖率 87.3%。
     upload_test.go 中有一个边界用例验证了 MIME 拒绝逻辑，测试结果符合预期。
```

**核心价值**：Claude Code 本身就是完整的编程 Agent——能读写文件、执行命令、理解项目上下文。通过飞书作为交互界面，你只需要有网络，随时随地都是你的开发环境。

---

### 案例四：饮食管理——热量追踪 + 营养分析 + 定期报告

**场景**：想系统管理日常饮食、控制热量和宏量营养素（蛋白质 / 碳水 / 脂肪），但不想打开专门 App 逐条填表，只想吃完一句话搞定记录，同时积累自己的私人食谱库。

**初始化——建立个人档案**：

```
用户：初始化
助手：这个工作空间主要用来做什么？你希望我扮演什么角色？
用户：饮食管理助手，帮我追踪每日饮食摄入、计算营养、管理私人食谱库
助手：好的，简单介绍一下你自己？
用户：男，28岁，75kg，175cm，目标是增肌减脂，每日目标热量 2000 大卡，
     蛋白质 150g，碳水 200g，脂肪 60g
助手：（写入 memory/user_profile.md）
     ✅ 已记录。你的 TDEE 约为 2350 大卡，目标缺口 350 大卡，
     蛋白质优先策略，每公斤体重 2g 蛋白质目标设置合理。
```

**日常记录——一句话搞定**：

```
用户：早饭：燕麦 50g 牛奶 250ml 鸡蛋 2 个
助手：已记录早餐 ✅
     热量 ~485 大卡 | 蛋白质 28g | 碳水 52g | 脂肪 16g
     今日已摄入 485 / 2000 大卡

用户：午饭在外面吃的，鸡腿饭一份加个汤
助手：已记录午餐 ✅（估算）
     鸡腿饭（快餐）约 750 大卡 | 蛋白质 32g | 碳水 90g | 脂肪 22g
     今日累计 1235 / 2000 大卡，蛋白质 60 / 150g，
     下午还需补充约 90g 蛋白质，建议加一顿高蛋白小食。

用户：下午茶吃了包薯片（乐事原味 50g）
助手：已记录 ✅（注：薯片为高脂高钠零食，今日脂肪已达 82%）
     今日累计 1516 / 2000 大卡
```

**食谱库——记录常吃的菜**：

```
用户：我今晚自己做了番茄炒蛋，两个鸡蛋两个番茄加点油盐，帮我存到食谱库
助手：好的，我来记录这道菜的营养信息并存入食谱库...
     （写入 memory/recipes.md）
     ✅ 「番茄炒蛋（家常版）」已入库：
     每份约 220 大卡 | 蛋白质 14g | 碳水 8g | 脂肪 15g
     下次说「番茄炒蛋」我直接调用这个数据，不用重新估算。
```

**定期分析报告**——创建定时任务：

```
用户：每周日晚上 9 点给我发一份本周饮食分析，发到私聊
助手：好的，我来创建这个定时任务...
     （写入 tasks/<uuid>.yaml，cron: "0 21 * * 0"）
     ✅ 已创建「每周饮食分析」任务
```

每周日自动推送的报告示例：

```
📊 本周饮食报告（3.3 - 3.9）

热量完成率：92%（均值 1840 / 目标 2000 大卡）
蛋白质：日均 118g（目标 150g，达成率 79%⚠️）
碳水：日均 198g ✅  脂肪：日均 65g 略超

📌 本周亮点
- 连续 5 天坚持早餐高蛋白
- 外食控制较好，未出现单日超 2500 大卡

⚠️ 待改善
- 周三、周五蛋白质严重不足（< 80g），建议在下午加一勺蛋白粉或 200g 希腊酸奶
- 薯片、饼干等零食出现 4 次，空热量占比偏高

📈 趋势
连续两周蛋白质摄入均在目标 80% 以下，建议调整午餐选择（优先肉类主食）
```

**核心价值**：不需要下载专门 App，不需要逐条扫描条形码，说一句话助手就能估算营养并记录。食谱库越用越准，personal profile 记录了你的体重目标和偏好，每次分析都基于你的具体情况而不是通用建议。体重、体测数据也可以随手告诉助手，长期追踪进展。

---

## 前置要求

| 组件 | 版本要求 | 说明 |
|------|---------|------|
| Go | 1.23+ | 编译服务端代码 |
| Claude CLI | 最新版 | `claude` 命令须在 `$PATH` 中，并已完成认证 |
| Python | 3.9+ | 飞书 ops 脚本依赖（`requests` 库）|
| 飞书企业账号 | — | 需创建企业自建应用并开启 WS 模式 |

**飞书应用所需权限**（在开放平台 → 权限管理中开通）：

- `im:message:receive_v1`（接收消息）
- `im:message`（发送消息）
- 按需开通：`docs:doc`、`sheets:spreadsheet`、`bitable:app`、`calendar:calendar` 等

---

## 快速开始

### 安装

```bash
# 克隆仓库
git clone https://github.com/kid0317/cc-workspace-bot.git
cd cc-workspace-bot

# 安装 Go 依赖
go mod download

# 构建服务端二进制
go build -o server ./cmd/server
```

### 配置

```bash
# 复制配置模板
cp config.yaml.template config.yaml
```

编辑 `config.yaml`，至少填写一个 app 的飞书凭证和 workspace 路径（详见[配置参考](#配置参考)）。

> ⚠️ `config.yaml` 包含 App Secret，已通过 `.gitignore` 排除在 git 追踪之外，**切勿提交**。

### 初始化 Workspace

每个飞书应用对应一个 workspace 目录。使用初始化脚本一键完成：

```bash
./init_workspace.sh <app-id> <workspace-dir> <feishu-app-id> <feishu-app-secret>
```

**参数说明**：

| 参数 | 说明 | 示例 |
|------|------|------|
| `app-id` | 唯一标识，只含字母/数字/连字符 | `investment-assistant` |
| `workspace-dir` | workspace 目录（绝对或相对路径）| `/data/workspaces/investment` |
| `feishu-app-id` | 飞书应用 App ID | `cli_abc123xxx` |
| `feishu-app-secret` | 飞书应用 App Secret | `secretXXX` |

**脚本执行的操作**：

1. 创建目录结构（`skills/`、`memory/`、`tasks/`、`sessions/`）
2. 将飞书凭证写入 `skills/feishu_ops/feishu.json`（权限 `0600`，不暴露给 LLM）
3. 从 `workspaces/_template/` 复制初始文件（非破坏性，已存在的文件自动跳过）
4. 备份 `config.yaml` 并追加新 app 配置块

**示例**：

```bash
./init_workspace.sh investment-assistant \
  /data/workspaces/investment \
  cli_abc123 \
  secretXXX
```

脚本执行后，需要**重启服务**使新配置生效：

```bash
./start.sh restart
```

### 启动服务

**后台守护进程（推荐）**：

```bash
./start.sh start      # 启动
./start.sh status     # 查看状态
./start.sh stop       # 优雅停止（等待所有 worker 完成）
./start.sh restart    # 重启
```

| 文件 | 说明 |
|------|------|
| `server.pid` | 进程 PID |
| `server.log` | 标准输出日志 |
| `server.log.wf` | 标准错误日志 |

**前台运行（调试）**：

```bash
./server
./server -config /path/to/config.yaml
```

---

## 使用指南

### 基本对话

在飞书中直接向机器人发消息即可。支持以下消息类型：

- **文本**：直接处理
- **图片**：自动下载，提供给 Claude 读取（`[图片: /abs/path/to/file.jpg]`）
- **文件**：自动下载，提供给 Claude 读取（`[文件: /abs/path/to/file.pdf]`）
- **富文本**：自动提取文本内容

Claude 将以交互式卡片形式回复，首先显示「思考中...」，完成后一次性更新为最终结果。

### 命令参考

| 命令 | 说明 |
|------|------|
| `/new` | 开启新会话：归档当前会话，清空上下文，下条消息开始全新对话 |

### 定时任务

通过自然语言对话创建定时任务，Claude 会自动调用 `task` skill 写入 YAML 文件，框架通过 fsnotify 自动注册到 gocron 调度器：

```
用户：每天早上 9 点帮我发一份市场简报到群里
AI：好的，我来创建这个定时任务。
    （Claude 写入 tasks/<uuid>.yaml）
AI：✅ 已创建定时任务「每日市场简报」，将在每天 09:00 自动执行。
```

**任务 YAML 格式**（框架自动管理，一般无需手动编辑）：

```yaml
id: "550e8400-e29b-41d4-a716-446655440000"
app_id: "investment-assistant"
name: "每日市场简报"
cron: "0 9 * * 1-5"            # 周一到周五早 9 点
target_type: "group"            # p2p 或 group
target_id: "oc_xxxxxxxx"        # open_id 或 chat_id
prompt: "请生成今日市场简报"
created_by: "ou_xxxxxxxx"
created_at: "2026-03-01T00:00:00Z"
enabled: true
```

删除 YAML 文件即可注销任务。

### 自定义 Workspace

每个 workspace 的行为完全由 `CLAUDE.md` 和 `skills/` 定义。

**初始化引导**：首次使用时，向机器人发送「初始化」，助手将引导完成以下配置：

1. 工作空间用途（角色定位）
2. 用户基本信息
3. 日常任务规划
4. 工作模式（可选，如「分析模式 / 规划模式 / 汇报模式」）
5. 飞书默认发送目标

**长期记忆**：重要信息自动保存到 `memory/` 目录，跨 session 共享（通过 flock 文件锁保障并发安全）。

**飞书操作能力**（`skills/feishu_ops/`）：

| 能力 | 说明 |
|------|------|
| 发送消息 | 文字、富文本、图片、文件，支持私聊和群聊 |
| 文档操作 | 创建并写入飞书文档（Markdown → 富文本转换）|
| 电子表格 | 创建表格、导入 Excel、写入数据 |
| 多维表格 | 创建 Bitable、定义字段、批量写入记录 |
| 读取文档 | 读取飞书云文档 / 电子表格内容 |
| 日历管理 | 查询 / 创建日历事件 |
| 群成员 | 查询群聊成员列表 |

---

## 配置参考

完整的 `config.yaml` 配置说明：

```yaml
# 应用列表（支持配置多个飞书应用，每个对应一个 workspace）
apps:
  - id: "investment-assistant"          # 唯一标识（字母/数字/连字符）
    feishu_app_id: "cli_xxx"            # 飞书 App ID
    feishu_app_secret: "xxx"            # 飞书 App Secret
    feishu_verification_token: "xxx"    # 飞书事件订阅 Verification Token
    feishu_encrypt_key: ""              # 消息加密密钥（可选，不加密则留空）
    workspace_dir: "/data/workspaces/investment-assistant"
    allowed_chats: []                   # 白名单 chat_id 列表，空表示不限制
    claude:
      permission_mode: "acceptEdits"    # acceptEdits（推荐）或 bypassPermissions
      allowed_tools:                    # 允许 Claude 使用的工具，空表示不限制
        - "Bash"
        - "Read"
        - "Edit"
        - "Write"
        - "Glob"
        - "Grep"
        - "WebFetch"
        - "WebSearch"

  # 可追加更多 app...
  - id: "code-review"
    feishu_app_id: "cli_yyy"
    feishu_app_secret: "yyy"
    feishu_verification_token: "yyy"
    workspace_dir: "/data/workspaces/code-review"
    allowed_chats:
      - "oc_abc123"                     # 仅允许特定群聊
    claude:
      permission_mode: "acceptEdits"
      allowed_tools:
        - "Read"
        - "Bash"

server:
  port: 8080                            # HTTP 监听端口（健康检查 GET /health）

claude:
  timeout_minutes: 5                    # 单次 claude 执行超时（分钟）
  max_turns: 20                         # claude CLI --max-turns 参数

session:
  worker_idle_timeout_minutes: 30       # Worker 空闲超时，触发 session 归档

cleanup:
  attachments_retention_days: 7        # session 归档后附件保留天数
  attachments_max_days: 30             # 强制清理天数上限（无论是否归档）
  schedule: "0 2 * * *"               # 清理任务 cron（默认每天凌晨 2 点）
```

**配置说明**：

- **`permission_mode`**：`acceptEdits` 自动接受文件编辑操作（推荐生产环境）；`bypassPermissions` 跳过所有权限确认（高风险，仅测试用）
- **`allowed_tools`**：限制 Claude 可用的工具，建议按最小权限原则配置
- **`allowed_chats`**：留空则该应用接受所有来源的消息；填写后只处理白名单内的 chat_id
- **`encrypt_key`**：飞书消息加密配置，企业安全要求高时建议开启

---

## 项目结构

```
cc-workspace-bot/
├── cmd/
│   └── server/main.go          # 入口：配置加载、组件连线、优雅关闭
├── internal/
│   ├── config/
│   │   ├── config.go           # Viper YAML 配置结构 + Validate()
│   │   └── config_test.go
│   ├── model/
│   │   └── models.go           # GORM 数据模型（Channel / Session / Message / Task）
│   ├── db/
│   │   └── db.go               # SQLite WAL 连接 + AutoMigrate
│   ├── claude/
│   │   ├── executor.go         # 子进程调用 claude CLI、SESSION_CONTEXT.md 注入、stream-json 解析
│   │   └── executor_test.go
│   ├── feishu/
│   │   ├── receiver.go         # WS 事件解析、附件下载、Dispatcher 接口
│   │   └── sender.go           # SendThinking / UpdateCard / SendText
│   ├── session/
│   │   ├── manager.go          # channel_key → Worker 懒启动（sync.Map + WaitGroup）
│   │   └── worker.go           # 串行队列、/new、空闲超时归档、moveAttachments
│   ├── task/
│   │   ├── watcher.go          # fsnotify 监听 tasks/ 目录变更
│   │   ├── scheduler.go        # gocron/v2 调度器管理
│   │   ├── runner.go           # 任务执行（YAML 加载 + cron 校验 + claude 调用）
│   │   └── cleanup.go          # 附件清理（archived + retention_days / max_days）
│   └── workspace/
│       └── init.go             # workspace 目录初始化 + feishu.json 写入 + 模板复制
├── workspaces/
│   └── _template/              # 新 workspace 默认模板
│       ├── CLAUDE.md           # AI 指令（含初始化引导、技能索引、安全边界）
│       ├── .claude/
│       │   └── settings.local.json  # 最小权限白名单（flock / feishu_ops 脚本 / 基础命令）
│       ├── memory/
│       │   ├── MEMORY.md       # 主索引 + 初始化进度清单
│       │   └── user_profile.md # 用户档案模板
│       └── skills/
│           ├── memory.md       # 长记忆读写规范（含 flock 操作指南）
│           ├── task.md         # 定时任务 YAML 格式规范
│           ├── cases.md        # 事件记录规范（cases/ 目录管理）
│           └── feishu_ops/     # 飞书集成（SKILL.md + 18 个 Python 脚本）
├── docs/
│   ├── design.md               # 架构设计、时序图、设计决策
│   ├── requirements.md         # 功能需求
│   └── tech-research.md        # 技术调研
├── config.yaml.template        # 配置模板（复制为 config.yaml 使用）
├── init_workspace.sh           # Workspace 一键初始化脚本
├── start.sh                    # 服务启停脚本
├── go.mod / go.sum
└── bot.db                      # SQLite 数据库（运行时生成）
```

**Workspace 运行时目录结构**（由 `init_workspace.sh` 创建）：

```
workspaces/<app-id>/
├── CLAUDE.md                   # 场景级 AI 指令（从模板复制，按需自定义）
├── .memory.lock                # flock 锁文件（框架自动创建）
├── .claude/
│   └── settings.local.json    # Claude CLI 权限配置
├── skills/
│   ├── feishu_ops/
│   │   ├── SKILL.md            # 飞书 API 操作手册
│   │   ├── feishu.json         # 飞书凭证（0600 权限，不暴露给 LLM）
│   │   └── scripts/            # Python 脚本（18 个飞书操作）
│   ├── memory.md               # 长记忆读写规范
│   ├── task.md                 # 定时任务创建规范
│   └── cases.md                # 事件记录规范
├── memory/                     # 长期记忆（跨 session 共享）
│   ├── MEMORY.md               # 主索引 + 初始化进度
│   └── user_profile.md         # 用户档案
├── cases/                      # 事件案例库（claude 按需创建）
├── tasks/                      # 定时任务 YAML（claude 写，watcher 监听）
└── sessions/
    └── <session-id>/           # cmd.Dir 指向这里
        ├── SESSION_CONTEXT.md  # 框架注入的绝对路径上下文
        └── attachments/        # 附件（定期清理）
```

---

## 技术栈

| 组件 | 选型 | 说明 |
|------|------|------|
| 语言 | Go 1.23 | 高并发、低内存占用 |
| 飞书 SDK | oapi-sdk-go/v3 | 官方 SDK，WS 模式无需公网 IP |
| 数据库 | SQLite WAL | CGO-free，单机部署零依赖 |
| ORM | GORM | AutoMigrate，快速开发 |
| 配置 | Viper | YAML 配置加载与校验 |
| 定时任务 | gocron/v2 | 简洁的 cron 调度器 |
| 文件监听 | fsnotify | 跨平台 inotify 封装 |
| AI 引擎 | Claude CLI (claude) | 子进程调用，stream-json 输出 |
| 飞书操作 | Python + requests | 18 个独立脚本，凭证文件读取 |

---

## 开发指南

### 构建与测试

```bash
# 构建
go build ./...

# 运行测试
go test ./...

# 测试覆盖率
go test ./... -cover

# 竞态检测（CI 推荐）
go test -race ./...

# 静态检查
go vet ./...

# 格式化
gofmt -w .

# 依赖整理
go mod tidy
```

### 并发设计

本项目在三个层面保障并发安全：

| 层面 | 隔离方式 |
|------|---------|
| Session 级写操作 | `cmd.Dir = sessions/<id>/`，每个 session 独立目录 |
| 跨 session 共享内存 | flock 文件锁（`skills/memory.md` 规范，skill 层实现）|
| 同一 channel 消息 | Session Worker 串行队列（channel 深度 64）|

### 添加新 Skill

在 workspace 的 `skills/` 目录下新建 Markdown 文件，在 `CLAUDE.md` 的技能索引表中注册即可。

如需飞书 Python 脚本，参考 `skills/feishu_ops/scripts/` 中已有的 18 个脚本和 `_feishu_auth.py` 公共模块。

### 关键设计决策

| 决策点 | 方案 | 原因 |
|--------|------|------|
| 路由信息注入 | prepend 到 prompt（非 SESSION_CONTEXT.md）| 避免并发写同一文件的竞态问题 |
| 附件处理 | TempDir → moveAttachments → session/attachments/ | 先入队再移动，减少路由器阻塞 |
| 任务恢复 | 启动时从 DB 查询（非扫描文件系统）| watcher 保证 YAML→DB 同步，DB 记录运行时状态 |
| 优雅关闭 | `sessionMgr.Wait()` | 等待所有 worker goroutine 完成当前任务后退出 |

更多设计细节见 [设计文档](docs/design.md)。

---

## 文档

| 文档 | 说明 |
|------|------|
| [设计文档](docs/design.md) | 完整架构设计、时序图、数据模型、设计决策 |
| [需求文档](docs/requirements.md) | 功能需求和模块说明 |
| [技术调研](docs/tech-research.md) | 技术选型背景和实现细节 |

---

## 许可证

[MIT License](LICENSE)

---

## 贡献

欢迎提交 Issue 和 Pull Request！

提交 PR 前请确保：
- `go test ./...` 全部通过
- `go vet ./...` 无警告
- `gofmt -w .` 已格式化
