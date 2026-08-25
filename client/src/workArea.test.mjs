import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// Keep this copy in lockstep with workArea.ts. Node 22 here cannot import .ts.
const WINDOWS_TASKBAR_RESERVE_PX = 48;

function workAreaBottomInsetPx(input) {
  const safe = Math.max(0, input.safeAreaBottom ?? 0);
  const screenHeight = Math.max(0, input.screenHeight);
  const availHeight = Math.max(0, input.availHeight);
  const innerHeight = Math.max(0, input.innerHeight);
  let taskbar = 0;
  const windowCoversScreen =
    screenHeight > 0 && innerHeight + 8 >= screenHeight;
  const workAreaShorter =
    screenHeight > 0 && availHeight > 0 && availHeight < screenHeight;
  if (windowCoversScreen || (workAreaShorter && innerHeight + 8 >= availHeight)) {
    const reported = workAreaShorter
      ? screenHeight - availHeight
      : WINDOWS_TASKBAR_RESERVE_PX;
    taskbar = Math.min(96, Math.max(WINDOWS_TASKBAR_RESERVE_PX, reported));
  }
  return Math.max(12, safe, taskbar);
}

function readSafeAreaBottomPx(style) {
  for (const name of ["padding-bottom", "--safe-area-inset-bottom"]) {
    const raw = style.getPropertyValue(name);
    const parsed = Number.parseFloat(raw);
    if (Number.isFinite(parsed)) return parsed;
  }
  return 0;
}

test("workArea.ts still exports the same algorithm", () => {
  const src = readFileSync(
    join(dirname(fileURLToPath(import.meta.url)), "workArea.ts"),
    "utf8",
  );
  assert.match(src, /WINDOWS_TASKBAR_RESERVE_PX = 48/);
  assert.match(src, /innerHeight \+ 8 >= screenHeight/);
  assert.match(src, /Math\.min\(96, Math\.max\(WINDOWS_TASKBAR_RESERVE_PX, reported\)\)/);
  assert.match(src, /Math\.max\(12, safe, taskbar\)/);
});

test("reserves 12px when the window sits in a large work area", () => {
  assert.equal(
    workAreaBottomInsetPx({
      innerHeight: 900,
      screenHeight: 1440,
      availHeight: 1400,
    }),
    12,
  );
});

test("reserves ~48px when the WebView is as tall as a 1280x800 screen", () => {
  const inset = workAreaBottomInsetPx({
    innerHeight: 820,
    screenHeight: 800,
    availHeight: 752,
  });
  assert.equal(inset, 48);
  assert.equal(inset, WINDOWS_TASKBAR_RESERVE_PX);
});

test("uses the reported taskbar height when larger than 48px", () => {
  assert.equal(
    workAreaBottomInsetPx({
      innerHeight: 800,
      screenHeight: 800,
      availHeight: 720,
    }),
    80,
  );
});

test("keeps safe-area when it exceeds the taskbar reserve", () => {
  assert.equal(
    workAreaBottomInsetPx({
      innerHeight: 700,
      screenHeight: 1440,
      availHeight: 1400,
      safeAreaBottom: 34,
    }),
    34,
  );
});

test("reserves the taskbar when maximized into the work area", () => {
  assert.equal(
    workAreaBottomInsetPx({
      innerHeight: 752,
      screenHeight: 800,
      availHeight: 752,
    }),
    48,
  );
});

test("caps the taskbar reserve at 96px", () => {
  assert.equal(
    workAreaBottomInsetPx({
      innerHeight: 800,
      screenHeight: 800,
      availHeight: 680,
    }),
    96,
  );
});

test("reads safe-area pixels from computed padding-bottom", () => {
  assert.equal(
    readSafeAreaBottomPx({
      getPropertyValue(name) {
        return name === "padding-bottom" ? "34px" : "";
      },
    }),
    34,
  );
});
