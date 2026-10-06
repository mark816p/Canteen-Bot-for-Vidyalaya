package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func log(msg string) {
	fmt.Printf("[WA Sender] %s\n", msg)
}

const sentMessagesPath = "/tmp/sent_messages.json"

var (
	sentMessagesLock sync.RWMutex
	sentMessages     = make(map[string]string)
)

func initCustomSentMessageStore(dbPath string) {
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on", dbPath))
	if err != nil {
		return
	}
	defer db.Close()
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS custom_sent_messages (
		id TEXT PRIMARY KEY,
		text TEXT,
		sent_at INTEGER
	)`)
}

func loadSentMessages(dbPath string) {
	sentMessagesLock.Lock()
	defer sentMessagesLock.Unlock()

	initCustomSentMessageStore(dbPath)

	// 1. Load from SQLite database (persisted across runs in GitHub Actions cache)
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on", dbPath))
	if err == nil {
		rows, err := db.Query("SELECT id, text FROM custom_sent_messages ORDER BY sent_at DESC LIMIT 50")
		if err == nil {
			for rows.Next() {
				var id, text string
				if err := rows.Scan(&id, &text); err == nil {
					sentMessages[id] = text
				}
			}
			rows.Close()
		}
		db.Close()
	}

	// 2. Also read /tmp/sent_messages.json if available
	if data, err := os.ReadFile(sentMessagesPath); err == nil {
		_ = json.Unmarshal(data, &sentMessages)
	}

	log(fmt.Sprintf("Loaded %d previously sent messages from persistent store.", len(sentMessages)))
}

func saveSentMessage(dbPath string, id string, text string) {
	sentMessagesLock.Lock()
	defer sentMessagesLock.Unlock()

	sentMessages[id] = text

	// 1. Save to SQLite database
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on", dbPath))
	if err == nil {
		_, _ = db.Exec("INSERT OR REPLACE INTO custom_sent_messages (id, text, sent_at) VALUES (?, ?, ?)", id, text, time.Now().Unix())
		_, _ = db.Exec("DELETE FROM custom_sent_messages WHERE id NOT IN (SELECT id FROM custom_sent_messages ORDER BY sent_at DESC LIMIT 50)")
		db.Close()
	}

	// 2. Save to JSON file as fallback
	data, err := json.MarshalIndent(sentMessages, "", "  ")
	if err == nil {
		_ = os.WriteFile(sentMessagesPath, data, 0644)
	}
}

func getSentMessage(dbPath string, id string) (string, bool) {
	sentMessagesLock.RLock()
	text, ok := sentMessages[id]
	sentMessagesLock.RUnlock()
	if ok {
		return text, true
	}

	// Fallback lookup from SQLite DB
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on", dbPath))
	if err != nil {
		return "", false
	}
	defer db.Close()
	err = db.QueryRow("SELECT text FROM custom_sent_messages WHERE id = ?", id).Scan(&text)
	if err == nil {
		sentMessagesLock.Lock()
		sentMessages[id] = text
		sentMessagesLock.Unlock()
		return text, true
	}
	return "", false
}

func main() {
	mode := "send"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	log(fmt.Sprintf("Starting in '%s' mode...", mode))

	dbPath := "/tmp/whatsapp_session.db"
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "[WA Sender] Session DB not found at %s. Ensure GitHub Actions stitched the secrets correctly.\n", dbPath)
		os.Exit(1)
	}
	log(fmt.Sprintf("Using Session DB at %s", dbPath))

	// Load persistent sent message history from previous runs
	loadSentMessages(dbPath)

	// Load the current message to send early so it can serve as a fallback for retry receipts
	msgFile := "../message_to_send.txt"
	if _, err := os.Stat(msgFile); os.IsNotExist(err) {
		msgFile = "github_actions/message_to_send.txt"
	}
	var currentMessage string
	if msgBytes, err := os.ReadFile(msgFile); err == nil {
		currentMessage = strings.TrimSpace(string(msgBytes))
		log("Current message payload loaded successfully.")
	}

	// Open the whatsmeow store
	dbLog := waLog.Stdout("Database", "ERROR", true)
	container, err := sqlstore.New(context.Background(), "sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on", dbPath), dbLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[WA Sender] Failed to open DB: %v\n", err)
		os.Exit(1)
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil || deviceStore == nil {
		fmt.Fprintf(os.Stderr, "[WA Sender] No linked device found in session DB: %v\n", err)
		fmt.Fprintln(os.Stderr, "Have you linked GoWa to your phone?")
		os.Exit(1)
	}
	log(fmt.Sprintf("Loaded device: %s", deviceStore.ID))

	// Create and connect the whatsmeow client
	clientLog := waLog.Stdout("Client", "WARN", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)
	client.AutoTrustIdentity = true
	client.EnableAutoReconnect = true

	// We intentionally leave UseRetryMessageStore as false so whatsmeow delegates directly to GetMessageForRetry.
	// This guarantees that any retry for older messages (or unknown IDs) falls back to our persistent store and fallback message instead of throwing 'sql: no rows in result set'.
	client.UseRetryMessageStore = false

	// Hook into GetMessageForRetry to fulfill retry receipts for past messages across process restarts
	client.GetMessageForRetry = func(requester, to types.JID, id types.MessageID) *waE2E.Message {
		if text, ok := getSentMessage(dbPath, string(id)); ok {
			log(fmt.Sprintf("Fulfilling retry for message ID %s requested by %s from persistent history", id, requester.String()))
			return &waE2E.Message{Conversation: proto.String(text)}
		}
		if currentMessage != "" {
			log(fmt.Sprintf("Fulfilling retry for message ID %s requested by %s using current message fallback", id, requester.String()))
			return &waE2E.Message{Conversation: proto.String(currentMessage)}
		}
		log(fmt.Sprintf("Warning: No message content available for retry of %s requested by %s", id, requester.String()))
		return nil
	}

	// Register event handler to log incoming retry receipts
	client.AddEventHandler(func(rawEvt interface{}) {
		switch evt := rawEvt.(type) {
		case *events.Receipt:
			if evt.Type == types.ReceiptTypeRetry {
				log(fmt.Sprintf("Received RETRY request from %s for message IDs %v (whatsmeow will automatically re-encrypt keys)", evt.Sender.String(), evt.MessageIDs))
			}
		}
	})

	log("Connecting to WhatsApp...")
	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "[WA Sender] Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer client.Disconnect()

	// Wait for connection to stabilise and drain any queued retry receipts delivered by WhatsApp servers
	log("Waiting 10s for connection to stabilise and draining queued retry receipts from yesterday...")
	time.Sleep(10 * time.Second)

	if !client.IsConnected() {
		fmt.Fprintln(os.Stderr, "[WA Sender] Client is not connected after waiting. WhatsApp may have rejected the session.")
		os.Exit(1)
	}
	log("Connected successfully!")

	if currentMessage == "" {
		log("No message_to_send.txt found. Keeping connection open for 60s to fulfill queued retries, then exiting.")
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}

	// Determine target groups
	var groups []string
	if mode == "test" {
		groups = []string{"120363370008112217@g.us"} // Savya only
	} else {
		groups = []string{
			"120363295006728236@g.us", // Navrachana Sama Canteen
			"120363370008112217@g.us", // Savya
		}
	}

	// Send messages
	for _, jidStr := range groups {
		jid, err := types.ParseJID(jidStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[WA Sender] Invalid JID %s: %v\n", jidStr, err)
			continue
		}

		// Refresh group participants to ensure up-to-date device keys before sending
		log(fmt.Sprintf("Refreshing participant list for group %s...", jidStr))
		groupInfo, err := client.GetGroupInfo(context.Background(), jid)
		if err != nil {
			log(fmt.Sprintf("Notice: GetGroupInfo returned: %v (proceeding with cached participants)", err))
		} else {
			log(fmt.Sprintf("Verified group '%s' with %d participants", groupInfo.GroupName.Name, len(groupInfo.Participants)))
		}

		log(fmt.Sprintf("Sending message to %s...", jidStr))
		resp, err := client.SendMessage(context.Background(), jid, &waE2E.Message{
			Conversation: proto.String(currentMessage),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "[WA Sender] Failed to send to %s: %v\n", jidStr, err)
			os.Exit(1)
		}
		log(fmt.Sprintf("Successfully sent to %s! (Message ID: %s)", jidStr, resp.ID))

		// Persist the message ID so any future retry receipt can be answered cleanly
		saveSentMessage(dbPath, string(resp.ID), currentMessage)

		if len(groups) > 1 {
			time.Sleep(3 * time.Second)
		}
	}

	log("All messages sent! Keeping connection open for 180s (3 minutes) to fulfill live retry requests (especially for iPhones/APNs and Android background sleep)...")
	time.Sleep(180 * time.Second)
	log("Done waiting window. Disconnecting cleanly.")
}
