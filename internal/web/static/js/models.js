// The OpenRouter model catalogue, fetched once per page load and shared by
// every model picker. OpenRouter serves the list without an API key, so the
// dropdowns are populated even before the key is filled in.

import { api } from './api.js';

let entries = [];
let loading = null;
const listeners = new Set();

// price renders a per million token price the way OpenRouter quotes it.
function price(raw) {
  const value = Number(raw);
  if (!Number.isFinite(value) || value <= 0) return '';
  const perMillion = value * 1e6;
  if (perMillion < 1) return '$' + perMillion.toFixed(2) + '/M';
  if (perMillion < 100) return '$' + perMillion.toFixed(1) + '/M';
  return '$' + Math.round(perMillion) + '/M';
}

function context(length) {
  const value = Number(length);
  if (!Number.isFinite(value) || value <= 0) return '';
  if (value >= 1e6) return (value / 1e6).toFixed(value % 1e6 === 0 ? 0 : 1) + 'M ctx';
  if (value >= 1000) return Math.round(value / 1000) + 'k ctx';
  return value + ' ctx';
}

// shape turns an API entry into what the dropdown shows: the provider as a
// group heading, the name as the label, and size and price as the hint.
//
// A transcription model is billed by the second by one provider and by the
// token by the next, and the catalogue does not say which - so it is labelled
// for what it does instead of with a price that would be off by a thousand.
// A voice model is labelled with how many voices it has, because that is the
// next thing to be picked and the number is what says whether the list is
// worth opening.
function shape(model) {
  const id = model.id || '';
  const [provider] = id.split('/');
  const produces = model.output_modalities || [];
  const voices = model.supported_voices || [];
  let bits;
  if (produces.includes('transcription')) bits = ['transcribes'];
  else if (produces.includes('speech')) {
    bits = [voices.length ? voices.length + ' voices' : '', price(model.pricing && model.pricing.prompt)];
  } else bits = [context(model.context_length), price(model.pricing && model.pricing.prompt)];
  return {
    value: id,
    label: model.name || id,
    hint: [id, ...bits.filter(Boolean)].join(' · '),
    group: provider || 'other',
    modalities: model.input_modalities || [],
    produces,
    voices,
  };
}

/** load fetches the catalogue once and remembers it. */
export function load() {
  if (loading) return loading;
  loading = api('/api/models')
    .then((data) => {
      entries = (data.models || []).map(shape);
      entries.sort((a, b) => a.group.localeCompare(b.group) || a.label.localeCompare(b.label));
      listeners.forEach((fn) => fn(entries));
      return entries;
    })
    .catch((err) => {
      // A missing catalogue must not break the dashboard: every picker still
      // works as a text field.
      loading = null;
      throw err;
    });
  return loading;
}

/** all returns the catalogue loaded so far. */
export function all() { return entries; }

/**
 * chat returns the models that can hold a conversation. A transcription model
 * answers on its own endpoint and cannot complete a chat at all, so offering
 * one where an answering model is asked for is offering a dead end.
 */
export function chat() {
  const talking = entries.filter((entry) => !entry.produces.includes('transcription'));
  return talking.length ? talking : entries;
}

/**
 * audio returns the models that can turn a recording into words: a dedicated
 * transcription model, or a chat model that accepts audio. Anything else is a
 * 400 from the provider the first time the microphone is used, which is what
 * the filter exists to prevent.
 */
export function audio() {
  const heard = entries.filter((entry) => entry.modalities.includes('audio'));
  return heard.length ? heard : entries;
}

/**
 * speech returns the models that read text out loud. There is no fallback to
 * the whole catalogue here: a chat model handed to /audio/speech is a 400, and
 * an empty list is the honest answer while the catalogue is still on its way.
 */
export function speech() {
  return entries.filter((entry) => entry.produces.includes('speech'));
}

/**
 * voicesOf lists the voices one speech model answers to. Every one of them
 * refuses a request that names a voice it does not have, and the names have
 * nothing in common between models - "aura-2-lara-de", "Zephyr",
 * "en_paul_neutral" - so this list is the only safe thing to offer.
 *
 * A model the catalogue publishes no voices for gets an empty list, and the
 * picker falls back to letting the name be typed.
 */
export function voicesOf(id) {
  const found = entries.find((entry) => entry.value === id);
  return found ? found.voices : [];
}

/** onLoad registers a callback for when the catalogue arrives. */
export function onLoad(fn) {
  listeners.add(fn);
  if (entries.length) fn(entries);
  return () => listeners.delete(fn);
}

/** count is how many models are known. */
export function count() { return entries.length; }
