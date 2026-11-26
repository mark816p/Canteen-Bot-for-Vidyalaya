package wa

import (
	"context"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
)

func TestParseGroupJID(t *testing.T) {
	valid := "120363012345678901@g.us"
	jid, err := ParseGroupJID(valid)
	if err != nil {
		t.Fatalf("expected valid group JID, got err=%v", err)
	}
	if jid.String() != valid {
		t.Fatalf("got %q, want %q", jid.String(), valid)
	}

	invalidCases := []string{
		"",
		"919876543210@s.whatsapp.net", // must reject DMs
		"120363012345678901@newsletter",
		"not-a-jid",
	}
	for _, tc := range invalidCases {
		if _, err := ParseGroupJID(tc); err == nil {
			t.Errorf("expected error for non-group/invalid JID %q", tc)
		}
	}
}

func TestOpenWithOptionsPurgesOnlyEphemeralKeysAndWiresRetry(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test-session.db")

	// First open creates the whatsmeow SQLite schema and a valid device store.
	initial, err := Open(ctx, dbPath, "ERROR")
	if err != nil {
		t.Fatalf("initial Open failed: %v", err)
	}
	jid := types.JID{User: "919999999999", Device: 1, Server: types.DefaultUserServer}
	initial.client.Store.ID = &jid
	initial.client.Store.AdvSecretKey = make([]byte, 32)
	initial.client.Store.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte{1},
		AccountSignatureKey: make([]byte, 32),
		AccountSignature:    make([]byte, 64),
		DeviceSignature:     make([]byte, 64),
	}
	if err := initial.client.Store.Save(ctx); err != nil {
		_ = initial.Close()
		t.Fatalf("save device failed: %v", err)
	}

	// Populate identity_keys (must be preserved) plus sessions and sender_keys (must be purged).
	seedStmts := []string{
		`INSERT INTO whatsmeow_identity_keys (our_jid, their_id, identity) VALUES (?, 'peer@s.whatsapp.net', X'AA')`,
		`INSERT INTO whatsmeow_sessions (our_jid, their_id, session) VALUES (?, 'peer@s.whatsapp.net', X'BB')`,
		`INSERT INTO whatsmeow_sender_keys (our_jid, chat_id, sender_id, sender_key) VALUES (?, '120363012345678901@g.us', 'peer@s.whatsapp.net', X'CC')`,
	}
	for _, stmt := range seedStmts {
		if _, err := initial.db.Exec(stmt, jid.String()); err != nil {
			_ = initial.Close()
			t.Fatalf("seed %q failed: %v", stmt, err)
		}
	}
	if err := initial.Close(); err != nil {
		t.Fatalf("initial Close failed: %v", err)
	}

	store, err := OpenWithOptions(ctx, dbPath, "ERROR", OpenOptions{PurgeEphemeralKeys: true})
	if err != nil {
		t.Fatalf("OpenWithOptions failed: %v", err)
	}
	defer store.Close()

	if !store.client.AutoTrustIdentity || !store.client.EnableAutoReconnect || store.client.UseRetryMessageStore {
		t.Fatalf("unexpected retry/identity flags: AutoTrustIdentity=%v EnableAutoReconnect=%v UseRetryMessageStore=%v",
			store.client.AutoTrustIdentity, store.client.EnableAutoReconnect, store.client.UseRetryMessageStore)
	}
	if store.client.GetMessageForRetry == nil {
		t.Fatal("expected GetMessageForRetry callback to be configured")
	}

	store.sentMu.Lock()
	store.sentMsgs["MSG123"] = "Menu body"
	store.sentMu.Unlock()
	retryMsg := store.client.GetMessageForRetry(types.EmptyJID, types.EmptyJID, "MSG123")
	if retryMsg == nil || retryMsg.GetConversation() != "Menu body" {
		t.Fatalf("unexpected retry message: %+v", retryMsg)
	}

	assertCount := func(table string, want int) {
		t.Helper()
		var got int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Errorf("%s row count = %d, want %d", table, got, want)
		}
	}
	assertCount("whatsmeow_device", 1)
	assertCount("whatsmeow_identity_keys", 1)
	assertCount("whatsmeow_sessions", 0)
	assertCount("whatsmeow_sender_keys", 0)
}
