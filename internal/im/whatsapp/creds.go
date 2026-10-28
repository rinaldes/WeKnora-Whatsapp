package whatsapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// ErrNoAllowedSenders is returned when a channel would answer nobody.
var ErrNoAllowedSenders = errors.New("whatsapp channel requires owner_number or allowed_numbers")

// Credentials is the WhatsApp channel's credentials JSON. Device keys are
// not here; they live in the whatsmeow store, keyed by DeviceJID.
type Credentials struct {
	DeviceJID      string   `json:"device_jid"`
	OwnerNumber    string   `json:"owner_number"`
	AllowedNumbers []string `json:"allowed_numbers"`
}

// ParseCredentials decodes the channel's credentials JSON.
func ParseCredentials(raw []byte) (Credentials, error) {
	var c Credentials
	if len(raw) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("parse whatsapp credentials: %w", err)
	}
	return c, nil
}

// Normalize reduces numbers to digits only (E.164 without "+") and drops
// empty and duplicate entries.
func (c *Credentials) Normalize() {
	c.DeviceJID = strings.TrimSpace(c.DeviceJID)
	c.OwnerNumber = normalizeNumber(c.OwnerNumber)
	seen := make(map[string]bool, len(c.AllowedNumbers))
	out := make([]string, 0, len(c.AllowedNumbers))
	for _, n := range c.AllowedNumbers {
		n = normalizeNumber(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	c.AllowedNumbers = out
}

// Validate checks the channel is paired and answers at least one sender.
func (c Credentials) Validate() error {
	if c.DeviceJID == "" {
		return errors.New("whatsapp channel is not paired: device_jid is required")
	}
	jid, err := types.ParseJID(c.DeviceJID)
	if err != nil || jid.User == "" || jid.Server != types.DefaultUserServer {
		return fmt.Errorf("invalid device_jid %q", c.DeviceJID)
	}
	if c.OwnerNumber == "" && len(c.AllowedNumbers) == 0 {
		return ErrNoAllowedSenders
	}
	return nil
}

// Allowed reports whether a sender phone number (digits only) may use the bot.
// The owner is always allowed.
func (c Credentials) Allowed(pn string) bool {
	if pn == "" {
		return false
	}
	if pn == c.OwnerNumber {
		return true
	}
	for _, n := range c.AllowedNumbers {
		if n == pn {
			return true
		}
	}
	return false
}

// BotNumber is the bot's phone number: the user part of DeviceJID without
// the device suffix.
func (c Credentials) BotNumber() string {
	user, _, _ := strings.Cut(c.DeviceJID, "@")
	user, _, _ = strings.Cut(user, ":")
	return user
}

// JSON returns the credentials encoded for storage.
func (c Credentials) JSON() []byte {
	if c.AllowedNumbers == nil {
		c.AllowedNumbers = []string{}
	}
	b, _ := json.Marshal(c)
	return b
}

func normalizeNumber(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
