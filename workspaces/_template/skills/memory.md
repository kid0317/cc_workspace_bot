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

### 写入流程（使用 filelock 防并发冲突）

```bash
# 使用 filelock 工具（跨平台，支持 Linux/macOS/Windows）
# 语法: filelock <lock-file> <timeout-seconds> <command> [args...]

# 示例 1: 追加内容到 memory 文件
filelock <memory_lock_path> 10 bash -c "
  echo '新内容' >> <memory_dir>/user_profile.md
"

# 示例 2: 读取后更新
filelock <memory_lock_path> 10 bash -c "
  cat <memory_dir>/user_profile.md 2>/dev/null
  echo '更新内容' >> <memory_dir>/user_profile.md
"

# Windows PowerShell 示例:
filelock <memory_lock_path> 10 powershell -Command "
  Add-Content -Path '<memory_dir>/user_profile.md' -Value '新内容'
"
```

**参数说明**：
- `<lock-file>`: 锁文件路径（从 SESSION_CONTEXT.md 读取）
- `<timeout-seconds>`: 等待锁的超时时间（秒），0 表示无限等待
- `<command>`: 要执行的命令

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

- memory 文件由多个 session 共享，写入前必须使用 filelock 工具加锁
- filelock 工具位于项目根目录，跨平台支持 Linux/macOS/Windows
- 使用增量更新，不要覆盖整个文件（除非明确重置）
- 敏感信息（密码、密钥）**不写入 memory**
