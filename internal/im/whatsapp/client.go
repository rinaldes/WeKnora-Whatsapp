package whatsapp

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// waClient is the part of *whatsmeow.Client the adapter uses. Only the
// factory touches the concrete client, so tests run against a fake.
type waClient interface {
	Connect() error
	Disconnect()
	IsConnected() bool
	SendMessage(ctx context.Context, to types.JID, msg *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
	SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error
	SendPresence(ctx context.Context, state types.Presence) error
	MarkRead(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID, receiptTypeExtra ...types.ReceiptType) error
}

var _ waClient = (*whatsmeow.Client)(nil)
