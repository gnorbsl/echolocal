package echod

import (
	"encoding/binary"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/ygelfand/echolocal/internal/hardware/mic"
)

func TestCueScheduleKeepsPromptsApart(t *testing.T) {
	const (
		lead, every, jitter = 3.0, 4.0, 0.4
		rate                = mic.Rate
	)
	cues := cueSchedule(50, lead, every, jitter, rand.New(rand.NewPCG(1, 2)))

	if len(cues) != 50 {
		t.Fatalf("got %d cues, want 50", len(cues))
	}
	if first := cues[0]; first < int((lead-jitter)*rate) {
		t.Fatalf("first cue at sample %d, before the lead-in ends", first)
	}
	for i := 1; i < len(cues); i++ {
		gap := float64(cues[i]-cues[i-1]) / rate
		if gap < every-2*jitter || gap > every+2*jitter {
			t.Fatalf("cues %d and %d are %.2fs apart, want %.1f±%.1fs", i, i+1, gap, every, 2*jitter)
		}
	}
}

func TestTallyKeepsTheBestScoreOfEachPrompt(t *testing.T) {
	tl := newTally([]int{16000, 80000}, 0.8)

	tl.add(16000+8000, 0.90)
	tl.add(16000+9000, 0.95)
	tl.add(80000+100, 0.50)

	got := tl.cues
	if got[0].Peak != 0.95 || !got[0].Heard {
		t.Fatalf("first prompt = %+v, want peak 0.95 and heard", got[0])
	}
	if got[1].Peak != 0.50 || got[1].Heard {
		t.Fatalf("second prompt = %+v, want peak 0.50 and not heard", got[1])
	}
	if len(tl.stray) != 0 {
		t.Fatalf("got %d wakes outside prompts, want none", len(tl.stray))
	}
}

func TestTallyCountsOneWakeOutsidePromptsPerSecond(t *testing.T) {
	tl := newTally([]int{160000}, 0.8)

	tl.add(0, 0.90)
	tl.add(1000, 0.92)
	tl.add(40000, 0.85)
	tl.add(41000, 0.10)

	if len(tl.stray) != 2 {
		t.Fatalf("got %d wakes outside prompts, want 2: %+v", len(tl.stray), tl.stray)
	}
	if tl.stray[0].AtSec != 0 || tl.stray[1].AtSec != 2.5 {
		t.Fatalf("wakes at %+v, want 0s and 2.5s", tl.stray)
	}
}

func TestWAVHeaderMatchesWhatWasWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec.wav")
	w, err := createWAV(path, mic.Rate)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]int16, mic.FrameSamples)
	frame[0] = 1234
	for range 3 {
		if err := w.write(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pcm := 3 * mic.FrameSamples * 2
	if len(data) != wavHeader+pcm {
		t.Fatalf("file is %d bytes, want %d", len(data), wavHeader+pcm)
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" || string(data[36:40]) != "data" {
		t.Fatalf("not a WAV header: %q", data[:44])
	}
	if got := binary.LittleEndian.Uint32(data[4:]); got != uint32(36+pcm) {
		t.Fatalf("RIFF size %d, want %d", got, 36+pcm)
	}
	if got := binary.LittleEndian.Uint32(data[40:]); got != uint32(pcm) {
		t.Fatalf("data size %d, want %d", got, pcm)
	}
	if got := binary.LittleEndian.Uint32(data[24:]); got != mic.Rate {
		t.Fatalf("rate %d, want %d", got, mic.Rate)
	}
	if got := int16(binary.LittleEndian.Uint16(data[44:])); got != 1234 {
		t.Fatalf("first sample %d, want 1234", got)
	}
}
