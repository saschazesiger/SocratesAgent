/**
 * The gestures besides a tap: a swipe, a hold (long press), a double tap or double click, a drag and the
 * mouse wheel, as W3C action sequences. On phones and tablets they are a finger on the native screen
 * (Appium's native context, coordinates in its points); on desktops the mouse in the page (CSS px).
 */
import type { ActionSequence } from "./webdriver";

export type Direction = "up" | "down" | "left" | "right";
export const DIRECTIONS: readonly Direction[] = ["up", "down", "left", "right"];

/** Where in the target (or the viewport) a gesture starts: CSS px from its top-left, or percent of its size. */
export interface At {
  x: number;
  y: number;
  xPercent: boolean;
  yPercent: boolean;
}

/** Parses `--at 40,120` (CSS px from the target's top-left) or `--at 10%,50%` (of its size); each axis on its own. */
export function parseAt(spec: string): At {
  const m = /^\s*(-?\d+(?:\.\d+)?)(%?)\s*,\s*(-?\d+(?:\.\d+)?)(%?)\s*$/.exec(spec);
  if (!m) throw new Error(`--at takes x,y in CSS px from the target's top-left (40,120) or in percent of its size (10%,50%), not ${spec}`);
  const at = { x: Number(m[1]), xPercent: m[2] === "%", y: Number(m[3]), yPercent: m[4] === "%" };
  if ((at.xPercent && (at.x < 0 || at.x > 100)) || (at.yPercent && (at.y < 0 || at.y > 100))) throw new Error(`--at percentages run from 0% to 100%, not ${spec}`);
  return at;
}

/** Parses `--by 120,-40` (CSS px right/down). */
export function parseBy(spec: string): { dx: number; dy: number } {
  const m = /^\s*(-?\d+(?:\.\d+)?)\s*,\s*(-?\d+(?:\.\d+)?)\s*$/.exec(spec);
  if (!m) throw new Error(`--by takes dx,dy in CSS px (120,0 to the right, 0,-80 up), not ${spec}`);
  return { dx: Number(m[1]), dy: Number(m[2]) };
}

export function parseDirection(word: string | undefined, command: string): Direction {
  const direction = (word ?? "").toLowerCase();
  if (!(DIRECTIONS as readonly string[]).includes(direction)) throw new Error(`qa ${command} up, down, left or right, then the target (TOOL.md), not ${word ?? "nothing"}`);
  return direction as Direction;
}

/** The unit vector of a direction (screen coordinates: y grows downwards). */
export function vector(direction: Direction): { x: number; y: number } {
  return { up: { x: 0, y: -1 }, down: { x: 0, y: 1 }, left: { x: -1, y: 0 }, right: { x: 1, y: 0 } }[direction];
}

export interface Point {
  x: number;
  y: number;
}

const round = (point: Point): Point => ({ x: Math.round(point.x), y: Math.round(point.y) });

/** How long a finger rests after it came down before a swipe moves (short: a longer rest is a long press). */
export const SWIPE_REST_MS = 40;
/** How long a swipe's finger rests before it lifts: it stops, so the scroll does not fling on. */
export const SWIPE_STOP_MS = 120;
/** A double tap's or double click's two presses: down this long, this far apart. */
export const DOUBLE_PRESS_MS = 60;
export const DOUBLE_GAP_MS = 110;

function finger(actions: Record<string, unknown>[]): ActionSequence {
  return { type: "pointer", id: "finger", parameters: { pointerType: "touch" }, actions };
}

function mouse(actions: Record<string, unknown>[]): ActionSequence {
  return { type: "pointer", id: "mouse", parameters: { pointerType: "mouse" }, actions };
}

const move = (point: Point, duration: number) => ({ type: "pointerMove", duration, origin: "viewport", ...round(point) });

/** A finger (touch) or the mouse moving from `from` to `to` in `ms`, pressed down; held `holdMs` before it moves. */
export function dragActions(kind: "touch" | "mouse", from: Point, to: Point, ms: number, holdMs = 0): ActionSequence[] {
  const actions = [move(from, 0), { type: "pointerDown", button: 0 }, { type: "pause", duration: holdMs || (kind === "touch" ? SWIPE_REST_MS : 0) }, move(to, ms), { type: "pause", duration: SWIPE_STOP_MS }, { type: "pointerUp", button: 0 }];
  return [kind === "touch" ? finger(actions) : mouse(actions)];
}

/** A press held still for `ms` (a long press with a finger, a long click with the mouse). */
export function holdActions(kind: "touch" | "mouse", at: Point, ms: number): ActionSequence[] {
  const actions = [move(at, 0), { type: "pointerDown", button: 0 }, { type: "pause", duration: ms }, { type: "pointerUp", button: 0 }];
  return [kind === "touch" ? finger(actions) : mouse(actions)];
}

/** One tap, or two in quick succession at the same point (a double tap, a double click). */
export function tapActions(kind: "touch" | "mouse", at: Point, double = false): ActionSequence[] {
  const press = [{ type: "pointerDown", button: 0 }, { type: "pause", duration: kind === "touch" ? 80 : 0 }, { type: "pointerUp", button: 0 }];
  const actions = double ? [move(at, 0), { type: "pointerDown", button: 0 }, { type: "pause", duration: DOUBLE_PRESS_MS }, { type: "pointerUp", button: 0 }, { type: "pause", duration: DOUBLE_GAP_MS }, { type: "pointerDown", button: 0 }, { type: "pause", duration: DOUBLE_PRESS_MS }, { type: "pointerUp", button: 0 }] : [move(at, 0), ...press];
  return [kind === "touch" ? finger(actions) : mouse(actions)];
}

/**
 * The mouse wheel turned at a point of the page: `amount` CSS px in the direction the content should move
 * towards (up: towards its top, as turning the wheel away from you), in notches of 100 px as a wheel sends them.
 */
export function wheelActions(at: Point, direction: Direction, amount: number): ActionSequence[] {
  const v = vector(direction);
  const notches = Math.max(1, Math.round(amount / 100));
  const step = amount / notches;
  const actions: Record<string, unknown>[] = [];
  for (let i = 0; i < notches; i++) {
    if (i) actions.push({ type: "pause", duration: 60 });
    actions.push({ type: "scroll", origin: "viewport", ...round(at), deltaX: Math.round(v.x * step), deltaY: Math.round(v.y * step), duration: 60 });
  }
  return [{ type: "wheel", id: "wheel", actions }];
}

/** Keeps a point inside a box with a margin (the finger must stay on the screen and off its edges). */
export function clampPoint(point: Point, box: { x: number; y: number; width: number; height: number }, margin: number): Point {
  return {
    x: Math.min(Math.max(point.x, box.x + margin), box.x + box.width - margin),
    y: Math.min(Math.max(point.y, box.y + margin), box.y + box.height - margin),
  };
}

/** The far end of a swipe of `distance` from `start` in `direction`, kept inside `box`. */
export function swipeEnd(start: Point, direction: Direction, distance: number, box: { x: number; y: number; width: number; height: number }, margin: number): Point {
  const v = vector(direction);
  return clampPoint({ x: start.x + v.x * distance, y: start.y + v.y * distance }, box, margin);
}
