package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testWhatsAppJID = "6281234567890:12@s.whatsapp.net"

type whatsappTestAgents struct {
	interfaces.CustomAgentService
}

func (whatsappTestAgents) GetAgentByID(_ context.Context, id string) (*types.CustomAgent, error) {
	return &types.CustomAgent{ID: id, TenantID: 42}, nil
}

var whatsappTestDBSeq atomic.Int64

func newWhatsAppTestHandler(t *testing.T) (*IMHandler, *gorm.DB) {
	t.Helper()
	name := fmt.Sprintf("file:im-whatsapp-%d?mode=memory&cache=shared", whatsappTestDBSeq.Add(1))
	db, err := gorm.Open(sqlite.Open(name), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Minimal im_channels: the production schema's uuid_generate_v4() default
	// doesn't parse on SQLite.
	if err := db.Exec(`CREATE TABLE im_channels (
		id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, agent_id TEXT NOT NULL,
		platform TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', enabled NUMERIC NOT NULL DEFAULT 1,
		mode TEXT NOT NULL DEFAULT 'websocket', output_mode TEXT NOT NULL DEFAULT 'stream',
		locale TEXT NOT NULL DEFAULT '', knowledge_base_id TEXT DEFAULT '',
		bot_identity TEXT NOT NULL DEFAULT '', session_mode TEXT NOT NULL DEFAULT 'user',
		credentials TEXT NOT NULL DEFAULT '{}', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create im_channels: %v", err)
	}
	svc := im.NewService(db, nil, nil, nil, whatsappTestAgents{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(func() {
		svc.Stop()
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return NewIMHandler(svc), db
}

func doIMRequest(t *testing.T, h gin.HandlerFunc, method, route, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Handle(method, route, h)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), types.TenantIDContextKey, uint64(42)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestCreateWhatsAppChannelForcesSettings(t *testing.T) {
	h, db := newWhatsAppTestHandler(t)
	body := fmt.Sprintf(`{"platform":"whatsapp","mode":"webhook","output_mode":"stream","session_mode":"thread",
		"knowledge_base_id":"kb-1","credentials":{"device_jid":%q,"owner_number":"+62 811-1111-1111",
		"allowed_numbers":["+62 822 2222 2222"]}}`, testWhatsAppJID)
	rec := doIMRequest(t, h.CreateIMChannel, http.MethodPost, "/agents/:id/im-channels", "/agents/agent-1/im-channels", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var ch im.IMChannel
	if err := db.First(&ch).Error; err != nil {
		t.Fatalf("load channel: %v", err)
	}
	if ch.Mode != "websocket" || ch.OutputMode != "full" || ch.SessionMode != "user" || ch.KnowledgeBaseID != "" {
		t.Fatalf("channel = mode %q output %q session %q kb %q", ch.Mode, ch.OutputMode, ch.SessionMode, ch.KnowledgeBaseID)
	}
	if ch.BotIdentity != "whatsapp:6281234567890" {
		t.Fatalf("bot identity = %q", ch.BotIdentity)
	}
	var creds map[string]any
	_ = json.Unmarshal(ch.Credentials, &creds)
	if creds["owner_number"] != "6281111111111" {
		t.Fatalf("owner_number not normalised: %v", creds)
	}
}

func TestCreateWhatsAppChannelRequiresAllowlist(t *testing.T) {
	h := NewIMHandler(nil)
	body := fmt.Sprintf(`{"platform":"whatsapp","credentials":{"device_jid":%q}}`, testWhatsAppJID)
	rec := doIMRequest(t, h.CreateIMChannel, http.MethodPost, "/agents/:id/im-channels", "/agents/agent-1/im-channels", body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "owner_number or allowed_numbers") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateWhatsAppChannelKeepsForcedSettings(t *testing.T) {
	h, db := newWhatsAppTestHandler(t)
	ch := &im.IMChannel{
		ID: "ch-1", TenantID: 42, AgentID: "agent-1", Platform: "whatsapp", Enabled: false,
		Mode: "websocket", OutputMode: "full", SessionMode: "user",
		Credentials: types.JSON(fmt.Sprintf(`{"device_jid":%q,"owner_number":"1"}`, testWhatsAppJID)),
	}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	rec := doIMRequest(t, h.UpdateIMChannel, http.MethodPut, "/im-channels/:id", "/im-channels/ch-1",
		`{"mode":"webhook","output_mode":"stream","session_mode":"thread","knowledge_base_id":"kb-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got im.IMChannel
	if err := db.First(&got, "id = ?", "ch-1").Error; err != nil {
		t.Fatalf("load channel: %v", err)
	}
	if got.Mode != "websocket" || got.OutputMode != "full" || got.SessionMode != "user" || got.KnowledgeBaseID != "" {
		t.Fatalf("channel = mode %q output %q session %q kb %q", got.Mode, got.OutputMode, got.SessionMode, got.KnowledgeBaseID)
	}

	rec = doIMRequest(t, h.UpdateIMChannel, http.MethodPut, "/im-channels/:id", "/im-channels/ch-1",
		fmt.Sprintf(`{"credentials":{"device_jid":%q}}`, testWhatsAppJID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update without allowlist: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
