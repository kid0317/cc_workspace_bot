package main

import (
	"fmt"
	"github.com/kid0317/cc-workspace-bot/internal/db"
	"github.com/kid0317/cc-workspace-bot/internal/model"
)

func main() {
	database, _ := db.Open("bot.db")

	// All yzk_worker sessions
	fmt.Println("=== All YZK Worker Sessions ===")
	var sessions []model.Session
	database.Where("channel_key LIKE ?", "%yzk_worker%").
		Order("updated_at DESC").Find(&sessions)
	for _, s := range sessions {
		fmt.Printf("  ID=%s  channel=%s  status=%s  claude_sid=%q  created=%s  updated=%s\n",
			s.ID, s.ChannelKey, s.Status, s.ClaudeSessionID,
			s.CreatedAt.Format("15:04:05"), s.UpdatedAt.Format("15:04:05"))

		// All messages in this session
		var msgs []model.Message
		database.Where("session_id = ?", s.ID).Order("created_at ASC").Find(&msgs)
		for _, m := range msgs {
			content := m.Content
			if len(content) > 120 {
				content = content[:120] + "..."
			}
			fmt.Printf("    [%s] role=%-9s time=%s  [%d chars] %s\n",
				m.ID[:8], m.Role, m.CreatedAt.Format("15:04:05"),
				len(m.Content), content)
		}
	}
}
