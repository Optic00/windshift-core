package email

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"

	"windshift/internal/utils"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
)

// ErrBlockedIMAPHost aliases the shared SSRF-block error so existing callers
// that errors.Is(err, ErrBlockedIMAPHost) keep compiling. The reject list
// (loopback, RFC1918, link-local, multicast, unspecified, CGNAT 100.64/10)
// now lives in internal/utils/dialer.go and is shared with SMTP/HTTP dialers.
var ErrBlockedIMAPHost = utils.ErrBlockedSSRFAddr

// Client wraps an IMAP client connection
type Client struct {
	client  *imapclient.Client
	conn    net.Conn
	host    string
	port    int
	ctx     context.Context
	timeout time.Duration
}

// ConnectOptions configures IMAP connection. Encryption canonical values are
// "ssl" (implicit TLS, port 993) and "starttls" (STARTTLS upgrade, port 143).
// "tls" is accepted as a legacy alias for "ssl" — the old UI labeled it as
// STARTTLS but Connect always treated it as direct TLS, so we preserve that
// behavior for existing email_providers rows rather than silently changing
// connection semantics.
type ConnectOptions struct {
	Context    context.Context
	Host       string
	Port       int
	Encryption string // "ssl" or "starttls" (legacy: "tls" == "ssl")
	Timeout    time.Duration
}

// Connect establishes an IMAP connection
func Connect(opts ConnectOptions) (*Client, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}

	addr := net.JoinHostPort(opts.Host, strconv.Itoa(opts.Port))

	var conn net.Conn
	var err error

	var client *imapclient.Client

	clientOpts := &imapclient.Options{
		WordDecoder: nil, // Use default
	}

	dialer := utils.SafeNetDialer(opts.Timeout)
	parentCtx := opts.Context
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, opts.Timeout)
	defer cancel()

	switch opts.Encryption {
	case "ssl", "tls":
		// Direct TLS connection (port 993) — SSRF-safe dialer rejects
		// private/loopback/link-local resolutions before handshake.
		tlsDialer := &tls.Dialer{
			NetDialer: dialer,
			Config: &tls.Config{
				ServerName: opts.Host,
				MinVersion: tls.VersionTLS12,
			},
		}
		conn, err = tlsDialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to %s: %w", addr, err)
		}
		client = imapclient.New(conn, clientOpts)

	case "starttls":
		// Plain connection with STARTTLS upgrade (port 143)
		conn, err = dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to %s: %w", addr, err)
		}
		client, err = imapclient.NewStartTLS(conn, clientOpts)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("STARTTLS failed: %w", err)
		}

	default:
		// Plaintext IMAP sends credentials in the clear and bypasses the
		// identity check on the server. Refuse it explicitly — if someone
		// actually needs it for a weird lab setup, they can add a separate
		// opt-in flag rather than defaulting to unsafe.
		return nil, fmt.Errorf("IMAP encryption %q not allowed; use \"ssl\", \"tls\", or \"starttls\"", opts.Encryption)
	}
	deadline := time.Now().Add(opts.Timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set IMAP operation deadline: %w", err)
	}

	return &Client{
		client:  client,
		conn:    conn,
		host:    opts.Host,
		port:    opts.Port,
		ctx:     parentCtx,
		timeout: opts.Timeout,
	}, nil
}

// setOperationDeadline gives each command its own bounded window while still
// honoring the channel-level context. A single deadline set at connect time
// expires while message processing runs and makes every later STORE/EXPUNGE
// fail even though those operations have not started yet.
func (c *Client) setOperationDeadline() error {
	if c.conn == nil {
		return fmt.Errorf("IMAP connection is closed")
	}
	if c.ctx != nil {
		if err := c.ctx.Err(); err != nil {
			return err
		}
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if c.ctx != nil {
		if ctxDeadline, ok := c.ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
	}
	return c.conn.SetDeadline(deadline)
}

// AuthenticateBasic performs basic username/password authentication
func (c *Client) AuthenticateBasic(username, password string) error {
	if err := c.setOperationDeadline(); err != nil {
		return fmt.Errorf("set login deadline: %w", err)
	}
	if err := c.client.Login(username, password).Wait(); err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	return nil
}

// AuthenticateXOAuth2 performs XOAUTH2 SASL authentication (for OAuth providers)
func (c *Client) AuthenticateXOAuth2(email, accessToken string) error {
	if err := c.setOperationDeadline(); err != nil {
		return fmt.Errorf("set OAuth authentication deadline: %w", err)
	}
	// Try OAUTHBEARER first (RFC 7628), then fall back to XOAUTH2
	// Both mechanisms are supported by the same sasl.NewOAuthBearerClient

	// Build XOAUTH2 SASL client (legacy but widely supported by O365/Gmail)
	xoauth2Client := newXOAuth2Client(email, accessToken)
	if err := c.client.Authenticate(xoauth2Client); err != nil {
		// If XOAUTH2 fails, try OAUTHBEARER
		if deadlineErr := c.setOperationDeadline(); deadlineErr != nil {
			return fmt.Errorf("set OAuth fallback deadline: %w", deadlineErr)
		}
		saslClient := sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{
			Username: email,
			Token:    accessToken,
		})
		if err2 := c.client.Authenticate(saslClient); err2 != nil {
			return fmt.Errorf("XOAUTH2 authentication failed: %w (OAUTHBEARER also failed: %v)", err, err2)
		}
		return nil
	}

	return nil
}

// SelectMailbox selects a mailbox and returns its status
func (c *Client) SelectMailbox(name string) (*imap.SelectData, error) {
	if err := c.setOperationDeadline(); err != nil {
		return nil, fmt.Errorf("set mailbox selection deadline: %w", err)
	}
	data, err := c.client.Select(name, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("failed to select mailbox %s: %w", name, err)
	}
	return data, nil
}

// FetchMessages fetches messages with UID greater than sinceUID. The caller
// must have already selected the mailbox (so UIDVALIDITY can be inspected
// separately before the fetch proceeds).
func (c *Client) FetchMessages(sinceUID uint32, batchSize int) ([]*FetchedMessage, error) {
	if err := c.setOperationDeadline(); err != nil {
		return nil, fmt.Errorf("set search deadline: %w", err)
	}
	// Search for messages with UID > sinceUID
	var searchCriteria *imap.SearchCriteria
	if sinceUID > 0 {
		searchCriteria = &imap.SearchCriteria{
			UID: []imap.UIDSet{{
				imap.UIDRange{Start: imap.UID(sinceUID + 1), Stop: 0}, // 0 means * (max)
			}},
		}
	} else {
		// Fetch all messages
		searchCriteria = &imap.SearchCriteria{}
	}

	searchData, err := c.client.UIDSearch(searchCriteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	if len(searchData.AllUIDs()) == 0 {
		return nil, nil
	}

	uids := searchData.AllUIDs()
	slog.Info("found messages to fetch", "count", len(uids), "since_uid", sinceUID)

	// Limit batch size
	if batchSize > 0 && len(uids) > batchSize {
		uids = uids[:batchSize]
	}

	// Build UID set
	uidSet := imap.UIDSet{}
	for _, uid := range uids {
		uidSet = append(uidSet, imap.UIDRange{Start: uid, Stop: uid})
	}

	// Fetch the entire RFC822 message body. The previous "HEADER + TEXT" split
	// silently drops top-level MIME headers and Content-Transfer-Encoding on
	// some servers, which broke multipart parsing and attachment extraction.
	// Fetching the whole message in one section keeps the message intact.
	const maxRawMessageBytes = 20 << 20
	const maxRawBatchBytes = 64 << 20
	fetchOptions := &imap.FetchOptions{
		UID:      true,
		Envelope: true,
		Flags:    true,
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierNone, Partial: &imap.SectionPartial{Offset: 0, Size: maxRawMessageBytes + 1}},
		},
	}
	if err := c.setOperationDeadline(); err != nil {
		return nil, fmt.Errorf("set fetch deadline: %w", err)
	}

	fetchCmd := c.client.Fetch(uidSet, fetchOptions)
	defer func() { _ = fetchCmd.Close() }()

	var messages []*FetchedMessage
	totalRawBytes := 0
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}

		fetched, err := parseFetchedMessage(msg, maxRawMessageBytes)
		if err != nil {
			return nil, err
		}
		if totalRawBytes+len(fetched.Raw) > maxRawBatchBytes && len(messages) > 0 {
			// Return the bounded prefix. The omitted message's UID is above the
			// returned watermark, so it is fetched again on the next poll.
			break
		}
		totalRawBytes += len(fetched.Raw)
		messages = append(messages, fetched)
	}

	if err := fetchCmd.Close(); err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}

	return messages, nil
}

// FetchedMessage represents a raw fetched IMAP message. Raw is the full
// RFC822 bytes (header + body); callers should parse it once rather than
// stitching pieces back together.
type FetchedMessage struct {
	UID        uint32
	Envelope   *imap.Envelope
	Flags      []imap.Flag
	Raw        []byte
	FetchError error
}

func parseFetchedMessage(msg *imapclient.FetchMessageData, maxRawBytes int64) (*FetchedMessage, error) {
	fetched := &FetchedMessage{}
	var bodyRead bool
	var bodyErr error
	for {
		item := msg.Next()
		if item == nil {
			break
		}
		switch item := item.(type) {
		case imapclient.FetchItemDataUID:
			fetched.UID = uint32(item.UID)
		case imapclient.FetchItemDataEnvelope:
			fetched.Envelope = item.Envelope
		case imapclient.FetchItemDataFlags:
			fetched.Flags = item.Flags
		case imapclient.FetchItemDataBodySection:
			if bodyRead || item.Literal == nil {
				continue
			}
			bodyRead = true
			// Limit the read even if a non-compliant server ignores BODY[]<offset.size>.
			// Advancing to the next item drains the remainder through go-imap's
			// discarder, so the connection stays protocol-synchronized.
			fetched.Raw, bodyErr = io.ReadAll(io.LimitReader(item.Literal, maxRawBytes+1))
		}
	}
	if bodyErr != nil {
		return nil, fmt.Errorf("read IMAP message UID %d: %w", fetched.UID, bodyErr)
	}
	if int64(len(fetched.Raw)) > maxRawBytes {
		fetched.FetchError = fmt.Errorf("IMAP message UID %d exceeds %d-byte limit", fetched.UID, maxRawBytes)
	}
	return fetched, nil
}

// MarkAsRead marks a message as read (adds \Seen flag)
func (c *Client) MarkAsRead(uid uint32) error {
	if err := c.setOperationDeadline(); err != nil {
		return fmt.Errorf("set mark-as-read deadline: %w", err)
	}
	uidSet := imap.UIDSet{imap.UIDRange{Start: imap.UID(uid), Stop: imap.UID(uid)}}
	flags := []imap.Flag{imap.FlagSeen}

	storeCmd := c.client.Store(uidSet, &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: flags,
	}, nil)

	if err := storeCmd.Close(); err != nil {
		return fmt.Errorf("failed to mark message as read: %w", err)
	}
	return nil
}

// DeleteMessage marks a message for deletion (adds \Deleted flag)
func (c *Client) DeleteMessage(uid uint32) error {
	if err := c.setOperationDeadline(); err != nil {
		return fmt.Errorf("set delete deadline: %w", err)
	}
	uidSet := imap.UIDSet{imap.UIDRange{Start: imap.UID(uid), Stop: imap.UID(uid)}}
	flags := []imap.Flag{imap.FlagDeleted}

	storeCmd := c.client.Store(uidSet, &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: flags,
	}, nil)

	if err := storeCmd.Close(); err != nil {
		return fmt.Errorf("failed to delete message: %w", err)
	}
	return nil
}

// Expunge permanently removes messages marked for deletion
func (c *Client) Expunge() error {
	if err := c.setOperationDeadline(); err != nil {
		return fmt.Errorf("set expunge deadline: %w", err)
	}
	expungeCmd := c.client.Expunge()
	if err := expungeCmd.Close(); err != nil {
		return fmt.Errorf("expunge failed: %w", err)
	}
	return nil
}

// Close closes the IMAP connection
func (c *Client) Close() error {
	if c.client != nil {
		if c.conn != nil {
			_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
		}
		_ = c.client.Logout().Wait()
		return c.client.Close()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// xoauth2Client implements SASL XOAUTH2 mechanism
type xoauth2Client struct {
	email       string
	accessToken string
}

func newXOAuth2Client(email, accessToken string) sasl.Client {
	return &xoauth2Client{
		email:       email,
		accessToken: accessToken,
	}
}

func (c *xoauth2Client) Start() (mech string, ir []byte, err error) {
	// XOAUTH2 initial response format:
	// user=<email>\x01auth=Bearer <token>\x01\x01
	authString := fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", c.email, c.accessToken)
	return "XOAUTH2", []byte(base64.StdEncoding.EncodeToString([]byte(authString))), nil
}

func (c *xoauth2Client) Next(challenge []byte) (response []byte, err error) {
	// XOAUTH2 doesn't have a challenge-response flow
	return nil, nil
}
