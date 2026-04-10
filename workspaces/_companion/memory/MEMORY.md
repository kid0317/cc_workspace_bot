# 陪伴记忆索引

> 本文件由 Claude Code 自动注入到每次会话上下文。保持简洁（≤200 行）。
> 详细内容见各 Topic 文件。

---

## 初始化进度

```
initialization_status: pending          # pending | phase1_done | phase2_done | done
```

- [ ] 阶段一：角色设定（persona.md 写入 + CLAUDE.md 角色设定块更新）
- [ ] 阶段二：用户信息收集（user_profile.md 写入，含 routing_key + 时区）
- [ ] 阶段三：主动唤醒任务已创建（tasks/proactive_reach.yaml）
- [ ] initialization_status 更新为 done

---

## 关系摘要

> 初始化完成后填写（2-3 句，描述当前关系现状）。

角色名：[待设定]
用户名：[待设定]
关系现状：[待填写]

---

## 最近未解决事项（Top 3）

> 从 events.md 自动摘要，每次记忆写入后更新。

（暂无）

---

## 文件索引

| 文件 | 内容 |
|------|------|
| [persona.md](./persona.md) | 角色完整设定（外貌/性格/口头禅/示例对话） |
| [user_profile.md](./user_profile.md) | 用户画像（基本信息/偏好/禁忌/当前重要事项） |
| [events.md](./events.md) | 事件/约定/Todo（结构化条目，含 last_active 哨兵） |
| [life_log.md](./life_log.md) | 角色生活日志（life_sim cron 自动写入） |
| [life_log_index.md](./life_log_index.md) | 专有名词去重索引（life_sim 维护） |
| [distill_state.md](./distill_state.md) | [D000] 记忆提炼时间戳哨兵 |
| [RECENT_HISTORY.md](./RECENT_HISTORY.md) | 跨 session 最近对话（UserPromptSubmit Hook 自动注入） |
