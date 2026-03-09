# Windows 兼容性说明

本项目已完全支持 Windows 平台（Windows 10/11）。

## 已解决的兼容性问题

### 1. 进程组设置

**问题**：原代码使用 Unix 特有的 `syscall.SysProcAttr{Setpgid: true}`，Windows 不支持。

**解决方案**：使用条件编译，创建平台特定文件：
- `internal/claude/executor_unix.go` - Unix/Linux/macOS 平台
- `internal/claude/executor_windows.go` - Windows 平台

### 2. 文件锁机制

**问题**：原 skill 文档使用 Linux 的 `flock` 命令，Windows 上不可用。

**解决方案**：创建跨平台 `filelock` 工具（基于 `github.com/gofrs/flock`）：
- 位置：`cmd/filelock/`
- 支持：Linux、macOS、Windows
- 用法：`filelock <lock-file> <timeout-seconds> <command> [args...]`

## 跨平台特性

以下组件已确认跨平台兼容：

- ✅ **路径处理**：全部使用 `filepath.Join()`，自动适配 Windows 路径分隔符
- ✅ **SQLite**：使用 CGO-free 的 `glebarez/sqlite`，无需 C 编译器
- ✅ **文件监听**：`fsnotify` 原生支持 Windows
- ✅ **定时任务**：`gocron/v2` 纯 Go 实现
- ✅ **飞书 SDK**：`oapi-sdk-go/v3` 跨平台
- ✅ **Claude CLI**：Go 的 `exec` 包自动处理 `.exe` 后缀

## Windows 部署建议

### 推荐环境

1. **Git Bash**（推荐）：运行 Shell 脚本（`start.sh`、`init_workspace.sh`）
2. **WSL2**：完整 Linux 环境，兼容性最佳
3. **PowerShell**：可直接运行 `go` 命令和 `filelock` 工具

### 构建与运行

```powershell
# 克隆项目
git clone https://github.com/kid0317/cc-workspace-bot.git
cd cc-workspace-bot

# 构建（包含 filelock 工具）
go build ./...

# 运行
go run ./cmd/server -config config.yaml
```

### filelock 工具使用

```powershell
# 构建 filelock
go build ./cmd/filelock

# 使用示例（PowerShell）
.\filelock.exe C:\temp\.lock 10 powershell -Command "Add-Content -Path 'C:\temp\data.txt' -Value 'new line'"

# 使用示例（Git Bash）
./filelock /c/temp/.lock 10 bash -c "echo 'new line' >> /c/temp/data.txt"
```

## 已知限制

1. **Shell 脚本**：`start.sh` 和 `init_workspace.sh` 需要在 Git Bash 或 WSL2 中运行
2. **Python 脚本**：飞书操作脚本（`skills/feishu_ops/scripts/`）需要安装 Python 3.7+

## 测试状态

- ✅ 编译通过：`go build ./...`
- ✅ 测试通过：`go test ./...`
- ✅ 跨平台构建标签正确应用
- ✅ filelock 工具在 Windows 上正常工作

## 技术细节

### 条件编译

使用 Go 的 build tags 实现平台特定代码：

```go
//go:build unix
// 仅在 Unix/Linux/macOS 上编译

//go:build windows
// 仅在 Windows 上编译
```

### 文件锁实现

`filelock` 工具使用 `github.com/gofrs/flock`，底层实现：
- **Unix/Linux**：`flock(2)` 系统调用
- **Windows**：`LockFileEx` Win32 API

两者语义一致，确保跨平台行为统一。
