// wakeeval scores recordings with a wake word model exactly the way echod does, so two
// models can be compared fairly on takes neither of them was trained on.
//
//	go run ./cmd/wakeeval -models <dir> -id hey_jarvis recordings/round01.wav ...
//
// A recording from `echod tools record` has a JSON file next to it with the prompt times.
// Prompts -from to -to are the test takes; everything away from any prompt counts as
// "should not wake". A WAV without a JSON file is all "should not wake".
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/zserge/microwakeword"

	"github.com/ygelfand/libcountertop/pkg/inference/wake"
)

const (
	rate  = 16000
	frame = rate / 50
	// Same window the recorder uses around each prompt.
	before = 0.25
	after  = 2.75
	// echod waits this long after a wake before it can wake again.
	pause = 1.0
)

type prompt struct {
	N     int     `json:"n"`
	AtSec float64 `json:"at_s"`
}

type result struct {
	peaks      []float64
	heard      []int
	wakes      []int
	wakesAt    [][]float64 // seconds into the recording, per cutoff
	outsideSec float64
}

func main() {
	dir := flag.String("models", "", "folder holding the model's .tflite and .json")
	id := flag.String("id", "hey_jarvis", "model to use: its file name without .tflite")
	from := flag.Int("from", 41, "first prompt that is a test take")
	to := flag.Int("to", 50, "last prompt that is a test take")
	cut := flag.String("cutoffs", "0.5,0.6,0.7,0.8,0.9,0.95", "scores to report")
	own := flag.Bool("own-cutoff", false, "let the detector fire and reset at the model's own cutoff, like `echod tools wake` (echod itself doesn't)")
	flag.Parse()

	if err := run(*dir, *id, *from, *to, *cut, *own, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(dir, id string, from, to int, cut string, own bool, files []string) error {
	models, err := wake.Installed(dir)
	if err != nil {
		return err
	}
	m := wake.Pick(models, id)
	if m.ID != id {
		return fmt.Errorf("no model %q in %s", id, dir)
	}
	// echod never lets the detector fire by itself; it reads the score and decides.
	if !own {
		m.Config.ProbabilityCutoff = 1.1
	}

	var cutoffs []float64
	for _, s := range strings.Split(cut, ",") {
		var v float64
		if _, err := fmt.Sscanf(s, "%g", &v); err != nil {
			return fmt.Errorf("bad cutoff %q", s)
		}
		cutoffs = append(cutoffs, v)
	}

	fmt.Printf("model %s, cutoffs %v\n", id, cutoffs)
	for _, path := range files {
		samples, err := readWAV(path)
		if err != nil {
			return err
		}
		prompts, err := readPrompts(path + ".json")
		if err != nil {
			return err
		}
		det, err := microwakeword.NewDetector(m.Config)
		if err != nil {
			return err
		}
		scores := make([]float64, 0, len(samples)/frame)
		for off := 0; off+frame <= len(samples); off += frame {
			det.ProcessAudio(samples[off : off+frame])
			scores = append(scores, det.SlidingAverage())
		}

		r := summarize(scores, prompts, from, to, cutoffs)
		fmt.Printf("\n%s\n", path)
		if len(r.peaks) > 0 {
			fmt.Printf("  test takes %d-%d, best scores:", from, to)
			for _, p := range r.peaks {
				fmt.Printf(" %.2f", p)
			}
			fmt.Println()
		}
		for i, c := range cutoffs {
			line := fmt.Sprintf("  at %.2f:", c)
			if len(r.peaks) > 0 {
				line += fmt.Sprintf("  heard %2d of %d", r.heard[i], len(r.peaks))
			}
			fmt.Printf("%s  wakes when it shouldn't: %d in %.0f s", line, r.wakes[i], r.outsideSec)
			for _, at := range r.wakesAt[i] {
				fmt.Printf("  @%.1fs", at)
			}
			fmt.Println()
		}
	}
	return nil
}

// summarize takes one score per 20 ms frame and works out how the test takes and the
// rest of the recording did at each cutoff.
func summarize(scores []float64, prompts []prompt, from, to int, cutoffs []float64) result {
	near := make([]int, len(scores)) // which prompt a frame belongs to, 0 = none
	for _, p := range prompts {
		lo, hi := int((p.AtSec-before)*50), int((p.AtSec+after)*50)
		for i := max(lo, 0); i < min(hi, len(scores)); i++ {
			near[i] = p.N
		}
	}

	r := result{heard: make([]int, len(cutoffs)), wakes: make([]int, len(cutoffs)),
		wakesAt: make([][]float64, len(cutoffs))}
	peak := map[int]float64{}
	outside := 0
	quietUntil := make([]int, len(cutoffs))
	for i, s := range scores {
		if n := near[i]; n != 0 {
			peak[n] = max(peak[n], s)
			continue
		}
		outside++
		for c, cut := range cutoffs {
			if s >= cut && i >= quietUntil[c] {
				r.wakes[c]++
				r.wakesAt[c] = append(r.wakesAt[c], float64(i)/50)
				quietUntil[c] = i + int(pause*50)
			}
		}
	}
	r.outsideSec = float64(outside) / 50

	for _, p := range prompts {
		if p.N < from || p.N > to {
			continue
		}
		r.peaks = append(r.peaks, peak[p.N])
		for c, cut := range cutoffs {
			if peak[p.N] >= cut {
				r.heard[c]++
			}
		}
	}
	return r
}

func readPrompts(path string) ([]prompt, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec struct {
		Prompts []prompt `json:"prompts"`
	}
	return rec.Prompts, json.Unmarshal(b, &rec)
}

// readWAV reads 16 kHz mono 16-bit audio, the format the recorder writes.
func readWAV(path string) ([]int16, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, fmt.Errorf("%s is not a WAV file", path)
	}
	if ch := binary.LittleEndian.Uint16(data[22:]); ch != 1 {
		return nil, fmt.Errorf("%s has %d channels, want mono", path, ch)
	}
	if r := binary.LittleEndian.Uint32(data[24:]); r != rate {
		return nil, fmt.Errorf("%s is %d Hz, want %d", path, r, rate)
	}
	pcm := data[44:]
	out := make([]int16, len(pcm)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(pcm[i*2:]))
	}
	return out, nil
}
