"use strict";

function $(id) { return document.getElementById(id); }

async function apiGet(path) {
  const res = await fetch(path, { credentials: "include" });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

async function apiPost(path, body) {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "include",
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

function setMsg(id, text, ok) {
  const el = $(id);
  el.textContent = text || "";
  el.className = "msg " + (ok ? "ok" : "bad");
  if (text) setTimeout(() => (el.textContent = ""), 2500);
}

function bindRange(rangeId, valId) {
  const r = $(rangeId);
  const v = $(valId);
  const sync = () => (v.textContent = r.value);
  r.addEventListener("input", sync);
  sync();
}

async function loadAPIKeys() {
  const data = await apiGet("/api/apikey");
  const keys = (data.apiKeys || []).join("\n");
  $("apikeys").value = keys;
}

async function saveAPIKeys() {
  const raw = $("apikeys").value || "";
  const keys = raw.split("\n").map(s => s.trim()).filter(Boolean);
  try {
    const out = await apiPost("/api/apikey", { apiKeys: keys });
    $("apikeys").value = (out.apiKeys || []).join("\n");
    setMsg("msg-apikey", "已保存", true);
  } catch (e) {
    setMsg("msg-apikey", "保存失败: " + e.message, false);
  }
}

async function loadRules() {
  const r = await apiGet("/api/rules");

  $("on-enabled").checked = !!r.on?.enabled;
  $("off-enabled").checked = !!r.off?.enabled;
  $("hit-enabled").checked = !!r.hit?.enabled;

  $("on-threshold").value = (r.on?.threshold ?? 5);
  $("off-threshold").value = (r.off?.threshold ?? 5);
  $("hit-offset").value = (r.hit?.offset ?? 1);
  $("hit-expect").value = (r.hit?.expect ?? "ON");

  $("on-threshold-val").textContent = $("on-threshold").value;
  $("off-threshold-val").textContent = $("off-threshold").value;
  $("hit-offset-val").textContent = $("hit-offset").value;
}

async function saveRules() {
  const body = {
    on: {
      enabled: $("on-enabled").checked,
      threshold: parseInt($("on-threshold").value, 10),
    },
    off: {
      enabled: $("off-enabled").checked,
      threshold: parseInt($("off-threshold").value, 10),
    },
    hit: {
      enabled: $("hit-enabled").checked,
      offset: parseInt($("hit-offset").value, 10),
      expect: $("hit-expect").value,
    }
  };

  try {
    await apiPost("/api/rules", body);
    setMsg("msg-rules", "已保存", true);
  } catch (e) {
    setMsg("msg-rules", "保存失败: " + e.message, false);
  }
}

function renderStatus(st) {
  $("sys-status").textContent = st.listening ? "Listening" : "Idle";
  $("ws-reconnect").textContent = String(st.reconnects ?? 0);
  $("last-height").textContent = st.lastHeight ? String(st.lastHeight) : "-";
  $("last-time").textContent = st.lastTimeISO || "-";
}

async function loadStatus() {
  try {
    const st = await apiGet("/api/status");
    renderStatus(st);
  } catch (e) {
    // likely not logged in
  }
}

function startSSE() {
  const es = new EventSource("/sse/status");
  es.addEventListener("status", (ev) => {
    try {
      const st = JSON.parse(ev.data);
      renderStatus(st);
    } catch {}
  });
  es.onerror = () => {
    // EventSource will auto-reconnect
  };
}

function init() {
  bindRange("on-threshold", "on-threshold-val");
  bindRange("off-threshold", "off-threshold-val");
  bindRange("hit-offset", "hit-offset-val");

  $("btn-save-apikey").addEventListener("click", saveAPIKeys);
  $("btn-save-rules").addEventListener("click", saveRules);

  loadAPIKeys();
  loadRules();
  loadStatus();
  startSSE();

  setInterval(loadStatus, 3000);
}

window.addEventListener("DOMContentLoaded", init);
