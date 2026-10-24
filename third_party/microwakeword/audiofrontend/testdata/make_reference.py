"""Makes the reference features that frontend_ref_test.go compares against.

It takes the library's own public test recordings (../testdata: a "Hey Jarvis"
take and a piece with no wake word), adds one made-up loud piece, and runs them through microWakeWord's own feature code
(generate_features_for_clip, which uses pymicro-features, the C frontend every
microWakeWord model is trained with). It also stores the int8 values the model
gets, made the way microWakeWord's Model class makes them.

Run with the microWakeWord training environment:
  .../envs/mww/bin/python -I make_reference.py

Writes <name>.wav and <name>.features next to this script. A .features file is:
  uint32 frames, uint32 channels (little endian),
  frames*channels uint16 raw frontend values,
  frames*channels int8 model input values.
"""

import struct
import wave
from pathlib import Path

import numpy as np
import scipy.io.wavfile

from microwakeword.audio.audio_utils import generate_features_for_clip
from microwakeword.inference import Model

RATE = 16000
OUT = Path(__file__).resolve().parent
FLOAT_SCALE = 0.0390625  # pymicro-features multiplies the raw values by this


def cut(path, start_s, end_s):
    rate, audio = scipy.io.wavfile.read(path)
    assert rate == RATE and audio.dtype == np.int16 and audio.ndim == 1
    return audio[int(start_s * RATE) : int(end_s * RATE)]


def loud_piece():
    """Silence, then a clipped full-scale sweep and noise, to reach the rare paths."""
    rng = np.random.default_rng(1)
    silence = np.zeros(RATE // 5)
    t = np.arange(RATE // 4) / RATE
    sweep = 1.6 * np.sin(2 * np.pi * (100 + 7000 * t) * t)
    noise = rng.uniform(-1.0, 1.0, RATE // 4) * np.linspace(0.01, 1.0, RATE // 4)
    audio = np.concatenate([silence, sweep, noise])
    return np.clip(np.round(audio * 32767), -32768, 32767).astype(np.int16)


def write_wav(path, audio):
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(RATE)
        w.writeframes(audio.astype("<i2").tobytes())


def main():
    shared = OUT.parent.parent / "testdata"
    model = Model(str(shared / "hey_jarvis.tflite"))
    pieces = {
        "hey_jarvis_speech": cut(shared / "hey_jarvis.wav", 0.0, 6.17),
        "nowake_speech": cut(shared / "nowake.wav", 2.0, 5.0),
        "loud": loud_piece(),
    }
    for name, audio in pieces.items():
        features = generate_features_for_clip(audio, step_ms=10)
        raw = features.astype(np.float64) / FLOAT_SCALE
        assert np.all(raw == np.round(raw)), "raw values should be whole numbers"
        raw = raw.astype(np.uint16)
        model_input = model.quantize_input_data(features, model.input_details[0])
        frames, channels = raw.shape

        write_wav(OUT / f"{name}.wav", audio)
        with open(OUT / f"{name}.features", "wb") as f:
            f.write(struct.pack("<II", frames, channels))
            f.write(raw.astype("<u2").tobytes())
            f.write(model_input.astype(np.int8).tobytes())
        print(f"{name}: {len(audio) / RATE:.2f} s, {frames} frames, raw max {raw.max()}, "
              f"int8 range {model_input.min()}..{model_input.max()}")


if __name__ == "__main__":
    main()
