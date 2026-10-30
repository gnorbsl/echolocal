//go:build !board_biscuit

package remotescreen

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"

	backend "github.com/ygelfand/libcountertop/pkg/display/gogui"
	sharedshell "github.com/ygelfand/libcountertop/pkg/display/shell"
	"github.com/ygelfand/libcountertop/pkg/display/widgets"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	"github.com/ygelfand/libcountertop/pkg/settings/schema"
)

type screenSection struct{ s schema.Screen }

func (c *screenSection) Read() schema.Screen { return c.s }

func (c *screenSection) Update(change func(*schema.Screen)) error {
	change(&c.s)
	return nil
}

type fired struct {
	mu     sync.Mutex
	events []sharedcomponent.Event
}

func (f *fired) add(e sharedcomponent.Event) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

func (f *fired) all() []sharedcomponent.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sharedcomponent.Event(nil), f.events...)
}

func rig(t *testing.T) (*Remote, *sharedshell.Shell, *fired) {
	t.Helper()
	sh := sharedshell.New()
	f := &fired{}
	r := New(Options{Shell: sh, Fire: f.add, Client: &http.Client{Timeout: time.Second}})
	look := widgets.NewUI(&screenSection{s: schema.DefaultScreen()})
	r.Use(look)
	return r, sh, f
}

// pictures serves a PNG so the image part has something to download.
func pictures(t *testing.T) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2000, 1000))
	for x := range 2000 {
		img.Set(x, 500, color.NRGBA{R: 255, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(s.Close)
	return s.URL + "/front.png"
}

func showExample(t *testing.T, r *Remote, seconds int) {
	t.Helper()
	raw := strings.Replace(
		example(t),
		"http://homeassistant.local:8123/local/jarvis/front.jpg",
		pictures(t),
		1,
	)
	if err := r.Show(raw, seconds); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 100 {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("gave up waiting for %s", what)
}

func window(t *testing.T, r *Remote) *gogui.Window {
	t.Helper()
	w := gogui.NewTestWindow(t, gogui.WindowCfg{Width: 960, Height: 480})
	w.TestRender(r.build)
	return w
}

// click taps a part the way a finger does and waits for EchoLocal's press to go through.
func click(t *testing.T, w *gogui.Window, f *fired, leaf string) sharedcomponent.Event {
	t.Helper()
	ids := w.ResolveID(leaf)
	if len(ids) == 0 {
		t.Fatalf("nothing on the screen is called %s", leaf)
	}
	before := len(f.all())
	if err := w.TestClick(ids[0]); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the tap on "+leaf, func() bool {
		w.FrameFn()
		return len(f.all()) > before
	})
	return f.all()[before]
}

func TestTapsOnTheScreenReachHomeAssistant(t *testing.T) {
	r, _, f := rig(t)
	showExample(t, r, 0)
	w := window(t, r)

	for _, tc := range []struct{ leaf, want string }{
		{"remote-1-0", "type=button id=movie value=pressed"},
		{"remote-2", "type=switch id=kitchen_light value=off"},
		{"remote-4-0", "type=checklist id=shopping item=milk value=on"},
	} {
		e := click(t, w, f, tc.leaf)
		for _, pair := range strings.Fields(tc.want) {
			k, v, _ := strings.Cut(pair, "=")
			if e.Data[k] != v {
				t.Errorf("%s: %s is %q, want %q (%v)", tc.leaf, k, e.Data[k], v, e.Data)
			}
		}
		if e.Data["screen"] != "evening" {
			t.Errorf("%s: screen is %q", tc.leaf, e.Data["screen"])
		}
	}
	r.mu.Lock()
	on, done := r.layout.Body[2].On, r.layout.Body[4].Items[0].Done
	r.mu.Unlock()
	if on || !done {
		t.Errorf("the screen did not keep the taps: switch %t, milk %t", on, done)
	}
}

func TestSlidersSpeakWhenLetGo(t *testing.T) {
	r, _, f := rig(t)
	showExample(t, r, 0)
	r.mu.Lock()
	level := r.layout.Body[3]
	r.mu.Unlock()
	r.slid(level, 51)
	r.slid(level, 72)
	if n := len(f.all()); n != 0 {
		t.Fatalf("%d events while the finger was still down", n)
	}
	r.settle()
	got := f.all()
	if len(got) != 1 || got[0].Data["value"] != "70" || got[0].Data["id"] != "living_brightness" {
		t.Errorf("events %v, want one with 70", got)
	}
	r.settle()
	if len(f.all()) != 1 {
		t.Error("letting go again sent the value twice")
	}
}

func TestCloseButtonTimeoutAndClear(t *testing.T) {
	r, sh, f := rig(t)
	showExample(t, r, 0)
	if !sh.Visible(r.view) {
		t.Fatal("the screen is not showing")
	}
	w := window(t, r)
	e := click(t, w, f, "remote-close")
	if e.Data["type"] != "closed" || e.Data["value"] != "close_button" {
		t.Errorf("close gave %v", e.Data)
	}
	if sh.Open() {
		t.Error("the screen is still open after the close key")
	}

	showExample(t, r, 1)
	eventually(t, "the timeout", func() bool { return !sh.Open() })
	if last := f.all()[len(f.all())-1]; last.Data["value"] != "timeout" {
		t.Errorf("timeout gave %v", last.Data)
	}

	showExample(t, r, 0)
	before := len(f.all())
	r.Clear()
	if sh.Open() || len(f.all()) != before {
		t.Error("clear_screen left the screen up or sent an event")
	}

	showExample(t, r, 0)
	sh.Pop()
	eventually(t, "the swipe back", func() bool { return len(f.all()) > before })
	if last := f.all()[len(f.all())-1]; last.Data["value"] != "dismissed" {
		t.Errorf("swiping back gave %v", last.Data)
	}
}

func TestShowingAgainSwapsInPlace(t *testing.T) {
	r, sh, f := rig(t)
	showExample(t, r, 0)
	changes := 0
	sh.Changed.Listen(func(sharedshell.Change) { changes++ })
	if err := r.Show(`{"title": "Next", "body": [{"type": "text", "text": "hi"}]}`, 0); err != nil {
		t.Fatal(err)
	}
	if changes != 0 || !sh.Visible(r.view) || len(f.all()) != 0 {
		t.Errorf("swapping closed and reopened the screen (%d changes, %d events)", changes, len(f.all()))
	}
	if err := r.Show(`{"body": [`, 0); err == nil {
		t.Error("bad JSON was accepted")
	}
	r.mu.Lock()
	title := r.layout.Title
	r.mu.Unlock()
	if title != "Next" {
		t.Errorf("a failed show changed the screen to %q", title)
	}
}

func TestPicturesDownloadAndShrink(t *testing.T) {
	r, _, _ := rig(t)
	showExample(t, r, 0)
	var p *picture
	eventually(t, "the picture", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, got := range r.pictures {
			p = got
		}
		return p != nil
	})
	if b := p.img.Bounds(); b.Dx() != mostSide || b.Dy() != mostSide/2 {
		t.Errorf("picture is %v, want it shrunk to %dx%d", b, mostSide, mostSide/2)
	}
	window(t, r)
}

type frames chan int

func (frames) Program(int, string, string) error                          { return nil }
func (frames) Texture(uint32, int, int, int, int, int, int, []byte) error { return nil }
func (f frames) Frame(_ int, _ [4]float32, quads []float32, _ []uint32) (uint32, uint32, error) {
	select {
	case f <- len(quads) / 36:
	default:
	}
	return 0, 0, nil
}

// EchoLocal's own renderer draws the screen, minus the GPU.
func TestTheRendererDrawsIt(t *testing.T) {
	r, _, _ := rig(t)
	showExample(t, r, 0)
	f := make(frames, 64)
	w := gogui.SimpleWindow("remote", 960, 480, &struct{}{}, func(w *gogui.Window) {
		w.SetView(r.build)
	})
	rend, err := backend.New(f, w)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() { defer close(done); _ = rend.Run(ctx, w) }()
	most := 0
	deadline := time.After(3 * time.Second)
	for most < 100 {
		select {
		case n := <-f:
			most = max(most, n)
		case <-deadline:
			t.Fatalf("frames had at most %d quads, want text and controls", most)
		}
	}
}
