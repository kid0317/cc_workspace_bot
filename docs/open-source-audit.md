# 开源发布审计

> 状态：开源版整理
> 最后更新：2026-07-18

## 1. 结论

本项目可以开源，但必须只提交源码、模板、测试 fixture 和面向开发者的公开文档。真实配置、数据库、运行时 workspace、用户记忆、聊天记录、附件、日志和本机权限文件不能进入 git。

## 2. 可以公开的内容

| 路径 | 原因 |
|---|---|
| `cmd/` | Go 命令入口源码 |
| `internal/` | 核心服务源码 |
| `scripts/` | 开发、迁移、dry-run 脚本源码 |
| `workspaces/_template/` | 通用 workspace 模板 |
| `workspaces/_companion/` | 陪伴型 workspace 模板，内容为占位模板 |
| `config.yaml.template` | 无真实密钥的主配置模板 |
| `config.bailian.yaml.template` | 无真实密钥的第三方 provider 示例 |
| `docs/requirements.md` | 公开需求分析 |
| `docs/prd.md` | 公开 PRD |
| `docs/high-level-design.md` | 公开概要设计 |
| `docs/detailed-design.md` | 公开详细设计 |
| `docs/tech-research.md` | 公开技术选型说明 |
| `docs/archive/` | 阶段性历史设计，已从主文档入口降级为归档 |
| `testdata/` 或源码下测试 fixture | 不含真实凭证时可公开 |

## 3. 不能公开的内容

| 路径或模式 | 风险 |
|---|---|
| `config.yaml` | 飞书 App Secret、第三方 provider token |
| `config.bailian.yaml` | 第三方 provider token |
| `config.yaml.bak*` | 历史配置备份，可能包含旧密钥 |
| `config.yaml.no_sonnet` | 本机实验配置 |
| `bot.db`、`bot.db.*` | 真实会话、消息和任务数据 |
| `workspaces/<app>/bot.db` | 单 workspace 运行时数据库 |
| `workspaces/<app>/memory/` | 用户画像、长期记忆、事件记录 |
| `workspaces/<app>/sessions/` | 聊天历史、附件、SESSION_CONTEXT |
| `workspaces/<app>/tasks/` | 真实定时任务目标和 prompt |
| `workspaces/<app>/tmp/`、`tmp_file/` | 临时附件和生成物 |
| `.claude/settings.local.json` | 本机工具权限和 hook 配置；`workspaces/_companion/.claude/settings.local.json` 是模板例外 |
| `**/feishu_ops/feishu.json` | 飞书脚本真实凭证 |
| `server`、`filelock` | 本机编译产物 |
| `*.log`、`server.pid` | 运行日志和进程状态 |

## 4. 已提供的安全替代物

| 私有文件 | 公开替代 |
|---|---|
| `config.yaml` | `config.yaml.template` |
| `config.bailian.yaml` | `config.bailian.yaml.template` |
| `feishu_ops/feishu.json` | `workspaces/_template/.claude/skills/feishu_ops/feishu.json.template` |
| 普通 `.claude/settings.local.json` | `workspaces/_companion/.claude/settings.local.json` 仅作为模板权限白名单提交 |
| `filelock` | `go build -o filelock ./cmd/filelock` |
| `server` | `go build -o server ./cmd/server` |

## 5. 发布前检查命令

```bash
git status --short
git ls-files | rg '(^config\\.yaml$|config\\.yaml\\.bak|config\\.bailian\\.yaml$|bot\\.db|feishu\\.json$|settings\\.local\\.json$|^server$|^filelock$)'
git ls-files -z | xargs -0 rg -n '<真实密钥正则，如 App ID、API Key、私钥头>'
go build ./...
go test ./... -cover
go vet ./...
```

第二条命令应只返回允许公开的模板例外，或无输出。第三条命令中允许出现占位符，例如 `cli_your_feishu_app_id` 和 `your_dashscope_api_key`。

## 6. 维护规则

- 新增任何必须本地填写的配置时，同步新增 `.template`。
- 新增运行时目录时，同步更新 `.gitignore` 和本文档。
- 历史排障、内部实验、客户案例类文档默认放入 `docs/archive/`。
- 提交前运行 secret scan，不用人工目测替代命令检查。
