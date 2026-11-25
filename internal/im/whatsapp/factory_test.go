package whatsapp

import (
	"context"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestFactoryRejectsBadChannels(t *testing.T) {
	factory := NewFactory(newTestStore(t))
	noop := func(context.Context, *im.IncomingMessage) error { return nil }
	for _, tt := range []struct{ name, creds, wantErr string }{
		{"no allowlist", `{"device_jid":"6281234567890:1@s.whatsapp.net"}`, "owner_number or allowed_numbers"},
		{"unpaired", `{"owner_number":"1"}`, "not paired"},
		{"unknown device", `{"device_jid":"6281234567890:1@s.whatsapp.net","owner_number":"1"}`, "not paired"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ch := &im.IMChannel{ID: "c1", Platform: "whatsapp", KnowledgeBaseID: "kb-1", Credentials: types.JSON(tt.creds)}
			_, _, err := factory(context.Background(), ch, noop)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("factory error = %v, want %q", err, tt.wantErr)
			}
			if ch.KnowledgeBaseID != "" {
				t.Fatal("factory must clear knowledge_base_id")
			}
		})
	}
}
