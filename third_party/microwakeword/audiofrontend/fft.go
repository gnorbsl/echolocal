package audiofrontend

import "math"

// This is a port of the 16-bit fixed-point KISS FFT (kiss_fft.c and
// kiss_fftr.c built with FIXED_POINT=16) that the TFLite Micro frontend uses.
// Every step rounds and wraps around exactly like the C code, so the output
// is the same down to the last bit.

// cpx is a complex number with 16-bit parts.
type cpx struct{ r, i int16 }

// realFFT turns n real values into n/2+1 complex values.
type realFFT struct {
	half     int   // n/2: the real input is handled as n/2 complex values
	factors  []int // radix and remaining length for each FFT stage
	twiddles []cpx
	split    []cpx // twiddles that split the half-size result into the real one
	tmp      []cpx
}

// nextPow2 returns the smallest power of 2 >= n.
func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

func newRealFFT(n int) *realFFT {
	half := n / 2
	f := &realFFT{
		half:     half,
		factors:  fftFactors(half),
		twiddles: make([]cpx, half),
		split:    make([]cpx, half/2),
		tmp:      make([]cpx, half),
	}
	for i := range f.twiddles {
		f.twiddles[i] = unitCpx(-2 * math.Pi * float64(i) / float64(half))
	}
	for i := range f.split {
		f.split[i] = unitCpx(-math.Pi * (float64(i+1)/float64(half) + 0.5))
	}
	return f
}

// unitCpx is cos + i*sin of the angle, scaled to 15 fraction bits.
func unitCpx(phase float64) cpx {
	return cpx{
		r: int16(math.Floor(math.FMA(32767, math.Cos(phase), 0.5))),
		i: int16(math.Floor(math.FMA(32767, math.Sin(phase), 0.5))),
	}
}

// fftFactors splits n into FFT stages, preferring 4s, like kf_factor.
func fftFactors(n int) []int {
	var out []int
	p := 4
	floorSqrt := math.Floor(math.Sqrt(float64(n)))
	for {
		for n%p != 0 {
			switch p {
			case 4:
				p = 2
			case 2:
				p = 3
			default:
				p += 2
			}
			if float64(p) > floorSqrt {
				p = n
			}
		}
		n /= p
		out = append(out, p, n)
		if n <= 1 {
			return out
		}
	}
}

// compute runs the FFT on in (n values) and writes n/2+1 values to out.
func (f *realFFT) compute(in []int16, out []cpx) {
	f.work(f.tmp, in, 0, 1, f.factors)

	n := f.half
	dc := f.tmp[0]
	fixDiv(&dc, 2)
	out[0] = cpx{dc.r + dc.i, 0}
	out[n] = cpx{dc.r - dc.i, 0}
	for k := 1; k <= n/2; k++ {
		a := f.tmp[k]
		b := cpx{f.tmp[n-k].r, -f.tmp[n-k].i}
		fixDiv(&a, 2)
		fixDiv(&b, 2)
		sum := cpx{a.r + b.r, a.i + b.i}
		tw := mulCpx(cpx{a.r - b.r, a.i - b.i}, f.split[k-1])
		out[k] = cpx{halve(int32(sum.r) + int32(tw.r)), halve(int32(sum.i) + int32(tw.i))}
		out[n-k] = cpx{halve(int32(sum.r) - int32(tw.r)), halve(int32(tw.i) - int32(sum.i))}
	}
}

// work is kf_work: it reads the real input as complex pairs, starting at
// complex index pos and stepping by stride.
func (f *realFFT) work(out []cpx, in []int16, pos, stride int, factors []int) {
	p, m := factors[0], factors[1]
	if m == 1 {
		for k := 0; k < p; k++ {
			j := 2 * (pos + k*stride)
			out[k] = cpx{in[j], in[j+1]}
		}
	} else {
		for k := 0; k < p; k++ {
			f.work(out[k*m:], in, pos+k*stride, stride*p, factors[2:])
		}
	}
	switch p {
	case 2:
		f.butterfly2(out, stride, m)
	case 4:
		f.butterfly4(out, stride, m)
	default:
		panic("audiofrontend: FFT size must be a power of two")
	}
}

func (f *realFFT) butterfly2(out []cpx, stride, m int) {
	tw := 0
	for k := 0; k < m; k++ {
		a, b := &out[k], &out[k+m]
		fixDiv(a, 2)
		fixDiv(b, 2)
		t := mulCpx(*b, f.twiddles[tw])
		tw += stride
		*b = cpx{a.r - t.r, a.i - t.i}
		*a = cpx{a.r + t.r, a.i + t.i}
	}
}

func (f *realFFT) butterfly4(out []cpx, stride, m int) {
	tw1, tw2, tw3 := 0, 0, 0
	for k := 0; k < m; k++ {
		a, b, c, d := &out[k], &out[k+m], &out[k+2*m], &out[k+3*m]
		fixDiv(a, 4)
		fixDiv(b, 4)
		fixDiv(c, 4)
		fixDiv(d, 4)

		s0 := mulCpx(*b, f.twiddles[tw1])
		s1 := mulCpx(*c, f.twiddles[tw2])
		s2 := mulCpx(*d, f.twiddles[tw3])
		s5 := cpx{a.r - s1.r, a.i - s1.i}
		*a = cpx{a.r + s1.r, a.i + s1.i}
		s3 := cpx{s0.r + s2.r, s0.i + s2.i}
		s4 := cpx{s0.r - s2.r, s0.i - s2.i}
		*c = cpx{a.r - s3.r, a.i - s3.i}
		tw1 += stride
		tw2 += 2 * stride
		tw3 += 3 * stride
		*a = cpx{a.r + s3.r, a.i + s3.i}
		*b = cpx{s5.r + s4.i, s5.i - s4.r}
		*d = cpx{s5.r - s4.i, s5.i + s4.r}
	}
}

// roundQ15 drops 15 fraction bits with rounding (sround in C).
func roundQ15(x int32) int16 {
	return int16((x + 1<<14) >> 15)
}

func mulCpx(a, b cpx) cpx {
	return cpx{
		r: roundQ15(int32(a.r)*int32(b.r) - int32(a.i)*int32(b.i)),
		i: roundQ15(int32(a.r)*int32(b.i) + int32(a.i)*int32(b.r)),
	}
}

// fixDiv divides by div the way the C code does: multiply by 32767/div.
func fixDiv(c *cpx, div int32) {
	k := 32767 / div
	c.r = roundQ15(int32(c.r) * k)
	c.i = roundQ15(int32(c.i) * k)
}

func halve(x int32) int16 {
	return int16(x >> 1)
}
