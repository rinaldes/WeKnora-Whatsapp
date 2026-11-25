package whatsapp

import (
	"regexp"
	"strings"
)

// notice is one core status reply in the languages the adapter translates to.
type notice struct{ en, id string }

// pick returns the translation for locale: Indonesian for id locales,
// English for everything else.
func (n notice) pick(locale string) string {
	if strings.HasPrefix(locale, "id") {
		return n.id
	}
	return n.en
}

// Core's IM service sends a few hardcoded zh-CN status replies through
// SendReply. Rather than patch core, the adapter swaps them for the
// channel's locale. Matching is exact, so if upstream rewords one it simply
// reaches users in Chinese. After each upstream rebase, compare these with:
//
//	grep -n 'Content: *"' internal/im/service.go internal/im/cmd_*.go
//	grep -n 'Fallback *=' internal/im/service.go
var coreNotices = map[string]notice{
	// service.go: rate limited
	"您的消息发送过于频繁，请稍后再试。": {
		"You're sending messages too quickly. Please try again later.",
		"Anda mengirim pesan terlalu cepat. Silakan coba lagi nanti.",
	},
	// service.go: queue full
	"当前排队人数较多，请稍后再试。": {
		"Too many requests are waiting right now. Please try again later.",
		"Saat ini terlalu banyak permintaan yang mengantre. Silakan coba lagi nanti.",
	},
	// service.go: queued (multi-instance)
	"收到，当前排队中，请稍候 ⏳": {
		"Got it, your message is queued. Please wait ⏳",
		"Diterima, pesan Anda sedang dalam antrean. Mohon tunggu ⏳",
	},
	// service.go: attachment download/parse failed
	"❌ 无法读取此附件，请重试或改用文字描述。": {
		"❌ Couldn't read this attachment. Please try again or describe it in text.",
		"❌ Lampiran ini tidak dapat dibaca. Silakan coba lagi atau jelaskan dengan teks.",
	},
	// service.go: unknown slash command
	"未知指令，发送 `/help` 查看所有可用指令。": {
		"Unknown command. Send `/help` to see all commands.",
		"Perintah tidak dikenal. Kirim `/help` untuk melihat semua perintah.",
	},
	// service.go: slash command failed
	"抱歉，执行指令时出现了异常，请稍后再试。": {
		"Sorry, something went wrong running that command. Please try again later.",
		"Maaf, terjadi kesalahan saat menjalankan perintah itu. Silakan coba lagi nanti.",
	},
	// service.go: QA fallbacks
	"抱歉，我暂时无法回答这个问题。": {
		"Sorry, I can't answer that right now.",
		"Maaf, saya belum bisa menjawab pertanyaan itu saat ini.",
	},
	"抱歉，处理您的问题时出现了异常，请稍后再试。": {
		"Sorry, something went wrong while answering. Please try again later.",
		"Maaf, terjadi kesalahan saat menjawab. Silakan coba lagi nanti.",
	},
	"抱歉，回答已被取消。": {
		"The answer was cancelled.",
		"Jawaban dibatalkan.",
	},
	// cmd_stop.go, cmd_clear.go
	"✅ 已请求中止当前回答。": {
		"✅ Stopping the current answer.",
		"✅ Menghentikan jawaban saat ini.",
	},
	"✅ 对话已清空，下次消息将开始全新会话。": {
		"✅ Conversation cleared. Your next message starts a new one.",
		"✅ Percakapan dihapus. Pesan berikutnya akan memulai percakapan baru.",
	},
}

// coreNoticePatterns cover the replies that embed a value.
var coreNoticePatterns = []struct {
	re   *regexp.Regexp
	repl notice
}{
	// service.go: queue position
	{regexp.MustCompile(`^收到，前面还有 (\d+) 条消息在处理，请稍候 ⏳$`), notice{
		"Got it. $1 message(s) ahead of yours are being handled, please wait ⏳",
		"Diterima. Ada $1 pesan sebelum pesan Anda yang sedang diproses, mohon tunggu ⏳",
	}},
	// cmd_help.go: unknown command name
	{regexp.MustCompile("^未知指令 `([^`]*)`，发送 `/help` 查看所有可用指令。$"), notice{
		"Unknown command `$1`. Send `/help` to see all commands.",
		"Perintah `$1` tidak dikenal. Kirim `/help` untuk melihat semua perintah.",
	}},
}

// localizeCoreNotice replaces a known core status reply with its
// translation for locale. Chinese locales and unknown text pass through
// unchanged; Indonesian locales get Indonesian and the rest get English.
func localizeCoreNotice(content, locale string) string {
	if strings.HasPrefix(locale, "zh") {
		return content
	}
	trimmed := strings.TrimSpace(content)
	if n, ok := coreNotices[trimmed]; ok {
		return n.pick(locale)
	}
	for _, p := range coreNoticePatterns {
		if p.re.MatchString(trimmed) {
			return p.re.ReplaceAllString(trimmed, p.repl.pick(locale))
		}
	}
	return content
}
