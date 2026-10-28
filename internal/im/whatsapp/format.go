package whatsapp

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxChunkRunes caps one outgoing message. WhatsApp allows more, but long
// messages render poorly.
const maxChunkRunes = 4000

var (
	headingRE   = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	bulletRE    = regexp.MustCompile(`^(\s*)[*+]\s+`)
	hrRE        = regexp.MustCompile(`^\s{0,3}([-*_])(\s*[-*_]){2,}\s*$`)
	tableSepRE  = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)
	imageRE     = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	linkRE      = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	boldStarRE  = regexp.MustCompile(`\*\*(\S(?:.*?\S)?)\*\*`)
	boldUnderRE = regexp.MustCompile(`(^|[^\w])__(\S(?:.*?\S)?)__([^\w]|$)`)
	italicRE    = regexp.MustCompile(`(^|[^*\w])\*(\S(?:[^*]*?\S)?)\*([^*\w]|$)`)
	strikeRE    = regexp.MustCompile(`~~(\S(?:.*?\S)?)~~`)
)

// boldMark stands in for WhatsApp's "*" while italics are rewritten, so a
// converted **bold** isn't then read as *italic*.
const boldMark = "\x00"

// ToWhatsApp converts Markdown to WhatsApp's text formatting. Fenced code
// blocks and inline code spans are left untouched.
func ToWhatsApp(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isFence(trimmed) {
			inFence = !inFence
			out = append(out, "```") // drop the language tag
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trimmed, "|") && tableSepRE.MatchString(trimmed) {
			continue // table header separator row
		}
		out = append(out, convertLine(line))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func isFence(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

func convertLine(line string) string {
	trimmed := strings.TrimSpace(line)
	switch {
	case trimmed == "":
		return ""
	case hrRE.MatchString(line):
		return "───"
	case strings.HasPrefix(trimmed, "|"):
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		for i, c := range cells {
			cells[i] = convertInline(strings.TrimSpace(c))
		}
		return strings.Join(cells, " · ")
	}
	if m := headingRE.FindStringSubmatch(line); m != nil {
		text := convertInline(m[1])
		text = strings.ReplaceAll(text, "*", "") // no nested bold in a bold heading
		if text == "" {
			return ""
		}
		return "*" + text + "*"
	}
	line = bulletRE.ReplaceAllString(line, "$1- ")
	return convertInline(line)
}

// convertInline rewrites emphasis and links outside inline code spans.
func convertInline(s string) string {
	parts := strings.Split(s, "`")
	for i := 0; i < len(parts); i += 2 { // even parts are outside code spans
		if i == len(parts)-1 && len(parts)%2 == 0 {
			break // unmatched backtick: leave the tail alone
		}
		parts[i] = convertEmphasis(parts[i])
	}
	return strings.Join(parts, "`")
}

func convertEmphasis(s string) string {
	s = imageRE.ReplaceAllStringFunc(s, func(m string) string {
		sm := imageRE.FindStringSubmatch(m)
		return linkText(sm[1], sm[2])
	})
	s = linkRE.ReplaceAllStringFunc(s, func(m string) string {
		sm := linkRE.FindStringSubmatch(m)
		return linkText(sm[1], sm[2])
	})
	s = boldStarRE.ReplaceAllString(s, boldMark+"$1"+boldMark)
	s = boldUnderRE.ReplaceAllString(s, "$1"+boldMark+"$2"+boldMark+"$3")
	s = italicRE.ReplaceAllString(s, "${1}_${2}_${3}")
	s = strikeRE.ReplaceAllString(s, "~$1~")
	return strings.ReplaceAll(s, boldMark, "*")
}

func linkText(text, url string) string {
	text = strings.TrimSpace(text)
	if text == "" || text == url {
		return url
	}
	return text + " (" + url + ")"
}

// Chunk splits text into messages of at most limit runes, preferring blank
// lines, then line breaks, then a hard cut. A code block split across
// messages is closed and reopened so each part renders on its own.
func Chunk(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if runeLen(text) <= limit {
		return []string{text}
	}

	var chunks []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}
	add := func(block string) {
		sep := ""
		if cur.Len() > 0 {
			sep = "\n\n"
		}
		if runeLen(cur.String())+runeLen(sep)+runeLen(block) <= limit {
			cur.WriteString(sep + block)
			return
		}
		flush()
		if runeLen(block) <= limit {
			cur.WriteString(block)
			return
		}
		for _, part := range splitBlock(block, limit) {
			chunks = append(chunks, part)
		}
	}
	for _, b := range blocks(text) {
		add(b)
	}
	flush()
	return chunks
}

// blocks splits text into paragraphs; a fenced code block is always one
// block of its own, blank lines included.
func blocks(text string) []string {
	var out, para []string
	flushPara := func() {
		if len(para) > 0 {
			out = append(out, strings.Join(para, "\n"))
			para = nil
		}
	}
	var fence []string
	for _, line := range strings.Split(text, "\n") {
		if fence != nil {
			fence = append(fence, line)
			if isFence(strings.TrimSpace(line)) {
				out = append(out, strings.Join(fence, "\n"))
				fence = nil
			}
			continue
		}
		if isFence(strings.TrimSpace(line)) {
			flushPara()
			fence = []string{line}
			continue
		}
		if strings.TrimSpace(line) == "" {
			flushPara()
			continue
		}
		para = append(para, line)
	}
	if fence != nil {
		out = append(out, strings.Join(fence, "\n"))
	}
	flushPara()
	return out
}

// splitBlock splits one oversized block on line breaks, hard-cutting lines
// that are still too long.
func splitBlock(block string, limit int) []string {
	lines := strings.Split(block, "\n")
	fenced := len(lines) >= 2 && isFence(strings.TrimSpace(lines[0]))
	open, close := "", ""
	if fenced {
		lines = lines[1:]
		if n := len(lines); n > 0 && isFence(strings.TrimSpace(lines[n-1])) {
			lines = lines[:n-1]
		}
		open, close = "```\n", "\n```"
		limit -= runeLen(open) + runeLen(close)
	}

	var parts []string
	var cur []string
	curLen := 0
	emit := func() {
		if len(cur) > 0 {
			parts = append(parts, open+strings.Join(cur, "\n")+close)
			cur, curLen = nil, 0
		}
	}
	for _, line := range lines {
		for runeLen(line) > limit {
			emit()
			head, tail := cutRunes(line, limit)
			parts = append(parts, open+head+close)
			line = tail
		}
		extra := runeLen(line)
		if len(cur) > 0 {
			extra++ // newline
		}
		if curLen+extra > limit {
			emit()
			extra = runeLen(line)
		}
		cur = append(cur, line)
		curLen += extra
	}
	emit()
	return parts
}

func cutRunes(s string, n int) (string, string) {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos], s[pos:]
		}
		i++
	}
	return s, ""
}

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}
