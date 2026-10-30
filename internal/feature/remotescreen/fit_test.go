//go:build !board_biscuit

package remotescreen

import (
	"context"
	"testing"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/ygelfand/libcountertop/pkg/display/appearance"
	backend "github.com/ygelfand/libcountertop/pkg/display/gogui"
	"github.com/ygelfand/libcountertop/pkg/settings/schema"
)

const screenW, screenH = 960, 480

// look switches to EchoLocal's theme at a UI size, as the device does, until the test ends.
func look(t *testing.T, size string) {
	t.Helper()
	was := gogui.CurrentTheme()
	t.Cleanup(func() { gogui.SetTheme(was) })
	s := schema.DefaultScreen()
	gogui.SetTheme(appearance.Look(s.Style, s.Theme, size, s.Custom))
}

// app wraps the screen the way EchoLocal's GUI does.
func app(r *Remote) func(*gogui.Window) gogui.View {
	return func(w *gogui.Window) gogui.View {
		return gogui.Column(gogui.ContainerCfg{
			Sizing:  gogui.FillFill,
			Padding: gogui.NoPadding,
			Content: []gogui.View{r.build(w)},
		})
	}
}

// measured is a window that measures text with EchoLocal's own fonts, without drawing.
func measured(t *testing.T, r *Remote) *gogui.Window {
	t.Helper()
	w := gogui.SimpleWindow("remote", screenW, screenH, &struct{}{}, func(*gogui.Window) {})
	if _, err := backend.New(make(frames, 1), w); err != nil {
		t.Fatal(err)
	}
	w.TestRender(app(r))
	return w
}

func shape(t *testing.T, l *gogui.Layout, id string) *gogui.Shape {
	t.Helper()
	var find func(*gogui.Layout) *gogui.Shape
	find = func(l *gogui.Layout) *gogui.Shape {
		if l.Shape != nil && l.Shape.ID == id {
			return l.Shape
		}
		for i := range l.Children {
			if s := find(&l.Children[i]); s != nil {
				return s
			}
		}
		return nil
	}
	s := find(l)
	if s == nil {
		t.Fatalf("nothing on the screen is called %s", id)
	}
	return s
}

func bottom(s *gogui.Shape) float32 { return s.Y + s.Height }

// The example ends with a column holding a picture, called remote-6.
const lastPart = "remote-6"

func TestALongScreenFitsUnderTheTitleAndScrollsToTheEnd(t *testing.T) {
	for _, size := range schema.ScreenSizes() {
		t.Run(size, func(t *testing.T) {
			look(t, size)
			r, _, _ := rig(t)
			showExample(t, r, 0)
			w := measured(t, r)
			l := w.TestRender(nil)

			key, body := shape(t, l, "remote-close"), shape(t, l, "remote-body")
			last := shape(t, l, lastPart)
			if body.Y < bottom(key) {
				t.Errorf("the body starts at %.0f, inside the title bar that ends at %.0f", body.Y, bottom(key))
			}
			if bottom(body) > screenH {
				t.Errorf("the body ends at %.0f, below the %d pixel screen", bottom(body), screenH)
			}
			if bottom(last) <= bottom(body) {
				t.Fatalf("the example fits without scrolling, so this proves nothing")
			}

			if err := w.TestScroll(w.ResolveID("remote-body")[0], 0, -10*screenH); err != nil {
				t.Fatal(err)
			}
			l = w.TestRender(nil)
			body, last = shape(t, l, "remote-body"), shape(t, l, lastPart)
			// A part taller than the body may have its top scrolled away; its end must show.
			if bottom(last) <= body.Y || bottom(last) > bottom(body)+1 {
				t.Errorf("scrolled to the end, the last part ends at %.0f, outside the body's %.0f to %.0f",
					bottom(last), body.Y, bottom(body))
			}
		})
	}
}

// A finger drag through EchoLocal's renderer reaches the end too.
func TestAFingerScrollsToTheEnd(t *testing.T) {
	look(t, schema.UISizeMini)
	r, _, _ := rig(t)
	showExample(t, r, 0)
	f := make(frames, 256)
	w := gogui.SimpleWindow("remote", screenW, screenH, &struct{}{}, func(w *gogui.Window) {
		w.SetView(app(r))
	})
	rend, err := backend.New(f, w)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = rend.Run(ctx, w) }()
	<-f
	for flick := range 3 {
		x := float32(300 + 100*flick)
		rend.Touch(backend.Began, 1, x, 440)
		for y := float32(440); y >= 100; y -= 10 {
			time.Sleep(5 * time.Millisecond)
			rend.Touch(backend.Moved, 1, x, y)
		}
		rend.Touch(backend.Ended, 1, x, 100)
		time.Sleep(300 * time.Millisecond)
	}
	cancel()
	<-done

	l := w.TestRender(nil)
	body, last := shape(t, l, "remote-body"), shape(t, l, lastPart)
	if bottom(last) > bottom(body)+1 {
		t.Errorf("after dragging up, the last part still ends at %.0f, below the body's %.0f",
			bottom(last), bottom(body))
	}
}

func TestRowsPackFromTheLeftAndHugeTextIsHuge(t *testing.T) {
	look(t, schema.UISizeMini)
	r, _, _ := rig(t)
	showExample(t, r, 0)
	l := measured(t, r).TestRender(nil)

	huge, beside := shape(t, l, "remote-0-0"), shape(t, l, "remote-0-1")
	large := shape(t, l, "remote-0-1-0")
	if gap := beside.X - (huge.X + huge.Width); gap < 0 || gap > 40 || beside.X > screenW/3 {
		t.Errorf("the column next to the huge text starts at %.0f, %.0f pixels after it, "+
			"want it close by on the left", beside.X, gap)
	}
	if huge.Height < 1.8*large.Height {
		t.Errorf("huge text is %.0f tall and large text %.0f, want huge clearly bigger",
			huge.Height, large.Height)
	}
	first, second := shape(t, l, "remote-1-0"), shape(t, l, "remote-1-1")
	if gap := second.X - (first.X + first.Width); gap < 0 || gap > 40 {
		t.Errorf("the buttons are %.0f pixels apart, want a normal gap", gap)
	}
}

// Without a title there is no title bar: the body starts at the top, beside the close key.
func TestWithoutATitleTheBodyStartsAtTheTop(t *testing.T) {
	look(t, schema.UISizeMini)
	r, _, _ := rig(t)
	raw := `{"body": [{"type": "text", "text": "No title here"},
		{"type": "button", "id": "go", "label": "Go"}]}`
	if err := r.Show(raw, 0); err != nil {
		t.Fatal(err)
	}
	l := measured(t, r).TestRender(nil)

	key, body := shape(t, l, "remote-close"), shape(t, l, "remote-body")
	if body.Y >= bottom(key) {
		t.Errorf("the body starts at %.0f, under a title bar that ends at %.0f", body.Y, bottom(key))
	}
	if body.X+body.Width > key.X {
		t.Errorf("the body runs to %.0f, under the close key that starts at %.0f", body.X+body.Width, key.X)
	}
	if key.X+key.Width < screenW-80 {
		t.Errorf("the close key ends at %.0f, not in the top right corner", key.X+key.Width)
	}
}
