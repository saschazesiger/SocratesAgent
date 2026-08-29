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

/* ------------------------------------------------------------- playback */

let currentAudio = null;
let speakingFlag = false;

export function isSpeaking() {
  return speakingFlag || (window.speechSynthesis && window.speechSynthesis.speaking);
}

export function stopSpeaking() {
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
  stopSpeaking();
  speakingFlag = true;
  try {
    const res = await fetch('/api/voice/speak', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ text: content }),
    });
    if (res.status === 204) return await browserSpeak(content, options);
    if (!res.ok) throw new Error('tts endpoint failed');
    const blob = await res.blob();
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
    await browserSpeak(content, options);
  } finally {
    speakingFlag = false;
  }
}

function pickVoice(lang) {
  if (!window.speechSynthesis) return null;
  const voices = window.speechSynthesis.getVoices() || [];
  if (!voices.length) return null;
  const wanted = (lang || document.documentElement.lang || navigator.language || 'en').toLowerCase();
  const base = wanted.split('-')[0];
  return voices.find((v) => v.lang && v.lang.toLowerCase() === wanted)
    || voices.find((v) => v.lang && v.lang.toLowerCase().startsWith(base))
    || voices.find((v) => v.default)
    || voices[0];
}

function browserSpeak(text, options = {}) {
  return new Promise((resolve) => {
    if (!window.speechSynthesis || !window.SpeechSynthesisUtterance) {
      resolve();
      return;
    }
    // Chrome truncates long utterances, so read sentence by sentence.
    const parts = chunkSentences(text, 220);
    const voice = pickVoice(options.lang);
    let index = 0;
    const next = () => {
      if (index >= parts.length) {
        speakingFlag = false;
        resolve();
        return;
      }
      const utterance = new SpeechSynthesisUtterance(parts[index++]);
      if (voice) {
        utterance.voice = voice;
        utterance.lang = voice.lang;
      }
      utterance.rate = options.rate || 1;
      utterance.onend = next;
      utterance.onerror = next;
      window.speechSynthesis.speak(utterance);
    };
    next();
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
