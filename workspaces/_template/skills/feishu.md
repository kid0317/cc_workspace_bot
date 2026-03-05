# Feishu Skill

本 skill 说明如何在 workspace 中调用飞书相关能力。

## 说明

飞书 API 调用由框架层（cc-workspace-bot）封装处理。在大多数场景下，Claude 不需要直接调用飞书 API——框架负责收发消息。

## Claude 可做的飞书相关操作

### 1. 发送消息（通过框架）

Claude 的输出文本会由框架自动发送到对应的飞书渠道。不需要手动调用 API。

### 2. 构造富文本回复

回复中使用 Markdown 格式，框架会将其渲染为飞书卡片：

```markdown
## 分析结果

**关键发现：**
- 问题一：...
- 问题二：...

\`\`\`python
# 示例代码
print("hello")
\`\`\`
```

### 3. 附件处理

当用户发送图片/文件时，框架会将其下载到本地，并在 prompt 中替换为绝对路径：

```
[图片: /data/workspaces/app/sessions/abc/attachments/1234_photo.jpg]
[文件: /data/workspaces/app/sessions/abc/attachments/5678_doc.pdf]
```

使用 Read 工具读取这些文件进行处理。

## 注意事项

- 不直接调用飞书 API（无需 SDK）
- 如需访问飞书文档/日历等高级功能，需要在应用配置的 allowed_tools 中添加对应 MCP 工具
- 回复内容应简洁，避免过长（飞书卡片有字符限制）
