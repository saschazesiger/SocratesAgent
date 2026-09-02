// Voice input and output.
//
// Recording captures raw PCM through the Web Audio API and encodes a 16 kHz
// mono WAV in the browser, because that is the one format both kinds of model
// OpenRouter serves - an audio capable chat model and a dedicated transcriber
// - accept. Playback is not the browser's at all: the server renders the
// answer with the voice installed next to it and sends back a WAV, so every
// device reads with the same voice instead of with whatever it happens to
// ship, and a browser with no voices at all - a car's, typically - reads too.

const WORKLET_SOURCE = `
class PCMCapture extends AudioWorkletProcessor {
  process(inputs) {
    const channel = inputs[0] && inputs[0][0];
    if (channel && channel.length) this.port.postMessage(new Float32Array(channel));
    return true;
  }
}
registerProcessor('pcm-capture', PCMCapture);
`;

const TARGET_RATE = 16000;
let workletURL = null;

/* ---------------------------------------------------------- microphone */

// The microphone is the one piece of hardware this app cannot do without, and
// the browser reports every way it can go wrong through the same rejected
// promise. Its message ("Requested device not found") means nothing to someone
// holding a phone that plainly has a microphone, so every failure is
// translated into one sentence that says what to do next.

// Constraints are a wish, not a demand: everything below is asked for as an
// ideal. A phone that cannot deliver mono at the requested processing must
// still hand over its microphone rather than refuse the recording - some
// mobile browsers answer an unsatisfiable audio constraint with
// NotFoundError, which is exactly the "no device" that isn't one.
const TUNED_AUDIO = { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true };

// micError carries a message that is meant to be read by a person, so the
// callers can show it as is instead of guessing at a DOMException.
function micError(message) {
  const err = new Error(message);
  err.userMessage = message;
  return err;
}

// describeMicError maps a getUserMedia rejection to that sentence.
export function describeMicError(err) {
  if (err && err.userMessage) return err.userMessage;
  const name = String((err && (err.name || err.code)) || '');
  switch (name) {
    case 'NotAllowedError':
    case 'PermissionDeniedError':
      return "Microphone access was denied — allow it in the browser's site settings.";
    case 'NotFoundError':
    case 'DevicesNotFoundError':
      return 'No microphone was found on this device.';
    case 'NotReadableError':
    case 'TrackStartError':
      return 'The microphone is in use by another app.';
    case 'OverconstrainedError':
    case 'ConstraintNotSatisfiedError':
      return 'This microphone could not be started with the settings it was asked for.';
    case 'SecurityError':
      return 'Microphone needs a secure (https) connection — open Socrates over https or on localhost.';
    default: {
      const detail = name || (err && err.message) || 'unknown error';
      return 'The microphone could not be started (' + detail + ').';
    }
  }
}

// micDenied reports a standing "no" without asking again. The query itself
// never opens a prompt, so it can run before the request and turn a silent
// second refusal into the one hint that helps: where the switch is. Safari
// does not implement it, which reads as "unknown" rather than as denied.
async function micDenied() {
  try {
    if (!navigator.permissions || !navigator.permissions.query) return false;
    const status = await navigator.permissions.query({ name: 'microphone' });
    return !!status && status.state === 'denied';
  } catch {
    return false;
  }
}

// openMicrophone asks for the stream, then asks for less. A constraint this
// phone cannot meet must cost one retry rather than the whole recording.
async function openMicrophone() {
  const attempts = [{ audio: TUNED_AUDIO }, { audio: true }];
  let last = null;
  for (const constraints of attempts) {
    try {
      return await navigator.mediaDevices.getUserMedia(constraints);
    } catch (err) {
      last = err;
      const name = String((err && err.name) || '');
      // A refusal, a security block or a cancelled prompt is an answer about
      // the browser, not about the constraints: retrying only asks twice.
      if (name === 'NotAllowedError' || name === 'PermissionDeniedError' ||
        name === 'SecurityError' || name === 'AbortError') break;
    }
  }
  throw last;
}

function releaseStream(stream) {
  if (!stream) return;
  try { stream.getTracks().forEach((track) => track.stop()); } catch { /* ignore */ }
}

// How many microphones are open right now. Speech and a microphone in the same
// room are one device talking to itself: whatever the voice reads out is picked
// up by the recording and transcribed back as if somebody had said it. So the
// two never overlap, and this counter is the whole of the rule - a recording
// that starts silences the voice, and while it is running nothing is read out
// at all.
let dictations = 0;

export class Recorder {
  constructor() {
    this.recording = false;
    this.startedAt = 0;
  }

  // claim is the recording flag and the claim on the room, together, because
  // they have to be: a flag set without silencing the voice is exactly the
  // overlap this is here to prevent.
  claim(on) {
    if (this.recording === !!on) return;
    this.recording = !!on;
    if (this.recording) {
      dictations += 1;
      stopSpeaking();
    } else {
      dictations = Math.max(0, dictations - 1);
    }
  }

  // start opens the microphone. It is only ever called from a click, because a
  // permission prompt a mobile browser did not ask for is a permission prompt
  // it silently denies.
  async start() {
    if (this.recording) return;
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      throw micError('Microphone needs a secure (https) connection — open Socrates over https or on localhost.');
    }
    if (!(window.AudioContext || window.webkitAudioContext)) {
      throw micError('This browser cannot record audio.');
    }
    if (await micDenied()) {
      throw micError("Microphone access was denied — allow it in the browser's site settings.");
    }
    try {
      this.stream = await openMicrophone();
    } catch (err) {
      throw micError(describeMicError(err));
    }
    try {
      await this.attach();
    } catch (err) {
      // The stream is live at this point: dropping it without stopping the
      // tracks would leave the recording indicator on for a recording that
      // never started.
      this.claim(false);
      releaseStream(this.stream);
      this.stream = null;
      if (this.ctx && this.ctx.state !== 'closed') { try { await this.ctx.close(); } catch { /* ignore */ } }
      this.ctx = null;
      throw micError(describeMicError(err));
    }
  }

  async attach() {
    const AudioCtx = window.AudioContext || window.webkitAudioContext;
    this.ctx = new AudioCtx();
    if (this.ctx.state === 'suspended') await this.ctx.resume();
    this.sampleRate = this.ctx.sampleRate;
    this.chunks = [];
    this.startedAt = Date.now();
    this.claim(true);

    const source = this.ctx.createMediaStreamSource(this.stream);
    const mute = this.ctx.createGain();
    mute.gain.value = 0;

    let attached = false;
    if (this.ctx.audioWorklet) {
      try {
        if (!workletURL) {
          workletURL = URL.createObjectURL(new Blob([WORKLET_SOURCE], { type: 'text/javascript' }));
        }
        await this.ctx.audioWorklet.addModule(workletURL);
        this.node = new AudioWorkletNode(this.ctx, 'pcm-capture');
        this.node.port.onmessage = (event) => {
          if (this.recording) this.chunks.push(event.data);
        };
        attached = true;
      } catch { attached = false; }
    }
    if (!attached) {
      this.node = this.ctx.createScriptProcessor(4096, 1, 1);
      this.node.onaudioprocess = (event) => {
        if (this.recording) this.chunks.push(new Float32Array(event.inputBuffer.getChannelData(0)));
      };
    }
    source.connect(this.node);
    this.node.connect(mute);
    mute.connect(this.ctx.destination);
    this.source = source;
  }

  get seconds() {
    return this.recording ? (Date.now() - this.startedAt) / 1000 : 0;
  }

  // release closes the microphone and hands back what it captured. Both
  // endings share it: whether a recording is sent or thrown away is a decision
  // about the audio, not about the hardware, and the hardware goes either way.
  async release() {
    this.claim(false);
    const chunks = this.chunks || [];
    const sampleRate = this.sampleRate || 48000;
    try { this.source && this.source.disconnect(); } catch { /* ignore */ }
    try { this.node && this.node.disconnect(); } catch { /* ignore */ }
    if (this.node && this.node.port) this.node.port.onmessage = null;
    if (this.stream) this.stream.getTracks().forEach((track) => track.stop());
    if (this.ctx && this.ctx.state !== 'closed') { try { await this.ctx.close(); } catch { /* ignore */ } }
    this.chunks = [];
    this.stream = null;
    this.ctx = null;
    this.node = null;
    return { chunks, sampleRate };
  }

  /** cancel closes the microphone and keeps nothing. Nothing is transcribed. */
  async cancel() {
    if (!this.recording) return;
    await this.release();
  }

  async stop() {
    if (!this.recording) return null;
    const { chunks, sampleRate } = await this.release();

    let total = 0;
    for (const chunk of chunks) total += chunk.length;
    if (!total) return null;
    const merged = new Float32Array(total);
    let offset = 0;
    for (const chunk of chunks) {
      merged.set(chunk, offset);
      offset += chunk.length;
    }
    const resampled = resample(merged, sampleRate, TARGET_RATE);
    const wav = encodeWav(resampled, TARGET_RATE);
    return {
      base64: toBase64(new Uint8Array(wav)),
      format: 'wav',
      seconds: total / sampleRate,
    };
  }
}

/**
 * dictateOnce records one utterance and hands back what was said.
 *
 * It exists because the words are the message: nothing is written into a field
 * on the way, so the whole round trip - microphone, WAV, transcription - is one
 * await. `onReady` is how the caller gets hold of the two endings a recording
 * has: it is handed `{ stop, cancel }`, and whichever is called decides what
 * this promise resolves with - the transcript, or the empty string for a
 * recording that was thrown away. A discarded recording is not a failure and
 * says nothing.
 *
 * `onTime` is called with the seconds recorded so far, for a clock beside the
 * button. Every failure arrives as one sentence a person can read, on
 * `err.userMessage` as well as on `err.message`, exactly as Recorder's own do.
 */
export async function dictateOnce({ onTime, onReady } = {}) {
  const recorder = new Recorder();
  await recorder.start();
  let ticker = null;
  if (onTime) {
    onTime(0);
    ticker = setInterval(() => onTime(recorder.seconds), 200);
  }
  let settle = null;
  const ended = new Promise((resolve) => { settle = resolve; });
  let how = 'stop';
  try {
    if (onReady) onReady({ stop: () => settle('stop'), cancel: () => settle('cancel') });
    how = await ended;
  } catch {
    /* a caller that threw from onReady still gets its microphone back below */
  }
  if (ticker) clearInterval(ticker);
  if (onTime) onTime(0);
  if (how === 'cancel') {
    await recorder.cancel();
    return '';
  }
  const result = await recorder.stop();
  if (!result) throw micError('I did not hear anything.');
  if (result.seconds < 0.4) throw micError('That was too short.');
  const { request, NetworkError } = await network();
  let data = null;
  try {
    // Transcription only reads the audio back as words, so retrying it costs a
    // moment and loses nothing - which is what a bad line wants.
    data = await request('/api/voice/transcribe', {
      method: 'POST', attempts: 3, timeout: 60000,
      body: { audio: result.base64, format: result.format },
    });
  } catch (err) {
    throw micError(err instanceof NetworkError
      ? 'No connection \u2014 that recording could not be transcribed.'
      : 'The recording could not be transcribed. Try again.');
  }
  const text = String((data && data.text) || '').trim();
  if (!text) throw micError('I did not catch that.');
  return text;
}

function resample(samples, from, to) {
  if (from === to) return samples;
  const ratio = from / to;
  const length = Math.floor(samples.length / ratio);
  const out = new Float32Array(length);
  for (let i = 0; i < length; i++) {
    const position = i * ratio;
    const index = Math.floor(position);
    const frac = position - index;
    const a = samples[index] || 0;
    const b = samples[index + 1] !== undefined ? samples[index + 1] : a;
    out[i] = a + (b - a) * frac;
  }
  return out;
}

function encodeWav(samples, rate) {
  const buffer = new ArrayBuffer(44 + samples.length * 2);
  const view = new DataView(buffer);
  const writeString = (offset, text) => {
    for (let i = 0; i < text.length; i++) view.setUint8(offset + i, text.charCodeAt(i));
  };
  writeString(0, 'RIFF');
  view.setUint32(4, 36 + samples.length * 2, true);
  writeString(8, 'WAVE');
  writeString(12, 'fmt ');
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true);
  view.setUint16(22, 1, true);
  view.setUint32(24, rate, true);
  view.setUint32(28, rate * 2, true);
  view.setUint16(32, 2, true);
  view.setUint16(34, 16, true);
  writeString(36, 'data');
  view.setUint32(40, samples.length * 2, true);
  let offset = 44;
  for (let i = 0; i < samples.length; i++, offset += 2) {
    const value = Math.max(-1, Math.min(1, samples[i]));
    view.setInt16(offset, value < 0 ? value * 0x8000 : value * 0x7fff, true);
  }
  return buffer;
}

function toBase64(bytes) {
  let binary = '';
  const step = 0x8000;
  for (let i = 0; i < bytes.length; i += step) {
    binary += String.fromCharCode.apply(null, bytes.subarray(i, i + step));
  }
  return btoa(binary);
}

/* ------------------------------------------------------------- playback */

let currentAudio = null;
let speakingFlag = false;

// Socrates has one voice and it lives on the server, so a request that fails
// leaves silence - and silence is indistinguishable from an answer that had
// nothing to say. Whoever is waiting to be read to is told why instead.
let errorListener = null;

/**
 * onSpeechError registers that notice - one slot, so the last caller wins.
 * The session page is the one that takes it, because an answer it reads out by
 * itself has nowhere else to report from; the dashboard reads on a button
 * press and shows the failure at the button. It is called with the sentence
 * and the toast kind it should be shown as - '' for an install that is simply
 * not finished yet, 'error' for everything that actually went wrong.
 */
export function onSpeechError(fn) { errorListener = fn; }

// said keeps a reason from being repeated on every single answer. A new reason
// is a new thing to know and is said again.
const said = new Set();

function reportError(reason, kind) {
  if (!errorListener || !reason || said.has(reason)) return;
  said.add(reason);
  try { errorListener(reason, kind); } catch { /* a notice must not break playback */ }
}

// speechError carries the sentence and how the page should show it. A voice
// that is still downloading itself is ordinary first run progress, and telling
// somebody in red that something failed, when nothing has, is how a first
// start reads as a broken app.
function speechError(message, kind) {
  const err = new Error(message);
  err.kind = kind;
  return err;
}

// Every utterance belongs to a generation. Stopping bumps it, so the callbacks
// of the speech that was interrupted quietly stop instead of queueing the rest
// of a cancelled answer on top of the new one.
let generation = 0;

export function isSpeaking() {
  return speakingFlag;
}

export function stopSpeaking() {
  generation++;
  speakingFlag = false;
  if (currentAudio) {
    try { currentAudio.pause(); } catch { /* ignore */ }
    currentAudio = null;
  }
}

// The deadline for one render, in milliseconds, from how much text it is. The
// voice runs on this machine, so a long answer honestly takes longer than a
// short one and a flat deadline hangs up on exactly the answers the server
// rendered perfectly.
//
// The slope comes from measuring rather than from taste: German at rate 1.0 on
// an x86 laptop renders 2,000 characters in 7.9 s, 4,000 in 16.0 s and 6,000
// in 25.8 s, so a little over 4 ms per character. Four times that leaves room
// for the ARM board this also runs on. The floor is the protection against a
// request that never gets anywhere at all, which is the failure a short answer
// really has.
//
// The ceiling is not a limit on rendering and must not become one: it sits
// just above the five minutes the server allows one render, so that timeout
// always fires first and fails with a sentence saying what happened, instead
// of the browser hanging up and telling the listener the voice did not answer.
const SPEECH_MS_PER_CHAR = 16;
const SPEECH_FLOOR_MS = 20000;
const SPEECH_CEILING_MS = 330000;

/** speechDeadline is how long to wait for a render of `length` characters. */
export function speechDeadline(length) {
  const scaled = Math.max(0, Number(length) || 0) * SPEECH_MS_PER_CHAR;
  return Math.min(SPEECH_CEILING_MS, Math.max(SPEECH_FLOOR_MS, scaled));
}

// network is the shared request layer, pulled in when it is first needed
// rather than at the top of the file. net.js registers its window listeners
// the moment it loads, and this module is also loaded in a bare Node - by
// internal/web/voice_test.go, to check the deadline arithmetic against the
// file that is really served - where there is no window to register them on.
//
// The build stamp is carried over from this module's own address. A specifier
// does not inherit it and the server only stamps static imports, so without
// this the browser would fetch an unstamped second copy of net.js with its own
// connection state, and the offline worker - which keeps the stamped address -
// would have nothing to answer with.
function network() {
  const here = new URL(import.meta.url);
  return import(new URL('./net.js' + here.search, here).href);
}

/**
 * fetchSpeech renders one line and hands back the audio without playing it,
 * for the caller that needs the sound before the moment it is needed.
 * It throws one sentence a person can read.
 */
export async function fetchSpeech(text) {
  const content = plainSpeech(text);
  if (!content) return null;
  const { request, HttpError } = await network();
  // A stalled connection must not leave the answer unsaid for ever. The
  // request gets a deadline of its own, long enough for this much text, and
  // gives up out loud instead. It is passed as the signal rather than as the
  // timeout, because the timeout is per attempt and this one is the whole
  // wait; the per attempt timer is turned off so only this deadline decides.
  const controller = new AbortController();
  const limit = speechDeadline(content.length);
  const deadline = setTimeout(() => controller.abort(), limit);
  let res;
  try {
    // raw hands back the response itself, because what comes back is audio
    // rather than JSON. One attempt only: a render that failed halfway is not
    // worth the minutes a retry would spend before the listener hears
    // anything, and the reason is more useful than another wait.
    res = await request('/api/voice/speak', {
      method: 'POST',
      body: { text: content },
      raw: true,
      attempts: 1,
      signal: controller.signal,
      timeout: 0,
    });
  } catch (err) {
    // 503 is the voice saying it is still installing itself, which is progress
    // rather than a failure and is shown as such. The sentence is the server's
    // own: while the voice is installing it carries how far it has got.
    if (err instanceof HttpError) throw speechError(err.message, err.status === 503 ? '' : 'error');
    throw speechError(err && err.name === 'AbortError'
      ? 'The voice did not answer within ' + Math.round(limit / 1000) + ' seconds.'
      : 'The server could not be reached, so nothing was read out loud.', 'error');
  } finally {
    clearTimeout(deadline);
  }
  return await res.blob();
}

// play resolves when the audio has finished. The object URL is released on the
// way out: a page that reads every answer out loud would otherwise hold on to
// every answer it ever read.
function play(blob) {
  const url = URL.createObjectURL(blob);
  return new Promise((resolve) => {
    const audio = new Audio(url);
    currentAudio = audio;
    audio.onended = audio.onerror = () => {
      URL.revokeObjectURL(url);
      if (currentAudio === audio) currentAudio = null;
      resolve();
    };
    audio.play().catch(() => resolve());
  });
}

/**
 * speak reads text out loud and resolves when playback finished. Which voice
 * that is and how fast it reads are the server's business - it renders with
 * what the dashboard has stored - so there is nothing to pass here.
 */
export async function speak(text) {
  if (!plainSpeech(text)) return;
  // A microphone is open, so this is not the moment to talk: the recording
  // would hear the voice and the transcript would come back with the answer
  // read into it. Nothing is queued either - what was worth saying was worth
  // saying now, and a sentence read out minutes later answers a question
  // nobody is still holding.
  if (dictations > 0) return;
  stopSpeaking();
  const mine = generation;
  speakingFlag = true;
  try {
    const blob = await fetchSpeech(text);
    if (mine !== generation) return;
    await play(blob);
  } catch (err) {
    // The reason is both told to the page, once per distinct reason, and
    // handed back to the caller: an answer that reads silently is a bug the
    // listener has to hear about, and the test button has to fail out loud
    // every time it is pressed.
    if (mine === generation) reportError(err.message, speechKind(err));
    throw err;
  } finally {
    if (mine === generation) speakingFlag = false;
  }
}

/**
 * speechKind is the toast kind a failed render should be shown as. Anything
 * that did not come back from the server with a reason of its own is an error.
 */
export function speechKind(err) {
  return err && typeof err.kind === 'string' ? err.kind : 'error';
}

/** playSpeech plays audio that fetchSpeech returned earlier. */
export async function playSpeech(blob) {
  if (!blob) return;
  stopSpeaking();
  const mine = generation;
  speakingFlag = true;
  try {
    await play(blob);
  } finally {
    if (mine === generation) speakingFlag = false;
  }
}

// plainSpeech strips markdown so the voice does not read punctuation out loud.
export function plainSpeech(text) {
  return String(text || '')
    .replace(/```[\s\S]*?```/g, ' code block ')
    .replace(/`([^`]+)`/g, '$1')
    .replace(/!\[[^\]]*\]\([^)]*\)/g, '')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/https?:\/\/\S+/g, ' link ')
    .replace(/[*_#>|]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}
