// Prints the lookup tables of the reference frontend (the C code inside
// pymicro-features 2.0.2, set up the same way) for reference_tables.txt, and
// can print the raw uint16 features for a 16 kHz mono 16-bit WAV file.
//
// Build it against the unpacked pymicro-features 2.0.2 source package (S),
// with the file list and -DFIXED_POINT=16 flag from its setup.py:
//
//   L=$S/tensorflow/lite/experimental/microfrontend/lib
//   clang++ -O3 -DFIXED_POINT=16 -I $S -I $S/kissfft -o dump dump_tables.cc \
//     $L/kiss_fft_int16.cc $L/fft.cc $L/fft_util.cc $L/filterbank.cc \
//     $L/filterbank_util.cc $L/frontend.cc $L/frontend_util.cc $L/log_lut.cc \
//     $L/log_scale.cc $L/log_scale_util.cc $L/noise_reduction.cc \
//     $L/noise_reduction_util.cc $L/pcan_gain_control.cc \
//     $L/pcan_gain_control_util.cc $L/window.cc $L/window_util.cc
//
//   ./dump tables > reference_tables.txt
//   ./dump features x.wav  -> one line of 40 raw values per frame
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

extern "C" {
#include "tensorflow/lite/experimental/microfrontend/lib/frontend.h"
#include "tensorflow/lite/experimental/microfrontend/lib/frontend_util.h"
}
#include "tensorflow/lite/experimental/microfrontend/lib/kiss_fft_int16.h"

namespace kissfft_fixed16 {
// Copied from _kiss_fft_guts.h and kiss_fftr.cc so we can read the twiddles.
struct kiss_fft_state {
  int nfft;
  int inverse;
  int factors[2 * 32];
  kiss_fft_cpx twiddles[1];
};
struct kiss_fftr_state {
  kiss_fft_cfg substate;
  kiss_fft_cpx* tmpbuf;
  kiss_fft_cpx* super_twiddles;
};
}  // namespace kissfft_fixed16

static void init_cfg(FrontendConfig* cfg) {
  cfg->window.size_ms = 30;
  cfg->window.step_size_ms = 10;
  cfg->filterbank.num_channels = 40;
  cfg->filterbank.lower_band_limit = 125.0f;
  cfg->filterbank.upper_band_limit = 7500.0f;
  cfg->noise_reduction.smoothing_bits = 10;
  cfg->noise_reduction.even_smoothing = 0.025f;
  cfg->noise_reduction.odd_smoothing = 0.06f;
  cfg->noise_reduction.min_signal_remaining = 0.05f;
  cfg->pcan_gain_control.enable_pcan = 1;
  cfg->pcan_gain_control.strength = 0.95f;
  cfg->pcan_gain_control.offset = 80.0f;
  cfg->pcan_gain_control.gain_bits = 21;
  cfg->log_scale.enable_log = 1;
  cfg->log_scale.scale_shift = 6;
}

static void print_i16(const char* name, const int16_t* v, int n) {
  printf("%s:", name);
  for (int i = 0; i < n; ++i) printf(" %d", v[i]);
  printf("\n");
}

static void dump_tables(FrontendState* st) {
  print_i16("window", st->window.coefficients, (int)st->window.size);

  auto* fr = (kissfft_fixed16::kiss_fftr_state*)st->fft.scratch;
  auto* sub = (kissfft_fixed16::kiss_fft_state*)fr->substate;
  printf("fft_twiddles:");
  for (int i = 0; i < sub->nfft; ++i)
    printf(" %d %d", sub->twiddles[i].r, sub->twiddles[i].i);
  printf("\n");
  printf("fft_super_twiddles:");
  for (int i = 0; i < sub->nfft / 2; ++i)
    printf(" %d %d", fr->super_twiddles[i].r, fr->super_twiddles[i].i);
  printf("\n");
  printf("fft_factors:");
  for (int i = 0; i < 8; ++i) printf(" %d", sub->factors[i]);
  printf("\n");

  FilterbankState* fb = &st->filterbank;
  int n = fb->num_channels + 1;
  printf("filterbank_start_end: %d %d\n", fb->start_index, fb->end_index);
  print_i16("filterbank_frequency_starts", fb->channel_frequency_starts, n);
  print_i16("filterbank_weight_starts", fb->channel_weight_starts, n);
  print_i16("filterbank_widths", fb->channel_widths, n);
  int total = 0;
  for (int i = 0; i < n; ++i) {
    int end = fb->channel_weight_starts[i] + fb->channel_widths[i];
    if (end > total) total = end;
  }
  print_i16("filterbank_weights", fb->weights, total);
  print_i16("filterbank_unweights", fb->unweights, total);

  NoiseReductionState* nr = &st->noise_reduction;
  printf("noise_reduction: %d %d %d %d\n", nr->smoothing_bits,
         nr->even_smoothing, nr->odd_smoothing, nr->min_signal_remaining);

  PcanGainControlState* pc = &st->pcan_gain_control;
  printf("pcan_snr_shift: %d\n", pc->snr_shift);
  // Entries 4*interval-3 are never written or read by the C code, skip them.
  printf("pcan_gain_lut:");
  printf(" %d %d", pc->gain_lut[0], pc->gain_lut[1]);
  for (int interval = 2; interval <= 32; ++interval) {
    const int16_t* l = pc->gain_lut + 4 * interval - 6;
    printf(" %d %d %d", l[0], l[1], l[2]);
  }
  printf("\n");
}

static int dump_features(FrontendState* st, const char* path) {
  FILE* f = fopen(path, "rb");
  if (!f) {
    perror(path);
    return 1;
  }
  fseek(f, 0, SEEK_END);
  long size = ftell(f);
  fseek(f, 44, SEEK_SET);  // plain 44-byte WAV header
  size_t n = (size - 44) / 2;
  int16_t* pcm = (int16_t*)malloc(n * 2);
  if (fread(pcm, 2, n, f) != n) {
    fprintf(stderr, "short read\n");
    return 1;
  }
  fclose(f);
  for (size_t off = 0; off + 160 <= n; off += 160) {
    size_t read = 0;
    FrontendOutput out = FrontendProcessSamples(st, pcm + off, 160, &read);
    if (out.size == 0) continue;
    for (size_t i = 0; i < out.size; ++i)
      printf(i ? " %u" : "%u", out.values[i]);
    printf("\n");
  }
  free(pcm);
  return 0;
}

int main(int argc, char** argv) {
  FrontendConfig cfg;
  init_cfg(&cfg);
  FrontendState st;
  if (!FrontendPopulateState(&cfg, &st, 16000)) return 1;
  if (argc >= 2 && strcmp(argv[1], "tables") == 0) {
    dump_tables(&st);
    return 0;
  }
  if (argc >= 3 && strcmp(argv[1], "features") == 0) {
    return dump_features(&st, argv[2]);
  }
  fprintf(stderr, "usage: dump tables | dump features file.wav\n");
  return 2;
}
