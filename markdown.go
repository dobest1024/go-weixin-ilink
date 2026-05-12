package ilink

import (
	"strings"
)

type inlineType int

const (
	inlCode    inlineType = iota // `code`
	inlImage                     // ![alt](url)
	inlStrike                    // ~~strike~~
	inlBold3                     // ***bold-italic***
	inlItalic                    // *italic*
	inlUBold3                    // ___bold-italic___
	inlUItalic                   // _italic_
	inlTable                     // | table row |
)

type inlineState struct {
	typ inlineType
	acc string
}

// MarkdownFilter is a streaming state machine that strips WeChat-unsupported
// markdown from text. It outputs as much filtered text as possible on each
// Feed call, holding back only the minimum characters needed for pattern
// disambiguation.
//
// Preserved: **bold**, H1-H4, lists, plain text, indentation, code fence content.
// Stripped: *italic*, ***bold-italic***, images, ~~strike~~, H5-H6 markers,
// blockquote >, horizontal rules, table pipes, inline backticks.
type MarkdownFilter struct {
	buf   string
	fence bool
	sol   bool
	inl   *inlineState
}

// NewMarkdownFilter creates a filter in start-of-line state.
func NewMarkdownFilter() *MarkdownFilter {
	return &MarkdownFilter{sol: true}
}

// Feed processes a chunk of text and returns filtered output.
func (f *MarkdownFilter) Feed(delta string) string {
	f.buf += delta
	return f.pump(false)
}

// Flush returns any remaining buffered content.
func (f *MarkdownFilter) Flush() string {
	return f.pump(true)
}

func (f *MarkdownFilter) pump(eof bool) string {
	var out strings.Builder
	for len(f.buf) > 0 {
		sLen := len(f.buf)
		sSol := f.sol
		sFence := f.fence
		sInl := f.inl

		if f.fence {
			out.WriteString(f.pumpFence(eof))
		} else if f.inl != nil {
			out.WriteString(f.pumpInline(eof))
		} else if f.sol {
			out.WriteString(f.pumpSOL(eof))
		} else {
			out.WriteString(f.pumpBody(eof))
		}

		if len(f.buf) == sLen && f.sol == sSol &&
			f.fence == sFence && f.inl == sInl {
			break
		}
	}

	if eof && f.inl != nil {
		if f.inl.typ == inlTable {
			out.WriteString(extractTableRow(f.inl.acc))
		} else {
			markers := map[inlineType]string{
				inlCode: "`", inlImage: "![", inlStrike: "~~",
				inlBold3: "***", inlItalic: "*", inlUBold3: "___", inlUItalic: "_",
			}
			out.WriteString(markers[f.inl.typ])
			out.WriteString(f.inl.acc)
		}
		f.inl = nil
	}
	return out.String()
}

func (f *MarkdownFilter) pumpFence(eof bool) string {
	if f.sol {
		if len(f.buf) < 3 && !eof {
			return ""
		}
		if strings.HasPrefix(f.buf, "```") {
			nl := strings.Index(f.buf[3:], "\n")
			if nl != -1 {
				f.fence = false
				f.buf = f.buf[3+nl+1:]
				f.sol = true
				return ""
			}
			if eof {
				f.fence = false
				f.buf = ""
				f.sol = true
				return ""
			}
			return ""
		}
		f.sol = false
	}
	nl := strings.IndexByte(f.buf, '\n')
	if nl != -1 {
		chunk := f.buf[:nl+1]
		f.buf = f.buf[nl+1:]
		f.sol = true
		return chunk
	}
	chunk := f.buf
	f.buf = ""
	return chunk
}

func (f *MarkdownFilter) pumpSOL(eof bool) string {
	b := f.buf

	if b[0] == '\n' {
		f.buf = b[1:]
		return "\n"
	}

	if b[0] == '`' {
		if len(b) < 3 && !eof {
			return ""
		}
		if strings.HasPrefix(b, "```") {
			nl := strings.IndexByte(b[3:], '\n')
			if nl != -1 {
				f.fence = true
				f.buf = b[3+nl+1:]
				f.sol = true
				return ""
			}
			if eof {
				f.fence = true
				f.buf = ""
				f.sol = true
				return ""
			}
			return ""
		}
		f.sol = false
		return ""
	}

	if b[0] == '>' {
		if len(b) < 2 && !eof {
			return ""
		}
		if len(b) >= 2 && b[1] == ' ' {
			f.buf = b[2:]
		} else {
			f.buf = b[1:]
		}
		f.sol = false
		return ""
	}

	if b[0] == '#' {
		n := 0
		for n < len(b) && b[n] == '#' {
			n++
		}
		if n == len(b) && !eof {
			return ""
		}
		if n >= 5 && n <= 6 && n < len(b) && b[n] == ' ' {
			f.buf = b[n+1:]
			f.sol = false
			return ""
		}
		f.sol = false
		return ""
	}

	if b[0] == '|' {
		f.buf = b[1:]
		f.inl = &inlineState{typ: inlTable, acc: ""}
		f.sol = false
		return ""
	}

	if b[0] == ' ' || b[0] == '\t' {
		allWhitespace := true
		for i := 0; i < len(b); i++ {
			if b[i] != ' ' && b[i] != '\t' {
				allWhitespace = false
				break
			}
		}
		if allWhitespace && !eof {
			return ""
		}
		f.sol = false
		return ""
	}

	if b[0] == '-' || b[0] == '*' || b[0] == '_' {
		ch := b[0]
		j := 0
		for j < len(b) && (b[j] == ch || b[j] == ' ') {
			j++
		}
		if j == len(b) && !eof {
			return ""
		}
		if j == len(b) || b[j] == '\n' {
			count := 0
			for k := 0; k < j; k++ {
				if b[k] == ch {
					count++
				}
			}
			if count >= 3 {
				if j < len(b) {
					f.buf = b[j+1:]
				} else {
					f.buf = ""
				}
				f.sol = true
				return ""
			}
		}
		f.sol = false
		return ""
	}

	f.sol = false
	return ""
}

func (f *MarkdownFilter) pumpBody(eof bool) string {
	var out strings.Builder
	i := 0
	for i < len(f.buf) {
		c := f.buf[i]
		if c == '\n' {
			out.WriteString(f.buf[:i+1])
			f.buf = f.buf[i+1:]
			f.sol = true
			return out.String()
		}
		if c == '`' {
			out.WriteString(f.buf[:i])
			f.buf = f.buf[i+1:]
			f.inl = &inlineState{typ: inlCode, acc: ""}
			return out.String()
		}
		if c == '!' && i+1 < len(f.buf) && f.buf[i+1] == '[' {
			out.WriteString(f.buf[:i])
			f.buf = f.buf[i+2:]
			f.inl = &inlineState{typ: inlImage, acc: ""}
			return out.String()
		}
		if c == '~' && i+1 < len(f.buf) && f.buf[i+1] == '~' {
			out.WriteString(f.buf[:i])
			f.buf = f.buf[i+2:]
			f.inl = &inlineState{typ: inlStrike, acc: ""}
			return out.String()
		}
		if c == '*' {
			if i+2 < len(f.buf) && f.buf[i+1] == '*' && f.buf[i+2] == '*' {
				out.WriteString(f.buf[:i])
				f.buf = f.buf[i+3:]
				f.inl = &inlineState{typ: inlBold3, acc: ""}
				return out.String()
			}
			if i+1 < len(f.buf) && f.buf[i+1] == '*' {
				i += 2
				continue
			}
			if i+1 < len(f.buf) && f.buf[i+1] != ' ' && f.buf[i+1] != '\n' {
				out.WriteString(f.buf[:i])
				f.buf = f.buf[i+1:]
				f.inl = &inlineState{typ: inlItalic, acc: ""}
				return out.String()
			}
			i++
			continue
		}
		if c == '_' {
			if i+2 < len(f.buf) && f.buf[i+1] == '_' && f.buf[i+2] == '_' {
				out.WriteString(f.buf[:i])
				f.buf = f.buf[i+3:]
				f.inl = &inlineState{typ: inlUBold3, acc: ""}
				return out.String()
			}
			if i+1 < len(f.buf) && f.buf[i+1] == '_' {
				i += 2
				continue
			}
			if i+1 < len(f.buf) && f.buf[i+1] != ' ' && f.buf[i+1] != '\n' {
				out.WriteString(f.buf[:i])
				f.buf = f.buf[i+1:]
				f.inl = &inlineState{typ: inlUItalic, acc: ""}
				return out.String()
			}
			i++
			continue
		}
		i++
	}

	hold := 0
	if !eof {
		if strings.HasSuffix(f.buf, "**") {
			hold = 2
		} else if strings.HasSuffix(f.buf, "__") {
			hold = 2
		} else if strings.HasSuffix(f.buf, "*") {
			hold = 1
		} else if strings.HasSuffix(f.buf, "_") {
			hold = 1
		} else if strings.HasSuffix(f.buf, "~") {
			hold = 1
		} else if strings.HasSuffix(f.buf, "!") {
			hold = 1
		}
	}
	out.WriteString(f.buf[:len(f.buf)-hold])
	if hold > 0 {
		f.buf = f.buf[len(f.buf)-hold:]
	} else {
		f.buf = ""
	}
	return out.String()
}

func (f *MarkdownFilter) pumpInline(_ bool) string {
	if f.inl == nil {
		return ""
	}
	f.inl.acc += f.buf
	f.buf = ""

	switch f.inl.typ {
	case inlCode:
		idx := strings.IndexByte(f.inl.acc, '`')
		if idx != -1 {
			content := f.inl.acc[:idx]
			f.buf = f.inl.acc[idx+1:]
			f.inl = nil
			return content
		}
		nl := strings.IndexByte(f.inl.acc, '\n')
		if nl != -1 {
			r := "`" + f.inl.acc[:nl+1]
			f.buf = f.inl.acc[nl+1:]
			f.inl = nil
			f.sol = true
			return r
		}
		return ""

	case inlStrike:
		idx := strings.Index(f.inl.acc, "~~")
		if idx != -1 {
			content := f.inl.acc[:idx]
			f.buf = f.inl.acc[idx+2:]
			f.inl = nil
			return content
		}
		return ""

	case inlBold3:
		idx := strings.Index(f.inl.acc, "***")
		if idx != -1 {
			content := f.inl.acc[:idx]
			f.buf = f.inl.acc[idx+3:]
			f.inl = nil
			return content
		}
		return ""

	case inlUBold3:
		idx := strings.Index(f.inl.acc, "___")
		if idx != -1 {
			content := f.inl.acc[:idx]
			f.buf = f.inl.acc[idx+3:]
			f.inl = nil
			return content
		}
		return ""

	case inlItalic:
		for j := 0; j < len(f.inl.acc); j++ {
			if f.inl.acc[j] == '\n' {
				r := "*" + f.inl.acc[:j+1]
				f.buf = f.inl.acc[j+1:]
				f.inl = nil
				f.sol = true
				return r
			}
			if f.inl.acc[j] == '*' {
				if j+1 < len(f.inl.acc) && f.inl.acc[j+1] == '*' {
					j++
					continue
				}
				content := f.inl.acc[:j]
				f.buf = f.inl.acc[j+1:]
				f.inl = nil
				return content
			}
		}
		return ""

	case inlUItalic:
		for j := 0; j < len(f.inl.acc); j++ {
			if f.inl.acc[j] == '\n' {
				r := "_" + f.inl.acc[:j+1]
				f.buf = f.inl.acc[j+1:]
				f.inl = nil
				f.sol = true
				return r
			}
			if f.inl.acc[j] == '_' {
				if j+1 < len(f.inl.acc) && f.inl.acc[j+1] == '_' {
					j++
					continue
				}
				content := f.inl.acc[:j]
				f.buf = f.inl.acc[j+1:]
				f.inl = nil
				return content
			}
		}
		return ""

	case inlImage:
		cb := strings.IndexByte(f.inl.acc, ']')
		if cb == -1 {
			return ""
		}
		if cb+1 >= len(f.inl.acc) {
			return ""
		}
		if f.inl.acc[cb+1] != '(' {
			r := "![" + f.inl.acc[:cb+1]
			f.buf = f.inl.acc[cb+1:]
			f.inl = nil
			return r
		}
		cp := strings.IndexByte(f.inl.acc[cb+2:], ')')
		if cp != -1 {
			f.buf = f.inl.acc[cb+2+cp+1:]
			f.inl = nil
			return ""
		}
		return ""

	case inlTable:
		nl := strings.IndexByte(f.inl.acc, '\n')
		if nl != -1 {
			line := f.inl.acc[:nl]
			f.buf = f.inl.acc[nl+1:]
			f.inl = nil
			f.sol = true
			row := extractTableRow(line)
			if row != "" {
				return row + "\n"
			}
			return ""
		}
		return ""
	}
	return ""
}

func extractTableRow(line string) string {
	if isTableSeparator(line) {
		return ""
	}
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for i, p := range parts {
		trimmed := strings.TrimSpace(p)
		if i == 0 && trimmed == "" {
			continue
		}
		if i == len(parts)-1 && trimmed == "" {
			continue
		}
		cells = append(cells, trimmed)
	}
	return strings.Join(cells, "\t")
}

func isTableSeparator(line string) bool {
	hasDash := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case ' ', '\t', '|', ':':
			continue
		case '-':
			hasDash = true
		default:
			return false
		}
	}
	return hasDash
}

// StripMarkdown filters an entire string at once, removing WeChat-unsupported
// markdown syntax while preserving supported constructs like **bold** and H1-H4.
func StripMarkdown(text string) string {
	f := NewMarkdownFilter()
	return f.Feed(text) + f.Flush()
}
