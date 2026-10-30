package course_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/progress"
)

// TestSchemasDocumented keeps MODULE_TEMPLATE.md in sync with the Go types: every
// JSON field of course.json, module.json, quiz.json and progress.json must be named
// in the template, so a UI author can rely on the doc alone.
func TestSchemasDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../MODULE_TEMPLATE.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	for _, v := range []any{course.Course{}, course.Module{}, course.Quiz{}, progress.Progress{}} {
		for _, tag := range jsonTags(reflect.TypeOf(v), map[reflect.Type]bool{}) {
			if !strings.Contains(text, "`"+tag+"`") && !strings.Contains(text, `"`+tag+`"`) {
				t.Errorf("%T: JSON field %q is not documented in MODULE_TEMPLATE.md", v, tag)
			}
		}
	}
}

func jsonTags(t reflect.Type, seen map[reflect.Type]bool) []string {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] || t.PkgPath() == "time" {
		return nil
	}
	seen[t] = true
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" || !f.IsExported() {
			continue
		}
		if name != "" {
			out = append(out, name)
		}
		out = append(out, jsonTags(f.Type, seen)...)
	}
	return out
}
