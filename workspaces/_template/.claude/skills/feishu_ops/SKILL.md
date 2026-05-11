---
name: feishu_ops
description: "飞书操作：向用户/群组发送消息（文字/富文本/图片/文件）、读写云文档/电子表格/多维表格、查询群成员、管理日历事件。适合推送通知、发送处理结果文件、读取共享文档、批量发送报告等场景。"
type: task
version: "3.1"
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

### read_doc.py — 读取飞书文档内容（带本地缓存）

```
python {_skill_base}/scripts/read_doc.py \
    --doc "https://xxx.feishu.cn/docx/doccnXXXXXX" [--no-cache] [--verify-remote]
```

| 参数 | 必填 | 说明 |
|------|------|------|
| `--doc` | ✅ | 飞书文档 URL 或 doc_token |
| `--no-cache` | 否 | 跳过本地缓存，强制远程拉取 |
| `--verify-remote` | 否 | 命中本地后顺手校验远程 revision 是否变化（变化只 warning，仍返回本地快照） |

**缓存逻辑**：若该飞书文档由本工作区通过 `create_doc.py` 上传过，且本地源 md 文件仍存在、内容未变，则直接读本地文件返回（`data.source == "local_cache"`），不再下载。否则走远程（`data.source == "remote"`）。

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

### create_doc.py — 把本地 Markdown 文件上传为飞书文档（带去重缓存）

> ⚠️ **只接受本地 `.md` 文件路径**。`--content` / `--content_file` 已废弃，传了会报 `errcode: 2`。
> 上传前若同内容（按 sha256）已上传过，直接复用旧链接（`data.cached == true`），不重复上传。

```
python {_skill_base}/scripts/create_doc.py \
    --file_path /root/course/ai-pm/前期沟通/前期沟通报告_20260510.md \
    --title "AI PM 前期沟通报告" [--folder_token <token>] [--no-cache]
```

| 参数 | 必填 | 说明 |
|------|------|------|
| `--file_path` | ✅ | 本地 `.md` 文件绝对路径，**必须在工作区 `/root/course` 内**，≤10MB |
| `--title` | 否 | 文档标题（默认取文件名去后缀） |
| `--folder_token` | 否 | 目标飞书文件夹 token（留空放根目录） |
| `--no-cache` | 否 | 跳过缓存命中，强制重新上传 |

返回 `data`：`{url, document_id, cached(bool), sha256, record_id}`。

> **上传前要先生成/保存 md 文件时**：见下方「八、文件存放规范」——优先放到项目内合适位置，没有归属时才放 `/root/course/tmp_file/`，禁止放 `/tmp`、CWD、skill 目录内。

### dump_index.py — 查看上传索引

```
python {_skill_base}/scripts/dump_index.py [--status active|deleted|remote_missing|all]
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

## 七、文件存放规范（上传飞书文档前必读）

调用 `create_doc.py` 前若需要先生成或保存 `.md` 文件，存放位置遵循以下优先级：

1. **项目内合适位置**（首选）：md 文档若属于工作产物（课程稿、报告、设计文档），放到对应业务目录：
   `ai-pm/`、`multi-agent/`、`企业培训/<客户>/`、`.claude/skills/<skill>/docs/` 等。
2. **`/root/course/tmp_file/`**（次选）：纯过渡性、无明确归属的 md，放这里（可建子目录）。
3. **禁止**放到 `/tmp`、`/var/tmp`、当前工作目录（CWD）、skill 目录根目录或 `scripts/` 下。

`create_doc.py` 会拒绝工作区（`/root/course`）之外的文件路径，并提示重新放置。

---

## 八、文档缓存与索引（内部机制）

- 索引库：`{_skill_base}/index/doc_cache.db`（SQLite，自动创建）。
- 记录每次上传的 `(sha256, 飞书 doc_token/url, 本地源路径, 标题, ...)` 映射。
- `create_doc.py` 上传前查 sha256；`read_doc.py` 读取前查 doc_token。命中即走本地，省去重复上传/下载。
- 索引写入失败不会让上传失败（上传是不可逆操作，索引只是优化层）。
- 用 `dump_index.py` 查看，无需手工编辑 db 文件。

---

## 输出格式

所有脚本统一输出 JSON 到 stdout：

```json
{"errcode": 0, "errmsg": "success", "data": {...}}
{"errcode": 1, "errmsg": "错误说明\n建议：...", "data": {}}
```
