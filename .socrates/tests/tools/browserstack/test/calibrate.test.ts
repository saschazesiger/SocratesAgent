import assert from "node:assert/strict";
import { test } from "node:test";
import { findMarkers } from "../src/calibrate";
import { decodePng } from "../src/png";
import { encodePng } from "./png";

const MAGENTA: [number, number, number] = [255, 0, 255];
const WHITE: [number, number, number] = [255, 255, 255];

/** A phone screen with 21 px marker tiles at the given corners and 60 px magenta blobs (a page image) at `extra`. */
function screen(markers: [number, number][], extra: [number, number][] = []): Buffer {
  const inside = (x: number, y: number, [left, top]: [number, number], size: number) => x >= left && x < left + size && y >= top && y < top + size;
  return encodePng(1080, 2400, (x, y) => (markers.some((at) => inside(x, y, at, 21)) || extra.some((at) => inside(x, y, at, 60)) ? MAGENTA : WHITE));
}

test("the decoder reads back every filter", () => {
  const png = encodePng(7, 6, (x, y) => [x * 30, y * 40, (x * y * 13) % 256]);
  const image = decodePng(png);
  assert.equal(image.width, 7);
  assert.equal(image.height, 6);
  for (let y = 0; y < 6; y++) for (let x = 0; x < 7; x++) assert.deepEqual([...image.data.subarray((y * 7 + x) * 3, (y * 7 + x) * 3 + 3)], [x * 30, y * 40, (x * y * 13) % 256]);
  assert.throws(() => decodePng(Buffer.from("not a png")), /not a PNG/);
});

test("finds both markers below a browser toolbar", () => {
  const found = findMarkers(screen([[0, 323], [263, 586]]));
  assert.deepEqual(found, { imageWidth: 1080, first: { x: 0, y: 323 }, second: { x: 263, y: 586 } });
});

test("magenta in the page does not fool it", () => {
  const found = findMarkers(screen([[0, 177], [300, 477]], [[700, 1500]]));
  assert.deepEqual(found?.first, { x: 0, y: 177 });
  assert.deepEqual(found?.second, { x: 300, y: 477 });
});

test("no markers, no calibration", () => {
  assert.equal(findMarkers(screen([])), null);
});
