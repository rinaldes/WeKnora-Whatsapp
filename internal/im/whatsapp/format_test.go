package whatsapp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestToWhatsApp(t *testing.T) {
	for _, tt := range []struct{ name, in, want string }{
		{"bold stars", "a **bold** b", "a *bold* b"},
		{"bold underscores", "a __bold__ b", "a *bold* b"},
		{"snake case kept", "call my__func__name now", "call my__func__name now"},
		{"italic star", "an *italic* word", "an _italic_ word"},
		{"italic underscore", "an _italic_ word", "an _italic_ word"},
		{"bold and italic", "**b** and *i*", "*b* and _i_"},
		{"strike", "~~gone~~", "~gone~"},
		{"inline code untouched", "use `**x**` here **y**", "use `**x**` here *y*"},
		{"heading", "## Results **now**", "*Results now*"},
		{"bullet dash kept", "- item", "- item"},
		{"bullet star", "* item **b**", "- item *b*"},
		{"numbered kept", "1. first", "1. first"},
		{"link", "see [docs](https://x.io/a)", "see docs (https://x.io/a)"},
		{"bare link text", "[https://x.io](https://x.io)", "https://x.io"},
		{"image", "![chart](https://x.io/c.png)", "chart (https://x.io/c.png)"},
		{"quote", "> **note**", "> *note*"},
		{"hr", "---", "───"},
		{
			"table",
			"| a | **b** |\n|---|:-:|\n| 1 | 2 |",
			"a · *b*\n1 · 2",
		},
		{
			"fence keeps content, drops lang",
			"```go\nx := **y**\n```\n**after**",
			"```\nx := **y**\n```\n*after*",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToWhatsApp(tt.in); got != tt.want {
				t.Fatalf("ToWhatsApp(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestChunkShortTextIsOneChunk(t *testing.T) {
	if got := Chunk("  hello  ", 10); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("Chunk = %q", got)
	}
	if got := Chunk("   ", 10); got != nil {
		t.Fatalf("Chunk(blank) = %q, want nil", got)
	}
}

func TestChunkPrefersParagraphs(t *testing.T) {
	text := strings.Repeat("a", 30) + "\n\n" + strings.Repeat("b", 30) + "\n\n" + strings.Repeat("c", 30)
	got := Chunk(text, 70)
	want := []string{strings.Repeat("a", 30) + "\n\n" + strings.Repeat("b", 30), strings.Repeat("c", 30)}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Chunk = %q, want %q", got, want)
	}
}

func TestChunkSplitsLinesThenHardCuts(t *testing.T) {
	text := strings.Repeat("x", 25) + "\n" + strings.Repeat("y", 25) + "\n" + strings.Repeat("é", 45)
	got := Chunk(text, 30)
	for _, c := range got {
		if n := utf8.RuneCountInString(c); n > 30 {
			t.Fatalf("chunk %q has %d runes > 30", c, n)
		}
	}
	if joined := strings.Join(got, ""); strings.Count(joined, "é") != 45 || strings.Count(joined, "x") != 25 {
		t.Fatalf("content lost: %q", got)
	}
}

func TestChunkReopensFences(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "line of code")
	}
	text := "intro\n\n```\n" + strings.Join(lines, "\n") + "\n```"
	got := Chunk(text, 60)
	if len(got) < 3 {
		t.Fatalf("expected several chunks, got %q", got)
	}
	for _, c := range got[1:] {
		if !strings.HasPrefix(c, "```\n") || !strings.HasSuffix(c, "\n```") {
			t.Fatalf("code chunk not fenced: %q", c)
		}
		if utf8.RuneCountInString(c) > 60 {
			t.Fatalf("chunk too long: %d", utf8.RuneCountInString(c))
		}
	}
}
