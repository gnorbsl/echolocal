package audiofrontend

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The reference files in testdata/ come from microWakeWord's own feature code
// (pymicro-features). See testdata/make_reference.py. reference_tables.txt
// holds the lookup tables of the same C code, printed by
// testdata/dump_tables.cc built from the pymicro-features 2.0.2 sources.
var referenceClips = []string{"hey_jarvis_speech", "nowake_speech", "loud"}

func testdataPath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

type referenceFeatures struct {
	raw   [][]uint16
	input [][]int8
}

func loadReference(t testing.TB, name string) ([]int16, referenceFeatures) {
	t.Helper()
	samples, err := loadWAV(testdataPath(name + ".wav"))
	if err != nil {
		t.Fatalf("load %s.wav: %v", name, err)
	}
	data, err := os.ReadFile(testdataPath(name + ".features"))
	if err != nil {
		t.Fatalf("load %s.features: %v", name, err)
	}
	frames := int(binary.LittleEndian.Uint32(data[0:]))
	channels := int(binary.LittleEndian.Uint32(data[4:]))
	if len(data) != 8+frames*channels*3 {
		t.Fatalf("%s.features: unexpected size %d", name, len(data))
	}
	var ref referenceFeatures
	raw := data[8:]
	input := data[8+frames*channels*2:]
	for i := 0; i < frames; i++ {
		r := make([]uint16, channels)
		in := make([]int8, channels)
		for j := 0; j < channels; j++ {
			r[j] = binary.LittleEndian.Uint16(raw[(i*channels+j)*2:])
			in[j] = int8(input[i*channels+j])
		}
		ref.raw = append(ref.raw, r)
		ref.input = append(ref.input, in)
	}
	return samples, ref
}

func TestFrontendMatchesReference(t *testing.T) {
	for _, name := range referenceClips {
		t.Run(name, func(t *testing.T) {
			samples, ref := loadReference(t, name)
			fe := New(DefaultConfig(10))
			cfg := DefaultConfig(10)
			window := cfg.SampleRate * cfg.WindowSizeMs / 1000
			step := cfg.SampleRate * cfg.StepSizeMs / 1000

			mismatches, maxDiff := 0, 0
			for i, want := range ref.raw {
				off := i * step
				if off+window > len(samples) {
					t.Fatalf("frame %d: not enough audio", i)
				}
				got := fe.ProcessFrame(samples[off : off+window])
				for j := range want {
					d := int(got[j]) - int(want[j])
					if d < 0 {
						d = -d
					}
					if d > maxDiff {
						maxDiff = d
					}
					if d != 0 {
						if mismatches < 5 {
							t.Errorf("frame %d channel %d: got %d, want %d", i, j, got[j], want[j])
						}
						mismatches++
					}
				}
			}
			if mismatches > 0 {
				t.Errorf("%d of %d raw values differ, largest difference %d",
					mismatches, len(ref.raw)*len(ref.raw[0]), maxDiff)
			}
		})
	}
}

func TestProcessAllInt8MatchesReference(t *testing.T) {
	for _, name := range referenceClips {
		t.Run(name, func(t *testing.T) {
			samples, ref := loadReference(t, name)
			got := New(DefaultConfig(10)).ProcessAllInt8(samples)
			if len(got) < len(ref.input) {
				t.Fatalf("got %d frames, want at least %d", len(got), len(ref.input))
			}
			mismatches := 0
			for i, want := range ref.input {
				for j := range want {
					if got[i][j] != want[j] {
						if mismatches < 5 {
							t.Errorf("frame %d channel %d: got %d, want %d", i, j, got[i][j], want[j])
						}
						mismatches++
					}
				}
			}
			if mismatches > 0 {
				t.Errorf("%d of %d int8 values differ", mismatches, len(ref.input)*len(ref.input[0]))
			}
		})
	}
}

func loadReferenceTables(t *testing.T) map[string][]int {
	t.Helper()
	data, err := os.ReadFile(testdataPath("reference_tables.txt"))
	if err != nil {
		t.Fatal(err)
	}
	tables := map[string][]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		name, values, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("bad line %q", line)
		}
		var nums []int
		for _, f := range strings.Fields(values) {
			n, err := strconv.Atoi(f)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			nums = append(nums, n)
		}
		tables[name] = nums
	}
	return tables
}

func TestTablesMatchReference(t *testing.T) {
	ref := loadReferenceTables(t)
	fe := New(DefaultConfig(10))

	check := func(name string, got []int) {
		t.Helper()
		if want := ref[name]; !slices.Equal(got, want) {
			t.Errorf("%s differs:\n got %v\nwant %v", name, got, want)
		}
	}
	ints := func(v []int16) []int {
		out := make([]int, len(v))
		for i, x := range v {
			out[i] = int(x)
		}
		return out
	}
	pairs := func(v []cpx) []int {
		var out []int
		for _, c := range v {
			out = append(out, int(c.r), int(c.i))
		}
		return out
	}

	check("window", ints(fe.window))
	check("fft_twiddles", pairs(fe.fft.twiddles))
	check("fft_super_twiddles", pairs(fe.fft.split))
	check("fft_factors", fe.fft.factors)
	check("filterbank_start_end", []int{fe.fb.startIndex, fe.fb.endIndex})
	check("noise_reduction", []int{smoothingBits, int(fe.evenSmoothing), int(fe.oddSmoothing), int(fe.minSignal)})
	check("pcan_snr_shift", []int{fe.snrShift})

	// The C code never fills or reads every fourth entry of its gain table.
	lut := []int{int(fe.pcanLUT[0]), int(fe.pcanLUT[1])}
	for interval := 2; interval <= 32; interval++ {
		lut = append(lut, ints(fe.pcanLUT[4*interval-6:4*interval-3])...)
	}
	check("pcan_gain_lut", lut)

	// The C code pads each channel's weights with zeros so they line up in
	// memory; compare the weight of every channel and FFT bin instead.
	bins := fe.fftSize/2 + 1
	dense := func(weights [][]int16) []int {
		out := make([]int, len(weights)*bins)
		for ch, w := range weights {
			for j, v := range w {
				out[ch*bins+fe.fb.chanStart[ch]+j] = int(v)
			}
		}
		return out
	}
	refDense := func(name string) []int {
		starts := ref["filterbank_frequency_starts"]
		weightStarts := ref["filterbank_weight_starts"]
		widths := ref["filterbank_widths"]
		out := make([]int, len(starts)*bins)
		for ch := range starts {
			for j := 0; j < widths[ch]; j++ {
				if v := ref[name][weightStarts[ch]+j]; v != 0 {
					out[ch*bins+starts[ch]+j] = v
				}
			}
		}
		return out
	}
	if len(ref["filterbank_weights"]) == 0 {
		t.Fatal("no filterbank weights in reference_tables.txt")
	}
	if !slices.Equal(dense(fe.fb.weights), refDense("filterbank_weights")) {
		t.Errorf("filterbank weights differ")
	}
	if !slices.Equal(dense(fe.fb.unweights), refDense("filterbank_unweights")) {
		t.Errorf("filterbank unweights differ")
	}
}
