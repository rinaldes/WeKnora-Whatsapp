package whatsapp

import (
	"fmt"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

func TestMediaCachePutGet(t *testing.T) {
	c := newMediaCache()
	img := &waE2E.ImageMessage{}
	c.put("a", img)
	if got, ok := c.get("a"); !ok || got != img {
		t.Fatalf("get(a) = %v, %v", got, ok)
	}
	if _, ok := c.get("b"); ok {
		t.Fatal("get(b) ok = true")
	}
}

func TestMediaCacheTTL(t *testing.T) {
	now := time.Now()
	c := newMediaCache()
	c.now = func() time.Time { return now }
	c.put("a", &waE2E.ImageMessage{})
	now = now.Add(mediaTTL + time.Second)
	if _, ok := c.get("a"); ok {
		t.Fatal("expired entry returned")
	}
}

func TestMediaCacheCapEvictsOldest(t *testing.T) {
	now := time.Now()
	c := newMediaCache()
	c.max = 3
	c.now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		now = now.Add(time.Second)
		c.put(fmt.Sprint(i), &waE2E.DocumentMessage{})
	}
	if _, ok := c.get("0"); ok {
		t.Fatal("oldest entry not evicted")
	}
	for _, k := range []string{"1", "2", "3"} {
		if _, ok := c.get(k); !ok {
			t.Fatalf("entry %s evicted", k)
		}
	}
}

func TestExtForMIME(t *testing.T) {
	for in, want := range map[string]string{
		"image/jpeg":                ".jpg",
		"image/png":                 ".png",
		"application/pdf":           ".pdf",
		"text/plain; charset=utf-8": ".txt",
		"":                          ".def",
		"application/x-nonexistent": ".def",
	} {
		if got := extForMIME(in, ".def"); got != want {
			t.Errorf("extForMIME(%q) = %q, want %q", in, got, want)
		}
	}
}
