"use strict";

const tabs = document.querySelector("#tabs");
const screen = document.querySelector("#screen");
const status = document.querySelector("#status");
const address = document.querySelector("#address");
const take = document.querySelector("#take");
const giveBack = document.querySelector("#return");
const go = document.querySelector("#go");
const newTab = document.querySelector("#new");
const client = crypto.randomUUID();
let owner = false;
let selected = "";
let lastTabs = "";
let imageURL;
let queue = Promise.resolve();

async function request(path, options) {
  const response = await fetch(path, options);
  if (!response.ok) throw new Error((await response.text()).trim());
  return response;
}

function post(path, body) {
  return request(path, {
    method: "POST",
    headers: { "X-Viewport-Client": client, "Content-Type": "application/json" },
    body: body && JSON.stringify(body)
  });
}

function setControl(active) {
  owner = active;
  take.disabled = active;
  giveBack.disabled = go.disabled = newTab.disabled = !active;
  status.textContent = active ? "You control the browser" : "Monitoring";
}

take.onclick = async () => {
  take.disabled = true;
  status.textContent = "Waiting for agent browser commands to finish…";
  try {
    await post("take");
    setControl(true);
    screen.focus();
  } catch (err) {
    setControl(false);
    status.textContent = err.message;
  }
};

giveBack.onclick = async () => {
  giveBack.disabled = true;
  try {
    await queue;
    await post("return");
    setControl(false);
  } catch (err) {
    setControl(true);
    status.textContent = err.message;
  }
};

setInterval(async () => {
  if (!owner) return;
  try {
    await post("hold");
  } catch (err) {
    setControl(false);
    status.textContent = err.message;
  }
}, 10000);

window.addEventListener("pagehide", () => {
  if (owner) fetch("return", { method: "POST", headers: { "X-Viewport-Client": client }, keepalive: true });
});

function send(input) {
  if (!owner) return;
  const tab = selected;
  queue = queue.then(async () => {
    if (owner) await post("input", { tab, ...input });
  }).catch(err => { status.textContent = err.message; });
}

document.querySelector("#nav").onsubmit = event => {
  event.preventDefault();
  send({ kind: "navigate", url: address.value });
};
newTab.onclick = () => send({ kind: "new", url: address.value || "about:blank" });
tabs.onchange = () => { selected = tabs.value; };

function modifiers(event) {
  return (event.altKey ? 1 : 0) | (event.ctrlKey ? 2 : 0) |
    (event.metaKey ? 4 : 0) | (event.shiftKey ? 8 : 0);
}

function mouse(event, kind) {
  if (!owner) return;
  event.preventDefault();
  const rect = screen.getBoundingClientRect();
  if (kind === "mousePressed") screen.focus();
  send({ kind, modifiers: modifiers(event),
    x: (event.clientX - rect.left) * screen.naturalWidth / rect.width,
    y: (event.clientY - rect.top) * screen.naturalHeight / rect.height,
    button: kind === "mouseMoved" || kind === "mouseWheel" ? "none" : ["left", "middle", "right"][event.button] || "none",
    clickCount: event.detail || 1, deltaX: event.deltaX || 0, deltaY: event.deltaY || 0 });
}

screen.onmousedown = event => mouse(event, "mousePressed");
screen.onmouseup = event => mouse(event, "mouseReleased");
screen.onmousemove = event => { if (event.buttons) mouse(event, "mouseMoved"); };
screen.addEventListener("wheel", event => mouse(event, "mouseWheel"), { passive: false });
screen.oncontextmenu = event => { if (owner) event.preventDefault(); };

for (const [eventName, kind] of [["keydown", "keyDown"], ["keyup", "keyUp"]]) {
  screen.addEventListener(eventName, event => {
    if (!owner || ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "v")) return;
    event.preventDefault();
    const text = kind === "keyDown" && !event.ctrlKey && !event.metaKey && !event.altKey && event.key.length === 1 ? event.key : "";
    send({ kind, key: event.key, code: event.code, keyCode: event.keyCode, text, modifiers: modifiers(event) });
  });
}
screen.addEventListener("paste", event => {
  if (!owner) return;
  event.preventDefault();
  send({ kind: "text", text: event.clipboardData.getData("text") });
});

async function refresh() {
  let wait = 350;
  try {
    const pages = await (await request("tabs")).json();
    const signature = JSON.stringify(pages);
    if (signature !== lastTabs) {
      lastTabs = signature;
      tabs.replaceChildren(...pages.map(page =>
        new Option(page.title || page.url || "New tab", page.targetId)));
      if (!pages.some(page => page.targetId === selected)) selected = pages[0]?.targetId || "";
      tabs.value = selected;
    }
    if (selected) {
      const tab = selected;
      const blob = await (await request("frame?tab=" + encodeURIComponent(tab))).blob();
      if (selected === tab) {
        if (imageURL) URL.revokeObjectURL(imageURL);
        imageURL = URL.createObjectURL(blob);
        screen.src = imageURL;
        screen.hidden = false;
      }
    } else {
      screen.hidden = true;
    }
    if (!owner) status.textContent = selected ? "Monitoring" : "No open tabs";
  } catch (err) {
    status.textContent = err.message;
    wait = 1500;
  }
  setTimeout(refresh, wait);
}

refresh();
