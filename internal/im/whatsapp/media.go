package whatsapp

import (
	"mime"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
)

const (
	// mediaTTL is how long an attachment stays downloadable after it arrives.
	// Core downloads it when the QA request runs, normally within seconds.
	mediaTTL = 5 * time.Minute
	// maxMediaEntries bounds memory if messages pile up in the QA queue.
	maxMediaEntries = 200
)

// mediaCache keeps the media protos core can't carry in IncomingMessage,
// keyed by message ID, until DownloadFile asks for them.
type mediaCache struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu    sync.Mutex
	items map[string]mediaEntry
}

type mediaEntry struct {
	msg   whatsmeow.DownloadableMessage
	added time.Time
}

func newMediaCache() *mediaCache {
	return &mediaCache{
		ttl:   mediaTTL,
		max:   maxMediaEntries,
		now:   time.Now,
		items: make(map[string]mediaEntry),
	}
}

func (c *mediaCache) put(id string, msg whatsmeow.DownloadableMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, e := range c.items {
		if now.Sub(e.added) > c.ttl {
			delete(c.items, k)
		}
	}
	for len(c.items) >= c.max {
		oldest := ""
		for k, e := range c.items {
			if oldest == "" || e.added.Before(c.items[oldest].added) {
				oldest = k
			}
		}
		delete(c.items, oldest)
	}
	c.items[id] = mediaEntry{msg: msg, added: now}
}

func (c *mediaCache) get(id string) (whatsmeow.DownloadableMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[id]
	if !ok {
		return nil, false
	}
	if c.now().Sub(e.added) > c.ttl {
		delete(c.items, id)
		return nil, false
	}
	return e.msg, true
}

// commonExts pins extensions for MIME types whose mime.ExtensionsByType
// answer depends on the host's mime database.
var commonExts = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/webp":      ".webp",
	"image/gif":       ".gif",
	"application/pdf": ".pdf",
	"text/plain":      ".txt",
}

// extForMIME returns a file extension for a MIME type, or def.
func extForMIME(mimetype, def string) string {
	base, _, _ := mime.ParseMediaType(mimetype)
	if ext, ok := commonExts[base]; ok {
		return ext
	}
	if exts, err := mime.ExtensionsByType(base); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return def
}
