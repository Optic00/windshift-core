package smtp

import (
	"path/filepath"
	"strings"
	"testing"

	"windshift/internal/database"
)

func TestBuildMimeCanonicalizesThreadingMessageIDs(t *testing.T) {
	mime := buildMime(mimeOptions{
		FromEmail: "team@example.com", ToEmail: "customer@example.com",
		Subject: "Reply", TextBody: "text", HTMLBody: "<p>text</p>",
		MessageID: "outbound@example.com", InReplyTo: "legacy-inbound@example.com",
		References: []string{"first@example.com", "<second@example.com>"},
	})
	for _, header := range []string{
		"Message-ID: <outbound@example.com>\r\n",
		"In-Reply-To: <legacy-inbound@example.com>\r\n",
		"References: <first@example.com> <second@example.com>\r\n",
	} {
		if !strings.Contains(mime, header) {
			t.Fatalf("MIME message is missing %q:\n%s", header, mime)
		}
	}
}

func TestNormalizeEnvelopeAddressRejectsHeaderSyntax(t *testing.T) {
	if got, err := normalizeEnvelopeAddress("  sender@example.com  "); err != nil || got != "sender@example.com" {
		t.Fatalf("valid address = (%q, %v)", got, err)
	}
	for _, invalid := range []string{
		"Sender <sender@example.com>",
		"one@example.com, two@example.com",
		"sender@example.com\r\nRCPT TO:<victim@example.com>",
		"",
	} {
		if _, err := normalizeEnvelopeAddress(invalid); err == nil {
			t.Fatalf("invalid address %q unexpectedly accepted", invalid)
		}
	}
}

func TestGetSMTPConfigUsesExplicitDefault(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "smtp.db"))
	if err != nil {
		t.Fatalf("NewSQLiteDB: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := db.ExecWrite("DELETE FROM channels WHERE type = 'smtp' AND direction = 'outbound'"); err != nil {
		t.Fatalf("clear smtp channels: %v", err)
	}
	if _, err := db.ExecWrite(`
		INSERT INTO channels (name, type, direction, status, is_default, config, updated_at)
		VALUES ('default', 'smtp', 'outbound', 'enabled', true,
		        '{"smtp_host":"default.example","smtp_port":587,"smtp_from_email":"default@example.com"}',
		        datetime('now', '-1 day'))
	`); err != nil {
		t.Fatalf("insert default SMTP channel: %v", err)
	}
	if _, err := db.ExecWrite(`
		INSERT INTO channels (name, type, direction, status, is_default, config, updated_at)
		VALUES ('newer non-default', 'smtp', 'outbound', 'enabled', false,
		        '{"smtp_host":"attacker.example","smtp_port":587,"smtp_from_email":"attacker@example.com"}',
		        datetime('now'))
	`); err != nil {
		t.Fatalf("insert non-default SMTP channel: %v", err)
	}

	cfg, err := NewNotificationSMTPSender(db).getSMTPConfig()
	if err != nil {
		t.Fatalf("getSMTPConfig: %v", err)
	}
	if cfg.SMTPHost != "default.example" {
		t.Fatalf("SMTP host = %q, want explicit default", cfg.SMTPHost)
	}
}
