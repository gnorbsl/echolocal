//go:build !board_biscuit

package remotescreen

import (
	"fmt"
	"image"
	"math"
	"slices"

	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/ygelfand/libcountertop/pkg/display/menu"
	"github.com/ygelfand/libcountertop/pkg/display/style"
	"github.com/ygelfand/libcountertop/pkg/display/widgets"
)

const (
	page = "remote"

	checked   = "mdi:checkbox-marked"
	unchecked = "mdi:checkbox-blank-outline"

	hugeScale = 2.2
)

func (r *Remote) build(w *gogui.Window) gogui.View {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.layout == nil || r.look == nil {
		return gogui.Column(gogui.ContainerCfg{Sizing: gogui.FillFill})
	}
	vw, vh := w.WindowSize()
	d := drawer{r: r, look: r.look, w: w, vw: vw, vh: vh}
	return d.frame(r.layout)
}

// drawer turns the layout into views. It runs with the lock held; the taps it wires up
// run later, without it.
type drawer struct {
	r      *Remote
	look   *widgets.UI
	w      *gogui.Window
	vw, vh int
}

// frame is laid out like EchoLocal's own pages, with the close key where they have back.
func (d drawer) frame(l *Layout) gogui.View {
	t := gogui.CurrentTheme()
	key := t.TextStyleIconMedium
	key.Color = t.TextStyleDisplay.Color
	// A little bigger than the back key on EchoLocal's pages, as it is the only way out.
	key.Size = t.TextStyleDisplay.Size * 0.7
	closer := d.look.Toolkit.KeyButton(
		"remote-close",
		gogui.IconClose,
		key,
		style.Rest,
		func(e gogui.EventCtx) {
			d.r.close(ClosedByButton, 0)
			e.Window.InvalidateLayout()
		},
	)
	body := gogui.Column(gogui.ContainerCfg{
		ID:         "remote-body",
		Sizing:     gogui.FillFill,
		Spacing:    gogui.SpacingSmall,
		Scrollable: true,
		OnGesture:  d.look.HoldStill,
		Content:    d.nodes(l.Body, "remote"),
	})
	var content []gogui.View
	if l.Title == "" {
		// No title bar: the body gets the whole height, with the close key beside it.
		content = []gogui.View{gogui.Row(gogui.ContainerCfg{
			Sizing:  gogui.FillFill,
			Padding: gogui.NoPadding,
			Spacing: gogui.SpacingMedium,
			Content: []gogui.View{body, closer},
		})}
	} else {
		title := widgets.Ellipsize(d.w, l.Title, t.TextStyleDisplay, float32(d.vw)*0.8)
		content = []gogui.View{
			gogui.Row(gogui.ContainerCfg{
				Sizing:  gogui.FillFit,
				Padding: gogui.NoPadding,
				VAlign:  gogui.VAlignMiddle,
				Spacing: gogui.SpacingMedium,
				Content: []gogui.View{
					gogui.Label(title, t.TextStyleDisplay),
					gogui.Row(gogui.ContainerCfg{Sizing: gogui.FillFit, Padding: gogui.NoPadding}),
					closer,
				},
			}),
			body,
		}
	}
	return gogui.Column(gogui.ContainerCfg{
		ID:     "remote-screen",
		Sizing: gogui.FillFill,
		// Less room above and below than EchoLocal's pages, as 480 pixels is not much height.
		Padding: gogui.NewPadding(gogui.PadMedium, gogui.PadLarge, gogui.PadMedium, gogui.PadLarge),
		Spacing: gogui.SpacingSmall,
		Content: content,
	})
}

// nodes draws parts stacked on top of each other, each as wide as the space.
func (d drawer) nodes(ns []*Node, path string) []gogui.View {
	out := make([]gogui.View, 0, len(ns))
	for i, n := range ns {
		if v := d.node(n, fmt.Sprintf("%s-%d", path, i), true); v != nil {
			out = append(out, v)
		}
	}
	return out
}

// row puts parts next to each other from the left, each only as wide as it needs.
// Controls such as switches and sliders take the width that is left. When there are none,
// the last text or column takes it instead, so long text can wrap there.
func (d drawer) row(n *Node, id string, wide bool) gogui.View {
	stretch := -1
	if !slices.ContainsFunc(n.Items, grows) {
		for i, c := range n.Items {
			if c.Type == TypeText || c.Type == TypeColumn || c.Type == TypeRow {
				stretch = i
			}
		}
	}
	out := make([]gogui.View, 0, len(n.Items))
	for i, c := range n.Items {
		v := d.node(c, fmt.Sprintf("%s-%d", id, i), grows(c) || i == stretch)
		if v != nil {
			out = append(out, v)
		}
	}
	return gogui.Row(gogui.ContainerCfg{
		ID:      id,
		Sizing:  sizing(wide),
		Padding: gogui.NoPadding,
		Spacing: gogui.SpacingMedium,
		VAlign:  gogui.VAlignMiddle,
		Content: out,
	})
}

// grows says whether a part needs all the width it can get to work, like a slider.
func grows(n *Node) bool {
	switch n.Type {
	case TypeSwitch, TypeSlider, TypeChecklist, TypeProgress:
		return true
	case TypeRow, TypeColumn:
		return slices.ContainsFunc(n.Items, grows)
	}
	return false
}

func sizing(wide bool) gogui.Sizing {
	if wide {
		return gogui.FillFit
	}
	return gogui.FitFit
}

// node draws one part. Wide says whether text, rows and columns take all the width they
// are given or only what they need; the other parts always size themselves.
func (d drawer) node(n *Node, id string, wide bool) gogui.View {
	switch n.Type {
	case TypeText:
		return d.text(n, id, wide)
	case TypeButton:
		return d.button(n, id)
	case TypeSwitch:
		return d.toggle(n, id)
	case TypeSlider:
		return d.slider(n, id)
	case TypeChecklist:
		return d.checklist(n, id)
	case TypeProgress:
		return d.progress(n, id)
	case TypeImage:
		return d.picture(n, id)
	case TypeRow:
		return d.row(n, id, wide)
	case TypeColumn:
		return gogui.Column(gogui.ContainerCfg{
			ID:      id,
			Sizing:  sizing(wide),
			Padding: gogui.NoPadding,
			Spacing: gogui.SpacingSmall,
			Content: d.nodes(n.Items, id),
		})
	}
	return nil
}

func (d drawer) text(n *Node, id string, wide bool) gogui.View {
	t := gogui.CurrentTheme()
	st := t.Cfg.TextStyleDef
	switch n.Size {
	case "small":
		st = widgets.Secondary()
		st.Size *= 0.8
	case "large":
		st = t.TextStyleTitle
	case "huge":
		// About twice the page titles, for a number you read from across the room.
		st = t.TextStyleDisplay
		st.Size *= hugeScale
	}
	if n.Bold {
		st = st.Bold()
	}
	return gogui.Text(gogui.TextCfg{
		ID:        id,
		Text:      n.Text,
		TextStyle: st,
		Mode:      gogui.TextModeWrap,
		Sizing:    sizing(wide),
	})
}

func (d drawer) button(n *Node, id string) gogui.View {
	t := gogui.CurrentTheme()
	st := t.TextStyleTitle
	// As tall as a settings row, so it is easy to hit.
	tall := style.Reach() * 1.4
	state := style.Rest
	if n.Style == "primary" {
		state = style.Chosen
	}
	return d.look.Toolkit.Keyed(
		gogui.ContainerCfg{
			ID:     id,
			Width:  widgets.KeyWidth(d.w, n.Label, st, tall) + tall*0.5,
			Height: tall,
			Sizing: gogui.FixedFixed,
			Radius: gogui.RadiusLarge,
			Color:  t.Cfg.ColorPanel,
		},
		n.Label,
		st,
		state,
		func(gogui.EventCtx) { d.r.act(func() tap { return press(n) }) },
	)
}

func (d drawer) toggle(n *Node, id string) gogui.View {
	row := menu.Row{Label: n.Label, Kind: menu.Toggle, On: n.On}
	return d.look.Row(id, page, row, func(int) { d.r.act(func() tap { return flip(n) }) }, false)
}

func (d drawer) slider(n *Node, id string) gogui.View {
	level := gogui.SliderCfg{
		ID:     id + "-slider",
		Value:  float32(n.Value),
		Min:    float32(*n.Min),
		Max:    float32(*n.Max),
		Sizing: gogui.FillFit,
		Height: style.Reach(),
		OnChange: func(v float32, e gogui.EventCtx) {
			d.r.slid(n, float64(v))
			e.Window.InvalidateLayout()
		},
	}
	cfg := gogui.ContainerCfg{
		ID:      id,
		Sizing:  gogui.FillFit,
		VAlign:  gogui.VAlignMiddle,
		Padding: gogui.PaddingMedium,
		Spacing: gogui.SpacingMedium,
		Radius:  gogui.RadiusMedium,
		Content: []gogui.View{
			gogui.Column(gogui.ContainerCfg{
				Sizing:  gogui.FillFit,
				Padding: gogui.NoPadding,
				Content: []gogui.View{gogui.Label(n.Label, gogui.TextStyle{})},
			}),
			gogui.Label(number(n.Value), widgets.Secondary()),
			d.look.Grip(d.look.Toolkit.Slider(level)),
		},
	}
	d.look.Toolkit.ListRow(&cfg, style.Rest)
	return gogui.Row(cfg)
}

func (d drawer) checklist(n *Node, id string) gogui.View {
	var rows []gogui.View
	if n.Label != "" {
		rows = append(rows, gogui.Label(n.Label, gogui.CurrentTheme().TextStyleTitle))
	}
	for i, item := range n.Items {
		glyph := unchecked
		if item.Done {
			glyph = checked
		}
		row := menu.Row{Glyph: glyph, Label: item.Label}
		rows = append(rows, d.look.Row(
			fmt.Sprintf("%s-%d", id, i),
			page,
			row,
			func(int) { d.r.act(func() tap { return tick(n, item) }) },
			true,
		))
	}
	return gogui.Column(gogui.ContainerCfg{
		ID:      id,
		Sizing:  gogui.FillFit,
		Padding: gogui.NoPadding,
		Spacing: gogui.SpacingTight,
		Content: rows,
	})
}

func (d drawer) progress(n *Node, id string) gogui.View {
	var content []gogui.View
	if n.Label != "" {
		content = append(content, gogui.Row(gogui.ContainerCfg{
			Sizing:  gogui.FillFit,
			Padding: gogui.NoPadding,
			Content: []gogui.View{
				gogui.Label(n.Label, gogui.TextStyle{}),
				gogui.Row(gogui.ContainerCfg{Sizing: gogui.FillFit, Padding: gogui.NoPadding}),
				gogui.Label(
					fmt.Sprintf("%d%%", int(math.Round(n.Value*100))),
					widgets.Secondary(),
				),
			},
		}))
	}
	content = append(content, d.look.Toolkit.ProgressBar(gogui.ProgressBarCfg{
		ID:      id + "-bar",
		Percent: float32(n.Value),
		Height:  style.Reach() * 0.25,
		Sizing:  gogui.FillFixed,
	}))
	cfg := gogui.ContainerCfg{
		ID:      id,
		Sizing:  gogui.FillFit,
		Padding: gogui.PaddingMedium,
		Spacing: gogui.SpacingSmall,
		Radius:  gogui.RadiusMedium,
		Content: content,
	}
	d.look.Toolkit.ListRow(&cfg, style.Rest)
	return gogui.Column(cfg)
}

// picture shows a grey box until the download is done.
func (d drawer) picture(n *Node, id string) gogui.View {
	boxW, boxH := d.vw*9/10, d.vh*6/10
	var content gogui.View
	if p := d.r.pictures[n.URL]; p != nil {
		pw, ph := fit(p.img.Bounds(), boxW, boxH)
		content = widgets.Picture(widgets.ImageSource(p.key, p.img), pw, ph)
	} else {
		content = gogui.Column(gogui.ContainerCfg{
			Width:  float32(boxH * 4 / 3),
			Height: float32(boxH),
			Sizing: gogui.FixedFixed,
			Color:  gogui.CurrentTheme().Cfg.ColorPanel,
			Radius: gogui.RadiusMedium,
		})
	}
	return gogui.Row(gogui.ContainerCfg{
		ID:      id,
		Sizing:  gogui.FillFit,
		Padding: gogui.NoPadding,
		HAlign:  gogui.HAlignCenter,
		Content: []gogui.View{content},
	})
}

func fit(b image.Rectangle, w, h int) (int, int) {
	if b.Dx() == 0 || b.Dy() == 0 {
		return 0, 0
	}
	scale := min(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	return int(float64(b.Dx()) * scale), int(float64(b.Dy()) * scale)
}
