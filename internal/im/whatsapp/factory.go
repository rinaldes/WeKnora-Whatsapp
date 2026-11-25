package whatsapp

import (
	"context"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// maxConnAge is the supervisor's recycle interval. whatsmeow reconnects on
// its own, and frequent fresh logins look suspicious to WhatsApp, so the
// recycle is only a long safety net.
const maxConnAge = 24 * time.Hour

// NewFactory returns an im.AdapterFactory for WhatsApp channels. Each
// channel connects with the device paired earlier through PairingManager.
func NewFactory(store *Store) im.AdapterFactory {
	return func(factoryCtx context.Context, channel *im.IMChannel, msgHandler func(context.Context, *im.IncomingMessage) error) (im.Adapter, context.CancelFunc, error) {
		// WhatsApp never saves files to a knowledge base (one KB is shared by
		// every allowlisted sender). The handler forces this; clear it here too
		// in case the row was edited another way. Core's QA request uses this
		// same channel value.
		if channel.KnowledgeBaseID != "" {
			logger.Warnf(factoryCtx, "[WhatsApp] Channel %s has knowledge_base_id set; ignoring it", channel.ID)
			channel.KnowledgeBaseID = ""
		}

		creds, err := ParseCredentials(channel.Credentials)
		if err != nil {
			return nil, nil, err
		}
		creds.Normalize()
		if err := creds.Validate(); err != nil {
			return nil, nil, err
		}

		container, err := store.Container(factoryCtx)
		if err != nil {
			return nil, nil, err
		}
		jid, err := types.ParseJID(creds.DeviceJID)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid device_jid: %w", err)
		}
		device, err := container.GetDevice(factoryCtx, jid)
		if err != nil {
			return nil, nil, fmt.Errorf("load whatsapp device: %w", err)
		}
		if device == nil {
			return nil, nil, fmt.Errorf("whatsapp channel not paired")
		}

		// Unlink devices left behind by deleted channels, rebinds and
		// abandoned pairings; keep doing it hourly.
		go store.sweepNow()
		store.startSweeping()

		client := whatsmeow.NewClient(device, newWALogger("client"))
		ctx, cancel := context.WithCancel(context.Background())
		adapter := newAdapter(ctx, cancel, whatsmeowClient{client}, creds, channel.Locale, msgHandler)
		client.AddEventHandler(adapter.handleEvent)

		go im.RunSupervised(ctx, im.SupervisorConfig{
			Name:       "WhatsApp channel " + channel.ID,
			MaxConnAge: maxConnAge,
			Connect: func(context.Context) (func(), error) {
				if err := client.Connect(); err != nil {
					return nil, err
				}
				return client.Disconnect, nil
			},
		})

		return adapter, adapter.close, nil
	}
}
