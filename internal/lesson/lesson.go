// Package lesson prepares lesson Markdown for a browser. Lessons mark predict blocks
// with HTML comments (MODULE_TEMPLATE.md §3); a Markdown renderer drops comments, so
// Prepare turns each block into a <div> that a front end can find and decorate with an
// answer box. Everything else, including the <details data-kind="reveal"> blocks, is
// left untouched.
package lesson

import (
	"regexp"
	"strings"
)

var (
	openRe  = regexp.MustCompile(`^\s*<!--\s*predict\s+id="([^"]+)"\s*-->\s*$`)
	closeRe = regexp.MustCompile(`^\s*<!--\s*/predict\s*-->\s*$`)
	fenceRe = regexp.MustCompile("^\\s{0,3}(```+|~~~+)")
)

// Prepare rewrites `<!-- predict id="X" -->` … `<!-- /predict -->` into
// `<div class="predict" data-predict-id="X">` … `</div>`, with blank lines around the
// body so it still renders as Markdown. Markers inside fenced code blocks (examples of
// the markup itself) are left alone. It returns the rewritten source and the predict
// ids in order.
func Prepare(src string) (string, []string) {
	lines := strings.Split(src, "\n")
	var ids []string
	fence := ""
	open := false
	for i, ln := range lines {
		if m := fenceRe.FindStringSubmatch(ln); m != nil {
			marker := m[1][:3]
			switch {
			case fence == "":
				fence = marker
			case marker == fence:
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if m := openRe.FindStringSubmatch(ln); m != nil && !open {
			ids = append(ids, m[1])
			lines[i] = `<div class="predict" data-predict-id="` + escapeAttr(m[1]) + `">` + "\n"
			open = true
		} else if closeRe.MatchString(ln) && open {
			lines[i] = "\n</div>"
			open = false
		}
	}
	if open {
		lines = append(lines, "\n</div>")
	}
	return strings.Join(lines, "\n"), ids
}

func escapeAttr(s string) string {
	return strings.NewReplacer(`&`, "&amp;", `"`, "&quot;", `<`, "&lt;", `>`, "&gt;").Replace(s)
}
