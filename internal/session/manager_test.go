package session

import (
	"testing"
	"time"

	"github.com/kid0317/cc-workspace-bot/internal/model"
)

// ── ArchiveChannel ───────────────────────────────────────────────────────────

// TestArchiveChannel_MarksActiveArchived verifies the happy path: an active
// session for the target channel is flipped to archived.
func TestArchiveChannel_MarksActiveArchived(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{db: db}

	const channelKey = "p2p:oc_abc:cli_xyz"
	sess := &model.Session{
		ID:         "sess-1",
		ChannelKey: channelKey,
		Status:     statusActive,
		CreatedBy:  "ou_user",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := db.Create(sess).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if err := m.ArchiveChannel(channelKey); err != nil {
		t.Fatalf("ArchiveChannel: %v", err)
	}

	var got model.Session
	if err := db.First(&got, "id = ?", "sess-1").Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != statusArchived {
		t.Errorf("Status = %q, want %q", got.Status, statusArchived)
	}
}

// TestArchiveChannel_Idempotent: calling twice is safe (second call is a no-op).
func TestArchiveChannel_Idempotent(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{db: db}

	const channelKey = "p2p:oc_abc:cli_xyz"
	if err := db.Create(&model.Session{
		ID:         "sess-1",
		ChannelKey: channelKey,
		Status:     statusActive,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := m.ArchiveChannel(channelKey); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := m.ArchiveChannel(channelKey); err != nil {
		t.Fatalf("second call: %v", err)
	}

	var got model.Session
	if err := db.First(&got, "id = ?", "sess-1").Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != statusArchived {
		t.Errorf("Status after double-archive = %q, want %q", got.Status, statusArchived)
	}
}

// TestArchiveChannel_OnlyTouchesActive verifies that already-archived sessions
// and sessions on other channels are not touched.
func TestArchiveChannel_OnlyTouchesActive(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{db: db}

	const targetChannel = "p2p:oc_abc:cli_xyz"
	const otherChannel = "p2p:oc_def:cli_xyz"

	// Seed: target has an active + an already-archived session; other channel
	// has an active one that must stay active.
	seeds := []model.Session{
		{ID: "sess-target-active", ChannelKey: targetChannel, Status: statusActive, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "sess-target-old", ChannelKey: targetChannel, Status: statusArchived, CreatedAt: time.Now().Add(-24 * time.Hour), UpdatedAt: time.Now().Add(-24 * time.Hour)},
		{ID: "sess-other-active", ChannelKey: otherChannel, Status: statusActive, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	for i := range seeds {
		if err := db.Create(&seeds[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seeds[i].ID, err)
		}
	}

	if err := m.ArchiveChannel(targetChannel); err != nil {
		t.Fatalf("ArchiveChannel: %v", err)
	}

	checks := []struct {
		id         string
		wantStatus string
	}{
		{"sess-target-active", statusArchived},
		{"sess-target-old", statusArchived}, // was already archived
		{"sess-other-active", statusActive}, // untouched
	}
	for _, c := range checks {
		var s model.Session
		if err := db.First(&s, "id = ?", c.id).Error; err != nil {
			t.Fatalf("reload %s: %v", c.id, err)
		}
		if s.Status != c.wantStatus {
			t.Errorf("%s: Status = %q, want %q", c.id, s.Status, c.wantStatus)
		}
	}
}

// TestArchiveChannel_NoActiveSession: calling on a channel with no active
// session returns nil (not an error). The framework uses this after task
// success regardless of session state.
func TestArchiveChannel_NoActiveSession(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{db: db}

	if err := m.ArchiveChannel("p2p:nobody:app"); err != nil {
		t.Errorf("ArchiveChannel on empty DB should be nil, got %v", err)
	}
}
