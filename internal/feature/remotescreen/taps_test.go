//go:build !board_biscuit

package remotescreen

import (
	"maps"
	"testing"
)

func TestTapsBecomeEvents(t *testing.T) {
	button := &Node{Type: TypeButton, ID: "movie"}
	lamp := &Node{Type: TypeSwitch, ID: "kitchen_light", On: true}
	list := &Node{Type: TypeChecklist, ID: "shopping"}
	milk := &Node{ID: "milk"}
	low, high := 0.0, 100.0
	level := &Node{Type: TypeSlider, ID: "living", Min: &low, Max: &high, Step: 5, Value: 40}

	for name, tc := range map[string]struct {
		tap  func() tap
		want map[string]string
	}{
		"button": {
			func() tap { return press(button) },
			map[string]string{"screen": "evening", "type": "button", "id": "movie", "value": "pressed"},
		},
		"switch turns off": {
			func() tap { return flip(lamp) },
			map[string]string{"screen": "evening", "type": "switch", "id": "kitchen_light", "value": "off"},
		},
		"checklist item ticked": {
			func() tap { return tick(list, milk) },
			map[string]string{
				"screen": "evening", "type": "checklist", "id": "shopping", "item": "milk", "value": "on",
			},
		},
		"slider let go": {
			func() tap { slide(level, 63); return settled(level) },
			map[string]string{"screen": "evening", "type": "slider", "id": "living", "value": "65"},
		},
		"closed": {
			func() tap { return closed(ClosedByTimeout) },
			map[string]string{"screen": "evening", "type": "closed", "id": "", "value": "timeout"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := tc.tap().event("evening")
			if e.Name != "esphome.screen_event" {
				t.Errorf("event %q", e.Name)
			}
			if !maps.Equal(e.Data, tc.want) {
				t.Errorf("data %v, want %v", e.Data, tc.want)
			}
		})
	}
	if lamp.On || !milk.Done || level.Value != 65 {
		t.Errorf("taps did not change the screen: lamp %t milk %t level %v",
			lamp.On, milk.Done, level.Value)
	}
	if flip(lamp).value != "on" || tick(list, milk).value != "off" {
		t.Error("a second tap did not flip back")
	}
}

func TestSlidingToTheSameStepIsNotAChange(t *testing.T) {
	low, high := 0.0, 10.0
	n := &Node{Type: TypeSlider, ID: "s", Min: &low, Max: &high, Step: 1, Value: 4}
	if slide(n, 4.2) {
		t.Error("4.2 counted as a change from 4")
	}
	if !slide(n, 4.6) || n.Value != 5 {
		t.Errorf("4.6 gave %v", n.Value)
	}
}
