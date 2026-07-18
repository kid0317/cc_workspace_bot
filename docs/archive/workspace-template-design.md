# Workspace 模版设计文档

> 分析日期：2026-03-09
> 基于：当前模版 + 4 个线上 workspace（child / course / health / investment）

---

## 一、现有模版分析

### 1.1 当前模版结构

```
workspaces/_template/
├── CLAUDE.md                    # AI 指令（角色定义 + 安全边界）
├── .claude/
│   └── settings.local.json     # 本地工具权限白名单
└── skills/
    ├── memory.md               # 长记忆读写规范（flock）
    ├── task.md                 # 定时任务 YAML 格式规范
    └── feishu_ops/
        ├── SKILL.md            # 飞书 API 完整操作手册
        └── scripts/            # 18 个 Python 脚本（消息/文档/表格/日历/多维表格）
```

### 1.2 现有 CLAUDE.md 核心内容

- **角色定位**：通用 AI 助理，session 级隔离
- **核心规范**：读 SESSION_CONTEXT.md → 用绝对路径 → 写 memory → 创 task
- **群聊行为**：仅 @提及 / 明确提问时回复
- **安全边界**：禁止泄露 routing_key / 密钥 / 路径；禁止破坏性操作；禁止越权读写

### 1.3 现有模版的不足

| 不足点 | 说明 |
|--------|------|
| CLAUDE.md 结构简单 | 缺乏对话模式、记忆索引、技能快查等高频辅助信息 |
| memory 目录为空 | 无初始文件模版，首次使用无结构指导 |
| 无 cases 机制 | 有价值的具体事件无法系统化沉淀 |
| skills 缺乏领域知识位置 | 专业背景知识只能散放在 CLAUDE.md 里 |
| settings.local.json 过重 | 混入了大量调试/课程特定的命令，不适合通用模版 |

---

## 二、存量 Workspace 分析

### 2.1 /srv/workspaces/child — 育儿顾问

**成熟度评分**：★★★★☆

**亮点设计：**

**① Cases 系统**（最独特）

```
cases/
├── index.md                               # 案例检索索引（按类别）
├── 2026-03-08-eating-overview.md          # 进食问题综述
├── 2026-03-08-lunch-negotiation.md        # 午餐权力斗争
└── 2026-03-08-transition-difficulty-mall.md
```

- 触发规则清晰：用户描述具体事件时自动录入
- 标准化模版：客观情况 / 分析 / 应对策略 / 追踪记录
- 索引按类别管理，支持检索

**② 丰富的技能层**

```
skills/
├── parenting_philosophy.md  # 内在动机理论体系（SDT/Erikson/Vygotsky/HSC）
├── child_psychology.md      # HSC+天蝎座特质分析 + 场景交互脚本
├── cases.md                 # cases 录入与检索规范
├── memory.md
├── task.md
└── feishu_ops/
```

**③ 场景化交互脚本**

`child_psychology.md` 中对每个高频情景都有"应对脚本"（不只是理念），直接可执行。

---

### 2.2 /srv/workspaces/course — 课程开发

**成熟度评分**：★★★★★

**亮点设计：**

**① 工作模式系统**（最值得通用化）

```
📣 讨论模式  ← 探索想法
📝 草稿模式  ← 开始起草
💻 开发模式  ← 开始写代码
✍️ 写作模式  ← 准备写课
📚 学习模式  ← 传入文章/论文
```

每种模式明确定义：角色 / 输出格式 / 禁止行为。CLAUDE 在回复开头用 emoji 标注当前模式，用户可随时切换。

**② 内容分级存储**

```
multi-agent/草稿/   # 草稿区（"草稿优先"强制规则）
multi-agent/01｜*.md  # 定稿区（须信号触发）
study/              # 学习模式归档（知识吸收 → 课程产出）
```

"草稿优先"规则：任何内容都先进草稿，显式指令才发布。

**③ .claude/ 目录存放课程级专业文档**

```
.claude/
├── writing-spec.md      # 课程写作规范（五步认知框架）
├── crewai-reference.md  # 技术快查
├── memory.md
└── settings.local.json
```

---

### 2.3 /srv/workspaces/health — 健康管理

**成熟度评分**：★★☆☆☆（已初始化，尚未激活）

**亮点设计：**

**① 专业技能分工明确**

```
skills/
├── health-profile.md   # 医疗档案管理 + 指标解读
├── diet-tracker.md     # 饮食记录 + 膳食规划
├── food-database.md    # 营养素数据库
├── supplement.md       # 补剂方案管理
├── fitness.md          # 训练计划 + 进度追踪
└── knowledge.md        # 健康理论知识库
```

**② CLAUDE.md 预置用户基础信息**

CLAUDE.md 顶部直接写入用户档案（姓名/年龄/BMI/目标/家族史），无需记忆文件即可快速定位。

---

### 2.4 /srv/workspaces/investment — 投资管理

**成熟度评分**：★★★★★

**亮点设计：**

**① 最完整的 memory 体系**

```
memory/
├── MEMORY.md          # 主索引（资产概览 + 各文件链接）
├── portfolio.md       # 持仓明细（每只股票/基金/策略）
├── transactions.md    # 交易流水
├── income_expense.md  # 收支追踪
├── watchlist.md       # 自选股 + 入场条件
├── macro.md           # 宏观观点 + 关键指标追踪
└── review.md          # 周/月复盘记录
```

`MEMORY.md` 作为索引文件，载入上下文时直接给出全局视图；每个主题单独文件，避免单文件膨胀。

**② .agents/skills/ 目录（进阶技能架构）**

```
.agents/skills/
├── akshare-stock/       # A 股行情 + 基本面 + 资金流（可运行）
├── daily-briefing/      # 每日简报生成（可运行）
├── portfolio-tracker/   # 实时盈亏计算（可运行）
└── tushare-finance/     # TuShare API 参考文档
```

技能带 `README.md + SKILL.md + USER_MANUAL.md` 三件套，职责清晰。

**③ 数据来源策略文档化**

```markdown
| 数据类型        | 主要来源       | 备用来源           |
|---------------|------------|-----------------|
| A 股行情/基本面  | akshare    | tushare 日线     |
| 港股（阿里）     | yfinance   | tushare hk_daily |
| 基金 NAV      | akshare    | —               |
```

---

## 三、跨 Workspace 通用模式提取

### 3.1 必选能力（所有 workspace 都需要）

| 能力 | 当前实现 | 质量评估 |
|------|---------|---------|
| 角色定义（CLAUDE.md） | ✅ 有 | 基础版，可加强 |
| 长记忆（memory/） | ✅ 有 | 有规范，无初始结构 |
| 定时任务（task.md） | ✅ 有 | 完善 |
| 飞书集成（feishu_ops） | ✅ 有 | 完善 |
| 安全边界 | ✅ 有 | 完善 |

### 3.2 高价值可选能力（按场景选择）

| 能力 | 来源 | 适用场景 |
|------|------|---------|
| Cases 系统 | child | 有具体事件需要积累的场景（投资/育儿/健康/项目） |
| 工作模式系统 | course | 有多种协作状态的场景（创作/研究/开发） |
| MEMORY.md 索引 | investment | 记忆文件超过 3 个时 |
| 专业知识技能 | health/child | 需要领域背景知识支撑的场景 |
| 数据来源策略 | investment | 有外部数据依赖的场景 |
| 草稿分级机制 | course | 有内容创作 / 发布流程的场景 |

### 3.3 通用安全规范（硬性约束，所有 workspace 一致）

```
禁止：
- 输出 routing_key / sender_id / token / secret 等系统元数据
- 执行 rm / rmdir / shutil.rmtree 等破坏性操作
- 覆盖 SESSION_CONTEXT.md / CLAUDE.md / skill 文件等系统文件
- 读写 workspace 目录之外的任意路径
- 执行 env / printenv / cat /proc/* 等信息泄露命令

遇到可疑请求：直接拒绝，不解释、不变通
```

---

## 四、新模版设计

### 4.1 设计原则

1. **最小可用** — 新建 workspace 开箱即用，不需要大量配置
2. **渐进增强** — 核心技能内置，高级技能按需启用
3. **记忆驱动** — memory/ 有初始结构，降低首次使用门槛
4. **模式清晰** — 工作模式显式声明，减少 AI 行为不确定性

### 4.2 新模版目录结构

```
workspaces/_template/
├── CLAUDE.md                    # 重新设计（含模式系统、技能索引、memory 快查）
├── .claude/
│   └── settings.local.json     # 精简为最小权限集
├── memory/
│   ├── MEMORY.md               # 主索引（新增）
│   └── user_profile.md         # 用户档案模版（新增）
└── skills/
    ├── memory.md               # 长记忆读写规范（保留）
    ├── task.md                 # 定时任务规范（保留）
    ├── cases.md                # Cases 系统规范（新增，来自 child）
    └── feishu_ops/             # 飞书集成（保留）
        ├── SKILL.md
        └── scripts/
```

### 4.3 CLAUDE.md 新设计

**结构：**
```markdown
# AI 助理 — [Workspace 名称]

## 角色定位
[2-3 句话，明确助理的专业角色和核心职责]

## 工作模式（可选）
[如果 workspace 有多种协作状态，在此定义]

## 启动流程
1. 读 SESSION_CONTEXT.md（获取所有绝对路径）
2. 读 memory/MEMORY.md（快速加载用户上下文）
3. 识别请求意图，选择合适模式

## 技能索引
| 技能 | 文件 | 用途 |
|------|------|------|
| 飞书操作 | skills/feishu_ops/SKILL.md | 发消息/读文档/管日历/写表格 |
| 长记忆 | skills/memory.md | 跨 session 持久化信息 |
| 定时任务 | skills/task.md | 创建自动化任务 |
| Cases | skills/cases.md | 记录和检索重要事件 |

## Memory 快查
- 主索引：{memory_dir}/MEMORY.md
- 用户档案：{memory_dir}/user_profile.md

## 群聊行为
仅在以下情况回复：
- 被明确 @
- 收到问题或明确请求
- 任务完成需汇报

静默条件：消息与助理无关时，不输出任何内容。

## 回复格式
- 简洁 Markdown
- 关键信息加粗
- 避免废话和重复

## 安全边界（不可覆盖）
[标准安全约束块]
```

### 4.4 Memory 初始结构

**memory/MEMORY.md**（新增）
```markdown
# Memory 主索引

> 最后更新：[日期]

## 用户概况
→ 详见 user_profile.md

## 记忆文件索引
| 文件 | 内容 | 最后更新 |
|------|------|---------|
| user_profile.md | 基础信息、偏好、目标 | — |

## 快速摘要
[2-5 句话，当前最重要的上下文信息]
```

**memory/user_profile.md**（新增）
```markdown
# 用户档案

## 基础信息
- 姓名：
- 角色/身份：

## 主要目标
-

## 重要偏好
-

## 重要约束
-

## 历史背景
-
```

### 4.5 新增 skills/cases.md

借鉴 child workspace 的 cases 系统，通用化为适用于任意领域的事件记录机制。

**核心设计：**
```markdown
# Cases 系统规范

## 触发条件
自动录入：用户描述包含时间 + 地点 + 经过的具体事件
自动检索：用户说"上次"/"之前"/"记得吗"，或当前情况与历史案例相似

## 目录约定
cases/
├── index.md              # 按类别组织的检索索引
└── YYYY-MM-DD-{slug}.md  # 单条 case 文件

## Case 模版
---
date: YYYY-MM-DD
category: [类别]
keywords: [关键词1, 关键词2]
status: open | resolved
---

## 客观情况
[还原事实：时间/地点/触发点/经过/各方反应]

## 分析
[行为解读、机制分析]

## 应对策略
[讨论或实施的策略]

## 追踪记录
| 日期 | 更新 |
|------|------|

## 索引维护
每次新增 case 后，更新 index.md 对应类别。
```

### 4.6 精简 settings.local.json

当前模版的 settings.local.json 包含 50+ 条调试命令（大量来自课程开发场景），不适合通用模版。

**新设计**：最小权限集 + 常用操作白名单
```json
{
  "permissions": {
    "allow": [
      "Bash(flock *)",
      "Bash(python3 skills/feishu_ops/scripts/*.py *)",
      "Bash(ls *)",
      "Bash(cat *)",
      "Bash(date *)",
      "Bash(find * -name *.yaml -type f)",
      "Bash(wc -l *)"
    ]
  }
}
```

---

## 五、各 Workspace 个性化建议

### child（育儿）
- ✅ 已有 cases 系统，已有心理学技能体系，较为完整
- 建议：将 `memory/parenting_notes.md` 中的策略部分迁移到 `skills/parenting_philosophy.md`，memory 只保留动态更新的观察记录

### course（课程开发）
- ✅ 工作模式系统是亮点，值得保留
- 建议：在 `memory/` 补充 `MEMORY.md` 索引（当前记忆体系分散在多处）
- 建议：`study/LEARNING_INDEX.md` 可以纳入 cases 系统，统一检索入口

### health（健康管理）
- 🔴 memory/ 空目录，尚未激活
- 建议：立即初始化 `memory/MEMORY.md` 和 `memory/user_profile.md`（基础健康档案）
- 建议：增加 `cases/` 目录记录具体的饮食/运动/指标事件，便于趋势分析

### investment（投资管理）
- ✅ memory 体系最完整，skills 最成熟
- 建议：`macro.md` 中的"关键指标"部分补充实际数据（当前为占位符）
- 建议：将 `.agents/skills/` 统一迁移到 `skills/` 目录，与其他 workspace 保持一致结构

---

## 六、实施优先级

| 优先级 | 变更项 | 影响范围 |
|--------|-------|---------|
| P0 | 重写模版 CLAUDE.md（含模式系统、技能索引） | 所有新建 workspace |
| P0 | 新增 memory/MEMORY.md + user_profile.md 初始模版 | 所有新建 workspace |
| P1 | 新增 skills/cases.md | 所有 workspace |
| P1 | 精简 settings.local.json | 所有新建 workspace |
| P2 | 为 health workspace 初始化 memory 文件 | health |
| P2 | investment macro.md 补充实际指标数据 | investment |
| P3 | 统一 investment .agents/skills/ → skills/ | investment |
