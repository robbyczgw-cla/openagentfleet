import http from "node:http";
import { execFile } from "node:child_process";
import { randomUUID, timingSafeEqual } from "node:crypto";
import { readFile, rm } from "node:fs/promises";
import { chromium } from "playwright-core";
import { promisify } from "node:util";

const port = Number.parseInt(process.env.COMPUTER_PORT ?? "9223", 10);
const cdpEndpoint = process.env.CDP_ENDPOINT ?? "http://127.0.0.1:9222";
const maxBodyBytes = 128 * 1024;
const nativeHandoffTargetTTL = 2 * 60 * 1000;
const controlToken = process.env.COMPUTER_CONTROL_TOKEN;
if (typeof controlToken !== "string" || controlToken.length < 32) {
  throw new Error("COMPUTER_CONTROL_TOKEN is required");
}
let browser;
let page;
let operation = Promise.resolve();
let lastBrowserViewStatus;
let desktopViewport;
const computerID = randomUUID();
const nativeHandoffTargets = new Map();
const execFileAsync = promisify(execFile);

function serialized(task) {
  const next = operation.then(task, task);
  operation = next.catch(() => undefined);
  return next;
}

function json(res, status, value) {
  res.writeHead(status, {
    "cache-control": "no-store",
    "content-type": "application/json; charset=utf-8",
  });
  res.end(JSON.stringify(value));
}

function authorized(req) {
  const expected = `Bearer ${controlToken}`;
  const received = req.headers.authorization;
  return typeof received === "string"
    && received.length === expected.length
    && timingSafeEqual(Buffer.from(received), Buffer.from(expected));
}

function fail(res, status, message) {
  json(res, status, { error: message });
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    let body = "";
    req.setEncoding("utf8");
    req.on("data", (chunk) => {
      body += chunk;
      if (Buffer.byteLength(body) > maxBodyBytes) {
        reject(new Error("request body is too large"));
        req.destroy();
      }
    });
    req.on("end", () => resolve(body));
    req.on("error", reject);
  });
}

async function connect() {
  if (browser?.isConnected()) return browser;
  for (let attempt = 0; attempt < 60; attempt += 1) {
    try {
      browser = await chromium.connectOverCDP(cdpEndpoint);
      return browser;
    } catch (error) {
      if (attempt === 59) throw error;
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
  }
  throw new Error("Chromium CDP connection failed");
}

async function currentPage() {
  const connected = await connect();
  const context = connected.contexts()[0] ?? await connected.newContext();
  const pages = context.pages();
  page = pages[0] ?? await context.newPage();
  return page;
}

async function browserTargetID(activePage) {
  const session = await activePage.context().newCDPSession(activePage);
  try {
    const result = await session.send("Target.getTargetInfo");
    const targetID = result?.targetInfo?.targetId;
    if (typeof targetID !== "string" || targetID.length === 0) {
      throw new Error("browser target is unavailable");
    }
    return targetID;
  } finally {
    await session.detach().catch(() => undefined);
  }
}

function originForURL(value) {
  try {
    return new URL(value).origin;
  } catch {
    return "";
  }
}

async function discardNativeHandoffTarget(id) {
  const binding = nativeHandoffTargets.get(id);
  nativeHandoffTargets.delete(id);
  await binding?.element.dispose().catch(() => undefined);
}

async function discardExpiredNativeHandoffTargets() {
  const expired = [];
  for (const [id, binding] of nativeHandoffTargets) {
    if (binding.expiresAt <= Date.now()) expired.push(id);
  }
  await Promise.all(expired.map((id) => discardNativeHandoffTarget(id)));
}

async function browserTargetBinding() {
  await discardExpiredNativeHandoffTargets();
  const activePage = await currentPage();
  const cdpTargetID = await browserTargetID(activePage);
  const focusedHandle = await activePage.evaluateHandle(() => document.activeElement);
  const element = focusedHandle.asElement();
  if (!element) {
    await focusedHandle.dispose().catch(() => undefined);
    throw new Error("focus a browser password or code field before secure entry");
  }
  const usable = await element.evaluate((candidate) => {
    const supported = candidate instanceof HTMLInputElement || candidate instanceof HTMLTextAreaElement;
    return supported
      && candidate.isConnected
      && document.activeElement === candidate
      && !candidate.disabled
      && !candidate.readOnly
      && (!(candidate instanceof HTMLInputElement) || candidate.type !== "hidden");
  }).catch(() => false);
  const documentURL = activePage.url();
  const origin = originForURL(documentURL);
  if (!usable || !origin) {
    await element.dispose().catch(() => undefined);
    throw new Error("focus a browser password or code field before secure entry");
  }
  const bindingID = `field_${randomUUID()}`;
  nativeHandoffTargets.set(bindingID, {
    cdpTargetID,
    documentURL,
    origin,
    page: activePage,
    element,
    expiresAt: Date.now() + nativeHandoffTargetTTL,
  });
  return { computer_id: computerID, target_id: bindingID };
}

async function desktopTargetBinding() {
  const { stdout } = await execFileAsync("xdotool", ["getactivewindow"], {
    env: { ...process.env, DISPLAY: process.env.DISPLAY ?? ":99" },
  });
  const targetID = stdout.trim();
  if (!/^[0-9]+$/.test(targetID)) {
    throw new Error("desktop target is unavailable");
  }
  return { computer_id: computerID, target_id: targetID };
}

async function targetBinding(surface) {
  try {
    if (surface === "browser") return await browserTargetBinding();
    if (surface === "desktop") return await desktopTargetBinding();
    throw new Error("unsupported target surface");
  } catch {
    throw new Error(`${surface} target is unavailable`);
  }
}

async function deliverNativeBrowserHandoff(request) {
  if (typeof request.computer_id !== "string" || typeof request.target_id !== "string") {
    throw new Error("secure target binding is required");
  }
  const bindingID = request.target_id;
  try {
    const binding = nativeHandoffTargets.get(bindingID);
    if (!binding || binding.expiresAt <= Date.now() || request.computer_id !== computerID) {
      throw new Error("secure target changed");
    }
    const activePage = await currentPage();
    if (activePage !== binding.page
      || activePage.url() !== binding.documentURL
      || originForURL(activePage.url()) !== binding.origin
      || await browserTargetID(activePage) !== binding.cdpTargetID) {
      throw new Error("secure target changed");
    }
    const stillFocused = await binding.element.evaluate((candidate) => {
      const supported = candidate instanceof HTMLInputElement || candidate instanceof HTMLTextAreaElement;
      return supported
        && candidate.isConnected
        && document.activeElement === candidate
        && !candidate.disabled
        && !candidate.readOnly
        && (!(candidate instanceof HTMLInputElement) || candidate.type !== "hidden");
    }).catch(() => false);
    if (!stillFocused) throw new Error("secure target changed");
    await binding.element.evaluate((candidate, text) => {
      if (!(candidate instanceof HTMLInputElement) && !(candidate instanceof HTMLTextAreaElement)) {
        throw new Error("secure target changed");
      }
      const prototype = candidate instanceof HTMLInputElement ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
      const setter = Object.getOwnPropertyDescriptor(prototype, "value")?.set;
      if (!setter || !candidate.isConnected || document.activeElement !== candidate || candidate.disabled || candidate.readOnly) {
        throw new Error("secure target changed");
      }
      setter.call(candidate, text);
      candidate.dispatchEvent(new InputEvent("input", {
        bubbles: true,
        composed: true,
        inputType: "insertText",
        data: null,
      }));
      candidate.dispatchEvent(new Event("change", { bubbles: true, composed: true }));
    }, request.text);
  } catch {
    throw new Error("secure target changed; enter the value again");
  } finally {
    await discardNativeHandoffTarget(bindingID);
  }
}

async function viewStatus() {
  const activePage = await currentPage();
  let viewport = activePage.viewportSize() ?? { width: 1440, height: 900 };
  try {
    viewport = await activePage.evaluate(() => ({ width: window.innerWidth, height: window.innerHeight }));
  } catch {
    // The page may be between navigations. The configured Xvfb size is a safe fallback.
  }
  const status = {
    ready: true,
    url: activePage.url(),
    title: await activePage.title().catch(() => ""),
    viewport,
    pages: activePage.context().pages().length,
  };
  lastBrowserViewStatus = status;
  return status;
}

function validWebURL(value) {
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

function validElementRef(value) {
  return typeof value === "string" && /^e[0-9]{1,6}$/.test(value);
}

function validWindowRef(value) {
  return typeof value === "string" && /^w[0-9]{1,8}$/.test(value);
}

async function browserSnapshot(activePage) {
  const payload = await activePage.evaluate(() => {
    document.querySelectorAll("[data-oaf-ref]").forEach((node) => node.removeAttribute("data-oaf-ref"));
    const selector = [
      "a[href]",
      "button",
      "input",
      "textarea",
      "select",
      "summary",
      "[role='button']",
      "[role='link']",
      "[role='textbox']",
      "[role='menuitem']",
      "[role='tab']",
      "[contenteditable='true']",
    ].join(",");
    const seen = new Set();
    const elements = [];
    for (const node of document.querySelectorAll(selector)) {
      if (!(node instanceof HTMLElement) || seen.has(node) || elements.length >= 80) continue;
      const style = window.getComputedStyle(node);
      const rect = node.getBoundingClientRect();
      if (style.visibility === "hidden" || style.display === "none" || rect.width < 2 || rect.height < 2) continue;
      if (rect.bottom < 0 || rect.right < 0 || rect.top > window.innerHeight || rect.left > window.innerWidth) continue;
      seen.add(node);
      const index = elements.length + 1;
      const ref = `e${index}`;
      node.setAttribute("data-oaf-ref", ref);
      const role = node.getAttribute("role")
        || (node instanceof HTMLInputElement ? (node.type === "submit" || node.type === "button" ? "button" : "textbox")
          : node.tagName.toLowerCase());
      const name = (
        node.getAttribute("aria-label")
        || node.getAttribute("placeholder")
        || (node instanceof HTMLInputElement && node.type === "submit" ? node.value : "")
        || node.innerText
        || node.getAttribute("title")
        || node.getAttribute("name")
        || ""
      ).replace(/\s+/g, " ").trim().slice(0, 80);
      elements.push({
        ref,
        role,
        name,
        tag: node.tagName.toLowerCase(),
        x: Math.round(rect.x),
        y: Math.round(rect.y),
        width: Math.round(rect.width),
        height: Math.round(rect.height),
      });
    }
    return { url: location.href, title: document.title, elements };
  });
  return {
    surface: "browser",
    url: payload.url,
    title: payload.title,
    ladder: ["element", "pixel"],
    elements: payload.elements ?? [],
  };
}

async function clickByRef(activePage, ref) {
  if (!validElementRef(ref)) return false;
  const locator = activePage.locator(`[data-oaf-ref="${ref}"]`);
  if (await locator.count() === 0) return false;
  await locator.first().click({ timeout: 2500 });
  return true;
}

async function focusByRef(activePage, ref) {
  if (!validElementRef(ref)) return false;
  const locator = activePage.locator(`[data-oaf-ref="${ref}"]`);
  if (await locator.count() === 0) return false;
  await locator.first().click({ timeout: 2500 });
  return true;
}

async function desktopSnapshot() {
  const env = { ...process.env, DISPLAY: process.env.DISPLAY ?? ":99" };
  let ids = [];
  try {
    const { stdout } = await execFileAsync("xdotool", ["search", "--onlyvisible", "--name", ""], { env });
    ids = stdout.trim().split(/\s+/).filter((id) => /^[0-9]+$/.test(id)).slice(0, 40);
  } catch {
    ids = [];
  }
  const elements = [];
  for (const id of ids) {
    try {
      const [{ stdout: nameOut }, { stdout: geoOut }] = await Promise.all([
        execFileAsync("xdotool", ["getwindowname", id], { env }),
        execFileAsync("xdotool", ["getwindowgeometry", "--shell", id], { env }),
      ]);
      const geometry = Object.fromEntries(
        geoOut.trim().split("\n").map((line) => {
          const [key, value] = line.split("=");
          return [key, Number(value)];
        }).filter((entry) => entry[0]),
      );
      const name = nameOut.trim().slice(0, 80);
      const x = Number.isFinite(geometry.X) ? geometry.X : NaN;
      const y = Number.isFinite(geometry.Y) ? geometry.Y : NaN;
      const width = Number.isFinite(geometry.WIDTH) ? geometry.WIDTH : 0;
      const height = Number.isFinite(geometry.HEIGHT) ? geometry.HEIGHT : 0;
      if (!name || width < 2 || height < 2 || x + width < 0 || y + height < 0) continue;
      elements.push({
        ref: `w${id}`,
        role: "window",
        name,
        tag: "window",
        x,
        y,
        width,
        height,
      });
    } catch {
      continue;
    }
  }
  return {
    surface: "desktop",
    ladder: ["element", "pixel"],
    elements,
    detail: elements.length ? undefined : "No visible windows were advertised by the desktop.",
  };
}

async function activateWindowRef(ref) {
  if (!validWindowRef(ref)) return false;
  const id = ref.slice(1);
  try {
    await execFileAsync("xdotool", ["windowactivate", "--sync", id], {
      env: { ...process.env, DISPLAY: process.env.DISPLAY ?? ":99" },
    });
    return true;
  } catch {
    return false;
  }
}

async function action(request) {
  const activePage = await currentPage();
  let method = "pixel";
  let resolvedRef = "";
  const ref = typeof request.ref === "string" ? request.ref.trim() : "";
  switch (request.action) {
    case "navigate":
      if (typeof request.url !== "string" || !validWebURL(request.url)) throw new Error("only http(s) navigation is allowed");
      await activePage.goto(request.url, { waitUntil: "domcontentloaded", timeout: 30000 });
      method = "navigate";
      break;
    case "click": {
      const used = await clickByRef(activePage, ref);
      if (used) {
        method = "element";
        resolvedRef = ref;
        break;
      }
      if (!Number.isFinite(Number(request.x)) || !Number.isFinite(Number(request.y))) {
        throw new Error(ref ? `element ${ref} is gone; click again from a fresh snapshot` : "click requires an element ref or coordinates");
      }
      await activePage.mouse.click(Number(request.x), Number(request.y));
      method = ref ? "pixel" : "pixel";
      resolvedRef = ref;
      break;
    }
    case "type":
      if (typeof request.text !== "string") throw new Error("text is required");
      if (request.native_handoff === true) {
        if (request.text.length > 4096) throw new Error("secure text is too large");
        await deliverNativeBrowserHandoff(request);
        method = "secure";
        break;
      }
      if (ref && await focusByRef(activePage, ref)) {
        method = "element";
        resolvedRef = ref;
      }
      await activePage.keyboard.insertText(request.text);
      break;
    case "press":
      if (typeof request.key !== "string" || request.key.length > 64) throw new Error("key is required");
      if (ref) {
        await focusByRef(activePage, ref).catch(() => false);
        resolvedRef = ref;
        method = "element";
      }
      await activePage.keyboard.press(request.key);
      break;
    case "scroll":
      await activePage.mouse.wheel(Number(request.delta_x ?? 0), Number(request.delta_y ?? 0));
      method = "pixel";
      break;
    case "reload":
      await activePage.reload({ waitUntil: "domcontentloaded", timeout: 30000 });
      method = "navigate";
      break;
    case "back":
      await activePage.goBack({ waitUntil: "domcontentloaded", timeout: 30000 }).catch(() => null);
      method = "navigate";
      break;
    case "forward":
      await activePage.goForward({ waitUntil: "domcontentloaded", timeout: 30000 }).catch(() => null);
      method = "navigate";
      break;
    default:
      throw new Error(`unsupported action: ${request.action}`);
  }
  const status = await viewStatus();
  return { ...status, method, resolved_ref: resolvedRef };
}

async function desktopFrame() {
  const path = `/tmp/openagentfleet-desktop-${process.pid}-${Date.now()}.png`;
  try {
    await execFileAsync("scrot", ["-z", path], { env: { ...process.env, DISPLAY: process.env.DISPLAY ?? ":99" } });
    return await readFile(path);
  } finally {
    await rm(path, { force: true }).catch(() => undefined);
  }
}

const xdotoolKeyAliases = {
  " ": "space",
  Enter: "Return",
  Return: "Return",
  Escape: "Escape",
  Esc: "Escape",
  Tab: "Tab",
  Backspace: "BackSpace",
  Delete: "Delete",
  Insert: "Insert",
  Home: "Home",
  End: "End",
  PageUp: "Page_Up",
  PageDown: "Page_Down",
  ArrowUp: "Up",
  ArrowDown: "Down",
  ArrowLeft: "Left",
  ArrowRight: "Right",
  CapsLock: "Caps_Lock",
  NumLock: "Num_Lock",
  ScrollLock: "Scroll_Lock",
  PrintScreen: "Print",
  NumpadEnter: "KP_Enter",
  NumpadAdd: "KP_Add",
  NumpadSubtract: "KP_Subtract",
  NumpadMultiply: "KP_Multiply",
  NumpadDivide: "KP_Divide",
  NumpadDecimal: "KP_Decimal",
  Numpad0: "KP_0",
  Numpad1: "KP_1",
  Numpad2: "KP_2",
  Numpad3: "KP_3",
  Numpad4: "KP_4",
  Numpad5: "KP_5",
  Numpad6: "KP_6",
  Numpad7: "KP_7",
  Numpad8: "KP_8",
  Numpad9: "KP_9",
  Control: "ctrl",
  Ctrl: "ctrl",
  Alt: "alt",
  Shift: "shift",
  Meta: "super",
  OS: "super",
  Super: "super",
  AltGraph: "ISO_Level3_Shift",
};
const xdotoolKeyAliasLookup = new Map(
  Object.entries(xdotoolKeyAliases).map(([key, value]) => [key.toLowerCase(), value]),
);

function normalizeXdotoolKey(value) {
  const tokens = value.split("+");
  if (tokens.some((token) => token.length === 0)) {
    throw new Error("desktop key is invalid");
  }
  return tokens
    .map((token) => xdotoolKeyAliasLookup.get(token.toLowerCase()) ?? token)
    .join("+");
}

function desktopCoordinate(value, axis) {
  const coordinate = Number(value);
  if (!Number.isFinite(coordinate) || coordinate < 0 || coordinate > 10000) {
    throw new Error(`desktop ${axis} coordinate is invalid`);
  }
  return Math.round(coordinate);
}

async function desktopStatus() {
  if (!desktopViewport) {
    try {
      const { stdout } = await execFileAsync("xdotool", ["getdisplaygeometry"], {
        env: { ...process.env, DISPLAY: process.env.DISPLAY ?? ":99" },
      });
      const [width, height] = stdout.trim().split(/\s+/).map(Number);
      if (Number.isFinite(width) && Number.isFinite(height) && width > 0 && height > 0) {
        desktopViewport = { width, height };
      }
    } catch {
      // Xvfb is provisioned at this size by the image entrypoint.
      desktopViewport = { width: 1440, height: 900 };
    }
  }
  return {
    ...(lastBrowserViewStatus ?? {
      ready: true,
      url: "",
      title: "",
      pages: 0,
    }),
    ready: true,
    viewport: desktopViewport ?? { width: 1440, height: 900 },
  };
}

async function desktopAction(request) {
  const run = (args) => execFileAsync("xdotool", args, { env: { ...process.env, DISPLAY: process.env.DISPLAY ?? ":99" } });
  const ref = typeof request.ref === "string" ? request.ref.trim() : "";
  let method = "pixel";
  let resolvedRef = "";
  if (ref) {
    const activated = await activateWindowRef(ref);
    if (activated) {
      method = "element";
      resolvedRef = ref;
    } else if (!Number.isFinite(Number(request.x)) && request.action === "click") {
      throw new Error(`window ${ref} is gone; inspect the desktop again`);
    }
  }
  switch (request.action) {
    case "click": {
      if (method === "element" && !Number.isFinite(Number(request.x))) {
        const id = ref.slice(1);
        const match = (await desktopSnapshot()).elements.find((item) => item.ref === ref);
        const width = match?.width > 0 ? match.width : 40;
        const height = match?.height > 0 ? match.height : 40;
        await run([
          "mousemove",
          "--window",
          id,
          "--sync",
          String(Math.max(1, Math.round(width / 2))),
          String(Math.max(1, Math.round(height / 2))),
          "click",
          "1",
        ]);
        break;
      }
      const x = desktopCoordinate(request.x, "x");
      const y = desktopCoordinate(request.y, "y");
      // --sync makes xdotool wait until the X server has applied the move
      // before the following click command is dispatched.
      await run(["mousemove", "--sync", String(x), String(y), "click", "1"]);
      if (method !== "element") method = "pixel";
      break;
    }
    case "type":
      if (typeof request.text !== "string") throw new Error("text is required");
      if (request.native_handoff === true) {
        throw new Error("secure handoff is available for browser fields only");
      }
      // Give X11 a small per-character interval and clear modifiers first.
      // A zero-delay burst can race XFCE/Chromium repainting and leave the
      // next root-window capture stale or black on some virtual displays.
      await run(["type", "--delay", "1", "--clearmodifiers", "--", request.text]);
      break;
    case "press":
      if (typeof request.key !== "string" || request.key.length > 64) throw new Error("key is required");
      await run(["key", "--clearmodifiers", "--", normalizeXdotoolKey(request.key)]);
      break;
    case "scroll": {
      const delta = Number(request.delta_y ?? 0);
      const clicks = Math.min(20, Math.max(1, Math.ceil(Math.abs(delta) / 480)));
      await run(["click", "--repeat", String(clicks), delta >= 0 ? "5" : "4"]);
      break;
    }
    default:
      throw new Error("unsupported desktop action");
  }
  // Let the virtual desktop compositor settle before the controller starts
  // its next frame poll after an input event.
  await new Promise((resolve) => setTimeout(resolve, 50));
  // Desktop input does not need a CDP round-trip. Keep the last browser
  // metadata for the controller response, but report the X11 desktop geometry.
  const status = await desktopStatus();
  return { ...status, method, resolved_ref: resolvedRef };
}

async function handle(req, res) {
  if (!authorized(req)) {
    fail(res, 401, "unauthorized");
    return;
  }
  if (req.method === "OPTIONS") {
    res.writeHead(204);
    res.end();
    return;
  }
  const url = new URL(req.url ?? "/", `http://${req.headers.host ?? "localhost"}`);
  try {
    if (req.method === "GET" && url.pathname === "/health") {
      json(res, 200, await serialized(() => viewStatus()));
      return;
    }
    if (req.method === "GET" && url.pathname === "/frame") {
      const image = await serialized(async () => {
        const activePage = await currentPage();
        return activePage.screenshot({ type: "png", animations: "disabled", timeout: 4000 });
      });
      res.writeHead(200, {
        "cache-control": "no-store, no-cache, must-revalidate",
        "content-type": "image/png",
        "content-length": image.length,
      });
      res.end(image);
      return;
    }
    if (req.method === "GET" && url.pathname === "/desktop-frame") {
      const image = await serialized(() => desktopFrame());
      res.writeHead(200, {
        "cache-control": "no-store, no-cache, must-revalidate",
        "content-type": "image/png",
        "content-length": image.length,
      });
      res.end(image);
      return;
    }
    if (req.method === "GET" && url.pathname === "/tabs") {
      const tabs = await serialized(async () => {
        const activePage = await currentPage();
        const pages = activePage.context().pages();
        const result = [];
        for (const tab of pages) {
          result.push({ url: tab.url(), title: await tab.title().catch(() => "") });
        }
        return result;
      });
      json(res, 200, { tabs });
      return;
    }
    if (req.method === "GET" && url.pathname === "/snapshot") {
      const surface = url.searchParams.get("surface") ?? "browser";
      const snapshot = await serialized(async () => {
        if (surface === "desktop") return desktopSnapshot();
        if (surface === "browser") return browserSnapshot(await currentPage());
        throw new Error("snapshot surface must be browser or desktop");
      });
      json(res, 200, snapshot);
      return;
    }
    if (req.method === "GET" && url.pathname === "/target") {
      const surface = url.searchParams.get("surface") ?? "";
      json(res, 200, await serialized(() => targetBinding(surface)));
      return;
    }
    if (req.method === "POST" && url.pathname === "/action") {
      const raw = await readBody(req);
      const request = raw ? JSON.parse(raw) : {};
      json(res, 200, await serialized(() => action(request)));
      return;
    }
    if (req.method === "POST" && url.pathname === "/desktop/action") {
      const raw = await readBody(req);
      const request = raw ? JSON.parse(raw) : {};
      json(res, 200, await serialized(() => desktopAction(request)));
      return;
    }
    fail(res, 404, "not found");
  } catch (error) {
    fail(res, 409, error instanceof Error ? error.message : "computer action failed");
  }
}

const server = http.createServer((req, res) => {
  void handle(req, res);
});

server.listen(port, "0.0.0.0", () => {
  console.log(`Agent Computer view listening on ${port}`);
});
