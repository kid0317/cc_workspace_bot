"""飞书认证与公共工具模块。

所有 feishu_ops scripts 通过 sys.path 导入本模块，避免重复代码。
凭证路径：{workspace}/.claude/skills/feishu_ops/feishu.json
由 init_workspace.sh 在初始化时写入，不暴露给 LLM。

注意：不依赖 FEISHU_APP_ID 等环境变量，因为 claude Bash tool
启动的子进程不会继承父进程注入的环境变量。
"""

import json
import os
import re
import subprocess
import sys
import requests


# ───────────────────────── 凭证路径 ─────────────────────────

# 凭证文件与 scripts/ 同级：{workspace}/.claude/skills/feishu_ops/feishu.json
_FEISHU_OPS_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_CRED_PATH = os.path.join(_FEISHU_OPS_DIR, "feishu.json")


def _load_creds() -> dict:
    """从 feishu_ops/feishu.json 加载凭证，返回完整 dict。"""
    if not os.path.exists(_CRED_PATH):
        _exit_error(f"未找到飞书凭证文件：{_CRED_PATH}")
    with open(_CRED_PATH) as f:
        return json.load(f)


# ───────────────────────── HTTP 认证（兼容旧脚本） ─────────────────────────

def _get_token() -> str:
    """获取 tenant_access_token（旧 HTTP 路径，兼容用）。"""
    creds = _load_creds()
    app_id = creds["app_id"]
    app_secret = creds["app_secret"]
    resp = requests.post(
        "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal",
        json={"app_id": app_id, "app_secret": app_secret},
        timeout=10,
    )
    data = resp.json()
    if data.get("code") != 0:
        _exit_error(f"获取 tenant_access_token 失败：code={data.get('code')}, msg={data.get('msg')}")
    return data["tenant_access_token"]


def get_headers() -> dict:
    """返回 JSON 请求 headers（含 Authorization Bearer token）。"""
    return {
        "Authorization": f"Bearer {_get_token()}",
        "Content-Type": "application/json",
    }


def get_auth_header() -> dict:
    """返回仅含 Authorization 的 headers（用于 multipart 文件上传）。"""
    return {"Authorization": f"Bearer {_get_token()}"}


# ───────────────────────── lark-cli 调用 ─────────────────────────

def get_lark_cli_base_cmd() -> list:
    """返回含 --profile 的 lark-cli 基础命令前缀。"""
    creds = _load_creds()
    profile = creds.get("lark_profile") or creds["app_id"]
    return ["lark-cli", "--profile", profile]


def get_lark_cli_target_flags(routing_key: str) -> list:
    """将 routing_key 转换为 lark-cli 的 --user-id 或 --chat-id 参数。"""
    receive_id_type, receive_id = parse_routing_key(routing_key)
    if receive_id_type == "open_id":
        return ["--user-id", receive_id]
    return ["--chat-id", receive_id]


def run_lark_cli(args: list) -> dict:
    """执行 lark-cli 命令，返回 data 字段。失败时 output_error 退出。

    lark-cli 成功时：stdout 为干净 JSON {"ok": true, "identity": "bot", "data": {...}}
    lark-cli 失败时：exit code 2，stderr 为 JSON {"ok": false, "error": {...}}
    """
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


# ───────────────────────── 路由解析 ─────────────────────────

def parse_routing_key(routing_key: str) -> tuple:
    """将 routing_key 转换为 (receive_id_type, receive_id)。

    支持格式：
    - p2p:ou_xxx   → ("open_id", "ou_xxx")
    - group:oc_xxx → ("chat_id", "oc_xxx")
    - ou_xxx       → ("open_id", "ou_xxx")
    - oc_xxx       → ("chat_id", "oc_xxx")
    """
    if routing_key.startswith("p2p:"):
        return "open_id", routing_key[4:]
    if routing_key.startswith("group:"):
        return "chat_id", routing_key[6:]
    if routing_key.startswith("ou_"):
        return "open_id", routing_key
    if routing_key.startswith("oc_"):
        return "chat_id", routing_key
    return "open_id", routing_key


# ───────────────────────── URL 解析 ─────────────────────────

def parse_doc_token(url_or_token: str) -> str:
    """从飞书文档 URL 或 token 字符串中提取 doc_token。"""
    m = re.search(r"/doc[xs]?/([A-Za-z0-9_-]+)", url_or_token)
    if m:
        return m.group(1)
    return url_or_token.strip().rstrip("/")


def parse_sheet_token(url_or_token: str) -> str:
    """从飞书电子表格 URL 或 token 字符串中提取 spreadsheet_token。"""
    m = re.search(r"/s(?:preadsheet|heet)s?/([A-Za-z0-9_-]+)", url_or_token)
    if m:
        return m.group(1)
    return url_or_token.strip().rstrip("/")


def parse_bitable_token(url_or_token: str) -> str:
    """从飞书多维表格 URL 或 token 字符串中提取 app_token。"""
    m = re.search(r"/base/([A-Za-z0-9_-]+)", url_or_token)
    if m:
        return m.group(1)
    return url_or_token.strip().rstrip("/")


# ───────────────────────── 输出规范 ─────────────────────────

def output_ok(data: dict) -> None:
    """打印成功结果并退出（exit 0）。"""
    print(json.dumps({"errcode": 0, "errmsg": "success", "data": data}, ensure_ascii=False))
    sys.exit(0)


def output_error(errmsg: str, hint: str = "") -> None:
    """打印错误结果并退出（exit 0，errcode=1）。"""
    msg = errmsg + (f"\n建议：{hint}" if hint else "")
    print(json.dumps({"errcode": 1, "errmsg": msg, "data": {}}, ensure_ascii=False))
    sys.exit(0)


def _exit_error(errmsg: str) -> None:
    """内部错误出口。"""
    output_error(errmsg)


def check_feishu_resp(resp_data: dict, hint: str = "") -> None:
    """检查飞书 API 响应，非 0 时打印错误并退出。"""
    if resp_data.get("code") != 0:
        output_error(
            f"飞书 API 错误：code={resp_data.get('code')}, msg={resp_data.get('msg')}",
            hint,
        )
