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
function shape(model) {
  const id = model.id || '';
  const [provider] = id.split('/');
  const produces = model.output_modalities || [];
  const bits = produces.includes('transcription')
    ? ['transcribes']
    : [context(model.context_length), price(model.pricing && model.pricing.prompt)];
  return {
    value: id,
    label: model.name || id,
    hint: [id, ...bits.filter(Boolean)].join(' · '),
    group: provider || 'other',
    modalities: model.input_modalities || [],
    produces,
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

/** onLoad registers a callback for when the catalogue arrives. */
export function onLoad(fn) {
  listeners.add(fn);
  if (entries.length) fn(entries);
  return () => listeners.delete(fn);
}

/** count is how many models are known. */
export function count() { return entries.length; }
