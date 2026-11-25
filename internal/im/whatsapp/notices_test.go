package whatsapp

import (
	"os"
	"strings"
	"testing"
)

func TestLocalizeCoreNotice(t *testing.T) {
	for zh, n := range coreNotices {
		for _, tt := range []struct{ locale, want string }{
			{"", n.en},
			{"en-US", n.en},
			{"ja-JP", n.en}, // no translation yet: English
			{"id-ID", n.id},
			{"zh-CN", zh},
			{"zh-TW", zh},
		} {
			if got := localizeCoreNotice(zh, tt.locale); got != tt.want {
				t.Errorf("localizeCoreNotice(%q, %q) = %q, want %q", zh, tt.locale, got, tt.want)
			}
		}
	}
}

func TestLocalizeCoreNoticePatterns(t *testing.T) {
	if got := localizeCoreNotice("收到，前面还有 3 条消息在处理，请稍候 ⏳", ""); got !=
		"Got it. 3 message(s) ahead of yours are being handled, please wait ⏳" {
		t.Fatalf("queue position = %q", got)
	}
	if got := localizeCoreNotice("未知指令 `foo`，发送 `/help` 查看所有可用指令。", "en-US"); got !=
		"Unknown command `foo`. Send `/help` to see all commands." {
		t.Fatalf("unknown command = %q", got)
	}
	if got := localizeCoreNotice("未知指令 `foo`，发送 `/help` 查看所有可用指令。", "id-ID"); got !=
		"Perintah `foo` tidak dikenal. Kirim `/help` untuk melihat semua perintah." {
		t.Fatalf("unknown command (id-ID) = %q", got)
	}
	if got := localizeCoreNotice("收到，前面还有 3 条消息在处理，请稍候 ⏳", "zh-CN"); !strings.HasPrefix(got, "收到") {
		t.Fatalf("zh-CN changed: %q", got)
	}
}

func TestLocalizeCoreNoticeLeavesOtherTextAlone(t *testing.T) {
	for _, s := range []string{
		"The answer is 42.",
		"Core said: 当前排队人数较多，请稍后再试。 in the logs", // mid-text, not a notice
		"**您的消息发送过于频繁，请稍后再试。**",
		"",
	} {
		if got := localizeCoreNotice(s, "en-US"); got != s {
			t.Errorf("localizeCoreNotice(%q) = %q, want unchanged", s, got)
		}
	}
}

// TestCoreNoticesMatchCoreSource fails when upstream rewords a reply the
// adapter translates, so a rebase can't silently bring the Chinese back.
func TestCoreNoticesMatchCoreSource(t *testing.T) {
	var src strings.Builder
	for _, f := range []string{"../service.go", "../cmd_stop.go", "../cmd_clear.go", "../cmd_help.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		src.Write(b)
	}
	core := src.String()
	for zh := range coreNotices {
		if !strings.Contains(core, `"`+zh+`"`) {
			t.Errorf("core no longer sends %q; update notices.go", zh)
		}
	}
	for _, s := range []string{
		`"收到，前面还有 %d 条消息在处理，请稍候 ⏳"`,
		"\"未知指令 `%s`，发送 `/help` 查看所有可用指令。\"",
	} {
		if !strings.Contains(core, s) {
			t.Errorf("core no longer sends %s; update coreNoticePatterns", s)
		}
	}
}
