# 陪伴 Workspace 输出过滤器（Output Gate）

治理陪伴型 workspace 的"操作露出"（"让我先读取记忆…"类文本到达用户）。
设计文档：`docs/archive/companion-output-filter-design.md`（v2.0）；需求：`docs/archive/companion-output-filter-requirements.md`。

**只作用于 companion workspace**。任务型（work 模式）workspace：不安装本目录任何文件，
框架侧改动全部在 `IsCompanion()` 分支内，work 路径零行为变化（含原"已收到图片"/"✅ 已开启新会话"文案）。

## 文件

| 文件 | 角色 |
|---|---|
| `output_filter.py` | **Stop hook 主流程**：门控 → transcript 提取 → Layer 1 → Layer 2（过滤 sub agent，只返回删除句序号）→ 重组 → persona 兜底 → 写 `FINAL_REPLY.md` + 全文日志 |
| `filter_rules.py` | **Layer 1 句级规则引擎**（确定性）；同时被 proactive 链路 `send_and_record.py` 复用 |
| `filter_prompt.md` | Layer 2 system prompt（指令/数据分层防注入；`{persona_name}`/`{persona_vocab}` 占位） |
| `write_turn_snapshot.py` | UserPromptSubmit hook：轮开始状态快照（消除 phase2_done→done 轮内切换竞态） |
| `install_to_workspace.sh` | 安装器（幂等）：同步 hooks → 注册 settings → 接入 proactive Layer 1 → 建兜底池 |
| `rollback_filter.sh` | 回滚（<5 分钟）：摘 hook 注册即生效，无需重启框架 |

框架侧（已合入）：`internal/session/filter.go`（文件协议消费 + canary）、`worker.go`（companion 分支替换 + 硬编码文案 persona 化）。

## 测试

```bash
go test ./internal/session/ ./internal/claude/          # 框架侧 + golden 契约
python3 -m pytest tests/output_filter/ -q               # hook 侧（67 用例）
```

跨语言契约：`internal/claude/testdata/golden_transcripts/` 同时被
`golden_test.go`（Go parseLine）与 `test_contract_golden.py`（Python 提取）消费，
两端拼接语义必须一致；改 fixtures 必须双端同步。

## 部署（按设计 §6 灰度顺序）

```bash
# 1. 重新构建并重启框架（worker.go 改动需生效）
go build -o server ./cmd/server && <重启>

# 2. 灰度安装到一个 companion workspace（评测门禁通过后）
bash companion_filter/install_to_workspace.sh ./workspaces/companion-demo
#    安装后按 persona 调整 ./workspaces/companion-demo/memory/fallback_replies.yaml

# 3. 观察 .filter_log.jsonl 七项指标 → 达标后推 mango_mother → 模板固化
bash companion_filter/install_to_workspace.sh --template

# 回滚（任一阶段）
bash companion_filter/rollback_filter.sh ./workspaces/companion-demo
```

## 运行时调参（env，hook 进程读取）

| 变量 | 默认 | 用途 |
|---|---|---|
| `CC_FILTER_MODEL` | `haiku` | Layer 2 过滤模型（建议部署时写完整模型名） |
| `CC_FILTER_TIMEOUT` | `10` | Layer 2 超时秒数，超时降级 Layer 1 |
| `CC_FILTER_CLAUDE_BIN` | `claude` | 故障注入/测试桩入口 |
| `CC_FILTER_CWD` | `/tmp/cc_output_filter` | sub agent 中性工作目录 |

## 待办（M3/M4，见设计 §6-7）

- [ ] 评测集 `evals/output_filter/`（dirty≥50 / clean≥150，分层 0 容忍指标）+ replay/judge/report
- [ ] 存量脏历史一次性清洗（bot.db ≥16 条 + RECENT_HISTORY.md 重生成，与评测集标注合并）
- [ ] e2e 资产固化（真实 claude -p + fixture workspace，发版前手动触发）
- [ ] 每日巡检 cron（漏网发现 + filter_log/bot.db 对账 + 样本回收）
- [ ] 生产机用真实 haiku 复测 Layer 2 延迟（POC 沙盒实测端到端 6.9s）
