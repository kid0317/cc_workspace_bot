---
name: task
description: "当用户要求创建定时自动执行的任务（如每天推送简报、每周生成报告）时使用"
type: procedure
version: "1.0"
---

# Task Skill

本 skill 规范如何通过写入 YAML 文件来创建定时任务。

## 任务文件格式

任务文件路径：`<tasks_dir>/<uuid>.yaml`

```yaml
id: "550e8400-e29b-41d4-a716-446655440000"   # 必填，UUID
app_id: "product-assistant"                    # 必填，当前应用 ID（从 SESSION_CONTEXT.md 读取）
name: "每日技术早报"                             # 任务名称
cron: "0 9 * * 1-5"                          # cron 表达式（工作日早9点）
target_type: "p2p"                             # p2p（私聊）或 group（群聊）
target_id: "ou_xxx"                           # open_id（p2p）或 chat_id（group）
prompt: "请生成今日技术早报，包含最新 AI 动态"    # 执行 prompt
created_by: "ou_xxx"                          # 创建者 open_id
created_at: "2026-03-05T09:00:00Z"           # 创建时间（ISO 8601）
enabled: true                                  # 是否启用
```

## 创建任务流程

1. 从 SESSION_CONTEXT.md 读取 `Tasks dir` 和 `App ID`
2. 从当前对话的 `<system_routing>` 块解析默认发送目标：
   - **私聊（p2p）**：`target_type: "p2p"`，`target_id` 填写 `sender_id`（`ou_*` 格式的用户 open_id）
   - **群聊（group）**：`routing_key` 格式为 `group:{chat_id}` → `target_type: "group"`，`target_id: "{chat_id}"`
   - `created_by` 填写 `<system_routing>` 中的 `sender_id`
   - ⚠️ **注意**：`routing_key` 中 `p2p:` 后面的是 `chat_id`（`oc_*` 格式），**不是** `open_id`，不可用作 `target_id`
   - 若用户明确指定了其他发送目标，以用户指定为准
3. 生成一个 UUID 作为任务 ID 和文件名
4. 确认用户的意图：cron 时间、执行内容（发送目标已从步骤 2 自动确定）
5. 按上述格式写入 `<tasks_dir>/<uuid>.yaml`
6. 框架会自动检测文件变更并注册定时任务

## cron 表达式速查

```
"0 9 * * 1-5"    每周一至周五 09:00
"0 9 * * *"      每天 09:00
"0 9 * * 1"      每周一 09:00
"0/30 9-18 * * *" 工作时间每30分钟
"0 20 * * *"     每天 20:00
```

## 管理任务

- **禁用任务**：将文件中的 `enabled: false`
- **删除任务**：删除对应 YAML 文件（框架自动注销）
- **修改任务**：直接修改 YAML 文件（框架自动更新）

## 执行流程

任务触发后，框架的完整执行流程：

```
cron 时间到达
  → 框架将 prompt 作为对话消息传给 claude CLI
  → claude 在 workspace 环境中自主执行
       （可使用 Read / Write / Bash / feishu_ops 等工具）
  → claude 输出最终文字结果
  → 框架将该结果通过飞书文字消息发送到 target_id 对应的聊天
```

### prompt 写作要点

- **写成任务指令**，而不是普通聊天消息。例如："请获取今日 A 股行情，生成简报并说明操作建议。"
- **避免在 prompt 中主动调用 feishu_ops 发送消息**：框架会把 claude 的最终输出自动发一次，若 prompt 中又指示 claude 通过 feishu_ops 发送，会导致重复发送。如需发送富文本/文档，应在 prompt 中指示 claude 调用 feishu_ops，并在最终输出中**只返回一个简短的完成确认**，避免框架再次重复发送。
- prompt 中可以引用 memory/ 内容，使用绝对路径（从 SESSION_CONTEXT.md 读取）

## 注意事项

- tasks/ 目录中每个任务独立文件，无冲突
- app_id 必须与当前 SESSION_CONTEXT.md 中一致
- target_id 必须是有效的飞书 open_id 或 chat_id
