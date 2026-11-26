package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

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
	case "compact":
		err = runCompact(args)
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
  compact  Remove cached WhatsApp data and write a smaller encrypted session copy

Environment variables:
  WA_SESSION_KEY   Required 256-bit encryption key (64 hex chars or 44-char base64)
  WA_GROUP_JID     Target WhatsApp group JID (e.g. 120363012345678901@g.us)
  WA_GROUP_JIDS    Comma- or newline-separated target group JIDs (preferred for multiple groups)
  WA_MESSAGE       Message body (alternative to --message or --message-file)
  WA_RETRY_WAIT    Duration to stay connected after sending for retry receipts (default 90s)
  WA_LOG_LEVEL     whatsmeow log level: ERROR, WARN (default), INFO, DEBUG`)
}

func loadKey() ([]byte, error) {
	raw := os.Getenv("WA_SESSION_KEY")
	_ = os.Unsetenv("WA_SESSION_KEY")
	return crypto.ParseKey(raw)
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
	defer crypto.ZeroBytes(key)

	return withEncryptedSession(*sessionEnc, key, true, true, false, func(ctx context.Context, client *wa.Store) error {
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
	defer crypto.ZeroBytes(key)

	return withEncryptedSession(*sessionEnc, key, false, true, false, func(ctx context.Context, client *wa.Store) error {
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

func runCompact(args []string) error {
	fs := flag.NewFlagSet("compact", flag.ContinueOnError)
	input := fs.String("session-enc", "session.db.enc", "input encrypted session file")
	output := fs.String("output", "session.compact.db.enc", "output encrypted session file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	key, err := loadKey()
	if err != nil {
		return err
	}
	defer crypto.ZeroBytes(key)

	if err := crypto.CompactSessionFile(*input, *output, key); err != nil {
		return err
	}
	f, err := os.Stat(*output)
	if err != nil {
		return err
	}
	fmt.Printf("Compacted encrypted session written to %s (%d bytes)\n", *output, f.Size())
	return nil
}

func runSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	sessionEnc := fs.String("session-enc", "session.db.enc", "path to encrypted session file")
	groupJID := fs.String("group", os.Getenv("WA_GROUP_JID"), "target WhatsApp group JID (@g.us)")
	message := fs.String("message", "", "message text to send")
	messageFile := fs.String("message-file", "", "path to UTF-8 text file containing message body")
	purgeKeys := fs.Bool("purge-keys", true, "purge stale Signal sessions and group sender keys before connecting")
	retryWait := fs.Duration("retry-wait", resolveRetryWait(), "duration to remain connected after sending for retry receipts")
	if err := fs.Parse(args); err != nil {
		return err
	}

	key, err := loadKey()
	if err != nil {
		return err
	}
	defer crypto.ZeroBytes(key)

	body, err := resolveMessage(*message, *messageFile)
	if err != nil {
		return err
	}

	// Capture target group spec before unsetting env vars.
	groupSpec := strings.TrimSpace(*groupJID)
	if multi := strings.TrimSpace(os.Getenv("WA_GROUP_JIDS")); multi != "" {
		groupSpec = multi
	}
	_ = os.Unsetenv("WA_GROUP_JID")
	_ = os.Unsetenv("WA_GROUP_JIDS")
	_ = os.Unsetenv("WA_MESSAGE")

	// Always save updated local SQLite state unless the session was materialized ephemerally from CI secrets.
	return withEncryptedSession(*sessionEnc, key, false, true, *purgeKeys, func(ctx context.Context, client *wa.Store) error {
		groups := splitGroupJIDs(groupSpec)
		if len(groups) == 0 {
			return errors.New("no target group provided (use WA_GROUP_JIDS or WA_GROUP_JID)")
		}
		responses, sendErr := client.SendGroupMessagesWithRetryWait(ctx, groups, body, *retryWait)
		if sendErr != nil {
			return sendErr
		}
		for index, resp := range responses {
			fmt.Printf("Message sent to target group #%d (ID=%s, timestamp=%s)\n", index+1, resp.ID, resp.Timestamp.Format("2006-01-02T15:04:05Z07:00"))
		}
		return nil
	})
}

func resolveRetryWait() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("WA_RETRY_WAIT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d >= 0 {
			return d
		}
	}
	return wa.DefaultRetryWait
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
	purgeEphemeralKeys bool,
	fn func(ctx context.Context, client *wa.Store) error,
) error {
	ctx := context.Background()
	materialized, err := materializeEncryptedSession(encPath)
	if err != nil {
		return err
	}
	if materialized {
		// In CI, sessions come from ephemeral secrets; shred the reconstructed file on exit
		// and do not re-encrypt stale state back onto the runner filesystem.
		defer crypto.ShredAndRemove(encPath)
		persistOnClose = false
	}

	tempDB, cleanup, err := crypto.DecryptToTemp(encPath, key, allowMissing)
	if err != nil {
		return err
	}
	defer cleanup()

	client, err := wa.OpenWithOptions(ctx, tempDB, os.Getenv("WA_LOG_LEVEL"), wa.OpenOptions{
		PurgeEphemeralKeys: purgeEphemeralKeys,
	})
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
// when the workflow did not provide a file directly. It never logs the secret and
// unsets the secret environment variables once read. Returns true if a file was written.
func materializeEncryptedSession(encPath string) (bool, error) {
	if _, err := os.Stat(encPath); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect encrypted session: %w", err)
	}

	raw := strings.TrimSpace(os.Getenv("WA_SESSION_ENC"))
	_ = os.Unsetenv("WA_SESSION_ENC")
	if raw == "" {
		parts := make(map[string]string)
		for _, item := range os.Environ() {
			key, value, ok := strings.Cut(item, "=")
			if ok && strings.HasPrefix(key, "WA_SESSION_ENC_PART") {
				parts[key] = strings.TrimSpace(value)
				_ = os.Unsetenv(key)
			}
		}
		keys := make([]string, 0, len(parts))
		for key := range parts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var builder strings.Builder
		for _, key := range keys {
			builder.WriteString(parts[key])
		}
		raw = builder.String()
	}
	if raw == "" {
		return false, nil
	}

	blob, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return false, fmt.Errorf("decode encrypted session secret: %w", err)
	}
	if err := os.WriteFile(encPath, blob, 0o600); err != nil {
		return false, fmt.Errorf("write encrypted session from secrets: %w", err)
	}
	return true, nil
}
