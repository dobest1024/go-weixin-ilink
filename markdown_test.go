package ilink

import (
	"math/rand"
	"testing"
)

func oneShot(input string) string {
	f := NewMarkdownFilter()
	return f.Feed(input) + f.Flush()
}

func charByChar(input string) string {
	f := NewMarkdownFilter()
	var out string
	for _, c := range input {
		out += f.Feed(string(c))
	}
	out += f.Flush()
	return out
}

func randomChunks(input string, seed int64) string {
	rng := rand.New(rand.NewSource(seed))
	f := NewMarkdownFilter()
	var out string
	for len(input) > 0 {
		n := rng.Intn(5) + 1
		if n > len(input) {
			n = len(input)
		}
		out += f.Feed(input[:n])
		input = input[n:]
	}
	out += f.Flush()
	return out
}

func expectFilter(t *testing.T, input, expected string) {
	t.Helper()

	got := oneShot(input)
	if got != expected {
		t.Errorf("oneShot(%q)\n  got:  %q\n  want: %q", input, got, expected)
	}

	got2 := charByChar(input)
	if got2 != expected {
		t.Errorf("charByChar(%q)\n  got:  %q\n  want: %q", input, got2, expected)
	}

	for seed := int64(0); seed < 3; seed++ {
		got3 := randomChunks(input, seed)
		if got3 != expected {
			t.Errorf("randomChunks(%q, seed=%d)\n  got:  %q\n  want: %q", input, seed, got3, expected)
		}
	}
}

func TestPlainText(t *testing.T) {
	expectFilter(t, "Hello world", "Hello world")
	expectFilter(t, "你好世界", "你好世界")
	expectFilter(t, "Hello 你好 world", "Hello 你好 world")
	expectFilter(t, "", "")
}

func TestBoldPreserved(t *testing.T) {
	expectFilter(t, "**bold**", "**bold**")
	expectFilter(t, "before **bold** after", "before **bold** after")
	expectFilter(t, "**多个** **粗体**", "**多个** **粗体**")
}

func TestItalicStripped(t *testing.T) {
	expectFilter(t, "*italic*", "italic")
	expectFilter(t, "before *italic* after", "before italic after")
	expectFilter(t, "*斜体*", "斜体")
}

func TestBoldItalicStripped(t *testing.T) {
	expectFilter(t, "***bold-italic***", "bold-italic")
	expectFilter(t, "before ***中文*** after", "before 中文 after")
}

func TestUnderscoreBoldItalicStripped(t *testing.T) {
	expectFilter(t, "___bold___", "bold")
	expectFilter(t, "before ___text___ after", "before text after")
}

func TestUnderscoreItalicStripped(t *testing.T) {
	expectFilter(t, "_italic_", "italic")
	expectFilter(t, "before _斜体_ after", "before 斜体 after")
}

func TestDoubleUnderscorePreserved(t *testing.T) {
	expectFilter(t, "__bold__", "__bold__")
}

func TestStrikethroughStripped(t *testing.T) {
	expectFilter(t, "~~strike~~", "strike")
	expectFilter(t, "before ~~删除~~ after", "before 删除 after")
}

func TestImageRemoved(t *testing.T) {
	expectFilter(t, "before![alt](url)after", "beforeafter")
	expectFilter(t, "![img](https://example.com/a.png)", "")
	expectFilter(t, "text ![](url) more", "text  more")
}

func TestImageIncomplete(t *testing.T) {
	expectFilter(t, "![not image", "![not image")
	expectFilter(t, "![alt]text", "![alt]text")
}

func TestInlineCodeStripped(t *testing.T) {
	expectFilter(t, "`code`", "code")
	expectFilter(t, "before `code` after", "before code after")
}

func TestInlineCodeNewline(t *testing.T) {
	expectFilter(t, "`unclosed\nnext", "`unclosed\nnext")
}

func TestCodeFence(t *testing.T) {
	expectFilter(t, "```\ncode\n```\n", "code\n")
	expectFilter(t, "```js\nvar x = 1;\n```\n", "var x = 1;\n")
	expectFilter(t, "```\nline1\nline2\n```\nafter\n", "line1\nline2\nafter\n")
}

func TestBlockquoteStripped(t *testing.T) {
	expectFilter(t, "> quoted\n", "quoted\n")
	expectFilter(t, ">no space\n", "no space\n")
	expectFilter(t, "> line1\n> line2\n", "line1\nline2\n")
}

func TestHeadingsH1toH4Preserved(t *testing.T) {
	expectFilter(t, "# Title\n", "# Title\n")
	expectFilter(t, "## Title\n", "## Title\n")
	expectFilter(t, "### Title\n", "### Title\n")
	expectFilter(t, "#### Title\n", "#### Title\n")
}

func TestHeadingsH5H6Stripped(t *testing.T) {
	expectFilter(t, "##### Title\n", "Title\n")
	expectFilter(t, "###### Title\n", "Title\n")
}

func TestHorizontalRuleRemoved(t *testing.T) {
	expectFilter(t, "---\n", "")
	expectFilter(t, "***\n", "")
	expectFilter(t, "___\n", "")
	expectFilter(t, "- - -\n", "")
	expectFilter(t, "before\n---\nafter\n", "before\nafter\n")
}

func TestTableRowConverted(t *testing.T) {
	expectFilter(t, "| A | B | C |\n", "A\tB\tC\n")
	expectFilter(t, "| --- | --- |\n", "")
	expectFilter(t, "| :---: | ---: |\n", "")
	expectFilter(t, "| Header |\n| --- |\n| Cell |\n", "Header\nCell\n")
}

func TestListPreserved(t *testing.T) {
	expectFilter(t, "  - item 1\n", "  - item 1\n")
	expectFilter(t, "  * item\n", "  * item\n")
}

func TestHoldBack(t *testing.T) {
	f := NewMarkdownFilter()
	out1 := f.Feed("text*")
	if out1 != "text" {
		t.Errorf("holdback: got %q, want %q", out1, "text")
	}
	out2 := f.Feed("more")
	out3 := f.Flush()
	combined := out2 + out3
	if combined != "*more" {
		t.Errorf("holdback continuation: got %q, want %q", combined, "*more")
	}
}

func TestUnclosedInlineRestored(t *testing.T) {
	expectFilter(t, "*unclosed", "*unclosed")
	expectFilter(t, "~~unclosed", "~~unclosed")
	expectFilter(t, "***unclosed", "***unclosed")
}

func TestStripMarkdownConvenience(t *testing.T) {
	got := StripMarkdown("**bold** *italic* `code`")
	want := "**bold** italic code"
	if got != want {
		t.Errorf("StripMarkdown: got %q, want %q", got, want)
	}
}

func TestMixedContent(t *testing.T) {
	input := "# Title\n\n**bold** and *italic*\n\n> quote\n\n```\ncode\n```\n"
	expected := "# Title\n\n**bold** and italic\n\nquote\n\ncode\n"
	expectFilter(t, input, expected)
}

func TestStreamingConsistency(t *testing.T) {
	inputs := []string{
		"**bold** *italic* `code`",
		"# Title\n> quote\n```\nfence\n```\n",
		"before ![alt](url) after",
		"| A | B |\n| --- | --- |\n| 1 | 2 |\n",
		"~~strike~~ ***bold*** _italic_",
	}
	for _, input := range inputs {
		one := oneShot(input)
		char := charByChar(input)
		if one != char {
			t.Errorf("streaming mismatch for %q:\n  oneShot:    %q\n  charByChar: %q", input, one, char)
		}
		for seed := int64(0); seed < 5; seed++ {
			rnd := randomChunks(input, seed)
			if one != rnd {
				t.Errorf("streaming mismatch for %q seed=%d:\n  oneShot: %q\n  random:  %q", input, seed, one, rnd)
			}
		}
	}
}
