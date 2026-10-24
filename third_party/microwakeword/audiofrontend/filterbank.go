package audiofrontend

const filterbankBits = 12

// filterbank adds FFT energies up into mel channels with triangle-shaped
// weights, like filterbank.c. There is one extra channel at the start: its
// leftover ("unweight") part feeds the first real channel.
type filterbank struct {
	numChannels int
	startIndex  int // first FFT bin used
	endIndex    int // one past the last FFT bin used
	chanStart   []int
	weights     [][]int16
	unweights   [][]int16
}

func newFilterbank(numChannels int, lower, upper float32, sampleRate, spectrumSize int) *filterbank {
	n := numChannels + 1
	fb := &filterbank{
		numChannels: numChannels,
		chanStart:   make([]int, n),
		weights:     make([][]int16, n),
		unweights:   make([][]int16, n),
	}

	melLow := freqToMel(lower)
	melSpacing := (freqToMel(upper) - melLow) / float32(n)
	center := make([]float32, n)
	for i := range center {
		center[i] = fmaf(melSpacing, float32(i+1), melLow)
	}

	// Always leave out the DC bin.
	hzPerBin := 0.5 * float32(sampleRate) / (float32(spectrumSize) - 1)
	fb.startIndex = int(1.5 + lower/hzPerBin)

	start := fb.startIndex
	for ch := 0; ch < n; ch++ {
		end := start
		for freqToMel(float32(end)*hzPerBin) <= center[ch] {
			end++
		}
		fb.chanStart[ch] = start

		left := melLow
		if ch > 0 {
			left = center[ch-1]
		}
		w := make([]int16, end-start)
		u := make([]int16, end-start)
		for j := range w {
			weight := (center[ch] - freqToMel(float32(start+j)*hzPerBin)) / (center[ch] - left)
			w[j] = int16(floorf(weight*(1<<filterbankBits) + 0.5))
			u[j] = int16(floorf((1-weight)*(1<<filterbankBits) + 0.5))
		}
		fb.weights[ch] = w
		fb.unweights[ch] = u
		if end > fb.endIndex {
			fb.endIndex = end
		}
		start = end
	}
	if fb.endIndex >= spectrumSize {
		panic("audiofrontend: filterbank goes past the end of the spectrum")
	}
	return fb
}

// process turns the FFT output into one value per channel: the square root
// of the weighted energy, shifted down by shift.
func (fb *filterbank) process(spectrum []cpx, energy []int32, out []uint32, shift int) {
	for i := fb.startIndex; i < fb.endIndex; i++ {
		re, im := int32(spectrum[i].r), int32(spectrum[i].i)
		energy[i] = re*re + im*im
	}

	var weighted, unweighted uint64
	for ch, w := range fb.weights {
		u := fb.unweights[ch]
		mags := energy[fb.chanStart[ch]:]
		for j := range w {
			// The C code widens the 32-bit energy with its sign, keep that.
			mag := uint64(int64(mags[j]))
			weighted += uint64(int64(w[j])) * mag
			unweighted += uint64(int64(u[j])) * mag
		}
		if ch > 0 {
			out[ch-1] = sqrt64(weighted) >> shift
		}
		weighted = unweighted
		unweighted = 0
	}
}

// sqrt32 and sqrt64 are the rounded whole-number square roots from
// filterbank.c, including their small rounding quirks.
func sqrt32(num uint32) uint16 {
	if num == 0 {
		return 0
	}
	var res uint32
	maxBit := (32 - msb32(num)) | 1
	bit := uint32(1) << (31 - maxBit)
	for n := (31-maxBit)/2 + 1; n > 0; n-- {
		if num >= res+bit {
			num -= res + bit
			res = (res >> 1) + bit
		} else {
			res >>= 1
		}
		bit >>= 2
	}
	if num > res && res != 0xFFFF {
		res++
	}
	return uint16(res)
}

func sqrt64(num uint64) uint32 {
	if num>>32 == 0 {
		return uint32(sqrt32(uint32(num)))
	}
	var res uint64
	maxBit := (64 - msb64(num)) | 1
	bit := uint64(1) << (63 - maxBit)
	for n := (63-maxBit)/2 + 1; n > 0; n-- {
		if num >= res+bit {
			num -= res + bit
			res = (res >> 1) + bit
		} else {
			res >>= 1
		}
		bit >>= 2
	}
	if num > res && res != 0xFFFFFFFF {
		res++
	}
	return uint32(res)
}
