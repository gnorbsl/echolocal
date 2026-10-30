//go:build !board_biscuit

package remotescreen

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
)

const (
	fetchTimeout = 10 * time.Second
	mostBytes    = 8 << 20
	// Decoding needs up to 4 bytes per pixel for a moment, so this caps it near 48 MB.
	mostPixels = 12_000_000
	// Pictures are shrunk to this on their longest side; the biggest Show screen is 1280 wide.
	mostSide = 1280
)

type picture struct {
	img image.Image
	key string
}

// fetch downloads the layout's pictures. A picture already on the screen stays there until
// its new copy arrives, so updating a screen does not blink.
func (r *Remote) fetch(l *Layout, shown uint64) {
	urls := map[string]bool{}
	walk(l.Body, func(n *Node) {
		if n.Type == TypeImage {
			urls[n.URL] = true
		}
	})
	r.mu.Lock()
	drop := r.forget(urls)
	r.mu.Unlock()
	for _, k := range drop {
		gogui.DropImage(k)
	}
	for url := range urls {
		go r.load(url, shown)
	}
}

func (r *Remote) load(url string, shown uint64) {
	img, err := download(r.o.Client, url)
	if err != nil {
		slog.Warn("screen: a picture failed", "url", url, "err", err)
		return
	}
	r.mu.Lock()
	if r.shown != shown {
		r.mu.Unlock()
		return
	}
	r.loaded++
	old := r.pictures[url]
	r.pictures[url] = &picture{img: img, key: fmt.Sprintf("remotescreen/%d", r.loaded)}
	r.mu.Unlock()
	if old != nil {
		gogui.DropImage(old.key)
	}
	r.o.Shell.Redraw()
}

// forget drops the pictures the screen no longer shows and returns their keys. Call it with
// the lock held.
func (r *Remote) forget(keep map[string]bool) []string {
	var keys []string
	for url, p := range r.pictures {
		if !keep[url] {
			keys = append(keys, p.key)
			delete(r.pictures, url)
		}
	}
	return keys
}

func download(c *http.Client, url string) (image.Image, error) {
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, mostBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > mostBytes {
		return nil, fmt.Errorf("%s is bigger than %d MB", url, mostBytes>>20)
	}
	size, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if size.Width*size.Height > mostPixels {
		return nil, fmt.Errorf("%s is %dx%d, too big", url, size.Width, size.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return shrink(img, mostSide), nil
}

// shrink scales a picture down by skipping pixels, so it fits in a square of most.
func shrink(src image.Image, most int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= most && h <= most {
		return src
	}
	scale := float64(most) / float64(max(w, h))
	dw, dh := max(int(float64(w)*scale), 1), max(int(float64(h)*scale), 1)
	out := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		sy := b.Min.Y + y*h/dh
		for x := range dw {
			out.Set(x, y, src.At(b.Min.X+x*w/dw, sy))
		}
	}
	return out
}

func walk(ns []*Node, visit func(*Node)) {
	for _, n := range ns {
		visit(n)
		walk(n.Items, visit)
	}
}
