package lesson

import (
	"strings"
	"testing"
)

func TestPrepareWrapsPredictBlocks(t *testing.T) {
	src := "# U\n\n<!-- predict id=\"A0-U1-P01\" -->\n**Predict.** Why?\n<!-- /predict -->\n\n" +
		"<details data-kind=\"reveal\" data-for=\"A0-U1-P01\">\n<summary>Reveal</summary>\n\nBecause.\n\n</details>\n"
	out, ids := Prepare(src)
	if len(ids) != 1 || ids[0] != "A0-U1-P01" {
		t.Fatalf("ids = %v, want [A0-U1-P01]", ids)
	}
	want := "<div class=\"predict\" data-predict-id=\"A0-U1-P01\">\n\n**Predict.** Why?\n\n</div>"
	if !strings.Contains(out, want) {
		t.Fatalf("predict block not wrapped with blank lines (Markdown inside would not render):\n%s", out)
	}
	if !strings.Contains(out, `<details data-kind="reveal" data-for="A0-U1-P01">`) {
		t.Fatalf("reveal block must be left as is:\n%s", out)
	}
}

func TestPrepareIgnoresMarkersInCodeFences(t *testing.T) {
	src := "```markdown\n<!-- predict id=\"X-U1-P01\" -->\nexample\n<!-- /predict -->\n```\n\n" +
		"~~~\n<!-- predict id=\"X-U1-P02\" -->\n~~~\n"
	out, ids := Prepare(src)
	if len(ids) != 0 || out != src {
		t.Fatalf("markers inside fences must not be rewritten; ids=%v\n%s", ids, out)
	}
}

func TestPrepareClosesUnterminatedBlock(t *testing.T) {
	out, ids := Prepare("<!-- predict id=\"X-U1-P01\" -->\nq")
	if len(ids) != 1 || !strings.HasSuffix(out, "</div>") {
		t.Fatalf("unterminated block should be closed so the page layout survives: %q", out)
	}
}
