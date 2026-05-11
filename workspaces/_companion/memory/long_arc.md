# 角色持续性事项（long_arc）

> **v2.2 Phase 2 新增** · 跨 session 持续的"角色自己未解决的事"
>
> 规则：
> - 最多 2 条 active，其余归档到 `long_arc_archive.md`
> - 新条目必须从 life_log 已出现的主题**升格**（≥ 3 次出现）
> - life_sim 每 7-14 天慢推进一次（小变化，不解决）
> - 对话中使用频率 ≤ 10%——是"背景辐射"，不是"剧本线"

---

## 格式规范

```markdown
## [ARC{NNN}] {active|dormant|resolved|archived} | 升格自 L{xxx}/L{yyy}/L{zzz} · YYYY-MM-DD → {status_date}

**主题**：一句话描述

**出处**：
- [Lxxx] 时间戳 · "原文引用"
- ...

**当前状态**：角色此刻的理解/停滞点

**标签**：stuck, work, ...

**触发场景**：
- 用户聊 X / Y / Z

**推进速度**：慢 / 中 / 停滞

**禁止行为**：
- ...
```

---

（本 workspace 尚无 active ARC。phase2_done 后由用户 + Claude 协同从 life_log 识别升格候选。）
