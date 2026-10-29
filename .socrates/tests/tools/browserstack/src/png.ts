/**
 * A PNG decoder for the screenshots the devices send (8 bit greyscale, RGB, palette, grey with alpha and
 * RGBA, not interlaced), to RGB pixels: all the calibration of a native tap needs, without an image library.
 */
import { inflateSync } from "node:zlib";

export interface RgbImage {
  width: number;
  height: number;
  /** width * height * 3 bytes, row by row. */
  data: Uint8Array;
}

const SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
/** Channels per colour type. */
const CHANNELS: Record<number, number> = { 0: 1, 2: 3, 3: 1, 4: 2, 6: 4 };

function paeth(a: number, b: number, c: number): number {
  const p = a + b - c;
  const pa = Math.abs(p - a);
  const pb = Math.abs(p - b);
  const pc = Math.abs(p - c);
  return pa <= pb && pa <= pc ? a : pb <= pc ? b : c;
}

/** Reverses the per-row filters (PNG spec, section 9) into a fresh buffer. */
export function unfilter(raw: Uint8Array, width: number, height: number, bpp: number): Uint8Array {
  const stride = width * bpp;
  const out = new Uint8Array(stride * height);
  for (let y = 0; y < height; y++) {
    const filter = raw[y * (stride + 1)]!;
    const source = y * (stride + 1) + 1;
    const row = y * stride;
    for (let x = 0; x < stride; x++) {
      const value = raw[source + x]!;
      const left = x >= bpp ? out[row + x - bpp]! : 0;
      const up = y > 0 ? out[row - stride + x]! : 0;
      const upLeft = y > 0 && x >= bpp ? out[row - stride + x - bpp]! : 0;
      let predicted: number;
      if (filter === 0) predicted = 0;
      else if (filter === 1) predicted = left;
      else if (filter === 2) predicted = up;
      else if (filter === 3) predicted = (left + up) >> 1;
      else if (filter === 4) predicted = paeth(left, up, upLeft);
      else throw new Error(`png: unknown filter ${filter} in row ${y}`);
      out[row + x] = (value + predicted) & 0xff;
    }
  }
  return out;
}

export function decodePng(png: Buffer): RgbImage {
  if (png.length < 8 || !png.subarray(0, 8).equals(SIGNATURE)) throw new Error("png: not a PNG image");
  let width = 0;
  let height = 0;
  let colorType = -1;
  let palette: Buffer | null = null;
  const chunks: Buffer[] = [];
  for (let at = 8; at + 8 <= png.length; ) {
    const length = png.readUInt32BE(at);
    const type = png.toString("latin1", at + 4, at + 8);
    const body = png.subarray(at + 8, at + 8 + length);
    if (type === "IHDR") {
      width = body.readUInt32BE(0);
      height = body.readUInt32BE(4);
      const depth = body[8]!;
      colorType = body[9]!;
      if (depth !== 8) throw new Error(`png: bit depth ${depth} is not supported`);
      if (body[12] !== 0) throw new Error("png: interlaced images are not supported");
      if (!(colorType in CHANNELS)) throw new Error(`png: colour type ${colorType} is not supported`);
    } else if (type === "PLTE") palette = body;
    else if (type === "IDAT") chunks.push(body);
    else if (type === "IEND") break;
    at += 12 + length;
  }
  if (!width || !height || !chunks.length) throw new Error("png: no image data");
  const bpp = CHANNELS[colorType]!;
  const pixels = unfilter(inflateSync(Buffer.concat(chunks)), width, height, bpp);
  const data = new Uint8Array(width * height * 3);
  for (let i = 0, o = 0; i < pixels.length; i += bpp, o += 3) {
    if (colorType === 3) {
      if (!palette) throw new Error("png: palette image without PLTE");
      const entry = pixels[i]! * 3;
      data[o] = palette[entry]!;
      data[o + 1] = palette[entry + 1]!;
      data[o + 2] = palette[entry + 2]!;
    } else if (bpp <= 2) {
      data[o] = data[o + 1] = data[o + 2] = pixels[i]!;
    } else {
      data[o] = pixels[i]!;
      data[o + 1] = pixels[i + 1]!;
      data[o + 2] = pixels[i + 2]!;
    }
  }
  return { width, height, data };
}
