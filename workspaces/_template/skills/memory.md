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
- Memory dir: <workspace_dir>/memory
- Memory lock: <workspace_dir>/.memory.lock
```

### 写入流程（使用 flock 防并发冲突）

```bash
# 使用系统内置 flock 命令（Linux/macOS）
# 语法: flock <lock-file> bash -c "<command>"

# 示例 1: 追加内容到 memory 文件
flock <memory_lock_path> bash -c "
  echo '新内容' >> <memory_dir>/user_profile.md
"

# 示例 2: 读取后更新
flock <memory_lock_path> bash -c "
  cat <memory_dir>/user_profile.md 2>/dev/null
  echo '更新内容' >> <memory_dir>/user_profile.md
"
```

### 文件组织

```
memory/
├── MEMORY.md           # 主索引，每次 session 自动加载
├── user_profile.md     # 用户基础信息（初始化时填写）
└── [自定义文件]         # 由各 skill 按需创建，例如：
                        #   todos_today.md（todo skill）
                        #   insights/index.md（insights skill）
                        #   cases/index.md（cases skill）
```

**规则**：每个自定义 skill 负责在自己的 skill 文件中声明它需要的 memory 文件结构，并负责创建和维护。

## 读取 memory

MEMORY.md 由启动流程自动加载。如需额外上下文：

```bash
cat <memory_dir>/user_profile.md 2>/dev/null
```

## 定期归档

`daily_logs/`、`insights/` 等目录会随时间积累。建议每月归档一次旧文件（保留最近 90 天，更早的移入 `memory/archive/`）。

可在初始化时创建月度归档定时任务，prompt 示例：

```
请将 memory/daily_logs/ 中 90 天前的日志文件移入 memory/archive/daily_logs/，
将 memory/insights/ 中 90 天前的月份文件移入 memory/archive/insights/。
操作前先列出待归档文件请求确认，归档后更新 MEMORY.md 的快速摘要。
```

## 注意事项

- memory 文件由多个 session 共享，写入前必须使用 `flock` 加锁
- 使用增量更新，不要覆盖整个文件（除非明确重置）
- 敏感信息（密码、密钥）**不写入 memory**
