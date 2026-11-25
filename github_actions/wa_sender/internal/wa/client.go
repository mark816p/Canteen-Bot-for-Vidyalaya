package wa

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

const (
	connectTimeout = 30 * time.Second
	sendTimeout    = 90 * time.Second // Extra headroom for ~1000-member group sender-key distribution
	flushDelay     = 2 * time.Second
)

// Store wraps an open whatsmeow client and its underlying SQLite handle so both
// can be closed cleanly before re-encrypting the session file.
type Store struct {
	db     *sql.DB
	client *whatsmeow.Client
}

// Open initializes a whatsmeow client backed by the plaintext SQLite file at dbPath.
// It uses DELETE journal mode so all state lives in the single .db file prior to encryption.
func Open(ctx context.Context, dbPath string, logLevel string) (*Store, error) {
	if logLevel == "" {
		logLevel = "WARN"
	}

	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(DELETE)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite session DB: %w", err)
	}
	db.SetMaxOpenConns(1)

	dbLog := waLog.Stdout("DB", logLevel, true)
	container := sqlstore.NewWithDB(db, "sqlite3", dbLog)
	if err := container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("upgrade session schema: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("load device store: %w", err)
	}

	clientLog := waLog.Stdout("WA", logLevel, true)
	client := whatsmeow.NewClient(deviceStore, clientLog)

	return &Store{
		db:     db,
		client: client,
	}, nil
}

// Close disconnects the WhatsApp WebSocket (if connected) and closes the SQLite DB.
func (s *Store) Close() error {
	if s.client != nil {
		s.client.Disconnect()
	}
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Pair runs interactive QR code pairing locally. It refuses to run in CI environments
// so QR pairing secrets can never be exposed in CI logs.
func (s *Store) Pair(ctx context.Context) error {
	if os.Getenv("GITHUB_ACTIONS") == "true" || os.Getenv("CI") == "true" {
		return errors.New("refusing interactive QR pairing inside CI; pair locally and upload the encrypted session")
	}

	if s.client.Store.ID != nil {
		return errors.New("session is already paired; delete/rotate the session file if you intend to re-pair")
	}

	qrChan, err := s.client.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("get QR channel: %w", err)
	}

	if err := s.client.Connect(); err != nil {
		return fmt.Errorf("connect for pairing: %w", err)
	}

	for evt := range qrChan {
		switch evt.Event {
		case "code":
			fmt.Println("Scan this QR code in WhatsApp -> Linked Devices:")
			qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
		case "success":
			fmt.Println("Pairing succeeded. Waiting briefly for initial key sync...")
			time.Sleep(3 * time.Second)
			return nil
		case "timeout":
			return errors.New("QR code timed out before scanning")
		default:
			fmt.Printf("Pairing event: %s\n", evt.Event)
		}
	}

	if s.client.Store.ID == nil {
		return errors.New("QR channel closed before pairing completed")
	}
	return nil
}

// connectExisting connects an already-paired session and blocks until Connected or error.
func (s *Store) connectExisting(ctx context.Context) error {
	if s.client.Store.ID == nil {
		return errors.New("session is not paired; run `pair` locally first")
	}

	connectedCh := make(chan struct{})
	loggedOutCh := make(chan error, 1)
	var once sync.Once

	handlerID := s.client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Connected:
			once.Do(func() { close(connectedCh) })
		case *events.LoggedOut:
			select {
			case loggedOutCh <- fmt.Errorf("WhatsApp session logged out (reason=%v); re-pair locally", v.Reason):
			default:
			}
		}
	})
	defer s.client.RemoveEventHandler(handlerID)

	if err := s.client.Connect(); err != nil {
		return fmt.Errorf("connect to WhatsApp: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	select {
	case <-connectedCh:
		return nil
	case err := <-loggedOutCh:
		return err
	case <-waitCtx.Done():
		return fmt.Errorf("timed out waiting for WhatsApp connection: %w", waitCtx.Err())
	}
}

// GroupSummary holds non-sensitive metadata for listing joined groups locally.
type GroupSummary struct {
	JID              string
	Name             string
	ParticipantCount int
}

// ListGroups connects and returns joined WhatsApp groups.
func (s *Store) ListGroups(ctx context.Context) ([]GroupSummary, error) {
	if err := s.connectExisting(ctx); err != nil {
		return nil, err
	}

	groups, err := s.client.GetJoinedGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch joined groups: %w", err)
	}

	out := make([]GroupSummary, 0, len(groups))
	for _, g := range groups {
		out = append(out, GroupSummary{
			JID:              g.JID.String(),
			Name:             g.Name,
			ParticipantCount: len(g.Participants),
		})
	}
	return out, nil
}

// ParseGroupJID validates that target is strictly a WhatsApp group JID (@g.us).
func ParseGroupJID(raw string) (types.JID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return types.JID{}, errors.New("group JID is required")
	}

	jid, err := types.ParseJID(trimmed)
	if err != nil {
		return types.JID{}, fmt.Errorf("invalid JID %q: %w", trimmed, err)
	}
	if jid.Server != types.GroupServer {
		return types.JID{}, fmt.Errorf("refusing non-group JID %q: target must end with @%s", trimmed, types.GroupServer)
	}
	return jid, nil
}

// SendGroupMessage validates the target group JID, connects, verifies group membership,
// sends the text message, and waits briefly so sender-key/DB updates flush cleanly.
func (s *Store) SendGroupMessage(ctx context.Context, groupJIDStr, text string) (whatsmeow.SendResponse, error) {
	jid, err := ParseGroupJID(groupJIDStr)
	if err != nil {
		return whatsmeow.SendResponse{}, err
	}

	msgText := strings.TrimSpace(text)
	if msgText == "" {
		return whatsmeow.SendResponse{}, errors.New("message text cannot be empty")
	}

	if err := s.connectExisting(ctx); err != nil {
		return whatsmeow.SendResponse{}, err
	}

	// Verify membership and warm participant metadata before sending to a large (~1000 member) group.
	infoCtx, cancelInfo := context.WithTimeout(ctx, connectTimeout)
	groupInfo, err := s.client.GetGroupInfo(infoCtx, jid)
	cancelInfo()
	if err != nil {
		return whatsmeow.SendResponse{}, fmt.Errorf("verify group %s: %w", jid.String(), err)
	}
	if groupInfo.IsAnnounce && !isSelfAdmin(s.client.Store.ID, s.client.Store.LID, groupInfo.Participants) {
		return whatsmeow.SendResponse{}, fmt.Errorf("group %q is announcement-only (admins only) and this account is not an admin", groupInfo.Name)
	}

	sendCtx, cancelSend := context.WithTimeout(ctx, sendTimeout)
	defer cancelSend()

	resp, err := s.client.SendMessage(sendCtx, jid, &waE2E.Message{
		Conversation: proto.String(msgText),
	})
	if err != nil {
		return whatsmeow.SendResponse{}, fmt.Errorf("send message to group %q: %w", groupInfo.Name, err)
	}

	// Allow trailing sender-key distribution / SQLite writes to settle before closing.
	time.Sleep(flushDelay)
	return resp, nil
}

func isSelfAdmin(selfID *types.JID, selfLID types.JID, participants []types.GroupParticipant) bool {
	var selfUser, selfLIDUser string
	if selfID != nil {
		selfUser = selfID.User
	}
	if !selfLID.IsEmpty() {
		selfLIDUser = selfLID.User
	}

	for _, p := range participants {
		matchesPN := selfUser != "" && (p.JID.User == selfUser || p.PhoneNumber.User == selfUser)
		matchesLID := selfLIDUser != "" && (p.JID.User == selfLIDUser || p.LID.User == selfLIDUser)
		if matchesPN || matchesLID {
			return p.IsAdmin || p.IsSuperAdmin
		}
	}
	return false
}
