package echod

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zserge/microwakeword"

	"github.com/ygelfand/libcountertop/pkg/inference/wake"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/hardware/led"
	"github.com/ygelfand/echolocal/internal/hardware/mic"
	"github.com/ygelfand/echolocal/internal/layout"
)

// How much audio around each prompt counts as an answer to it.
const (
	promptBefore = mic.Rate / 4
	promptAfter  = mic.Rate * 11 / 4
)

var (
	promptColor   = led.HomeAssistant
	progressColor = led.Color{R: 0x30, G: 0x18}
	doneColor     = led.Color{G: 0xC0}
)

func newRecordCmd() *cobra.Command {
	var (
		dir     string
		out     string
		prompts int
		lead    float64
		every   float64
		jitter  float64
		lit     float64
		secs    float64
	)

	c := &cobra.Command{
		Use:   "record",
		Short: "Record what the wake word hears, lighting the ring when to speak",
		Long: "Saves the same audio the wake word listens to as a 16 kHz mono WAV, for training\n" +
			"a wake word on this device's own microphones. With --prompts the whole ring lights\n" +
			"up each time the wake word should be said; between prompts a dim arc shows how far\n" +
			"along the round is. With --prompts 0 it just records for --seconds.\n\n" +
			"Every prompt is scored with the installed wake word, and a JSON file next to the\n" +
			"WAV lists when each prompt was shown, its best score, and any wake outside the\n" +
			"prompts. Stop echod first: it holds the microphones.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			models, err := wake.Installed(dir)
			if err != nil {
				return err
			}
			m := wake.Pick(models, config.Get().Wake.Slot(0).ID)
			cutoff := config.Get().Wake.Slot(0).Threshold
			if cutoff <= 0 {
				cutoff = m.Config.ProbabilityCutoff
			}
			// Like echod: the detector must never fire on its own. When it does, it starts
			// over and the scores read afterwards come out far too low.
			m.Config.ProbabilityCutoff = 1.1
			det, err := microwakeword.NewDetector(m.Config)
			if err != nil {
				return err
			}

			if out == "" {
				out = fmt.Sprintf("/data/local/tmp/wake-%s.wav", time.Now().Format("20060102-150405"))
			}
			var cues []int
			if prompts > 0 {
				cues = cueSchedule(prompts, lead, every, jitter, rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 1)))
			}
			end := int(secs * mic.Rate)
			if len(cues) > 0 {
				end = cues[len(cues)-1] + promptAfter + mic.Rate
			}

			source, err := mic.Acquire()
			if err != nil {
				return err
			}
			defer source.Close()

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			go func() {
				if err := source.Run(ctx); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "capture stopped:", err)
				}
			}()
			frames, unlisten := source.Listen("tools record")
			defer unlisten()

			w, err := createWAV(out, mic.Rate)
			if err != nil {
				return err
			}

			ring := newPainter(&led.Ring{Path: led.DefaultPath})
			defer ring.close()

			o := cmd.OutOrStdout()
			fmt.Fprintf(o, "recording to %s: %q, bar %.2f, %d prompts\n", out, m.Phrase, cutoff, len(cues))

			tl := newTally(cues, cutoff)
			started := time.Now()
			pos, told := 0, 0
			litFor := int(lit * mic.Rate)

		loop:
			for pos < end {
				select {
				case <-ctx.Done():
					break loop
				case frame, ok := <-frames:
					if !ok {
						break loop
					}
					if err := w.write(frame); err != nil {
						return err
					}
					det.ProcessAudio(frame)
					tl.add(pos, det.SlidingAverage())
					pos += len(frame)

					ring.show(ringFor(cues, pos, litFor))
					for told < len(cues) && pos > cues[told]+promptAfter {
						tellPrompt(o, tl.cues[told], len(cues))
						told++
					}
				}
			}

			unlisten()
			ring.show(led.Solid(doneColor))
			time.Sleep(time.Second)
			ring.show(nil)
			if err := ring.err(); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "the ring could not be lit:", err)
			}

			if err := w.close(); err != nil {
				return err
			}
			mc := config.Get().Microphone
			sum := recording{
				WAV: out, Rate: mic.Rate, Model: m.ID, Cutoff: cutoff,
				Mixing: string(mc.Mixing), GainDB: mc.Gain, Leveling: mc.Leveling,
				Started: started.Format(time.RFC3339), Seconds: float64(pos) / mic.Rate,
				Prompts: tl.cues[:told], Stray: tl.stray,
			}
			if err := writeJSON(out+".json", sum); err != nil {
				return err
			}
			tellSummary(o, sum)
			return nil
		},
	}

	c.Flags().StringVar(&dir, "models", layout.ModelDir, "directory of installed models")
	c.Flags().StringVar(&out, "out", "", "WAV to write (default /data/local/tmp/wake-<time>.wav)")
	c.Flags().IntVar(&prompts, "prompts", 50, "how many times to light the ring; 0 records without prompts")
	c.Flags().Float64Var(&lead, "lead", 3, "seconds before the first prompt")
	c.Flags().Float64Var(&every, "every", 4, "seconds between prompts")
	c.Flags().Float64Var(&jitter, "jitter", 0.4, "move each prompt up to this many seconds, so it isn't a fixed beat")
	c.Flags().Float64Var(&lit, "lit", 1.2, "seconds the ring stays lit for each prompt")
	c.Flags().Float64Var(&secs, "seconds", 600, "how long to record when --prompts is 0")
	return c
}

// cueSchedule returns the sample at which each prompt lights up.
func cueSchedule(n int, lead, every, jitter float64, rng *rand.Rand) []int {
	cues := make([]int, n)
	for i := range cues {
		at := lead + float64(i)*every + (rng.Float64()*2-1)*jitter
		cues[i] = int(at * mic.Rate)
	}
	return cues
}

type cue struct {
	N     int     `json:"n"`
	AtSec float64 `json:"at_s"`
	Peak  float64 `json:"peak"`
	Heard bool    `json:"heard"`

	at int
}

type stray struct {
	AtSec float64 `json:"at_s"`
	Score float64 `json:"score"`
}

// tally keeps the best score near each prompt, and wakes that happen away from them.
type tally struct {
	cutoff     float64
	cues       []cue
	stray      []stray
	quietUntil int
}

func newTally(at []int, cutoff float64) *tally {
	t := &tally{cutoff: cutoff, cues: make([]cue, len(at)), quietUntil: -1}
	for i, a := range at {
		t.cues[i] = cue{N: i + 1, AtSec: float64(a) / mic.Rate, at: a}
	}
	return t
}

func (t *tally) add(at int, score float64) {
	for i := range t.cues {
		c := &t.cues[i]
		if at >= c.at-promptBefore && at <= c.at+promptAfter {
			if score > c.Peak {
				c.Peak = score
				c.Heard = score >= t.cutoff
			}
			return
		}
	}
	// Same pause the device takes after a wake, so one wake isn't counted many times.
	if score >= t.cutoff && at > t.quietUntil {
		t.stray = append(t.stray, stray{AtSec: float64(at) / mic.Rate, Score: score})
		t.quietUntil = at + mic.Rate
	}
}

func ringFor(cues []int, pos, litFor int) []led.Color {
	done := 0
	for _, c := range cues {
		if pos >= c && pos < c+litFor {
			return led.Solid(promptColor)
		}
		if pos >= c {
			done++
		}
	}
	if len(cues) == 0 {
		// Free recording: one dim light says it is still running.
		out := make([]led.Color, led.Segments)
		out[0] = progressColor
		return out
	}
	return led.Arc(max(float64(done)/float64(len(cues)), 1.0/led.Segments), progressColor)
}

// painter writes the ring from its own goroutine, so a slow LED write never holds up the microphones.
type painter struct {
	ring  *led.Ring
	want  atomic.Pointer[[]led.Color]
	kick  chan struct{}
	last  []led.Color
	done  chan struct{}
	first atomic.Pointer[error]
}

func newPainter(r *led.Ring) *painter {
	p := &painter{ring: r, kick: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		for range p.kick {
			var err error
			if c := *p.want.Load(); c == nil {
				err = p.ring.Off()
			} else {
				err = p.ring.SetSegments(c)
			}
			if err != nil {
				p.first.CompareAndSwap(nil, &err)
			}
		}
	}()
	return p
}

// err is the first LED write that failed, if any.
func (p *painter) err() error {
	if e := p.first.Load(); e != nil {
		return *e
	}
	return nil
}

func (p *painter) show(c []led.Color) {
	if p.last != nil && c != nil && sameColors(p.last, c) {
		return
	}
	p.last = c
	p.want.Store(&c)
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *painter) close() {
	close(p.kick)
	<-p.done
	_ = p.ring.Off()
}

func sameColors(a, b []led.Color) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type recording struct {
	WAV      string  `json:"wav"`
	Rate     int     `json:"rate"`
	Model    string  `json:"model"`
	Cutoff   float64 `json:"cutoff"`
	Mixing   string  `json:"mixing"`
	GainDB   int     `json:"gain_db"`
	Leveling bool    `json:"leveling"`
	Started  string  `json:"started"`
	Seconds  float64 `json:"seconds"`
	Prompts  []cue   `json:"prompts"`
	Stray    []stray `json:"wakes_outside_prompts"`
}

func tellPrompt(o io.Writer, c cue, total int) {
	verdict := "missed"
	if c.Heard {
		verdict = "heard"
	}
	fmt.Fprintf(o, "  prompt %2d/%d at %6.1fs  best %.3f  %s\n", c.N, total, c.AtSec, c.Peak, verdict)
}

func tellSummary(o io.Writer, r recording) {
	heard := 0
	for _, c := range r.Prompts {
		if c.Heard {
			heard++
		}
	}
	fmt.Fprintf(o, "\n%.0fs recorded to %s\n", r.Seconds, r.WAV)
	if len(r.Prompts) > 0 {
		fmt.Fprintf(o, "heard %d of %d prompts at bar %.2f\n", heard, len(r.Prompts), r.Cutoff)
	}
	fmt.Fprintf(o, "%d wakes outside prompts\n", len(r.Stray))
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// wavFile writes 16-bit mono audio as it arrives and fixes the sizes in the header
// every few seconds, so a recording cut off by a lost connection can still be read.
type wavFile struct {
	f       *os.File
	buf     *bufio.Writer
	n       int
	unsaved int
}

func createWAV(path string, rate int) (*wavFile, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	var h []byte
	u32 := func(v uint32) { h = binary.LittleEndian.AppendUint32(h, v) }
	u16 := func(v uint16) { h = binary.LittleEndian.AppendUint16(h, v) }
	h = append(h, "RIFF"...)
	u32(36)
	h = append(h, "WAVEfmt "...)
	u32(16)
	u16(1) // PCM
	u16(1)
	u32(uint32(rate))
	u32(uint32(rate * 2))
	u16(2)
	u16(16)
	h = append(h, "data"...)
	u32(0)
	if _, err := f.Write(h); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &wavFile{f: f, buf: bufio.NewWriterSize(f, 64*1024)}, nil
}

func (w *wavFile) write(frame []int16) error {
	var b [2]byte
	for _, s := range frame {
		binary.LittleEndian.PutUint16(b[:], uint16(s))
		if _, err := w.buf.Write(b[:]); err != nil {
			return err
		}
	}
	w.n += len(frame) * 2
	w.unsaved += len(frame)
	if w.unsaved >= 10*mic.Rate {
		w.unsaved = 0
		return w.sync()
	}
	return nil
}

func (w *wavFile) sync() error {
	if err := w.buf.Flush(); err != nil {
		return err
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(36+w.n))
	if _, err := w.f.WriteAt(b[:], 4); err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(b[:], uint32(w.n))
	_, err := w.f.WriteAt(b[:], 40)
	return err
}

func (w *wavFile) close() error {
	if err := w.sync(); err != nil {
		_ = w.f.Close()
		return err
	}
	return w.f.Close()
}
