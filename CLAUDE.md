# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A personal/team assistant bot built on Feishu (Lark) and Claude Code. The bot receives messages via the Feishu event subscription system and responds using Claude AI. Written in Go, using `github.com/larksuite/oapi-sdk-go` as the Feishu SDK.
项目的设计文档在docs/design.md
项目的需求文档在docs/requirements.md

## Common Commands

```bash
# Build
go build ./...

# Run
go run ./cmd/server

# Test
go test ./...

# Run a single test
go test ./internal/feishu/... -run TestHandleMessage

# Test with coverage
go test ./... -cover

# Lint (requires golangci-lint)
golangci-lint run

# Format
gofmt -w .

# Tidy dependencies
go mod tidy
```

## Architecture

```
cmd/
  server/         # Main entry point: HTTP server + Feishu event listener
internal/
  feishu/         # Feishu SDK integration (oapi-sdk-go)
    handler.go    # Event dispatcher: routes incoming Feishu events
    sender.go     # Sends messages back to Feishu (text, cards, etc.)
  claude/         # Claude Code / Claude API integration
    client.go     # Wraps Claude API calls
    session.go    # Manages per-conversation context/history
  bot/            # Core bot logic
    router.go     # Dispatches commands/intents to handlers
    handlers/     # Individual command handlers
  config/         # Config loading from env vars
```

## Key Concepts

**Feishu Event Flow**: Feishu sends webhook POST requests to the server. The `internal/feishu/handler.go` verifies the signature, decodes the event, and dispatches it. Message events trigger Claude API calls; the response is sent back via `sender.go`.

**Session Management**: Feishu conversations (1:1 or group chats) map to Claude conversation sessions. Each chat ID gets its own message history to maintain context.

**Authentication**: Feishu app credentials (`APP_ID`, `APP_SECRET`) and Claude API key (`ANTHROPIC_API_KEY`) are loaded from environment variables — never hardcoded.

## Feishu SDK Usage

```go
import larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
import larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

client := lark.NewClient(appID, appSecret)
```

Event subscription uses the SDK's built-in HTTP handler:
```go
httpserver.NewEventDispatcherHandler(verificationToken, encryptKey, ...)
```

## Environment Variables

| Variable | Description |
|---|---|
| `FEISHU_APP_ID` | Feishu app ID |
| `FEISHU_APP_SECRET` | Feishu app secret |
| `FEISHU_VERIFICATION_TOKEN` | Webhook verification token |
| `FEISHU_ENCRYPT_KEY` | Webhook payload encryption key (optional) |
| `ANTHROPIC_API_KEY` | Claude API key |
| `PORT` | HTTP server port (default: 8080) |
