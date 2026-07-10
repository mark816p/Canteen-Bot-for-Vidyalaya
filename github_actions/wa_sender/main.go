package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func log(msg string) {
	fmt.Printf("[WA Sender] %s\n", msg)
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

	// Open the whatsmeow store
	dbLog := waLog.Stdout("Database", "ERROR", true)
	container, err := sqlstore.New(context.Background(), "sqlite", fmt.Sprintf("file:%s?_foreign_keys=on", dbPath), dbLog)
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

	log("Connecting to WhatsApp...")
	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "[WA Sender] Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer client.Disconnect()

	// Wait for connection to fully establish
	log("Waiting for connection to stabilise...")
	time.Sleep(5 * time.Second)

	if !client.IsConnected() {
		fmt.Fprintln(os.Stderr, "[WA Sender] Client is not connected after waiting. WhatsApp may have rejected the session.")
		os.Exit(1)
	}
	log("Connected successfully!")

	// Read the message to send
	msgFile := "github_actions/message_to_send.txt"
	if _, err := os.Stat(msgFile); os.IsNotExist(err) {
		log("No message_to_send.txt found. Nothing to send (holiday or no menu).")
		os.Exit(0)
	}

	msgBytes, err := os.ReadFile(msgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[WA Sender] Failed to read message file: %v\n", err)
		os.Exit(1)
	}
	message := strings.TrimSpace(string(msgBytes))
	log("Message loaded successfully.")

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

		log(fmt.Sprintf("Sending message to %s...", jidStr))
		_, err = client.SendMessage(context.Background(), jid, &waE2E.Message{
			Conversation: proto.String(message),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "[WA Sender] Failed to send to %s: %v\n", jidStr, err)
			os.Exit(1)
		}
		log(fmt.Sprintf("Successfully sent to %s!", jidStr))

		if len(groups) > 1 {
			time.Sleep(3 * time.Second)
		}
	}

	log("All messages sent! Done.")
	time.Sleep(2 * time.Second) // allow flush before disconnect
}

// openSQLiteDB is a helper to check if the DB has the required whatsmeow tables
func openSQLiteDB(path string) (*sql.DB, error) {
	return sql.Open("sqlite", fmt.Sprintf("file:%s?_foreign_keys=on", path))
}
