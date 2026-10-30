//go:build !board_biscuit

package remotescreen

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	maxNodes = 300
	maxDepth = 6
)

const (
	TypeText      = "text"
	TypeButton    = "button"
	TypeSwitch    = "switch"
	TypeSlider    = "slider"
	TypeChecklist = "checklist"
	TypeProgress  = "progress"
	TypeRow       = "row"
	TypeColumn    = "column"
	TypeImage     = "image"
)

// Layout is the screen Home Assistant describes in JSON.
type Layout struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Body  []*Node `json:"body"`

	// Skipped lists the parts that were left out, such as unknown types.
	Skipped []string `json:"-"`
}

// Node is one thing on the screen. Which fields count depends on Type.
// Checklist items are Nodes too, using ID, Label and Done.
type Node struct {
	Type  string   `json:"type"`
	ID    string   `json:"id"`
	Text  string   `json:"text"`
	Size  string   `json:"size"`
	Bold  bool     `json:"bold"`
	Label string   `json:"label"`
	Style string   `json:"style"`
	On    bool     `json:"on"`
	Done  bool     `json:"done"`
	Value float64  `json:"value"`
	Min   *float64 `json:"min"`
	Max   *float64 `json:"max"`
	Step  float64  `json:"step"`
	URL   string   `json:"url"`
	Items []*Node  `json:"items"`
}

func Parse(raw string) (*Layout, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("layout is empty")
	}
	var l Layout
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			return nil, fmt.Errorf("layout is not valid JSON at byte %d: %w", syntax.Offset, err)
		}
		return nil, fmt.Errorf("layout is not valid JSON: %w", err)
	}
	c := checker{layout: &l}
	body, err := c.nodes(l.Body, "body", 1)
	if err != nil {
		return nil, err
	}
	l.Body = body
	return &l, nil
}

type checker struct {
	layout *Layout
	count  int
}

func (c *checker) nodes(in []*Node, at string, depth int) ([]*Node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%s is nested more than %d deep", at, maxDepth)
	}
	out := make([]*Node, 0, len(in))
	for i, n := range in {
		where := fmt.Sprintf("%s[%d]", at, i)
		if n == nil {
			c.skip(where + " is empty")
			continue
		}
		c.count++
		if c.count > maxNodes {
			return nil, fmt.Errorf("layout has more than %d parts", maxNodes)
		}
		keep, err := c.node(n, where, depth)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, n)
		}
	}
	return out, nil
}

func (c *checker) node(n *Node, at string, depth int) (bool, error) {
	n.Type = strings.ToLower(strings.TrimSpace(n.Type))
	switch n.Type {
	case TypeText:
		if n.Text == "" {
			c.skip(at + " (text) has no text")
			return false, nil
		}
	case TypeButton, TypeSwitch:
		if n.ID == "" {
			return false, fmt.Errorf("%s (%s) needs an id", at, n.Type)
		}
		if n.Label == "" {
			n.Label = n.ID
		}
	case TypeSlider:
		if n.ID == "" {
			return false, fmt.Errorf("%s (slider) needs an id", at)
		}
		return true, slider(n, at)
	case TypeChecklist:
		if n.ID == "" {
			return false, fmt.Errorf("%s (checklist) needs an id", at)
		}
		for i, item := range n.Items {
			if item == nil || item.ID == "" {
				return false, fmt.Errorf("%s.items[%d] needs an id", at, i)
			}
			if item.Label == "" {
				item.Label = item.ID
			}
		}
	case TypeProgress:
		n.Value = clamp(n.Value, 0, 1)
	case TypeRow, TypeColumn:
		items, err := c.nodes(n.Items, at+".items", depth+1)
		if err != nil {
			return false, err
		}
		n.Items = items
	case TypeImage:
		if !strings.HasPrefix(n.URL, "http://") && !strings.HasPrefix(n.URL, "https://") {
			return false, fmt.Errorf("%s (image) needs an http or https url", at)
		}
	case "":
		c.skip(at + " has no type")
		return false, nil
	default:
		c.skip(fmt.Sprintf("%s has unknown type %q", at, n.Type))
		return false, nil
	}
	return true, nil
}

func slider(n *Node, at string) error {
	low, high := 0.0, 100.0
	if n.Min != nil {
		low = *n.Min
	}
	if n.Max != nil {
		high = *n.Max
	}
	if high <= low {
		return fmt.Errorf("%s (slider) needs max above min", at)
	}
	n.Min, n.Max = &low, &high
	if n.Step <= 0 {
		n.Step = 1
	}
	n.Value = snap(n, n.Value)
	if n.Label == "" {
		n.Label = n.ID
	}
	return nil
}

// snap keeps a slider value inside its range and on its steps.
func snap(n *Node, v float64) float64 {
	low, high := *n.Min, *n.Max
	v = clamp(v, low, high)
	v = low + math.Round((v-low)/n.Step)*n.Step
	v = clamp(v, low, high)
	return math.Round(v*1e6) / 1e6
}

func clamp(v, low, high float64) float64 { return min(max(v, low), high) }

func (c *checker) skip(why string) { c.layout.Skipped = append(c.layout.Skipped, why) }
