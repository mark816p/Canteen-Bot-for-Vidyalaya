package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mark816p/Canteen-Bot-for-Vidyalaya/github_actions/wa_sender/internal/crypto"
	"github.com/mark816p/Canteen-Bot-for-Vidyalaya/github_actions/wa_sender/internal/wa"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	subcmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch subcmd {
	case "pair":
		err = runPair(args)
	case "groups":
		err = runGroups(args)
	case "send":
		err = runSend(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", subcmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: notifier <command> [flags]

Commands:
  pair     Pair a WhatsApp account via QR code locally and save an encrypted session
  groups   List joined WhatsApp groups and their @g.us JIDs
  send     Send a message to the configured WhatsApp group(s) and re-encrypt the session

Environment variables:
  WA_SESSION_KEY   Required 256-bit encryption key (64 hex chars or 44-char base64)
  WA_GROUP_JID     Target WhatsApp group JID (e.g. 120363012345678901@g.us)
  WA_GROUP_JIDS    Comma- or newline-separated target group JIDs (preferred for multiple groups)
  WA_MESSAGE       Message body (alternative to --message or --message-file)
  WA_LOG_LEVEL     whatsmeow log level: ERROR, WARN (default), INFO, DEBUG`)
}

func loadKey() ([]byte, error) {
	return crypto.ParseKey(os.Getenv("WA_SESSION_KEY"))
}

func runPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	sessionEnc := fs.String("session-enc", "session.db.enc", "path to encrypted session file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	key, err := loadKey()
	if err != nil {
		return err
	}

	return withEncryptedSession(*sessionEnc, key, true, true, func(ctx context.Context, client *wa.Store) error {
		return client.Pair(ctx)
	})
}

func runGroups(args []string) error {
	fs := flag.NewFlagSet("groups", flag.ContinueOnError)
	sessionEnc := fs.String("session-enc", "session.db.enc", "path to encrypted session file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	key, err := loadKey()
	if err != nil {
		return err
	}

	return withEncryptedSession(*sessionEnc, key, false, true, func(ctx context.Context, client *wa.Store) error {
		groups, err := client.ListGroups(ctx)
		if err != nil {
			return err
		}
		if len(groups) == 0 {
			fmt.Println("No joined groups found.")
			return nil
		}
		for _, g := range groups {
			fmt.Printf("%s\t(%d members)\t%s\n", g.JID, g.ParticipantCount, g.Name)
		}
		return nil
	})
}

func runSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	sessionEnc := fs.String("session-enc", "session.db.enc", "path to encrypted session file")
	groupJID := fs.String("group", os.Getenv("WA_GROUP_JID"), "target WhatsApp group JID (@g.us)")
	message := fs.String("message", "", "message text to send")
	messageFile := fs.String("message-file", "", "path to UTF-8 text file containing message body")
	if err := fs.Parse(args); err != nil {
		return err
	}

	key, err := loadKey()
	if err != nil {
		return err
	}

	body, err := resolveMessage(*message, *messageFile)
	if err != nil {
		return err
	}

	// Always save updated SQLite state even if send fails after connecting/key-sync.
	return withEncryptedSession(*sessionEnc, key, false, true, func(ctx context.Context, client *wa.Store) error {
		groupSpec := strings.TrimSpace(*groupJID)
		if multi := strings.TrimSpace(os.Getenv("WA_GROUP_JIDS")); multi != "" {
			groupSpec = multi
		}
		groups := splitGroupJIDs(groupSpec)
		if len(groups) == 0 {
			return errors.New("no target group provided (use WA_GROUP_JIDS or WA_GROUP_JID)")
		}
		for _, target := range groups {
			resp, sendErr := client.SendGroupMessage(ctx, target, body)
			if sendErr != nil {
				return sendErr
			}
			fmt.Printf("Message sent to group %s (ID=%s, timestamp=%s)\n", target, resp.ID, resp.Timestamp.Format("2006-01-02T15:04:05Z07:00"))
		}
		return nil
	})
}

func splitGroupJIDs(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	groups := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		group := strings.TrimSpace(part)
		if group == "" {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		groups = append(groups, group)
	}
	return groups
}

func resolveMessage(flagMsg, filePath string) (string, error) {
	if strings.TrimSpace(filePath) != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read --message-file: %w", err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return "", errors.New("--message-file is empty")
		}
		return string(data), nil
	}
	if strings.TrimSpace(flagMsg) != "" {
		return flagMsg, nil
	}
	if envMsg := os.Getenv("WA_MESSAGE"); strings.TrimSpace(envMsg) != "" {
		return envMsg, nil
	}
	return "", errors.New("no message provided (use --message-file, --message, or WA_MESSAGE)")
}

func withEncryptedSession(
	encPath string,
	key []byte,
	allowMissing bool,
	persistOnClose bool,
	fn func(ctx context.Context, client *wa.Store) error,
) error {
	ctx := context.Background()
	if err := materializeEncryptedSession(encPath); err != nil {
		return err
	}

	tempDB, cleanup, err := crypto.DecryptToTemp(encPath, key, allowMissing)
	if err != nil {
		return err
	}
	defer cleanup()

	client, err := wa.Open(ctx, tempDB, os.Getenv("WA_LOG_LEVEL"))
	if err != nil {
		return err
	}

	fnErr := fn(ctx, client)
	closeErr := client.Close()

	var saveErr error
	if persistOnClose {
		// Only skip persisting if initial DB creation itself failed before pairing.
		if !(allowMissing && fnErr != nil) {
			saveErr = crypto.EncryptFromFile(tempDB, encPath, key)
		}
	}

	return errors.Join(fnErr, closeErr, saveErr)
}

// materializeEncryptedSession reconstructs the encrypted session from CI secrets
// when the workflow did not provide a file directly. It never logs the secret.
func materializeEncryptedSession(encPath string) error {
	if _, err := os.Stat(encPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect encrypted session: %w", err)
	}

	raw := strings.TrimSpace(os.Getenv("WA_SESSION_ENC"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("WA_SESSION_ENC_PART1")) +
			strings.TrimSpace(os.Getenv("WA_SESSION_ENC_PART2")) +
			strings.TrimSpace(os.Getenv("WA_SESSION_ENC_PART3"))
	}
	if raw == "" {
		return nil
	}

	blob, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("decode encrypted session secret: %w", err)
	}
	if err := os.WriteFile(encPath, blob, 0o600); err != nil {
		return fmt.Errorf("write encrypted session from secrets: %w", err)
	}
	return nil
}
