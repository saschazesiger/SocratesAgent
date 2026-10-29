import { decodePng } from "./png";

export interface Markers {
  imageWidth: number;
  first: { x: number; y: number };
  second: { x: number; y: number };
}

const isMagenta = (r: number, g: number, b: number) => r > 220 && g < 60 && b > 220;

/** Finds the two calibration markers (src/browser/marker.js) in a screenshot: their top-left corners in image pixels. */
export function findMarkers(png: Buffer): Markers | null {
  const info = decodePng(png);
  const { data } = info;
  const blobs: { x: number; y: number }[] = [];
  for (let y = 0; y < info.height; y++) {
    for (let x = 0; x < info.width; x++) {
      const i = (y * info.width + x) * 3;
      if (!isMagenta(data[i]!, data[i + 1]!, data[i + 2]!)) continue;
      if (blobs.some((blob) => Math.abs(blob.x - x) < 40 && Math.abs(blob.y - y) < 40)) continue;
      blobs.push({ x, y });
    }
  }
  // Magenta in the page itself (an image) must not fool it: the markers are the pair as far apart on both axes.
  let best: Markers | null = null;
  for (const first of blobs) {
    for (const second of blobs) {
      const dx = second.x - first.x;
      const dy = second.y - first.y;
      if (dx > 20 && Math.abs(dx - dy) <= Math.max(3, dx * 0.04) && (!best || first.x + first.y < best.first.x + best.first.y)) best = { imageWidth: info.width, first, second };
    }
  }
  return best;
}
