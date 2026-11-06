package sharedconfig

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"gorm.io/gorm"

	"obzly/internal/storage"
)

// Where SMTP settings come from: a control plane's own (and those it sends
// its Sites), or a standalone Site's own.
const (
	SourceCentral = "central"
	SourceLocal   = "local"
)

const (
	smtpCacheTTL = 30 * time.Second
	smtpTimeout  = 15 * time.Second
)

// ErrSMTPNotConfigured: nothing to send email with yet.
var ErrSMTPNotConfigured = errors.New("email isn't set up: add an SMTP server under Obzly settings → Email")

// SMTPSettings is one stored relay configuration.
type SMTPSettings struct {
	Source            string    `gorm:"column:source;primaryKey"`
	Host              string    `gorm:"column:host"`
	Port              int       `gorm:"column:port"`
	Username          string    `gorm:"column:username"`
	PasswordEncrypted string    `gorm:"column:password_encrypted"`
	From              string    `gorm:"column:from_address"`
	TLS               string    `gorm:"column:tls"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
}

func (SMTPSettings) TableName() string { return "smtp_settings" }

// SMTP is a relay ready to use (password decrypted).
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	From     string `json:"from"`
	TLS      string `json:"tls"`
}

var smtpCache struct {
	sync.Mutex
	value *SMTP
	at    time.Time
}

// CurrentSMTP is the relay this deployment sends with: the central one
// (a control plane's own, or what it sent this Site), else a local one.
func CurrentSMTP() (*SMTP, error) {
	smtpCache.Lock()
	defer smtpCache.Unlock()
	if time.Since(smtpCache.at) < smtpCacheTTL {
		return smtpCache.value, nil
	}
	var rows []SMTPSettings
	if err := storage.GetDb().Order("source").Find(&rows).Error; err != nil {
		return nil, err
	}
	var chosen *SMTPSettings
	for i := range rows {
		if rows[i].Source == SourceCentral || chosen == nil {
			chosen = &rows[i]
		}
	}
	var current *SMTP
	if chosen != nil {
		decrypted, err := decryptSMTP(chosen)
		if err != nil {
			return nil, err
		}
		current = decrypted
	}
	smtpCache.value, smtpCache.at = current, time.Now()
	return current, nil
}

// StoredSMTP is the settings stored for source, nil when none.
func StoredSMTP(source string) (*SMTPSettings, error) {
	var row SMTPSettings
	err := storage.GetDb().Where("source = ?", source).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, err
}

// SaveSMTP validates and stores settings for source. An empty password
// keeps the stored one.
func SaveSMTP(source string, settings SMTP) error {
	if err := settings.validate(); err != nil {
		return err
	}
	row := SMTPSettings{
		Source: source, Host: settings.Host, Port: settings.Port, Username: settings.Username,
		From: settings.From, TLS: settings.TLS, UpdatedAt: time.Now().UTC(),
	}
	existing, err := StoredSMTP(source)
	if err != nil {
		return err
	}
	switch {
	case settings.Password != "":
		if row.PasswordEncrypted, err = secrets.Encrypt(settings.Password); err != nil {
			return err
		}
	case existing != nil && existing.Username == settings.Username:
		row.PasswordEncrypted = existing.PasswordEncrypted
	}
	if err := storage.GetDb().Save(&row).Error; err != nil {
		return err
	}
	forgetSMTP()
	return nil
}

func DeleteSMTP(source string) error {
	if err := storage.GetDb().Where("source = ?", source).Delete(&SMTPSettings{}).Error; err != nil {
		return err
	}
	forgetSMTP()
	return nil
}

// SendMail sends a plain-text message through the current relay.
func SendMail(ctx context.Context, to []string, subjectLine, body string) error {
	relay, err := CurrentSMTP()
	if err != nil {
		return err
	}
	if relay == nil {
		return ErrSMTPNotConfigured
	}
	return relay.Send(ctx, to, subjectLine, body)
}

// Send delivers one message over TLS 1.2+ (implicit TLS or STARTTLS, never
// plaintext).
func (s *SMTP) Send(ctx context.Context, to []string, subjectLine, body string) error {
	from, recipients, err := envelope(s.From, to)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()
	address := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	tlsConfig := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: smtpTimeout}
	var conn net.Conn
	if s.TLS == "tls" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("failed to reach the mail relay: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail relay: %w", err)
	}
	defer func() { _ = client.Close() }()
	if s.TLS == "starttls" {
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("mail relay STARTTLS: %w", err)
		}
	}
	if s.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("mail relay auth: %w", err)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("mail relay: %w", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient.Address); err != nil {
			return fmt.Errorf("mail relay refused %s: %w", recipient.Address, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("mail relay: %w", err)
	}
	if _, err := writer.Write([]byte(message(from, recipients, subjectLine, body))); err != nil {
		return fmt.Errorf("mail relay: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("mail relay: %w", err)
	}
	return client.Quit()
}

// ImportEnvSMTP moves the retired ALERT_SMTP_* variables into the database
// once (a control plane's as central, a Site's as local); from then on
// settings come from there and the variables are ignored.
func ImportEnvSMTP(source string, log *slog.Logger) {
	host := strings.TrimSpace(os.Getenv("ALERT_SMTP_HOST"))
	if host == "" {
		return
	}
	existing, err := StoredSMTP(source)
	if err != nil || existing != nil {
		if existing != nil {
			log.Warn("ALERT_SMTP_* environment variables are ignored: email is set up in Obzly settings")
		}
		return
	}
	port, _ := strconv.Atoi(os.Getenv("ALERT_SMTP_PORT"))
	settings := SMTP{
		Host: host, Port: port, Username: os.Getenv("ALERT_SMTP_USERNAME"), Password: os.Getenv("ALERT_SMTP_PASSWORD"),
		From: os.Getenv("ALERT_SMTP_FROM"), TLS: strings.ToLower(strings.TrimSpace(os.Getenv("ALERT_SMTP_TLS"))),
	}
	if err := SaveSMTP(source, settings); err != nil {
		log.Error("Failed to import ALERT_SMTP_* into Obzly settings; set email up there", "error", err)
		return
	}
	log.Warn("Imported ALERT_SMTP_* into Obzly settings (Email); the variables are now ignored and can be removed")
}

func (s *SMTP) validate() error {
	s.Host = strings.TrimSpace(s.Host)
	if s.Host == "" {
		return errors.New("the SMTP host is required")
	}
	if s.Port == 0 {
		s.Port = 587
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("the SMTP port must be 1 to 65535")
	}
	if s.TLS == "" {
		s.TLS = "starttls"
	}
	if s.TLS != "starttls" && s.TLS != "tls" {
		return errors.New("TLS must be starttls or tls (plaintext isn't allowed)")
	}
	if _, err := mail.ParseAddress(s.From); err != nil {
		return errors.New("the From address isn't a valid email address")
	}
	return nil
}

func decryptSMTP(row *SMTPSettings) (*SMTP, error) {
	relay := &SMTP{Host: row.Host, Port: row.Port, Username: row.Username, From: row.From, TLS: row.TLS}
	if row.PasswordEncrypted != "" {
		password, err := secrets.Decrypt(row.PasswordEncrypted)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt the SMTP password: %w", err)
		}
		relay.Password = password
	}
	return relay, nil
}

func forgetSMTP() {
	smtpCache.Lock()
	smtpCache.at = time.Time{}
	smtpCache.Unlock()
}

// message builds the email. Control characters in the subject (it may come
// from a rule name; a bare CR ends a header on some relays) become spaces,
// and it's RFC 2047-encoded.
// envelope parses the sender and recipients once: headers and the SMTP
// envelope carry only parsed addresses, so an address that skipped
// validation can't add a header line.
func envelope(from string, to []string) (*mail.Address, []*mail.Address, error) {
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return nil, nil, errors.New("the From address isn't a valid email address")
	}
	if len(to) == 0 {
		return nil, nil, errors.New("no recipients")
	}
	recipients := make([]*mail.Address, 0, len(to))
	for _, address := range to {
		parsed, err := mail.ParseAddress(address)
		if err != nil {
			return nil, nil, fmt.Errorf("%q isn't a valid email address", address)
		}
		recipients = append(recipients, parsed)
	}
	return sender, recipients, nil
}

func message(from *mail.Address, to []*mail.Address, subjectLine, body string) string {
	subjectLine = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, subjectLine)
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		from.String(), joinAddresses(to), mime.QEncoding.Encode("utf-8", subjectLine), body)
}

func joinAddresses(addresses []*mail.Address) string {
	formatted := make([]string, len(addresses))
	for i, address := range addresses {
		formatted[i] = address.String()
	}
	return strings.Join(formatted, ", ")
}
