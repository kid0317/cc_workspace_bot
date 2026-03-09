# filelock

跨平台文件锁工具，用于保护并发文件访问。支持 Linux、macOS 和 Windows。

## 功能

- 跨平台文件锁（基于 `github.com/gofrs/flock`）
- 可配置超时时间
- 自动释放锁
- 透传命令的退出码和输出

## 使用方法

```bash
filelock <lock-file> <timeout-seconds> <command> [args...]
```

### 参数

- `<lock-file>`: 锁文件路径
- `<timeout-seconds>`: 等待锁的超时时间（秒），0 表示无限等待
- `<command>`: 获取锁后要执行的命令
- `[args...]`: 命令的参数

### 示例

```bash
# 追加内容到文件（10 秒超时）
filelock /tmp/.mylock 10 bash -c "echo 'new line' >> /tmp/data.txt"

# 读取并更新文件
filelock /tmp/.mylock 10 bash -c "cat /tmp/data.txt && echo 'update' >> /tmp/data.txt"

# Windows PowerShell
filelock C:\temp\.mylock 10 powershell -Command "Add-Content -Path 'C:\temp\data.txt' -Value 'new line'"

# 无限等待锁
filelock /tmp/.mylock 0 cat /tmp/data.txt
```

## 构建

```bash
go build ./cmd/filelock
```

## 在 cc-workspace-bot 中的使用

在 workspace 的 memory skill 中使用 filelock 保护并发写入：

```bash
filelock <memory_lock_path> 10 bash -c "
  echo '新内容' >> <memory_dir>/user_profile.md
"
```

其中 `<memory_lock_path>` 和 `<memory_dir>` 从 `SESSION_CONTEXT.md` 中读取。
