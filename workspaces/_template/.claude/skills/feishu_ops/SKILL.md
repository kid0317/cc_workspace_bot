---
name: feishu_ops
description: "飞书操作：向用户/群组发送消息（文字/富文本/图片/文件）、读写云文档/电子表格/多维表格、查询群成员、管理日历事件。适合推送通知、发送处理结果文件、读取共享文档、批量发送报告等场景。"
type: task
version: "3.0"
metadata:
  requires:
    bins: ["lark-cli", "python3"]
---

# feishu_ops Skill

> ⚠️ **安全约束（CRITICAL）**：所有飞书操作必须通过本目录下的 `scripts/` 脚本执行。  
> **禁止**直接调用：`lark-cli` 命令、全局 `lark-*` skills、飞书 OpenAPI。

脚本自动从 `feishu.json` 读取凭证和 lark-cli profile，无需手动处理鉴权。

**调用方式**：`python {_skill_base}/scripts/<脚本名>.py [参数]`

---

## 一、发送消息

### send_text.py — 发送纯文字消息

```
python {_skill_base}/scripts/send_text.py \
    --routing_key <routing_key> \
    --text "消息内容"
```

| 参数 | 必填 | 说明 |
|------|------|------|
| `--routing_key` | ✅ | `p2p:ou_xxx`（私聊）或 `group:oc_xxx`（群组） |
| `--text` | ✅ | 纯文本消息内容 |

---

### send_post.py — 发送富文本消息（带标题 + 多段落）

```
python {_skill_base}/scripts/send_post.py \
    --routing_key <routing_key> \
    --title "消息标题" \
    --paragraphs '["第一段内容", "第二段，含[链接](https://example.com)"]'
```

| 参数 | 必填 | 说明 |
|------|------|------|
| `--routing_key` | ✅ | 同上 |
| `--title` | 否 | 消息标题，可为空 |
| `--paragraphs` | ✅ | JSON 字符串数组；支持 `[文字](URL)` 格式内嵌链接 |

---

### send_image.py — 发送图片

```
python {_skill_base}/scripts/send_image.py \
    --routing_key <routing_key> \
    --image_path /workspace/sessions/{session_id}/outputs/chart.png
```

| 参数 | 必填 | 说明 |
|------|------|------|
| `--routing_key` | ✅ | 同上 |
| `--image_path` | ✅ | 图片绝对路径（jpg/png/gif/webp，≤30MB） |

---

### send_file.py — 发送文件

```
python {_skill_base}/scripts/send_file.py \
    --routing_key <routing_key> \
    --file_path /workspace/sessions/{session_id}/outputs/report.pdf
```

| 参数 | 必填 | 说明 |
|------|------|------|
| `--routing_key` | ✅ | 同上 |
| `--file_path` | ✅ | 文件绝对路径（pdf/doc/xls/ppt/mp4 等，≤30MB） |

---

## 二、读取飞书云文档

### read_doc.py — 读取飞书文档内容

```
python {_skill_base}/scripts/read_doc.py \
    --doc "https://xxx.feishu.cn/docx/doccnXXXXXX"
```

### read_sheet.py — 读取飞书电子表格数据

```
python {_skill_base}/scripts/read_sheet.py \
    --sheet "https://xxx.feishu.cn/sheets/shtcnXXXXXX" \
    --sheet_id Sheet1 \
    --range A1:D10
```

---

## 三、查询群成员

### get_chat_members.py — 获取群组成员列表

```
python {_skill_base}/scripts/get_chat_members.py --chat_id oc_xxxxx
```

---

## 四、日历操作

### list_events.py — 查询日历事件

```
python {_skill_base}/scripts/list_events.py \
    --calendar_id primary \
    --start_time 2026-03-01T00:00:00+08:00 \
    --end_time 2026-03-31T23:59:59+08:00
```

### create_event.py — 创建日历事件

```
python {_skill_base}/scripts/create_event.py \
    --summary "周例会" \
    --start_time 2026-04-09T10:00:00+08:00 \
    --end_time 2026-04-09T11:00:00+08:00 \
    --description "本周进度同步" \
    --attendees '["ou_aaa", "ou_bbb"]'
```

---

## 五、创建文档 / 表格

### create_doc.py — 创建飞书文档（支持 Markdown 内容）

```
python {_skill_base}/scripts/create_doc.py \
    --title "季度报告" \
    --content "# 一季度\n\n正文内容..."
```

### create_sheet.py — 创建电子表格

```
python {_skill_base}/scripts/create_sheet.py --title "销售数据"
```

### upload_sheet.py — 导入本地 Excel 为飞书表格

```
python {_skill_base}/scripts/upload_sheet.py \
    --file_path /workspace/outputs/report.xlsx \
    --title "销售报告"
```

### write_sheet.py — 向表格写入数据

```
python {_skill_base}/scripts/write_sheet.py \
    --sheet "https://xxx.feishu.cn/sheets/shtcnXXXX" \
    --values '[["姓名","年龄"],["Alice",30]]'
```

---

## 六、多维表格（Bitable）

### create_bitable.py — 创建多维表格应用

```
python {_skill_base}/scripts/create_bitable.py --name "项目管理"
```

### create_bitable_table.py — 创建数据表

```
python {_skill_base}/scripts/create_bitable_table.py \
    --app "https://xxx.feishu.cn/base/BxxXXXX" \
    --name "任务清单" \
    --fields '[{"name":"任务名称","type":"text"},{"name":"优先级","type":"select","options":["高","中","低"]}]'
```

### write_bitable_records.py — 批量写入记录

```
python {_skill_base}/scripts/write_bitable_records.py \
    --app "https://xxx.feishu.cn/base/BxxXXXX" \
    --table_id tblXXXXXX \
    --records '[{"任务名称":"完成API文档","优先级":"高"}]'
```

---

## 输出格式

所有脚本统一输出 JSON 到 stdout：

```json
{"errcode": 0, "errmsg": "success", "data": {...}}
{"errcode": 1, "errmsg": "错误说明\n建议：...", "data": {}}
```
