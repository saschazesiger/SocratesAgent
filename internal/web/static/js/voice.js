// Voice input and output.
//
// Recording captures raw PCM through the Web Audio API and encodes a 16 kHz
// mono WAV in the browser, because that is the one format both OpenRouter's
// audio capable chat models and OpenAI compatible transcription endpoints
// accept. Playback prefers a configured TTS endpoint and falls back to the
// speech synthesis built into the browser.

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

export class Recorder {
  constructor() {
    this.recording = false;
    this.startedAt = 0;
  }

  static get supported() {
    return !!(navigator.mediaDevices && navigator.mediaDevices.getUserMedia &&
      (window.AudioContext || window.webkitAudioContext));
  }

  async start() {
    if (this.recording) return;
    if (!Recorder.supported) {
      throw new Error('This browser blocks microphone access here. Open Socrates on localhost or over https.');
    }
    this.stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
    const AudioCtx = window.AudioContext || window.webkitAudioContext;
    this.ctx = new AudioCtx();
    if (this.ctx.state === 'suspended') await this.ctx.resume();
    this.sampleRate = this.ctx.sampleRate;
    this.chunks = [];
    this.recording = true;
    this.startedAt = Date.now();

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

  async stop() {
    if (!this.recording) return null;
    this.recording = false;
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
      bytes: wav.byteLength,
    };
  }
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

/* ------------------------------------------------------------- language */

// Socrates speaks one language, chosen in the admin dashboard. Which one it is
// decides which installed voice reads an answer out loud, and a German
// sentence read by an English voice is the one thing about voice mode nobody
// forgives.

const LANGUAGE_TAGS = { en: 'en-US', de: 'de-DE' };
const DEFAULT_LANGUAGE = 'en';

// languageTag is the BCP 47 tag the speech synthesiser matches voices against.
// Anything it does not recognise - an empty preference, a setting written by an
// older version - reads as the default rather than as no language at all.
export function languageTag(language) {
  const base = String(language || '').toLowerCase().split(/[-_]/)[0];
  return LANGUAGE_TAGS[base] || LANGUAGE_TAGS[DEFAULT_LANGUAGE];
}

/* ------------------------------------------------------------- playback */

let currentAudio = null;
let speakingFlag = false;
// Every utterance belongs to a generation. Stopping bumps it, so the callbacks
// of the speech that was interrupted quietly stop instead of queueing the rest
// of a cancelled answer on top of the new one.
let generation = 0;

export function isSpeaking() {
  return speakingFlag || (window.speechSynthesis && window.speechSynthesis.speaking);
}

export function stopSpeaking() {
  generation++;
  speakingFlag = false;
  if (currentAudio) {
    try { currentAudio.pause(); } catch { /* ignore */ }
    currentAudio = null;
  }
  if (window.speechSynthesis) {
    try { window.speechSynthesis.cancel(); } catch { /* ignore */ }
  }
}

// speak reads text out loud and resolves when playback finished.
export async function speak(text, options = {}) {
  const content = plainSpeech(text);
  if (!content) return;
  const tag = languageTag(options.lang);
  stopSpeaking();
  const mine = generation;
  speakingFlag = true;
  try {
    // A stalled connection must not leave the answer unsaid. The request gets a
    // deadline of its own, and missing it falls through to the voice built into
    // the browser, which needs no network at all.
    const controller = new AbortController();
    const deadline = setTimeout(() => controller.abort(), 20000);
    let res;
    try {
      res = await fetch('/api/voice/speak', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        signal: controller.signal,
        body: JSON.stringify({ text: content }),
      });
    } finally {
      clearTimeout(deadline);
    }
    if (mine !== generation) return;
    if (res.status === 204) return await browserSpeak(content, tag, options);
    if (!res.ok) throw new Error('tts endpoint failed');
    const blob = await res.blob();
    if (mine !== generation) return;
    const url = URL.createObjectURL(blob);
    await new Promise((resolve) => {
      const audio = new Audio(url);
      currentAudio = audio;
      audio.onended = audio.onerror = () => {
        URL.revokeObjectURL(url);
        if (currentAudio === audio) currentAudio = null;
        resolve();
      };
      audio.play().catch(() => resolve());
    });
  } catch {
    if (mine === generation) await browserSpeak(content, tag, options);
  } finally {
    if (mine === generation) speakingFlag = false;
  }
}

// voices resolves the installed voices. Chrome fills the list asynchronously,
// so asking for it too early hands back an empty array - and an empty array is
// how German text ends up being read by whatever default voice the browser
// happens to have, which is the accent problem in the first place.
function voices() {
  if (!window.speechSynthesis) return Promise.resolve([]);
  const ready = window.speechSynthesis.getVoices() || [];
  if (ready.length) return Promise.resolve(ready);
  return new Promise((resolve) => {
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      resolve(window.speechSynthesis.getVoices() || []);
    };
    try {
      window.speechSynthesis.addEventListener('voiceschanged', finish, { once: true });
    } catch { /* older browsers fire nothing; the timer below covers them */ }
    // Safari never fires the event when there is nothing left to load, so the
    // wait is capped rather than open ended.
    setTimeout(finish, 1200);
  });
}

function normalizedTag(voice) {
  return String(voice.lang || '').toLowerCase().replace('_', '-');
}

// rankVoice scores one candidate for a language. Exactness comes first, then
// whether the voice lives on the device: a remote voice sounds better but goes
// silent the moment the signal does, and voice mode is used in a moving car.
function rankVoice(voice, tag) {
  let score = 0;
  if (normalizedTag(voice) === tag.toLowerCase()) score += 4;
  if (voice.localService) score += 3;
  if (/google|natural|neural|premium|enhanced|siri/i.test(voice.name || '')) score += 2;
  if (voice.default) score += 1;
  return score;
}

// pickVoice returns the best installed voice for a language, or null when the
// device has none - in which case the utterance still carries the language tag
// and the browser gets to make its own choice.
function pickVoice(installed, tag) {
  const base = tag.toLowerCase().split('-')[0];
  const candidates = installed.filter((voice) => normalizedTag(voice).split('-')[0] === base);
  if (!candidates.length) return null;
  let best = candidates[0];
  let bestScore = rankVoice(best, tag);
  for (const voice of candidates.slice(1)) {
    const score = rankVoice(voice, tag);
    if (score > bestScore) {
      best = voice;
      bestScore = score;
    }
  }
  return best;
}

async function browserSpeak(text, tag, options = {}) {
  if (!window.speechSynthesis || !window.SpeechSynthesisUtterance) return;
  const mine = generation;
  const installed = await voices();
  if (mine !== generation) return;
  let voice = pickVoice(installed, tag);
  // Kept aside for the one failure a browser voice actually has: a voice that
  // is synthesised on a server needs the network, and losing it mid answer
  // would otherwise swallow the sentence.
  const offlineVoice = pickVoice(installed.filter((v) => v.localService), tag);

  return new Promise((resolve) => {
    // Chrome truncates long utterances, so read sentence by sentence.
    const parts = chunkSentences(text, 220);
    let index = 0;
    const done = () => {
      if (mine === generation) speakingFlag = false;
      resolve();
    };
    const next = () => {
      index++;
      say();
    };
    const say = () => {
      if (mine !== generation || index >= parts.length) {
        done();
        return;
      }
      const utterance = new SpeechSynthesisUtterance(parts[index]);
      // The tag is set whether or not a matching voice was found: without a
      // voice it is the only thing telling the engine which language this is.
      utterance.lang = voice ? voice.lang : tag;
      if (voice) utterance.voice = voice;
      utterance.rate = options.rate || 1;
      utterance.onend = next;
      utterance.onerror = (event) => {
        const reason = event && event.error;
        if (reason === 'canceled' || reason === 'interrupted') {
          done();
          return;
        }
        if (reason === 'network' && voice && !voice.localService && offlineVoice && offlineVoice !== voice) {
          // No signal: swap to a voice that lives on the device and read the
          // same sentence again rather than skipping it.
          voice = offlineVoice;
          say();
          return;
        }
        next();
      };
      window.speechSynthesis.speak(utterance);
    };
    say();
  });
}

function chunkSentences(text, max) {
  const sentences = text.split(/(?<=[.!?…])\s+/);
  const chunks = [];
  let current = '';
  for (const sentence of sentences) {
    if ((current + ' ' + sentence).trim().length > max && current) {
      chunks.push(current.trim());
      current = sentence;
    } else {
      current = (current + ' ' + sentence).trim();
    }
  }
  if (current.trim()) chunks.push(current.trim());
  return chunks.length ? chunks : [text];
}

// plainSpeech strips markdown so the synthesiser does not read punctuation.
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
