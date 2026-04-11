---
name: langfuse_query
description: 查询 Langfuse 可观测平台数据，帮助定位 Claude Code 任务执行异常
version: 1.0.0
triggers:
  - "查日志"
  - "任务报错了"
  - "最近有没有异常"
  - "查一下 trace"
  - "执行失败"
  - "调试一下"
  - "上次任务怎么了"
  - "有没有错误"
---

# langfuse_query — Langfuse 可观测数据查询

## 概述

`langfuse_query.py` 通过 Langfuse REST API 拉取 traces / observations，帮助定位 Claude Code 执行中的异常。

- **自动读取凭证**：从 `.claude/settings.local.json` 向上查找（无需手动传参）
- **自动读取 Workspace 信息**：从 `SESSION_CONTEXT.md` 获取上下文
- **零外部依赖**：仅使用 Python 标准库

## 使用方法

```bash
# 脚本路径（绝对路径）
_skill_base="$(dirname "$(python3 -c "import os; print(os.path.abspath('${BASH_SOURCE[0]}'))")")"
SCRIPT="${WORKSPACE_DIR}/.claude/skills/langfuse_query/langfuse_query.py"
```

### 四种查询模式

#### 1. 列出最近 traces（默认）

```bash
python3 "$SCRIPT" --mode traces --limit 10 --hours 24
```

输出示例：
```
=== Langfuse Traces  [workspace: /root/workspaces/yzk_worker] ===
过去 24h，共 5 条 trace

────────────────────────────────────────────────────────────
✓ [04-11 09:30:12] claude-code turn 3
  trace_id : abc123def456789...
  session  : session-xyz...
  input    : "帮我查一下今天的任务完成情况"
  output   : "已完成以下任务：..."
```

#### 2. 只看错误

```bash
python3 "$SCRIPT" --mode errors --hours 6
```

#### 3. 查看某条 trace 的工具调用详情

```bash
python3 "$SCRIPT" --mode observations --trace-id <trace_id_前16位或完整id>
```

输出工具调用链：
```
observations (8 条):
     [04-11 09:30:10] [generation] Claude Response
        input  : "帮我查今天的任务完成情况"
        output : "已完成以下任务..."
     [04-11 09:30:11] [tool] Tool: Read
        input  : {"file_path": "/root/workspaces/.../memory/MEMORY.md"}
```

#### 4. 查某个 session 的所有 traces

```bash
python3 "$SCRIPT" --mode session --session-id <session_id>
```

### 常用参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--mode` | `traces` | 查询模式：traces / observations / session / errors |
| `--limit` | `10` | 返回条数 |
| `--hours` | `24` | 查询最近 N 小时 |
| `--errors-only` | false | 仅显示含错误的 trace |
| `--task-name` | — | 按任务名过滤（部分匹配） |
| `--trace-id` | — | 指定 trace id（observations 模式必需）|
| `--session-id` | — | 指定 session id |

## 典型调试流程

当用户报告"任务没执行"或"执行报错"时：

```bash
# 第一步：看最近有没有异常
python3 "$SCRIPT" --mode errors --hours 6

# 第二步：按任务名过滤
python3 "$SCRIPT" --mode traces --task-name "每日简报" --hours 24

# 第三步：查具体 trace 的工具调用链
python3 "$SCRIPT" --mode observations --trace-id abc123def456789a
```

## 凭证配置

脚本按以下优先级查找凭证（自动处理，通常无需手动配置）：

1. 环境变量：`LANGFUSE_PUBLIC_KEY` / `LANGFUSE_SECRET_KEY` / `LANGFUSE_BASE_URL`
2. 从当前目录向上查找 `.claude/settings.local.json` 中的 `env` 字段

标准配置（已预置在 `workspaces/<app>/.claude/settings.local.json`）：
```json
{
  "env": {
    "TRACE_TO_LANGFUSE": "true",
    "LANGFUSE_PUBLIC_KEY": "pk-lf-cc-workspace-bot-local",
    "LANGFUSE_SECRET_KEY": "sk-lf-cc-workspace-bot-local",
    "LANGFUSE_BASE_URL": "http://localhost:3000"
  }
}
```

## Langfuse Web UI

访问 http://localhost:3000 查看完整的可视化 traces。
- 登录：`admin@cc-workspace.local` / `admin123`
- 项目：`cc-workspace-bot`
