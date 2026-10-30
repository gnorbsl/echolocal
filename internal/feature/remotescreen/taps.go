//go:build !board_biscuit

package remotescreen

import (
	"strconv"

	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
)

// Event is what Home Assistant receives for every tap, and when the screen closes.
const Event = "esphome.screen_event"

const (
	Pressed = "pressed"
	On      = "on"
	Off     = "off"

	TypeClosed = "closed"

	ClosedByButton  = "close_button"
	ClosedByTimeout = "timeout"
	ClosedElsewhere = "dismissed"
)

type tap struct {
	kind, id, item, value string
}

func (t tap) event(screen string) sharedcomponent.Event {
	data := map[string]string{
		"screen": screen,
		"type":   t.kind,
		"id":     t.id,
		"value":  t.value,
	}
	if t.kind == TypeChecklist {
		data["item"] = t.item
	}
	return sharedcomponent.Event{Name: Event, Data: data}
}

func onOff(on bool) string {
	if on {
		return On
	}
	return Off
}

func number(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// The functions below change the screen the way a tap does and say what to tell Home Assistant.

func press(n *Node) tap { return tap{kind: TypeButton, id: n.ID, value: Pressed} }

func flip(n *Node) tap {
	n.On = !n.On
	return tap{kind: TypeSwitch, id: n.ID, value: onOff(n.On)}
}

func tick(list, item *Node) tap {
	item.Done = !item.Done
	return tap{kind: TypeChecklist, id: list.ID, item: item.ID, value: onOff(item.Done)}
}

func slide(n *Node, v float64) (changed bool) {
	v = snap(n, v)
	changed = v != n.Value
	n.Value = v
	return changed
}

func settled(n *Node) tap { return tap{kind: TypeSlider, id: n.ID, value: number(n.Value)} }

func closed(why string) tap { return tap{kind: TypeClosed, value: why} }
