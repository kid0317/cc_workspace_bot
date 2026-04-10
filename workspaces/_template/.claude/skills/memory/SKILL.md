---
name: memory
description: "当需要将重要信息（用户偏好、背景、关键事实）跨 session 持久化保存时使用"
type: procedure
version: "1.0"
---

# Memory Skill

本 skill 规范如何将对话信息持久化到 memory/ 目录。

## 何时写入 memory

- 用户明确要求记住某些信息时
- 对话中获取到用户的重要偏好/背景/习惯时
- 需要在未来对话中复用的关键事实时

## 读写规范

### 路径来源

**所有路径从 SESSION_CONTEXT.md 中读取，不使用硬编码路径。**

```
SESSION_CONTEXT.md 中包含：
- Memory dir: /data/workspaces/<app>/memory
- Memory lock: /data/workspaces/<app>/.memory.lock
```

### 写入流程（使用 flock 防并发冲突）

```bash
# 1. 获取锁
flock -x <memory_lock_path> -c "
  # 2. 读取现有 memory 文件
  cat <memory_dir>/user_profile.md 2>/dev/null

  # 3. 写入更新内容（追加或覆盖）
  cat >> <memory_dir>/user_profile.md << 'EOF'
  ...新内容...
  EOF
"
```

### 文件组织建议

```
memory/
├── user_profile.md      # 用户基本信息、偏好
├── project_context.md   # 项目背景、技术栈
├── preferences.md       # 用户习惯、风格偏好
└── notes.md             # 其他重要笔记
```

## 读取 memory

对话开始时，如有必要，先读取相关 memory 文件作为上下文：

```bash
cat <memory_dir>/user_profile.md 2>/dev/null
cat <memory_dir>/project_context.md 2>/dev/null
```

## 注意事项

- memory 文件由多个 session 共享，写入前必须加 flock 锁
- 使用增量更新，不要覆盖整个文件（除非明确重置）
- 敏感信息（密码、密钥）**不写入 memory**
