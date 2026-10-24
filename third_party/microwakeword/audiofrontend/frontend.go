// Package audiofrontend implements the audio feature extraction pipeline
// used by microWakeWord models. It converts 16 kHz 16-bit PCM audio into
// the same features as the TFLite Micro audio frontend that microWakeWord
// models are trained with (via pymicro-features), value for value.
//
// It is a port of that C code and uses the same whole-number maths: Hann
// window, 16-bit FFT, mel filterbank (40 channels, 125–7500 Hz), noise
// reduction, PCAN gain control, log scaling, and finally the int8 values
// the model reads.
package audiofrontend

import (
	"math"
	"math/big"
	"math/bits"
)

// Config holds the frontend configuration parameters.
type Config struct {
	SampleRate   int
	WindowSizeMs int
	StepSizeMs   int
	NumChannels  int
	LowerFreq    float64
	UpperFreq    float64
}

// DefaultConfig returns the standard MWW frontend configuration.
func DefaultConfig(stepMs int) Config {
	return Config{
		SampleRate:   16000,
		WindowSizeMs: 30,
		StepSizeMs:   stepMs,
		NumChannels:  40,
		LowerFreq:    125.0,
		UpperFreq:    7500.0,
	}
}

// Fixed settings of the microWakeWord frontend (see pymicro-features).
const (
	windowBits = 12

	noiseReductionBits = 14
	smoothingBits      = 10
	evenSmoothing      = 0.025
	oddSmoothing       = 0.06
	minSignalRemaining = 0.05

	pcanStrength   = 0.95
	pcanOffset     = 80.0
	pcanGainBits   = 21
	pcanSnrBits    = 12
	pcanOutputBits = 6

	logScaleShift = 6
)

// The int8 input of microWakeWord models: one step is 26/255 of a feature
// unit and the scale starts at -128. ProcessAllInt8 uses these.
const (
	DefaultInputScale     float32 = 0.10196079
	DefaultInputZeroPoint int32   = -128
)

// Frontend processes audio samples into features.
type Frontend struct {
	cfg        Config
	windowSize int
	stepSize   int
	fftSize    int

	window []int16 // Hann window, 12 fraction bits
	fft    *realFFT
	fb     *filterbank

	noiseEstimate []uint32
	evenSmoothing uint32
	oddSmoothing  uint32
	minSignal     uint32

	pcanLUT  []int16
	snrShift int
	logShift int // how far values are moved left before the log (may be negative)

	// Work buffers, reused for every frame.
	fftInput []int16
	spectrum []cpx
	energy   []int32
	signal   []uint32
}

// New creates a new Frontend with the given config.
func New(cfg Config) *Frontend {
	windowSize := cfg.SampleRate * cfg.WindowSizeMs / 1000
	stepSize := cfg.SampleRate * cfg.StepSizeMs / 1000
	fftSize := nextPow2(windowSize)
	spectrumSize := fftSize/2 + 1
	// The FFT output is scaled down by the FFT size; this many bits are
	// added back later.
	correction := msb32(uint32(fftSize)) - 1 - filterbankBits/2

	return &Frontend{
		cfg:        cfg,
		windowSize: windowSize,
		stepSize:   stepSize,
		fftSize:    fftSize,

		window: makeWindow(windowSize),
		fft:    newRealFFT(fftSize),
		fb: newFilterbank(cfg.NumChannels, float32(cfg.LowerFreq), float32(cfg.UpperFreq),
			cfg.SampleRate, spectrumSize),

		noiseEstimate: make([]uint32, cfg.NumChannels),
		evenSmoothing: noiseFraction(evenSmoothing),
		oddSmoothing:  noiseFraction(oddSmoothing),
		minSignal:     noiseFraction(minSignalRemaining),

		pcanLUT:  makePcanLUT(pcanStrength, pcanOffset, pcanGainBits, smoothingBits-correction),
		snrShift: pcanGainBits - correction - pcanSnrBits,
		logShift: correction,

		fftInput: make([]int16, fftSize),
		spectrum: make([]cpx, spectrumSize),
		energy:   make([]int32, spectrumSize),
		signal:   make([]uint32, cfg.NumChannels),
	}
}

// Reset clears the noise estimates.
func (f *Frontend) Reset() {
	for i := range f.noiseEstimate {
		f.noiseEstimate[i] = 0
	}
}

// ProcessFrame extracts features from one window of samples.
// samples should have windowSize elements; missing ones count as silence.
func (f *Frontend) ProcessFrame(samples []int16) []uint16 {
	// 1. Window. Note the loudest result so the FFT input can be scaled up
	// as far as it fits.
	in := f.fftInput
	var maxAbs int16
	for i := 0; i < f.windowSize; i++ {
		var s int16
		if i < len(samples) {
			s = samples[i]
		}
		v := int16((int32(s) * int32(f.window[i])) >> windowBits)
		in[i] = v
		if v < 0 {
			v = -v
		}
		if v > maxAbs {
			maxAbs = v
		}
	}
	shift := 15 - msb32(uint32(maxAbs))
	for i := 0; i < f.windowSize; i++ {
		in[i] = int16(uint16(in[i]) << shift)
	}

	// 2. FFT, 3. mel filterbank (scaled back down by the same shift).
	f.fft.compute(in, f.spectrum)
	sig := f.signal
	f.fb.process(f.spectrum, f.energy, sig, shift)

	// 4. Noise reduction, 5. PCAN gain control.
	f.reduceNoise(sig)
	f.pcanGainControl(sig)

	// 6. Log scaling.
	out := make([]uint16, len(sig))
	for i, v := range sig {
		if f.logShift < 0 {
			v >>= -f.logShift
		} else {
			v <<= f.logShift
		}
		if v > 1 {
			v = fixedLog(v, logScaleShift)
		} else {
			v = 0
		}
		if v > 0xFFFF {
			v = 0xFFFF
		}
		out[i] = uint16(v)
	}
	return out
}

// ProcessAllInt8 processes the entire audio buffer and returns int8 features,
// using the standard microWakeWord input scale.
func (f *Frontend) ProcessAllInt8(samples []int16) [][]int8 {
	f.Reset()
	var frames [][]int8
	for offset := 0; offset+f.windowSize <= len(samples); offset += f.stepSize {
		raw := f.ProcessFrame(samples[offset:])
		frame := make([]int8, len(raw))
		ToInt8(frame, raw, DefaultInputScale, DefaultInputZeroPoint)
		frames = append(frames, frame)
	}
	return frames
}

// ToInt8 turns raw frontend values into a model's int8 input the same way
// microWakeWord does: value * 0.0390625 / scale + zeroPoint, dropping the
// fraction. Results outside -128..127 are clamped.
func ToInt8(dst []int8, raw []uint16, scale float32, zeroPoint int32) {
	for i, v := range raw {
		q := float64(float32(v)*0.0390625/scale) + float64(zeroPoint)
		switch {
		case q >= 127:
			dst[i] = 127
		case q <= -128:
			dst[i] = -128
		default:
			dst[i] = int8(q)
		}
	}
}

// reduceNoise follows a slow running average of each channel (the noise)
// and takes it away from the signal, keeping at least a small part of it.
func (f *Frontend) reduceNoise(sig []uint32) {
	for i, s := range sig {
		smoothing := f.evenSmoothing
		if i&1 == 1 {
			smoothing = f.oddSmoothing
		}
		scaled := s << smoothingBits
		estimate := uint32((uint64(scaled)*uint64(smoothing) +
			uint64(f.noiseEstimate[i])*uint64(1<<noiseReductionBits-smoothing)) >> noiseReductionBits)
		f.noiseEstimate[i] = estimate
		if estimate > scaled {
			estimate = scaled
		}
		floor := uint32((uint64(s) * uint64(f.minSignal)) >> noiseReductionBits)
		sig[i] = max((scaled-estimate)>>smoothingBits, floor)
	}
}

// pcanGainControl divides each channel by its noise level (roughly), so loud
// and quiet rooms look alike.
func (f *Frontend) pcanGainControl(sig []uint32) {
	for i, s := range sig {
		gain := uint32(int32(wideDynamic(f.noiseEstimate[i], f.pcanLUT)))
		snr := uint32((uint64(s) * uint64(gain)) >> f.snrShift)
		if snr < 2<<pcanSnrBits {
			sig[i] = (snr * snr) >> (2 + 2*pcanSnrBits - pcanOutputBits)
		} else {
			sig[i] = (snr >> (pcanSnrBits - pcanOutputBits)) - (1 << pcanOutputBits)
		}
	}
}

// wideDynamic looks up the PCAN gain for a noise level, filling in between
// table points with a curve.
func wideDynamic(x uint32, lut []int16) int16 {
	if x <= 2 {
		return lut[x]
	}
	interval := msb32(x)
	l := lut[4*interval-6:]
	var frac int32
	if interval < 11 {
		frac = int32((x << (11 - interval)) & 0x3FF)
	} else {
		frac = int32((x >> (interval - 11)) & 0x3FF)
	}
	result := (int32(l[2]) * frac) >> 5
	result += int32(l[1]) << 5
	result *= frac
	result = (result + 1<<14) >> 15
	result += int32(l[0])
	return int16(result)
}

// Log scaling, from log_scale.c and log_lut.c.
const (
	logSegmentsLog2 = 7
	logScale        = 65536
	logScaleLog2    = 16
	logCoeff        = 45426
)

var logLUT = [...]uint16{
	0, 224, 442, 654, 861, 1063, 1259, 1450, 1636, 1817, 1992, 2163,
	2329, 2490, 2646, 2797, 2944, 3087, 3224, 3358, 3487, 3611, 3732, 3848,
	3960, 4068, 4172, 4272, 4368, 4460, 4549, 4633, 4714, 4791, 4864, 4934,
	5001, 5063, 5123, 5178, 5231, 5280, 5326, 5368, 5408, 5444, 5477, 5507,
	5533, 5557, 5578, 5595, 5610, 5622, 5631, 5637, 5640, 5641, 5638, 5633,
	5626, 5615, 5602, 5586, 5568, 5547, 5524, 5498, 5470, 5439, 5406, 5370,
	5332, 5291, 5249, 5203, 5156, 5106, 5054, 5000, 4944, 4885, 4825, 4762,
	4697, 4630, 4561, 4490, 4416, 4341, 4264, 4184, 4103, 4020, 3935, 3848,
	3759, 3668, 3575, 3481, 3384, 3286, 3186, 3084, 2981, 2875, 2768, 2659,
	2549, 2437, 2323, 2207, 2090, 1971, 1851, 1729, 1605, 1480, 1353, 1224,
	1094, 963, 830, 695, 559, 421, 282, 142, 0, 0,
}

// fixedLog is the natural log of x, scaled by 2^scaleShift, in whole numbers.
func fixedLog(x uint32, scaleShift int) uint32 {
	integer := uint32(msb32(x) - 1)
	log2 := integer<<logScaleLog2 + log2Fraction(x, integer)
	const round = logScale / 2
	loge := uint32((uint64(logCoeff)*uint64(log2) + round) >> logScaleLog2)
	return ((loge << scaleShift) + round) >> logScaleLog2
}

func log2Fraction(x, log2x uint32) uint32 {
	frac := int32(int64(x) - int64(1)<<log2x)
	if log2x < logScaleLog2 {
		frac <<= logScaleLog2 - log2x
	} else {
		frac >>= log2x - logScaleLog2
	}
	const segUnit = (1 << logScaleLog2) >> logSegmentsLog2
	seg := uint32(frac) >> (logScaleLog2 - logSegmentsLog2)
	c0 := int32(logLUT[seg])
	c1 := int32(logLUT[seg+1])
	rel := ((c1 - c0) * (frac - int32(segUnit*seg))) >> logScaleLog2
	return uint32(frac + c0 + rel)
}

// Tables. The C code builds these with float32 maths; the same steps are
// repeated here so the whole-number tables come out identical.

func makeWindow(size int) []int16 {
	w := make([]int16, size)
	arg := float32(math.Pi) * 2 / float32(size)
	for i := range w {
		v := 0.5 - 0.5*cosf(arg*(float32(i)+0.5))
		w[i] = int16(floorf(v*(1<<windowBits) + 0.5))
	}
	return w
}

func noiseFraction(v float32) uint32 {
	return uint32(uint16(v * (1 << noiseReductionBits)))
}

// makePcanLUT builds the gain table for wideDynamic: for each power of two
// it stores the gain there and two numbers that bend the line to the next.
func makePcanLUT(strength, offset float32, gainBits, inputBits int) []int16 {
	gain := func(x uint32) int16 {
		xf := float32(x) / float32(uint32(1)<<inputBits)
		g := float32(uint32(1)<<gainBits) * powf(xf+offset, -strength)
		if g > 0x7FFF {
			return 0x7FFF
		}
		return int16(g + 0.5)
	}
	lut := make([]int16, 4*32-3)
	lut[0] = gain(0)
	lut[1] = gain(1)
	for interval := 2; interval <= 32; interval++ {
		x0 := uint32(1) << (interval - 1)
		x1 := x0 + x0>>1
		x2 := 2 * x0
		if interval == 32 {
			x2 = x0 + (x0 - 1)
		}
		y0, y1, y2 := gain(x0), gain(x1), gain(x2)
		diff1 := int32(y1) - int32(y0)
		diff2 := int32(y2) - int32(y0)
		a1 := 4*diff1 - diff2
		a2 := diff2 - a1
		i := 4*interval - 6
		lut[i] = y0
		lut[i+1] = int16(a1)
		lut[i+2] = int16(a2)
	}
	return lut
}

func freqToMel(freq float32) float32 {
	// The conversion keeps Go from merging this multiply with a later add.
	return float32(1127.0 * log1pf(freq/700.0))
}

// float32 versions of the C maths functions.

func cosf(x float32) float32    { return float32(math.Cos(float64(x))) }
func log1pf(x float32) float32  { return float32(math.Log1p(float64(x))) }
func powf(x, y float32) float32 { return float32(math.Pow(float64(x), float64(y))) }
func floorf(x float32) float32  { return float32(math.Floor(float64(x))) }

// fmaf is a*b+c rounded once, as the C compiler does for the filterbank's
// centre frequencies.
func fmaf(a, b, c float32) float32 {
	x := new(big.Float).SetPrec(200).SetFloat64(float64(a))
	x.Mul(x, new(big.Float).SetFloat64(float64(b)))
	x.Add(x, new(big.Float).SetFloat64(float64(c)))
	r, _ := x.Float32()
	return r
}

func msb32(n uint32) int { return 32 - bits.LeadingZeros32(n) }
func msb64(n uint64) int { return 64 - bits.LeadingZeros64(n) }
