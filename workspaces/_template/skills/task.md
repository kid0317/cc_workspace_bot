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
2. 生成一个 UUID 作为任务 ID 和文件名
3. 确认用户的意图：cron 时间、发送目标、执行内容
4. 按上述格式写入 `<tasks_dir>/<uuid>.yaml`
5. 框架会自动检测文件变更并注册定时任务

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

## 注意事项

- tasks/ 目录中每个任务独立文件，无冲突
- app_id 必须与当前 SESSION_CONTEXT.md 中一致
- target_id 必须是有效的飞书 open_id 或 chat_id
- prompt 中可以引用 memory/ 内容，但不能包含绝对路径
