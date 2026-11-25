package whatsapp

import (
	"errors"
	"reflect"
	"testing"
)

func TestCredentialsNormalize(t *testing.T) {
	c := Credentials{
		DeviceJID:      " 6281234567890:12@s.whatsapp.net ",
		OwnerNumber:    "+62 811-1111-1111",
		AllowedNumbers: []string{"+62 822 2222 2222", "6282222222222", "", " - "},
	}
	c.Normalize()
	want := Credentials{
		DeviceJID:      "6281234567890:12@s.whatsapp.net",
		OwnerNumber:    "6281111111111",
		AllowedNumbers: []string{"6282222222222"},
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("Normalize() = %+v, want %+v", c, want)
	}
}

func TestCredentialsValidate(t *testing.T) {
	const jid = "6281234567890:12@s.whatsapp.net"
	for _, tt := range []struct {
		name    string
		c       Credentials
		wantErr error
		ok      bool
	}{
		{name: "owner only", c: Credentials{DeviceJID: jid, OwnerNumber: "1"}, ok: true},
		{name: "list only", c: Credentials{DeviceJID: jid, AllowedNumbers: []string{"1"}}, ok: true},
		{name: "not paired", c: Credentials{OwnerNumber: "1"}},
		{name: "bad jid", c: Credentials{DeviceJID: "123@g.us", OwnerNumber: "1"}},
		{name: "nobody allowed", c: Credentials{DeviceJID: jid}, wantErr: ErrNoAllowedSenders},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.c.Validate()
			if tt.ok != (err == nil) {
				t.Fatalf("Validate() error = %v, ok want %v", err, tt.ok)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
	if ErrNoAllowedSenders.Error() != "whatsapp channel requires owner_number or allowed_numbers" {
		t.Fatalf("error text changed: %q", ErrNoAllowedSenders)
	}
}

func TestCredentialsAllowed(t *testing.T) {
	c := Credentials{OwnerNumber: "111", AllowedNumbers: []string{"222"}}
	for pn, want := range map[string]bool{"111": true, "222": true, "333": false, "": false} {
		if got := c.Allowed(pn); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", pn, got, want)
		}
	}
}

func TestCredentialsBotNumber(t *testing.T) {
	for jid, want := range map[string]string{
		"6281234567890:12@s.whatsapp.net": "6281234567890",
		"6281234567890@s.whatsapp.net":    "6281234567890",
		"":                                "",
	} {
		if got := (Credentials{DeviceJID: jid}).BotNumber(); got != want {
			t.Errorf("BotNumber(%q) = %q, want %q", jid, got, want)
		}
	}
}

func TestParseCredentialsRoundTrip(t *testing.T) {
	c, err := ParseCredentials([]byte(`{"device_jid":"1:2@s.whatsapp.net","owner_number":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ParseCredentials(c.JSON())
	if got.DeviceJID != c.DeviceJID || got.OwnerNumber != "1" || got.AllowedNumbers == nil {
		t.Fatalf("round trip = %+v", got)
	}
	if _, err := ParseCredentials([]byte(`{`)); err == nil {
		t.Fatal("ParseCredentials(invalid) error = nil")
	}
}
