import assert from "node:assert/strict";
import { test } from "node:test";
import { clampPoint, dragActions, holdActions, parseAt, parseBy, parseDirection, swipeEnd, tapActions, wheelActions } from "../src/gesture";

test("--at takes CSS px or percent per axis", () => {
  assert.deepEqual(parseAt("40,120"), { x: 40, xPercent: false, y: 120, yPercent: false });
  assert.deepEqual(parseAt("10%, 50%"), { x: 10, xPercent: true, y: 50, yPercent: true });
  assert.deepEqual(parseAt("5%,30"), { x: 5, xPercent: true, y: 30, yPercent: false });
  for (const bad of ["40", "a,b", "120%,5", "10;20"]) assert.throws(() => parseAt(bad), /--at/, bad);
  assert.deepEqual(parseBy("120,-40"), { dx: 120, dy: -40 });
  assert.throws(() => parseBy("120"), /--by/);
  assert.equal(parseDirection("Up", "swipe"), "up");
  assert.throws(() => parseDirection("sideways", "swipe"), /up, down, left or right/);
});

test("a swipe is one finger down, moving and lifting after a rest; it stays inside the page", () => {
  const box = { x: 0, y: 100, width: 400, height: 600 };
  assert.deepEqual(swipeEnd({ x: 200, y: 400 }, "down", 1000, box, 10), { x: 200, y: 690 });
  assert.deepEqual(swipeEnd({ x: 200, y: 400 }, "left", 50, box, 10), { x: 150, y: 400 });
  const [finger] = dragActions("touch", { x: 200.4, y: 400 }, { x: 200, y: 690 }, 400);
  assert.equal(finger!.parameters!.pointerType, "touch");
  assert.deepEqual(
    finger!.actions.map((action) => action.type),
    ["pointerMove", "pointerDown", "pause", "pointerMove", "pause", "pointerUp"],
  );
  assert.equal(finger!.actions[0]!.x, 200);
  assert.equal(finger!.actions[3]!.duration, 400);
  assert.deepEqual(clampPoint({ x: -5, y: 900 }, box, 4), { x: 4, y: 696 });
});

test("a hold rests as long as asked; a double tap presses twice; a drag with a hold rests before it moves", () => {
  const [hold] = holdActions("touch", { x: 10, y: 20 }, 1000);
  assert.deepEqual(hold!.actions[2], { type: "pause", duration: 1000 });
  const [double] = tapActions("mouse", { x: 10, y: 20 }, true);
  assert.equal(double!.parameters!.pointerType, "mouse");
  assert.equal(double!.actions.filter((action) => action.type === "pointerDown").length, 2);
  const [drag] = dragActions("touch", { x: 0, y: 0 }, { x: 0, y: 50 }, 300, 600);
  assert.deepEqual(drag!.actions[2], { type: "pause", duration: 600 });
});

test("the wheel turns in notches of 100 px; up shows what is above", () => {
  const [wheel] = wheelActions({ x: 100, y: 200 }, "up", 300);
  assert.equal(wheel!.type, "wheel");
  const scrolls = wheel!.actions.filter((action) => action.type === "scroll");
  assert.equal(scrolls.length, 3);
  assert.deepEqual(scrolls[0], { type: "scroll", origin: "viewport", x: 100, y: 200, deltaX: 0, deltaY: -100, duration: 60 });
  const [right] = wheelActions({ x: 0, y: 0 }, "right", 50);
  assert.equal(right!.actions[0]!.deltaX, 50);
});
