package audiofrontend

import "testing"

// BenchmarkProcessFrame measures one 30 ms frame of speech through the whole
// frontend.
func BenchmarkProcessFrame(b *testing.B) {
	samples, err := loadWAV(testdataPath("hey_jarvis_speech.wav"))
	if err != nil {
		b.Fatal(err)
	}
	cfg := DefaultConfig(10)
	window := cfg.SampleRate * cfg.WindowSizeMs / 1000
	step := cfg.SampleRate * cfg.StepSizeMs / 1000
	frames := (len(samples)-window)/step + 1
	fe := New(cfg)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := (i % frames) * step
		fe.ProcessFrame(samples[off : off+window])
	}
}
