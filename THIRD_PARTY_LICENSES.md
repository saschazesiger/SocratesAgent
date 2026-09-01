# Third party licences

Socrates itself is MIT — see [LICENSE](LICENSE). The voice that reads answers
out loud is not: it is Piper, a program somebody else wrote, together with the
libraries and the two voice models it needs. Those files are shipped inside the
published Docker image and are downloaded by the app on first use everywhere
else, and passing a binary on is exactly what obliges you to pass its notices
and its source on with it.

This file names every one of them, its licence, and where its source lives. It
covers what the image contains and what the app installs at runtime. None of it
is compiled into the `socrates` binary: everything below is a separate
executable or a shared library that Socrates starts as a child process, and the
voice models are data it reads.

In the Docker image these files live under `/opt/piper`; in a normal
installation, under `<data>/piper`.

## Piper

The speech synthesiser, release `2023.11.14-2`
(`piper_linux_x86_64.tar.gz` / `piper_linux_aarch64.tar.gz`).

- **Licence:** MIT, © Michael Hansen
- **Source:** <https://github.com/rhasspy/piper>

## espeak-ng

The text to phoneme front end Piper calls before it synthesises anything.
It is bundled inside the Piper release archive above as
`libespeak-ng.so.1.52.0.1`, along with `espeak-ng-data/` and an `espeak-ng`
executable.

- **Licence:** GPL-3.0
- **Source:** <https://github.com/espeak-ng/espeak-ng> — this is the
  corresponding source for the shared library that ships in the image, and
  where to obtain it.

espeak-ng is a shared library, dynamically linked and separately distributed.
Nothing of it is built into Socrates, which links no GPL code and remains MIT;
the two are conveyed side by side in the same image.

## piper-phonemize

Piper's phonemisation layer and the library that actually loads espeak-ng,
bundled in the same archive as `libpiper_phonemize.so.1.2.0` together with the
`libtashkeel_model.ort` diacritisation model it uses for Arabic.

- **Licence:** MIT
- **Source:** <https://github.com/rhasspy/piper-phonemize>

## ONNX Runtime

The inference engine that runs the voice model, bundled in the same archive as
`libonnxruntime.so.1.14.1`.

- **Licence:** MIT
- **Source:** <https://github.com/microsoft/onnxruntime>

## Voice: de_DE-thorsten-medium

The German voice, downloaded from
<https://huggingface.co/rhasspy/piper-voices>.

- **Licence:** CC0 1.0 — the Thorsten-Voice recordings it was trained on are
  dedicated to the public domain by Thorsten Müller.
- **Source:** <https://huggingface.co/rhasspy/piper-voices> (`de/de_DE/thorsten/medium`),
  <https://www.thorsten-voice.de>

## Voice: en_US-ljspeech-medium

The English voice, from the same repository.

- **Licence:** public domain — trained from scratch on the LJ Speech corpus,
  which is itself public domain.
- **Source:** <https://huggingface.co/rhasspy/piper-voices> (`en/en_US/ljspeech/medium`),
  <https://keithito.com/LJ-Speech-Dataset/>
