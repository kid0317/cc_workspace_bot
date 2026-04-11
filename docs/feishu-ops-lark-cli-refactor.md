# feishu_ops 改造设计文档：引入 lark-cli

**状态**：已确认，实施中  
**日期**：2026-04-09  
**背景**：引入 `@larksuite/cli`（lark-cli）后，将 feishu_ops 脚本内部实现从裸 HTTP 改为调用 lark-cli。

---

## 1. 现状（As-Is）

### 1.1 凭证注入链路

```
config.yaml (app_id + app_secret per app)
    ↓ init_workspace.sh 写文件
{workspace}/.claude/skills/feishu_ops/feishu.json
    ↓ _feishu_auth.py 读取 → 换取 tenant_access_token
Python scripts 直接调用飞书 OpenAPI（裸 HTTP）
```

### 1.2 脚本层职责（保留不变）

- **routing_key 解析**：`p2p:ou_xxx` / `group:oc_xxx` → lark-cli flag
- **统一输出格式**：`{"errcode": 0/1, "errmsg": "...", "data": {...}}`
- **参数校验与友好提示**
- **多步编排**（如 send_image = 上传 + 发送）
- **凭证隔离**：每个 workspace 只读自己目录下的 feishu.json

---

## 2. 目标（To-Be）

### 2.1 核心架构

```
feishu.json（新增 lark_profile 字段）
    ↓ _feishu_auth.get_lark_cli_base_cmd() 读取 profile 名
Python scripts 调用 lark-cli --profile <name> ...
    ↓ lark-cli 处理认证、分页、多步编排
飞书 OpenAPI
```

### 2.2 PoC 验证结果（2026-04-09）

| 场景 | 实测结果 |
|------|---------|
| 成功调用 stdout | 干净 JSON `{"ok": true, "identity": "bot", "data": {...}}` |
| 失败调用 | exit code 2，错误 JSON 在 **stderr**（`{"ok": false, "error": {...}}`），stdout 为空 |
| profile list 格式 | JSON 数组 `[{"name": "...", "appId": "...", "brand": "feishu", "active": true}]` |
| profile 不存在 | exit code 2，stderr `{"ok": false, "error": {"type": "config", "message": "profile \"xxx\" not found", "hint": "available profiles: ..."}}`  |

**结论**：`run_lark_cli()` 设计可行。成功时解析 stdout，失败时解析 stderr。

### 2.3 凭证隔离方案

lark-cli profile 全局存储在 `~/.lark-cli/config.json`，通过 `--profile <name>` 区分 app。

```
init_workspace.sh 新增步骤：
  echo "$SECRET" | lark-cli config init \
    --name <app-id> --app-id <feishu-app-id> --app-secret-stdin

feishu.json 新增字段：
  {"app_id": "cli_xxx", "app_secret": "xxx", "lark_profile": "<app-id>"}

所有 lark-cli 调用：
  lark-cli --profile <lark_profile> im +messages-send ...
```

### 2.4 安全双层隔离

**Layer 1 — CLAUDE.md 文本约束**（每个 workspace）：
```markdown
## 飞书操作规范（CRITICAL）
只能通过 feishu_ops 脚本操作飞书。禁止直接调用：lark-cli 命令、全局 lark-* skills、飞书 OpenAPI。
```

**Layer 2 — config.yaml allowedTools 屏蔽**：
lark-cli 是 Bash 命令，无法通过 allowedTools 精确屏蔽。但可在 workspace CLAUDE.md 中以强约束文本 + 不在 allowed_tools 以外注册任何工具来实现。实际隔离依赖：feishu.json 里的 profile 名不暴露给 AI，AI 无法猜测其他 workspace 的 profile 名。

---

## 3. 关键设计决策

### 3.1 `_feishu_auth.py` 核心变更

**`_load_creds()` 改返回 dict**（原返回 tuple，导致 `.get()` 调用报错）：

```python
def _load_creds() -> dict:
    with open(_CRED_PATH) as f:
        return json.load(f)
```

`get_headers()` 和 `get_auth_header()` 同步改为 dict 访问：
```python
def _get_token() -> str:
    creds = _load_creds()
    app_id, app_secret = creds["app_id"], creds["app_secret"]
    ...
```

**新增函数**：

```python
def get_lark_cli_base_cmd() -> list[str]:
    """返回含 --profile 的 lark-cli 基础命令前缀。"""
    creds = _load_creds()
    profile = creds.get("lark_profile") or creds["app_id"]
    return ["lark-cli", "--profile", profile]

def get_lark_cli_target_flags(routing_key: str) -> list[str]:
    """将 routing_key 转换为 lark-cli 的 --user-id 或 --chat-id 参数。"""
    receive_id_type, receive_id = parse_routing_key(routing_key)
    if receive_id_type == "open_id":
        return ["--user-id", receive_id]
    return ["--chat-id", receive_id]

def run_lark_cli(args: list[str]) -> dict:
    """执行 lark-cli 命令，返回 data 字段。失败时 output_error 退出。"""
    import subprocess
    cmd = get_lark_cli_base_cmd() + args
    result = subprocess.run(cmd, capture_output=True, text=True)

    if result.returncode != 0:
        try:
            err = json.loads(result.stderr)
            error_info = err.get("error", {})
            msg = error_info.get("message", "lark-cli 执行失败")
            hint = error_info.get("hint", "")
        except (json.JSONDecodeError, ValueError):
            msg = result.stderr.strip() or result.stdout.strip() or "lark-cli 执行失败"
            hint = ""
        output_error(msg, hint)

    try:
        out = json.loads(result.stdout)
    except (json.JSONDecodeError, ValueError):
        output_error(f"lark-cli 输出解析失败: {result.stdout[:300]}")

    return out.get("data") or {}
```

### 3.2 send_post.py 迁移策略（修正 review 问题）

`--markdown` 发送 markdown 消息类型，与原 `post` 类型行为不同（客户端渲染差异，title 字段丢失）。

**正确方案**：保留 post 格式，通过 `--content + --msg-type post` 传给 lark-cli：

```python
content_json = json.dumps({
    "zh_cn": {
        "title": args.title or "",
        "content": paragraphs_to_post_content(args.paragraphs)
    }
})
data = run_lark_cli(
    get_lark_cli_target_flags(args.routing_key) +
    ["--content", content_json, "--msg-type", "post"]
)
```

### 3.3 profile 存在性检查（修正 review 问题）

`lark-cli profile list` 输出 JSON 数组，不能用 grep。改用 python3 解析：

```bash
if lark-cli profile list 2>/dev/null | \
    python3 -c "import sys,json; names=[p['name'] for p in json.load(sys.stdin)]; exit(0 if '${APP_ID}' in names else 1)" 2>/dev/null; then
    warn "lark-cli profile '${APP_ID}' 已存在，跳过注册"
else
    echo "${FEISHU_APP_SECRET}" | lark-cli config init \
        --name "${APP_ID}" --app-id "${FEISHU_APP_ID}" --app-secret-stdin
fi
```

### 3.4 rollback 机制

feishu.json 中 `lark_profile` 字段存在时用 lark-cli，缺失时自动 fallback 到旧 HTTP 路径。存量 workspace 在 profile 注册完成后才更新 feishu.json，确保任何时刻都有可用路径。

---

## 4. 脚本迁移对照表

### 消息类

| 脚本 | lark-cli 命令 | 说明 |
|------|-------------|------|
| send_text.py | `im +messages-send --text` | 直接替换 |
| send_post.py | `im +messages-send --content <post_json> --msg-type post` | 保持 post 类型，不用 --markdown |
| send_image.py | `im +messages-send --image <path>` | 自动上传，一步完成 |
| send_file.py | `im +messages-send --file <path>` | 自动上传，一步完成 |

### 文档表格类

| 脚本 | lark-cli 命令 |
|------|-------------|
| read_doc.py | `docs +fetch --doc <url/token>` |
| create_doc.py | `docs +create --title ... --markdown @<file>` |
| read_sheet.py | `sheets +read --url ... --sheet-id ... --range ...` |
| create_sheet.py | `sheets +create --title ...` |
| write_sheet.py | `sheets +write --url ... --range ... --values ...` |
| upload_sheet.py | `drive +import --file ... --type sheet` |

### 日历多维表格类

| 脚本 | lark-cli 命令 |
|------|-------------|
| list_events.py | `calendar events instance_view --params '{...}'`（Unix 时间戳） |
| create_event.py | `calendar +create --summary ... --start ... --end ... --attendee-ids ...` |
| create_bitable.py | `base +base-create --name ...` |
| create_bitable_table.py | `base +table-create --base-token ... --name ... --fields ...` |
| write_bitable_records.py | `api POST .../records/batch_create --data ...` |

### 不迁移（本次）

| 脚本 | 原因 |
|------|------|
| get_chat_members.py | 保持现有 HTTP 实现，后续单独验证 |

---

## 5. init_workspace.sh 变更摘要

新增 Step（"写入飞书凭证"之后）：
1. 注册 lark-cli profile（用 python3 检查是否已存在）
2. feishu.json 模板新增 `lark_profile` 字段

模板 feishu.json 改为占位符：
```json
{"app_id": "YOUR_APP_ID", "app_secret": "YOUR_APP_SECRET", "lark_profile": "YOUR_PROFILE_NAME"}
```

---

## 6. 存量 workspace 迁移脚本

`scripts/migrate_workspaces.sh`：读取 config.yaml，对每个 app：
1. 注册 lark-cli profile（已存在则跳过）
2. 更新 feishu.json 添加 `lark_profile` 字段
3. 同步最新 scripts/ 到该 workspace

---

## 7. 已解决的问题清单

| 问题 | 解决方案 |
|------|---------|
| lark-cli 输出格式未验证 | PoC 已验证，stdout 干净 JSON |
| `_load_creds()` 返回 tuple 与 `.get()` 冲突 | 改返回 dict |
| send_post 用 --markdown 行为不等价 | 改用 --content + --msg-type post |
| profile list 用 grep 不可靠 | 改用 python3 解析 JSON |
| 多租户隔离退化 | feishu.json 不暴露给 AI，CLAUDE.md 强约束 |
| 模板 feishu.json 含真实凭证 | 改为占位符 |
| 缺 rollback | lark_profile 字段存在才用新路径，否则 fallback HTTP |
