---
name: conversation_summary
description: |
  v2.2 M3 · 对话历程摘要。RECENT_HISTORY.md 超过 30 条时，压缩旧条目为 3-5 句历程摘要
  写入 memory/session_summary.md，防止长 context 导致角色漂移到共情模板。
  被 memory_distill 在检测条数超阈时调用，或用户主动校验记忆时调用。
allowed-tools: Read, Write, Edit
---

# 对话历程摘要 SOP

> **CRITICAL**：压缩要保留情感脉络（哪些话题让用户投入 / 回避 / 情绪波动），
> 而非流水账。遗漏情感主线 = 比遗忘更危险的"错误记忆"。

## 触发时机

- RECENT_HISTORY.md 超过 30 条时（由 memory_distill 检测）
- 用户主动说"你还记得我跟你聊过什么吗" / 任何记忆校验
- Phase 5 用户偏好 override 发生时（重新对齐基线）

## 执行步骤

### Step 0：Gate check

```bash
MEMORY_FILE="$WORKSPACE_DIR/memory/MEMORY.md"
INIT_STATUS=$(grep 'initialization_status:' "$MEMORY_FILE" 2>/dev/null | grep -oP '(pending|phase1_done|phase2_done|done)' | head -1)
[[ "$INIT_STATUS" != "done" ]] && exit 0

RECENT_HISTORY="$WORKSPACE_DIR/memory/RECENT_HISTORY.md"
SUMMARY_FILE="$WORKSPACE_DIR/memory/session_summary.md"
MSG_COUNT=$(grep -c '^\*\*' "$RECENT_HISTORY" 2>/dev/null)
[[ $MSG_COUNT -lt 30 ]] && exit 0
```

### Step 1：读取全量 RECENT_HISTORY + 已有 summary

读两份文件。session_summary.md 是**滚动更新**的，不是覆盖：本次总结的是"上次总结后至今"的新增部分。

### Step 2：提炼摘要（作者视角）

**不要做的事**：
- ❌ 流水账："用户讲了 A、B、C、D"
- ❌ 罗列事件："2026-04-18 聊剧本 / 04-19 聊工作 / 04-20 聊墓地"
- ❌ 摘抄台词："用户说'我爸走了'，角色说'嗯'"

**要做的事**（3-5 句）：
- ✓ 情感主线："用户这段时间在处理父亲去世一年后的丧事——先是找墓地的务实，夹着对'拖了一年'的自责"
- ✓ 角色已知的用户偏好/重要事实："美国读书工作过，现和妈妈住北京；喜欢耽美/权斗小说，自己也在写剧本；平日打车不开车"
- ✓ 情感关键点（用户投入 / 回避 / 崩溃 / 特别开心）："聊到《妻为上》时投入，聊到'懒'时有防御感"
- ✓ **未解决的钩子**："她提过想讨论剧本但还没真正开始；墓地事情还没定下来"

### Step 3：写入 session_summary.md

格式：

```markdown
# 对话历程摘要（滚动）

> 由 conversation_summary skill 维护。rolling 更新，不覆盖。
> 作为 long context 的压缩锚，防止角色漂向共情模板。

## 最近情感主线（至 {YYYY-MM-DD}）

{3-5 句，见 Step 2 原则}

## 已知用户偏好 / 事实（归集自 user_profile.md，以最近为准）

- 偏好 X
- 事实 Y

## 情绪关键点（最近 30 条里）

- {话题} → {用户状态：投入 / 回避 / 波动}

## 未解决的钩子

- {钩子 1}
- {钩子 2}

---

<!-- last_updated: {ISO timestamp} -->
<!-- covered_range: L{起始消息序号} - L{结束序号} -->
```

### Step 4：裁剪 RECENT_HISTORY.md

**注意**：不要一上来就裁剪。session_summary.md 稳定运行 1-2 周后再启用裁剪。

Phase 2 阶段：session_summary 生成但**不裁剪** RECENT_HISTORY（inject_history.py 依然塞 tail -49）。
待 summary 质量稳定后，Phase 2+2 周：改 inject_history.py 改成 tail -20 + 注入 session_summary。

## 校验路径（防错误记忆）

首次生成后，角色有机会通过主动唤醒说一句：
> "我对咱们最近聊的事的印象是 [简短复述]，大致对吗？"

用户若否认，由 memory_write 更新 session_summary。不允许 silent 的错记忆。

## 禁止

- ❌ 用 Haiku 压缩（精度不足以识别情感主线）
- ❌ 覆盖已有 session_summary（rolling 更新）
- ❌ 单次压缩超过 10 句（太多意味着没提炼到主线）
- ❌ 包含具体日期细节（除非是时间线关键点）—— 摘要不是流水账

## 与 long_arc 的仲裁

当 summary 的情感基调与 long_arc 冲突时：
- long_arc 优先（长期稳定的"角色自己的心事"）
- summary 可以提"用户这段时间聊到 X 让角色想起 arc"，但不能改写 arc

## 失败降级

生成失败（模型超时 / 格式错误）：
- 不更新 session_summary.md
- RECENT_HISTORY.md 保持原样（不裁剪）
- 记录失败事件到 `.life_sim_events.jsonl`
- 下次触发重试
