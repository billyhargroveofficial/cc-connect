package telegram

import "strings"

// normalizeRichMath translates the TeX delimiters emitted by coding agents to
// Telegram Rich Markdown. A math fence protects multiline formulas from the
// hard-break pass. Code examples and already-native dollar math stay literal.
func normalizeRichMath(md string) string {
	var out strings.Builder
	for i := 0; i < len(md); {
		// Preserve code spans/fences, including unfinished streaming fences.
		if md[i] == '`' || (md[i] == '~' && strings.HasPrefix(md[i:], "~~~")) {
			j := i + 1
			for j < len(md) && md[j] == md[i] {
				j++
			}
			marker := md[i:j]
			end := matchingRichMarker(md, j, marker)
			if end < 0 {
				out.WriteString(md[i:])
				break
			}
			out.WriteString(md[i : end+len(marker)])
			i = end + len(marker)
			continue
		}
		if md[i] == '$' {
			marker := "$"
			if strings.HasPrefix(md[i:], "$$") {
				marker = "$$"
			}
			if end := matchingRichMarker(md, i+len(marker), marker); end >= 0 {
				out.WriteString(md[i : end+len(marker)])
				i = end + len(marker)
				continue
			}
		}
		if md[i] == '\\' && i+1 < len(md) {
			close := ""
			switch md[i+1] {
			case '(':
				close = `\)`
			case '[':
				close = `\]`
			}
			if close != "" {
				if offset := strings.Index(md[i+2:], close); offset >= 0 {
					end := i + 2 + offset
					body := strings.TrimSpace(md[i+2 : end])
					if close == `\]` {
						out.WriteString("\n```math\n" + body + "\n```\n")
					} else {
						out.WriteString("$" + body + "$")
					}
					i = end + 2
					continue
				}
			}
			// Preserve escaped delimiters (e.g. \\( in an example).
			out.WriteString(md[i : i+2])
			i += 2
			continue
		}
		out.WriteByte(md[i])
		i++
	}
	return out.String()
}

// Find an exact delimiter run, so a single backtick inside a double-backtick
// span doesn't expose its contents to formula normalization.
func matchingRichMarker(md string, start int, marker string) int {
	for start < len(md) {
		offset := strings.Index(md[start:], marker)
		if offset < 0 {
			return -1
		}
		pos := start + offset
		end := pos + len(marker)
		if (pos == 0 || md[pos-1] != marker[0]) && (end == len(md) || md[end] != marker[0]) {
			return pos
		}
		start = end
	}
	return -1
}

func prepareRichMarkdown(md string) string {
	return markdownHardBreaks(normalizeRichMath(md))
}
