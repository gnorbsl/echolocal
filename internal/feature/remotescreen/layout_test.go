//go:build !board_biscuit

package remotescreen

import (
	"os"
	"strings"
	"testing"
)

func example(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/example.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func types(ns []*Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Type
	}
	return out
}

func TestTheExampleParses(t *testing.T) {
	l, err := Parse(example(t))
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "evening" || l.Title != "Good evening" {
		t.Errorf("id %q title %q", l.ID, l.Title)
	}
	want := []string{
		TypeRow, TypeRow, TypeSwitch, TypeSlider, TypeChecklist, TypeProgress, TypeColumn,
	}
	if got := types(l.Body); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("body is %v, want %v", got, want)
	}
	head := l.Body[0]
	if got := types(head.Items); strings.Join(got, " ") != "text column" {
		t.Errorf("first row holds %v", got)
	}
	if inner := head.Items[1].Items; len(inner) != 2 || inner[1].Size != "small" {
		t.Errorf("nested column is %+v", inner)
	}
	if b := l.Body[1].Items[0]; b.ID != "movie" || b.Style != "primary" {
		t.Errorf("first button is %+v", b)
	}
	if s := l.Body[2]; !s.On || s.Label != "Kitchen light" {
		t.Errorf("switch is %+v", s)
	}
	if s := l.Body[3]; s.Value != 40 || *s.Min != 0 || *s.Max != 100 || s.Step != 5 {
		t.Errorf("slider is %+v", s)
	}
	if c := l.Body[4]; len(c.Items) != 2 || c.Items[0].ID != "milk" || !c.Items[1].Done {
		t.Errorf("checklist is %+v", c)
	}
	if p := l.Body[5]; p.Value != 0.65 {
		t.Errorf("progress is %+v", p)
	}
	if img := l.Body[6].Items[1]; img.Type != TypeImage || img.URL == "" {
		t.Errorf("image is %+v", img)
	}
	if len(l.Skipped) != 1 || !strings.Contains(l.Skipped[0], `"markdown"`) {
		t.Errorf("skipped %v, want the markdown part", l.Skipped)
	}
}

func TestUnknownPartsAreLeftOutNotFatal(t *testing.T) {
	l, err := Parse(`{"title": "x", "body": [
		{"type": "hologram", "id": "a"},
		{"text": "no type"},
		{"type": "text", "text": "kept"},
		{"type": "row", "items": [{"type": "sparkles"}, {"type": "text", "text": "also kept"}]}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(types(l.Body), " "); got != "text row" {
		t.Errorf("body is %s", got)
	}
	if len(l.Body[1].Items) != 1 {
		t.Errorf("row kept %d items, want 1", len(l.Body[1].Items))
	}
	if len(l.Skipped) != 3 {
		t.Errorf("skipped %v, want 3", l.Skipped)
	}
}

func TestBadLayoutsFailWithAReason(t *testing.T) {
	for name, tc := range map[string]struct{ raw, says string }{
		"empty":       {"  ", "empty"},
		"broken json": {`{"title": "x", "body": [`, "not valid JSON"},
		"wrong shape": {`{"body": {"type": "text"}}`, "not valid JSON"},
		"button without id": {
			`{"body": [{"type": "button", "label": "Go"}]}`,
			"body[0] (button) needs an id",
		},
		"nested switch without id": {
			`{"body": [{"type": "row", "items": [{"type": "switch"}]}]}`,
			"body[0].items[0] (switch) needs an id",
		},
		"checklist item without id": {
			`{"body": [{"type": "checklist", "id": "c", "items": [{"label": "Milk"}]}]}`,
			"items[0] needs an id",
		},
		"slider upside down": {
			`{"body": [{"type": "slider", "id": "s", "min": 10, "max": 5}]}`,
			"max above min",
		},
		"image without web address": {
			`{"body": [{"type": "image", "url": "file:///etc/passwd"}]}`,
			"http or https",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error %v, want one saying %q", err, tc.says)
			}
		})
	}
}

func TestTooDeepOrTooBigFails(t *testing.T) {
	deep := strings.Repeat(`{"type": "row", "items": [`, maxDepth+1) +
		strings.Repeat(`]}`, maxDepth+1)
	if _, err := Parse(`{"body": [` + deep + `]}`); err == nil {
		t.Error("a very deep layout parsed")
	}
	big := strings.Repeat(`{"type": "text", "text": "x"},`, maxNodes) + `{"type": "text", "text": "x"}`
	if _, err := Parse(`{"body": [` + big + `]}`); err == nil {
		t.Error("a very big layout parsed")
	}
}

func TestSliderValuesStayOnTheirSteps(t *testing.T) {
	l, err := Parse(`{"body": [
		{"type": "slider", "id": "t", "min": 16, "max": 24, "step": 0.5, "value": 30}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	s := l.Body[0]
	if s.Value != 24 {
		t.Errorf("value %v, want it held at max 24", s.Value)
	}
	for in, want := range map[float64]float64{20.2: 20, 20.3: 20.5, 3: 16, 99: 24} {
		if got := snap(s, in); got != want {
			t.Errorf("snap(%v) = %v, want %v", in, got, want)
		}
	}
	plain, _ := Parse(`{"body": [{"type": "slider", "id": "p"}]}`)
	if p := plain.Body[0]; *p.Min != 0 || *p.Max != 100 || p.Step != 1 || p.Label != "p" {
		t.Errorf("slider without settings is %+v", p)
	}
}
