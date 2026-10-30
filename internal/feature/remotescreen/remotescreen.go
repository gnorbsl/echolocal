//go:build !board_biscuit

// Package remotescreen shows a screen that Home Assistant describes in JSON, with buttons,
// switches, sliders and lists, and tells Home Assistant when they are tapped.
package remotescreen

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/ygelfand/libcountertop/pkg/display/navigation"
	sharedshell "github.com/ygelfand/libcountertop/pkg/display/shell"
	"github.com/ygelfand/libcountertop/pkg/display/widgets"
	"github.com/ygelfand/libcountertop/pkg/input/touch"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/feature/shell"
	hwtouch "github.com/ygelfand/echolocal/internal/hardware/touch"
)

const (
	mostSeconds = 24 * 60 * 60

	// How long to wait after a finger lifts before sending slider values, so the
	// last moves have reached the slider.
	liftSettle = 150 * time.Millisecond
)

func init() {
	component.Register(
		sharedcomponent.Device,
		Get,
		sharedcomponent.Order(41),
		component.Needs(board.Panel),
	)
}

// View is the screen's place among the open screens.
type View struct{}

func (*View) Covers() bool { return true }

// Wakes lets the screen open over the screen saver.
func (*View) Wakes() bool { return true }

type Options struct {
	Shell  *sharedshell.Shell
	Fire   func(sharedcomponent.Event)
	Client *http.Client
}

type Remote struct {
	o    Options
	view *View

	mu       sync.Mutex
	look     *widgets.UI
	layout   *Layout
	hold     *sharedshell.Hold
	timer    *time.Timer
	shown    uint64
	moved    map[*Node]bool
	pictures map[string]*picture
	loaded   uint64
}

var Get = sync.OnceValue(func() *Remote {
	r := New(Options{
		Shell:  shell.Get(),
		Fire:   component.Fire.Emit,
		Client: &http.Client{Timeout: fetchTimeout},
	})
	hwtouch.Get().Contacts.Listen(r.touched)
	return r
})

func New(o Options) *Remote {
	r := &Remote{
		o:        o,
		view:     &View{},
		moved:    map[*Node]bool{},
		pictures: map[string]*picture{},
	}
	o.Shell.Changed.Listen(func(sharedshell.Change) { r.stillOpen() })
	return r
}

func (r *Remote) Name() string { return "remotescreen" }

// Use hands over the GUI's look, so the screen draws like the rest of EchoLocal.
func (r *Remote) Use(look *widgets.UI) {
	r.mu.Lock()
	r.look = look
	r.mu.Unlock()
}

func (r *Remote) Actions() []*esphome.Action {
	return []*esphome.Action{
		{
			Name: "show_screen",
			Args: []esphome.Arg{
				{Name: "layout", Type: esphome.ArgString},
				{Name: "seconds", Type: esphome.ArgInt},
			},
			Run: func(c esphome.Call) (any, error) {
				return nil, r.Show(c.String("layout"), c.Int("seconds"))
			},
		},
		{
			Name: "clear_screen",
			Run:  func(esphome.Call) (any, error) { r.Clear(); return nil, nil },
		},
	}
}

// Show opens the screen, or swaps what an open one shows without closing it.
// Seconds of 0 or less keeps it up until it is cleared or closed.
func (r *Remote) Show(raw string, seconds int) error {
	l, err := Parse(raw)
	if err != nil {
		return err
	}
	for _, why := range l.Skipped {
		slog.Warn("screen: a part was left out", "why", why)
	}

	r.mu.Lock()
	r.shown++
	shown := r.shown
	r.layout = l
	clear(r.moved)
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	if seconds > 0 {
		after := time.Duration(min(seconds, mostSeconds)) * time.Second
		r.timer = time.AfterFunc(after, func() { r.close(ClosedByTimeout, shown) })
	}
	open := r.hold.Held()
	if !open {
		r.hold = nil
	}
	r.mu.Unlock()

	r.fetch(l, shown)
	slog.Info("screen shown", "title", l.Title, "seconds", seconds, "replaced", open)
	if open {
		r.o.Shell.Redraw()
		return nil
	}
	hold := r.o.Shell.Hold(r.view)
	if hold == nil {
		r.close("", shown)
		return errors.New("show_screen: the display would not open the screen")
	}
	r.mu.Lock()
	if r.layout != nil {
		r.hold, hold = hold, nil
	}
	r.mu.Unlock()
	// Cleared while it was opening, so let go of it again.
	hold.Release()
	return nil
}

func (r *Remote) Clear() { r.close("", 0) }

// close takes the screen down and says why, unless why is empty. With a nonzero shown it
// only closes that very screen, so a late timer cannot close a newer one.
func (r *Remote) close(why string, shown uint64) {
	r.mu.Lock()
	if r.layout == nil || (shown != 0 && shown != r.shown) {
		r.mu.Unlock()
		return
	}
	screen := r.layout.ID
	hold := r.hold
	r.layout, r.hold = nil, nil
	r.shown++
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	clear(r.moved)
	keys := r.forget(nil)
	r.mu.Unlock()

	hold.Release()
	for _, k := range keys {
		gogui.DropImage(k)
	}
	if why != "" {
		slog.Info("screen closed", "why", why)
		r.o.Fire(closed(why).event(screen))
	}
}

// stillOpen notices when something else closed the screen, such as a swipe back.
func (r *Remote) stillOpen() {
	r.mu.Lock()
	gone := r.hold != nil && !r.hold.Held()
	shown := r.shown
	r.mu.Unlock()
	if gone {
		r.close(ClosedElsewhere, shown)
	}
}

// act runs a tap's change on the screen and tells Home Assistant about it.
func (r *Remote) act(change func() tap) {
	r.mu.Lock()
	if r.layout == nil {
		r.mu.Unlock()
		return
	}
	t := change()
	screen := r.layout.ID
	r.mu.Unlock()
	r.o.Fire(t.event(screen))
}

func (r *Remote) slid(n *Node, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slide(n, v) {
		r.moved[n] = true
	}
}

func (r *Remote) touched(c touch.Contact) {
	if c.Phase == touch.Up {
		time.AfterFunc(liftSettle, r.settle)
	}
}

// settle sends the sliders that moved, once the finger is off the screen.
func (r *Remote) settle() {
	r.mu.Lock()
	if len(r.moved) == 0 || r.layout == nil {
		clear(r.moved)
		r.mu.Unlock()
		return
	}
	screen := r.layout.ID
	taps := make([]tap, 0, len(r.moved))
	for n := range r.moved {
		taps = append(taps, settled(n))
	}
	clear(r.moved)
	r.mu.Unlock()
	for _, t := range taps {
		r.o.Fire(t.event(screen))
	}
}

func (r *Remote) Screen(v sharedshell.View) *navigation.Screen {
	if v != r.view {
		return nil
	}
	return &navigation.Screen{View: v, Build: r.build}
}
