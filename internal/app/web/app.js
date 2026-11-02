"use strict";
const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const icon = (short, name = "") => short ? `<img src="https://rustlabs.com/img/items180/${encodeURIComponent(short)}.png" alt="${esc(name)}" loading="lazy" onerror="iconFail(this)">` : "";
window.iconFail = (img) => { if (img.alt && img.closest(".inv")) { const e = document.createElement("em"); e.textContent = img.alt; img.replaceWith(e); } else img.remove(); };
const clock = (unix) => new Date(unix * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
const ago = (unix) => {
  const s = Math.max(0, Date.now() / 1000 - unix);
  if (s < 60) return `${Math.floor(s)}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m ago`;
  return `${Math.floor(s / 86400)}d ago`;
};

let S = null;          // last state
let tab = "overview";
let seenAlerts = 0;
let unread = 0;

async function api(path, body) {
  const opt = body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
  const r = await fetch(path, opt);
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}
function act(path, body, okMsg) {
  return api(path, body).then((j) => { if (okMsg) toast(okMsg); refresh(); return j; }).catch((e) => { toast("Couldn't do that", e.message, "alarm"); throw e; });
}

/* ---------- toasts & sound ---------- */
function toast(title, text = "", kind = "") {
  const t = document.createElement("div");
  t.className = "toast k-" + kind;
  t.innerHTML = `<b>${esc(title)}</b>${esc(text)}`;
  $("#toasts").append(t);
  setTimeout(() => t.remove(), 6000);
  while ($("#toasts").children.length > 4) $("#toasts").firstChild.remove();
}
let actx;
function beep(kind) {
  try {
    actx = actx || new AudioContext();
    const o = actx.createOscillator(), g = actx.createGain();
    o.frequency.value = kind === "alarm" || kind === "team_death" ? 880 : 620;
    o.type = "triangle";
    g.gain.setValueAtTime(0.0001, actx.currentTime);
    g.gain.exponentialRampToValueAtTime(0.12, actx.currentTime + 0.02);
    g.gain.exponentialRampToValueAtTime(0.0001, actx.currentTime + 0.35);
    o.connect(g).connect(actx.destination);
    o.start(); o.stop(actx.currentTime + 0.4);
  } catch (e) {}
}

function notifyStatus() {
  if (!("Notification" in window)) return `<span class="muted">Not available in this window</span>`;
  if (Notification.permission === "granted") return `<span class="pill ok">On</span>`;
  if (Notification.permission === "denied") return `<span class="pill bad">Blocked</span> <span class="muted">Click the padlock or site-info icon in the title bar to allow notifications.</span>`;
  return `<button type="button" class="small" onclick="Notification.requestPermission().then(()=>{renderSettings(true);renderOverview()})">Allow Windows notifications</button>`;
}

/* ---------- live updates ---------- */
let refreshTimer = null, refreshing = false, again = false;
function refresh() {
  if (refreshTimer) return;
  refreshTimer = setTimeout(async () => {
    refreshTimer = null;
    if (refreshing) { again = true; return; }
    refreshing = true;
    try { S = await api("/api/state"); render(); } catch (e) {}
    refreshing = false;
    if (again) { again = false; refresh(); }
  }, 250);
}
function connectStream() {
  const es = new EventSource("/api/stream");
  es.addEventListener("update", refresh);
  es.addEventListener("alert", (e) => {
    const d = JSON.parse(e.data);
    toast(d.entry.title, d.entry.text, d.entry.kind);
    if (d.sound) beep(d.entry.kind);
    if (d.desktop && "Notification" in window && Notification.permission === "granted") {
      try { new Notification(d.entry.title, { body: d.entry.text, tag: d.entry.kind + ":" + d.entry.time + ":" + d.entry.title, icon: $("link[rel=icon]").href }); } catch (e) {}
    }
    if (tab !== "alerts") { unread++; $("#alertCount").textContent = unread; }
    refresh();
  });
  es.onerror = () => { es.close(); setTimeout(connectStream, 3000); };
}

/* ---------- tabs ---------- */
$$(".rail button").forEach((b) => b.addEventListener("click", () => setTab(b.dataset.tab)));
function setTab(t) {
  tab = t;
  $$(".rail button").forEach((b) => b.classList.toggle("on", b.dataset.tab === t));
  $$("section[data-panel]").forEach((s) => s.classList.toggle("on", s.dataset.panel === t));
  if (t === "alerts") { unread = 0; $("#alertCount").textContent = ""; }
  render();
}

/* ---------- render ---------- */
function render() {
  if (!S) return;
  renderStrip();
  const vids = [];
  (S.vending || []).forEach((v) => (v.orders || []).forEach((o) => vids.push(o.itemId, o.currencyId)));
  ensureNames(vids).then((ch) => ch && (maps.overview.draw(), maps.full.draw()));
  const r = { overview: renderOverview, map: () => maps.full.draw(), team: renderTeam, shops: renderShops, devices: renderDevices,
    players: renderPlayers, chat: renderChat, commands: renderCommands, items: () => {}, alerts: renderAlerts, servers: renderServers, settings: renderSettings }[tab];
  r && r();
}

function renderStrip() {
  const st = S.status, i = S.info, t = S.time;
  const lampText = { connected: "Connected", connecting: "Connecting…", reconnecting: "Reconnecting…", disconnected: "Not connected" }[st] || st;
  let h = "";
  if (S.active) {
    h += `<div class="srv" title="${esc(S.active.name)}">${esc((i && i.name) || S.active.name || S.active.ip)}</div>`;
    if (i) {
      h += `<div class="stat"><b>${i.players}/${i.maxPlayers}</b><span>${i.queuedPlayers ? i.queuedPlayers + " in queue" : "players"}</span></div>`;
    }
    if (t) h += `<div class="stat"><b>${t.clock} ${t.isDay ? "☀" : "☾"}</b><span>${t.until} ${t.untilLabel}</span></div>`;
    if (S.wipeAgo) h += `<div class="stat"><b>${S.wipeAgo}</b><span>since wipe</span></div>`;
    const on = (S.team || []).filter((m) => m.isOnline).length;
    if (S.team) h += `<div class="stat"><b>${on}/${S.team.length}</b><span>team online</span></div>`;
  } else {
    h += `<div class="srv muted">No server selected</div>`;
  }
  if (S.demo) h += `<span class="demo">Demo mode</span>`;
  if (S.update && S.update.available) h += `<button class="small updbtn" onclick="setTab('settings');setTimeout(()=>$('#updBox')?.scrollIntoView({block:'center'}),50)">Update to ${esc(S.update.latest)}</button>`;
  h += `<span class="lamp ${st}" title="${esc(S.statusError || "")}"><i></i>${esc(lampText)}${S.statusError && st !== "connected" ? " — " + esc(S.statusError) : ""}</span>`;
  $("#strip").innerHTML = h;
}

const EVENT_ROWS = [["cargo", "Cargo"], ["heli", "Heli"], ["chinook", "Chinook"], ["smallOil", "Small rig"], ["largeOil", "Large rig"], ["bradley", "Bradley"], ["vendor", "Vendor"]];
function renderOverview() {
  maps.overview.draw();
  if (!S.active) {
    $("#ovEvents").innerHTML = `<div class="empty-note">Pair a server to start. Open <a href="#" onclick="setTab('servers');return false">Servers</a>.</div>`;
    $("#ovTeam").innerHTML = ""; $("#ovAlerts").innerHTML = ""; $("#ovTimers").innerHTML = "";
    return;
  }
  const ev = S.events;
  const fc = Object.fromEntries((S.forecasts || []).map((f) => [f.kind, f]));
  const nextLine = (k) => {
    const f = fc[k === "chinook" ? "chinook" : k];
    if (!f) return "";
    const cls = { window: "due", overdue: "late", active: "", upcoming: "", unknown: "muted" }[f.state] || "";
    return `<div class="next ${cls}" title="${esc(f.source)}">Next: ${esc(f.short)}${f.learned ? ' <span class="learned">learned</span>' : ""}</div>`;
  };
  $("#ovEvents").innerHTML = ev ? EVENT_ROWS.map(([k, label]) => `<div class="ev ${ev.active?.includes(k) || fc[k]?.state === "window" ? "live" : ""}"><span class="k">${label}</span><span>${esc(ev[k])}${["cargo", "heli", "chinook", "vendor"].includes(k) ? nextLine(k) : ""}</span></div>`).join("") : `<div class="empty-note">Waiting for map data…</div>`;
  $("#ovTeam").innerHTML = (S.team || []).map(mateRow).join("") || `<div class="empty-note">You're not in a team.</div>`;
  $("#ovTimers").innerHTML = (S.timers || []).map((t) => `<div class="mate"><span class="dot afk"></span><span>${esc(t.message)} <small>in ${t.left}</small></span><button class="ghost small" onclick="act('/api/timers/cancel',{id:${t.id}})">Cancel</button></div>`).join("") || `<div class="muted" style="font-size:14px">No timers running. Team-chat command: !timer 10m message</div>`;
  $("#ovAlerts").innerHTML = (("Notification" in window && Notification.permission === "default") ? `<div class="al">${notifyStatus()}</div>` : "") + ((S.log || []).slice(-8).reverse().map(alertRow).join("") || `<div class="empty-note">Nothing yet.</div>`);
}
function mateRow(m) {
  const cls = !m.isOnline ? "" : !m.isAlive ? "dead" : m.afk ? "afk" : "on";
  const sub = !m.isOnline ? (m.offlineFor ? `offline ${m.offlineFor}` : "offline") : !m.isAlive ? `dead ${m.deadFor || ""}` : m.afk ? `AFK ${m.afkFor}` : m.grid;
  return `<div class="mate"><span class="dot ${cls}"></span><span>${esc(m.name)}${m.leader ? ' <small title="Team leader">★</small>' : ""}</span><small>${esc(sub)}</small></div>`;
}
function alertRow(e) {
  return `<div class="al"><time>${clock(e.time)}</time><b>${esc(e.title)}</b><div class="muted">${esc(e.text)}</div></div>`;
}

function renderTeam() {
  const team = S.team || [];
  if (!team.length) { $("#teamTable").innerHTML = `<div class="empty-note">${S.active ? "No team info yet — you need to be in a team in-game." : "Connect to a server first."}</div>`; return; }
  const iAmLeader = S.leaderId === S.myId;
  const hasKey = !!S.settings.steamApiKey;
  const statCells = (m) => {
    if (!hasKey) return `<td class="num muted">–</td><td class="num muted">–</td><td class="num muted">–</td>`;
    if (!m.hasStats) return `<td colspan="3" class="muted" style="font-size:13px">${esc(m.statsError || "Loading…")}</td>`;
    return `<td class="num"><b>${m.wipeKills}</b> <span class="muted">/ ${m.wipeDeathsSteam}</span></td><td class="num">${m.kills.toLocaleString()} <span class="muted">/ ${m.lifeDeaths.toLocaleString()}</span></td><td class="num">${m.kd}${m.hours ? `<div class="muted" style="font-size:12px">${m.hours.toLocaleString()} h played</div>` : ""}</td>`;
  };
  $("#teamTable").innerHTML = `<table><thead><tr><th></th><th>Name</th><th>Status</th><th>Location</th><th>Online for</th><th>Alive for</th><th>Deaths this wipe</th><th title="Kills / deaths since this wipe started, from Steam">Wipe K / D</th><th title="Lifetime Rust kills / deaths from Steam">Lifetime K / D</th><th>K/D ratio</th><th></th></tr></thead><tbody>${team.map((m) => {
    const cls = !m.isOnline ? "" : !m.isAlive ? "dead" : m.afk ? "afk" : "on";
    const status = !m.isOnline ? `<span class="pill">Offline${m.offlineFor ? " " + m.offlineFor : ""}</span>` : !m.isAlive ? `<span class="pill bad">Dead ${m.deadFor || ""}</span>` : m.afk ? `<span class="pill warn">AFK ${m.afkFor}</span>` : `<span class="pill ok">Online</span>`;
    const promote = iAmLeader && !m.leader ? `<button class="ghost small" onclick="promote('${m.steamId}','${esc(m.name)}')">Make leader</button>` : m.leader ? `<span class="muted">Leader</span>` : "";
    return `<tr><td><span class="dot ${cls}"></span></td><td><b>${esc(m.name)}</b><div class="muted" style="font-size:12px"><a href="https://steamcommunity.com/profiles/${m.steamId}" target="_blank" rel="noopener" style="color:inherit">${m.steamId}</a></div></td><td>${status}</td><td>${esc(m.grid)}</td><td class="num">${m.onlineFor || "–"}</td><td class="num">${m.isAlive ? m.aliveFor || "–" : "–"}</td><td class="num">${m.deaths}</td>${statCells(m)}<td>${promote}</td></tr>`;
  }).join("")}</tbody></table>
  <p class="muted">AFK means a teammate hasn't moved for ${S.settings.afkMinutes} minutes. Online time counts from when RustWatch first saw them.
  ${hasKey ? "Kills come from Steam's Rust stats, which count every server a player plays on and update when Steam syncs (usually within a few minutes). Wipe K / D counts from the first reading after this wipe." : `Rust+ doesn't share kills. To show kills, add a free Steam Web API key in <a href="#" onclick="setTab('settings');return false">Settings</a>.`}</p>`;
}
window.promote = (id, name) => { if (confirm(`Make ${name} the team leader?`)) act("/api/team/promote", { steamId: id }, `${name} is now leader`); };

/* ---------- shops ---------- */
let vendQuery = null;
function orderLine(o) {
  return `<div class="order ${o.amountInStock > 0 ? "" : "oos"}"><b>${o.quantity}× ${esc(itemName(o.itemId))}${o.itemIsBlueprint ? " BP" : ""}</b> for ${o.costPerItem} ${esc(itemName(o.currencyId))} · ${o.amountInStock} in stock</div>`;
}
const itemNames = {};
function itemName(id) { return itemNames[id] || `#${id}`; }
async function ensureNames(ids) {
  const missing = [...new Set(ids)].filter((id) => !(id in itemNames));
  if (!missing.length) return false;
  await Promise.all(missing.map(async (id) => { try { const d = await api("/api/items/" + id); itemNames[id] = d.name; } catch { itemNames[id] = `#${id}`; } }));
  return true;
}
function renderShops() {
  const vms = S.vending || [];
  const ids = [];
  vms.forEach((v) => (v.orders || []).forEach((o) => ids.push(o.itemId, o.currencyId)));
  ensureNames(ids).then((changed) => changed && renderShops());
  $("#vendCount").textContent = `(${vms.length})`;
  const mi = S.markerInfo || {};
  const diagLine = mi.at || mi.error
    ? `Rust+ map data: ${mi.total || 0} markers, ${mi.vending || 0} shops${mi.at ? " · updated " + ago(mi.at) : ""}${mi.error ? ` · <span style="color:#e8a097">last error: ${esc(mi.error)}</span>` : ""}`
    : "Waiting for the first map update…";
  $("#vendDiag").innerHTML = `<span class="muted">${diagLine}</span>`;
  const f = $("#vendFilter").value.trim().toLowerCase();
  const tracked = new Set(S.trackedVMs || []);
  const list = vms.filter((v) => !f || v.name.toLowerCase().includes(f) || (v.orders || []).some((o) => itemName(o.itemId).toLowerCase().includes(f)));
  if (S.active && !vms.length && (mi.at || mi.error)) {
    $("#vendList").innerHTML = `<div class="emptybox"><b>The server isn't reporting any shops.</b>
      <p>RustWatch only sees vending machines that Rust+ sends, the same ones the Rust+ phone app shows. If you can see shop icons on the in-game map (G) but not here, the server may be hiding them from Rust+ (some modded servers do). Shops also need their broadcast switch on.</p>
      <p>${mi.error ? "The last request failed: " + esc(mi.error) + ". " : ""}Save a diagnostics file and send it over so the cause can be found.</p>
      <button class="small" onclick="saveDiag()">Save diagnostics</button></div>`;
    return;
  }
  $("#vendList").innerHTML = list.map((v) => `<div class="shop"><header><span class="g">${esc(v.grid.split(" ")[0])}</span><b>${esc(v.name)}</b>${v.outOfStock ? '<span class="pill">Out of stock</span>' : ""}<button class="${tracked.has(v.key) ? "" : "ghost"} small" onclick="trackVM('${v.key}',${!tracked.has(v.key)})">${tracked.has(v.key) ? "Tracking changes" : "Track changes"}</button></header>${(v.orders || []).map(orderLine).join("")}</div>`).join("") || `<div class="empty-note">${S.active ? "No vending machines on the map yet." : "Connect to a server first."}</div>`;
  $("#watchList").innerHTML = (S.watches || []).map((w) => `<div class="hit"><span>${icon(w.short)}</span><span><b>${esc(w.name)}</b> ${w.maxPrice ? `<span class="muted">at most ${w.maxPrice}</span>` : '<span class="muted">any price</span>'} <button class="ghost small" onclick="act('/api/vending/watch',{itemId:${w.itemId},remove:true})">Stop watching</button></span></div>`).join("") || `<div class="empty-note">No item alerts yet.</div>`;
}
window.saveDiag = () => api("/api/diagnostics", {}).then((r) => toast("Diagnostics saved", r.path)).catch((e) => toast("Couldn't save diagnostics", e.message, "alarm"));
window.trackVM = (key, on) => act("/api/vending/track", { key, track: on });
$("#vendFilter").addEventListener("input", renderShops);
$("#vendSearch").addEventListener("submit", async (e) => {
  e.preventDefault();
  const q = e.target.q.value.trim(), mode = e.target.mode.value;
  if (!q) return;
  const r = await api(`/api/vending/search?q=${encodeURIComponent(q)}&mode=${mode}`);
  if (!r.item) { $("#vendResults").innerHTML = `<div class="empty-note">No item matches "${esc(q)}".</div>`; return; }
  await ensureNames(r.hits.flatMap((h) => [h.order.itemId, h.order.currencyId]));
  $("#vendResults").innerHTML = `<h2>${icon(r.item.shortname).replace("<img", '<img style="width:24px;height:24px;vertical-align:-5px"')} ${esc(r.item.name)} — ${mode === "buy" ? "for sale" : "wanted"} in ${r.hits.length} shop${r.hits.length === 1 ? "" : "s"}</h2>` +
    (r.hits.map((h) => `<div class="hit"><span class="g">${esc(h.grid.split(" ")[0])}</span><span><b>${esc(h.machine)}</b><div class="muted">${h.order.quantity}× ${esc(itemName(h.order.itemId))} for ${h.order.costPerItem} ${esc(itemName(h.order.currencyId))} · ${h.order.amountInStock} in stock</div></span></div>`).join("") || `<div class="empty-note">Nobody ${mode === "buy" ? "sells" : "buys"} it right now.</div>`);
});
$("#watchForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const q = e.target.q.value.trim();
  if (!q) return;
  const res = await api("/api/items/search?q=" + encodeURIComponent(q));
  if (!res.length) { toast("Unknown item", q); return; }
  await act("/api/vending/watch", { itemId: res[0].id, maxPrice: parseInt(e.target.max.value) || 0 }, `Watching ${res[0].name}`);
  e.target.reset();
});
$("#watchForm").q.addEventListener("input", async (e) => {
  const q = e.target.value.trim();
  if (q.length < 2) return;
  const res = await api("/api/items/search?q=" + encodeURIComponent(q));
  $("#itemList").innerHTML = res.slice(0, 10).map((r) => `<option value="${esc(r.name)}">`).join("");
});

/* ---------- devices ---------- */
function renderDevices() {
  const devs = S.devices || [];
  if (!S.active) { $("#devices").innerHTML = `<div class="empty-note">Connect to a server first.</div>`; return; }
  if (!devs.length) { $("#devices").innerHTML = `<div class="empty-note">No devices paired yet.</div>`; return; }
  const focus = document.activeElement && document.activeElement.dataset.dev;
  if (focus) return; // don't clobber the field being edited
  $("#devices").innerHTML = `<div class="devgrid">${devs.map(devCard).join("")}</div>`;
}
function devCard(d) {
  const kind = { 1: "Smart switch", 2: "Smart alarm", 3: "Storage monitor" }[d.type] || "Device";
  const err = d.error ? `<div class="pill bad" title="${esc(d.error)}">${/not_found/.test(d.error) ? "Not found — re-pair it" : "Error"}</div>` : "";
  let body = "";
  if (d.type === 1) {
    body = `<div class="row"><button class="switch ${d.value ? "on" : ""}" aria-label="Toggle ${esc(d.name)}" aria-pressed="${!!d.value}" onclick="act('/api/devices/toggle',{entityId:${d.entityId},value:${!d.value}})"></button><span>${d.value == null ? "Unknown" : d.value ? "On" : "Off"}</span></div>
      <div class="row"><span class="muted">${esc(S.settings.prefix)}</span><input data-dev="cmd" value="${esc(d.command || "")}" placeholder="chat command" id="cmd-${d.entityId}"><button class="ghost small" onclick="saveDev(${d.entityId})">Save</button></div>`;
  } else if (d.type === 2) {
    body = `<div class="row"><span class="pill ${d.value ? "bad" : ""}">${d.value ? "Triggered" : "Quiet"}</span></div>`;
  } else if (d.type === 3) {
    const low = d.upkeepSeconds && d.upkeepSeconds < S.settings.upkeepAlertHours * 3600;
    body = (d.hasProtection ? `<div class="upkeep ${low ? "low" : ""}">${d.upkeepLeft ? "Upkeep: " + d.upkeepLeft : "No upkeep"}</div>` : "") +
      `<div class="inv">${d.items.map((it) => `<div title="${esc(it.name)}${it.blueprint ? " blueprint" : ""}">${icon(it.short, it.name) || `<em>${esc(it.name)}</em>`}<span>${it.quantity >= 1000 ? (it.quantity / 1000).toFixed(it.quantity >= 10000 ? 0 : 1) + "k" : it.quantity}</span></div>`).join("")}</div>
      <div class="meta" style="margin-top:6px">${d.items.length}/${d.capacity || "?"} slots</div>`;
  }
  return `<div class="dev ${d.type === 2 ? "alarm" : ""} ${d.type === 2 && d.value ? "trig" : ""}"><header><div><h3>${esc(d.name)}</h3><div class="meta">${kind} · #${d.entityId}${d.updated ? " · updated " + ago(d.updated) : ""}</div></div>${err}</header>${body}
    <div class="row"><input data-dev="name" id="name-${d.entityId}" value="${esc(d.name)}" aria-label="Name"><button class="ghost small" onclick="saveDev(${d.entityId})">Rename</button><button class="danger small" onclick="if(confirm('Remove ${esc(d.name)}?'))act('/api/devices/remove',{entityId:${d.entityId}})">Remove</button></div></div>`;
}
window.saveDev = (id) => {
  const d = S.devices.find((x) => x.entityId === id);
  const name = $("#name-" + id)?.value || d.name, cmd = $("#cmd-" + id)?.value ?? d.command;
  document.activeElement.blur();
  act("/api/devices/save", { entityId: id, type: d.type, name, command: cmd }, "Saved");
};
$("#devForm").addEventListener("submit", (e) => {
  e.preventDefault();
  const id = parseInt(e.target.id.value);
  if (!id) return toast("Enter the entity ID", "", "alarm");
  act("/api/devices/save", { entityId: id, name: e.target.name.value, command: e.target.cmd.value }, "Device added").then(() => e.target.reset());
});

/* ---------- players ---------- */
function renderPlayers() {
  const T = S.tracker;
  if (!S.active) { $("#bmStatus").innerHTML = `<div class="empty-note">Connect to a server first.</div>`; return; }
  const direct = T.source !== "battlemetrics";
  const ok = T.status === "ok";
  if (!$("#bmStatus").contains(document.activeElement)) {
    const where = T.localMode
      ? `<span>Live player list from <b>your Steam client</b> (the overlay's Players list)</span>`
      : direct
      ? `<span>Live player list from the server${T.queryAddr ? ` <code>${esc(T.queryAddr)}</code>` : ""}</span>`
      : `<span>BattleMetrics: <b>${esc(T.serverName || (T.serverId ? "#" + T.serverId : "finding server…"))}</b></span>${T.serverId ? `<a href="https://www.battlemetrics.com/servers/rust/${T.serverId}" target="_blank" rel="noopener">Open on BattleMetrics</a>` : ""}`;
    const form = direct
      ? `<form class="inline" id="trkAddr" style="margin:0 0 0 auto"><input name="v" placeholder="Server address, e.g. 123.45.67.89:28015" size="34" aria-label="Server address"><button class="ghost">Set address</button></form>`
      : `<form class="inline" id="trkAddr" style="margin:0 0 0 auto"><input name="v" placeholder="BattleMetrics server link or ID" size="30" aria-label="BattleMetrics server"><button class="ghost">${T.serverId ? "Change server" : "Link server"}</button></form>`;
    $("#bmStatus").innerHTML = `<div class="bm">${where}<span class="pill ${ok ? "ok" : "warn"}" style="white-space:normal">${ok ? "Updated " + (T.lastPoll ? ago(T.lastPoll) : "") : esc(T.status)}</span>${T.steamErr ? `<span class="pill warn" style="white-space:normal">Steam: ${esc(T.steamErr)}</span>` : ""}${form}<button class="ghost" onclick="act('/api/tracker/refresh',{})">Refresh now</button></div>
      <p class="muted" style="margin-top:8px">${T.localMode
        ? "While you're in Rust on this server, RustWatch reads the same list as Shift+Tab → Players: real Steam names and IDs, even though the server hides names publicly. Players tracked by name are linked to their Steam profile automatically when their name shows up here. When you leave the game, RustWatch goes back to the server's own list."
        : direct
        ? "Names come straight from the server, the same way server browsers see them. Players tracked by name are matched by name; players tracked by Steam profile are followed through Steam, so they're found even when the server hides names or they rename." + (T.localState ? ` <br>In-game Steam list: ${esc(T.localState)}.` : "")
        : "Using BattleMetrics because an API key is set in Settings. Remove the key to use the free direct server query."}</p>`;
    $("#trkAddr").onsubmit = (e) => {
      e.preventDefault();
      const v = e.target.v.value.trim();
      if (!v) return;
      if (direct) act("/api/tracker/address", { addr: v }, "Looking for the server at " + v);
      else act("/api/tracker/link", { id: v }, "Linked");
      e.target.v.value = ""; e.target.v.blur();
    };
  }

  const now = Date.now() / 1000, span = 7 * 86400;
  $("#trackedList").innerHTML = (T.tracked || []).map((p) => {
    const bars = (p.sessions || []).filter((s) => (s.end || now) > now - span).map((s) => {
      const a = Math.max(s.start, now - span), b = s.end || now;
      return `<i style="left:${((a - (now - span)) / span) * 100}%;width:${Math.max(0.3, ((b - a) / span) * 100)}%"></i>`;
    }).join("");
    const state = p.online ? `<span class="pill ok">Online ${esc(p.onlineFor)}</span>` : `<span class="pill">${p.lastSeenAgo ? "Last seen " + esc(p.lastSeenAgo) + " ago" : "Not seen yet"}</span>`;
    const steam = p.steamId ? `<div class="sub">Steam: ${esc(p.presence || "")}${p.steamName && p.steamName !== p.name ? ` · now called <b>${esc(p.steamName)}</b>` : ""} · <a href="https://steamcommunity.com/profiles/${esc(p.steamId)}" target="_blank" rel="noopener">Profile</a></div>` : "";
    return `<div class="trk"><span class="dot ${p.online ? "on" : ""}" style="margin-top:6px"></span>
      <div><div class="name">${p.tag ? `<span class="tag" style="background:${esc(p.color || "#e0ad3e")}">${esc(p.tag)}</span>` : ""}${esc(p.name)}</div>
      <div class="sub">${state} · played ${esc(p.playtime24h)} in 24h, ${esc(p.playtime7d)} in 7 days${p.bmId && !direct ? ` · <a href="https://www.battlemetrics.com/players/${p.bmId}" target="_blank" rel="noopener">BattleMetrics</a>` : ""}</div>${steam}</div>
      <div class="acts"><label title="Alert me when they join or leave" class="muted" style="display:flex;gap:4px;align-items:center;font-size:13px"><input type="checkbox" ${p.notify ? "checked" : ""} onchange="retag('${p.bmId}','${esc(p.name)}',{notify:this.checked})">Alerts</label>
      <button class="ghost small" onclick="editTag('${p.bmId}','${esc(p.name)}')">Edit tag</button>${p.steamId ? "" : `<button class="ghost small" title="Follow them through their Steam profile instead of their name" onclick="linkSteam('${p.bmId}','${esc(p.name)}')">Link Steam</button>`}<button class="danger small" onclick="untrack('${p.bmId}','${esc(p.name)}','${esc(p.steamId || "")}')">Untrack</button></div>
      <div class="timeline" title="Online time over the last 7 days">${bars}</div><div class="timeline-legend"><span>7 days ago</span><span>now</span></div></div>`;
  }).join("") || `<div class="empty-note">Nobody tracked yet. Track someone from the online list, by name, or by Steam profile link.</div>`;

  const f = $("#onlineFilter").value.trim().toLowerCase();
  const on = (T.online || []).filter((p) => !f || p.name.toLowerCase().includes(f));
  const cnt = (T.online || []).length;
  $("#onlineCount").textContent = T.total && T.shown && T.shown < T.total - 2 ? `(${cnt} of ${T.total})` : `(${cnt})`;
  let note = "";
  if (T.censored && !T.localMode) note += `<div class="emptybox" style="margin-bottom:10px"><b>This server hides real player names.</b><p>Rust's <code>server.censorplayerlist</code> setting (on by default) swaps everyone's name for a random stand-in in the public player list, so these aren't Steam names and tracking by name won't match here. BattleMetrics can still show real names when the server owner has given it admin access, but that feed isn't public.</p><p><b>Easiest fix: start Rust and join this server.</b> While you're in the game, RustWatch reads the real names from your Steam client, the same list as Shift+Tab → Players.</p><p><b>To follow someone while you're not in the game, track them by Steam profile:</b> paste their Steam profile link in “Track player”, or press <b>Link Steam</b> on someone you already track. ${T.steamOn ? "RustWatch then checks Steam every minute for which server they're on." : "This needs the free Steam Web API key in Settings → Kills."} It works as long as their Steam profile is public.</p></div>`;
  if (T.total && T.shown && T.shown < T.total - 2) note += `<div class="muted" style="font-size:13px;margin-bottom:8px">${T.localMode ? `Steam lists ${T.shown} of the server's ${T.total} players — Steam only knows players it has seen on the server, so a few may be missing.` : `The server only sent ${T.shown} of its ${T.total} players — its reply is cut short, so some players never appear here.`}</div>`;
  $("#onlineList").innerHTML = note + `<table><tbody>${on.map((p) => `<tr><td>${p.tracked ? `<span class="tag" style="background:${esc(p.color || "#e0ad3e")}">${esc(p.tag || "tracked")}</span>` : ""}${esc(p.name)}${p.id.startsWith("s:") ? ` <a class="muted" style="font-size:12px" href="https://steamcommunity.com/profiles/${esc(p.id.slice(2))}" target="_blank" rel="noopener">Steam</a>` : ""}</td><td class="num muted">${esc(p.onlineFor)}</td><td style="text-align:right">${p.tracked ? "" : `<button class="ghost small" onclick="quickTrack('${p.id}','${esc(p.name)}')">Track</button>`}</td></tr>`).join("")}</tbody></table>` + (on.length ? "" : `<div class="empty-note">${ok ? "No players match." : "Waiting for the player list…"}</div>`);
  $("#activity").innerHTML = (T.activity || []).map((a) => `<div class="act"><span class="muted">${clock(a.time)}</span> <span class="${a.joined ? "j" : "l"}">${a.joined ? "joined" : "left"}</span> ${a.tag ? `<span class="tag" style="background:var(--event)">${esc(a.tag)}</span>` : ""}${esc(a.name)}</div>`).join("") || `<div class="empty-note">Joins and leaves appear here after the next update.</div>`;
}
$("#onlineFilter").addEventListener("input", renderPlayers);
$("#trackForm").addEventListener("submit", (e) => {
  e.preventDefault();
  const who = e.target.who.value.trim();
  if (!who) return;
  const isSteam = /steamcommunity\.com|^7656119\d{10}$/.test(who);
  const isBM = !isSteam && /battlemetrics|^\d{5,}$/.test(who);
  act("/api/tracker/track", { bmId: isBM ? who : "", steamId: isSteam ? who : "", name: isBM || isSteam ? "" : who, tag: e.target.tag.value.trim(), color: e.target.color.value, notify: true }, "Tracking " + (isSteam ? "Steam profile" : who)).then(() => { e.target.who.value = ""; });
});
window.quickTrack = (id, name) => {
  const tag = prompt(`Tag for ${name} (e.g. enemy, neighbour, clan name):`, "");
  if (tag === null) return;
  act("/api/tracker/track", { bmId: id.startsWith("n:") ? "" : id, name, tag, color: $("#trackForm").color.value, notify: true }, "Tracking " + name);
};
function findTracked(bmId, name) { return S.tracker.tracked.find((t) => (bmId && t.bmId === bmId) || (!bmId && t.name === name)); }
window.retag = (bmId, name, patch) => { const t = findTracked(bmId, name); act("/api/tracker/track", { ...t, ...patch }); };
window.editTag = (bmId, name) => { const t = findTracked(bmId, name); const tag = prompt("Tag:", t.tag || ""); if (tag !== null) retag(bmId, name, { tag }); };
window.untrack = (bmId, name, steamId) => { if (confirm(`Stop tracking ${name}?`)) act("/api/tracker/untrack", { bmId, name, steamId }); };
window.linkSteam = (bmId, name) => {
  const link = prompt(`Steam profile link for ${name}\n(open their Steam profile, right-click the page → Copy Page URL):`, "");
  if (link && link.trim()) retag(bmId, name, { steamId: link.trim() });
};

/* ---------- chat ---------- */
let chatLen = -1;
function renderChat() {
  const c = S.chat || [];
  if (c.length === chatLen) return;
  chatLen = c.length;
  const el = $("#chatLog");
  const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  el.innerHTML = c.map((m) => `<div class="msg ${m.bot ? "bot" : ""}"><time>${clock(m.time)}</time><b style="color:${/^#[0-9a-f]{3,8}$/i.test(m.color) ? m.color : "inherit"}">${esc(m.name)}</b>${esc(m.message)}</div>`).join("") || `<div class="empty-note">${S.active ? "No messages yet." : "Connect to a server first."}</div>`;
  if (atBottom) el.scrollTop = el.scrollHeight;
}
$("#chatForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const m = e.target.m.value.trim();
  if (!m) return;
  e.target.m.value = "";
  try {
    await api("/api/chat/send", { message: m });
  } catch (err) {
    if (m.startsWith(S.settings.prefix)) { const r = await api("/api/command", { line: m }); toast(m, r.reply); }
    else toast("Message not sent", err.message, "alarm");
  }
});

/* ---------- commands ---------- */
const COMMANDS = [
  ["Server", [
    ["pop", "", "Players online, max players and queue"],
    ["time", "", "In-game time and how long until sunset or sunrise"],
    ["wipe", "", "How long ago the server wiped"],
    ["team", "", "Who's online, AFK and offline", ["online", "offline", "afk"]],
  ]],
  ["World events", [
    ["next", "", "When heli, cargo, chinook and vendor are next expected", ["spawns"]],
    ["events", "", "Every event's status in one go"],
    ["cargo", "", "Where cargo is, how long it's been out, docked, when it leaves, next spawn"],
    ["heli", "", "Patrol heli position, or when and where it last went down, next spawn"],
    ["chinook", "", "Last locked crate drop and where, next spawn", ["ch47", "crate"]],
    ["oil", "", "Both oil rigs with the crate timer"],
    ["smoil", "", "Small oil rig only", ["small"]],
    ["loil", "", "Large oil rig only", ["large"]],
    ["bradley", "", "When Bradley was destroyed and its respawn estimate", ["brad"]],
    ["vendor", "", "Travelling vendor, next spawn"],
  ]],
  ["Shops and items", [
    ["buy", "<item>", "Cheapest shops selling an item, with grid", [], "buy ak"],
    ["sell", "<item>", "Shops that take an item as payment", [], "sell scrap"],
    ["craft", "<item>", "Crafting cost and workbench level", [], "craft ak"],
    ["recycle", "<item>", "What a safe-zone recycler gives back", [], "recycle roadsign jacket"],
    ["grecycle", "<item>", "What a monument recycler gives back", [], "grecycle roadsign jacket"],
  ]],
  ["Smart devices", [
    ["switch", "<id> <name>", "Name a smart switch so it can be toggled from chat", [], "switch 12345 turrets", true],
    ["switches", "", "All switches and whether they're on or off"],
    ["upkeep", "", "How long until your base decays (TC storage monitors)"],
  ]],
  ["Team and tracking", [
    ["tracked", "", "Which of your tagged players are online"],
    ["promote", "<name>", "Make a teammate leader (leader only)", ["leader"], "promote bob", true],
    ["timer", "<time> <message>", "Post a message after 15s, 20m, 3h or 1h30m", [], "timer 10m bradley up", true],
    ["timers", "", "Running timers"],
  ]],
  ["Other", [
    ["help", "", "Post the command list in chat"],
    ["version", "", "RustWatch version"],
    ["notification", "", "Send a test notification to your PC", [], "", true],
  ]],
];
function renderCommands() {
  const p = esc(S.settings.prefix || "!");
  const f = $("#cmdFilter").value.trim().toLowerCase().replace(/^[!.,:;]/, "");
  const switches = (S.devices || []).filter((d) => d.type === 1 && d.command);
  const groups = COMMANDS.map(([g, list]) => [g, list]);
  if (switches.length) groups.splice(4, 0, ["Your switches", switches.map((d) => [d.command, "[on|off]", `Toggle ${d.name} (currently ${d.value == null ? "unknown" : d.value ? "on" : "off"})`, [], d.command, true])]);
  const match = (c) => !f || [c[0], ...(c[3] || []), c[2]].join(" ").toLowerCase().includes(f);
  $("#cmdList").innerHTML = groups.map(([g, list]) => {
    const rows = list.filter(match);
    if (!rows.length) return "";
    return `<h2>${esc(g)}</h2><table class="cmds"><tbody>${rows.map(([name, args, desc, aliases = [], example = "", noTry]) => `<tr>
      <td class="c"><code>${p}${esc(name)}</code>${args ? ` <span class="muted">${esc(args)}</span>` : ""}${aliases.length ? `<div class="alias">also ${aliases.map((a) => `<code>${p}${esc(a)}</code>`).join(" ")}</div>` : ""}</td>
      <td>${esc(desc)}${example ? `<div class="muted" style="font-size:13px">e.g. <code>${p}${esc(example)}</code></div>` : ""}</td>
      <td style="text-align:right">${noTry ? "" : `<button class="ghost small" onclick="tryCmd('${esc(example || name)}')">Try</button>`}</td></tr>`).join("")}</tbody></table>`;
  }).join("") || `<div class="empty-note">No command matches "${esc(f)}".</div>`;
  if (!$("#cmdOut").innerHTML) $("#cmdOut").innerHTML = `<div class="muted">Replies show here.${S.status !== "connected" ? " Connect to a server first — most commands need live data." : ""}</div>`;
}
window.tryCmd = async (line) => {
  const p = S.settings.prefix || "!";
  $("#cmdOut").innerHTML = `<div class="muted">Running ${esc(p + line)}…</div>`;
  try {
    const r = await api("/api/command", { line: p + line });
    $("#cmdOut").innerHTML = `<div class="muted" style="font-size:13px">${esc(p + line)}</div><div class="reply">${esc(r.reply).replace(/\n/g, "<br>")}</div>`;
  } catch (e) { $("#cmdOut").innerHTML = `<div class="pill bad">${esc(e.message)}</div>`; }
};
$("#cmdFilter").addEventListener("input", renderCommands);

/* ---------- items ---------- */
$("#itemForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const res = await api("/api/items/search?q=" + encodeURIComponent(e.target.q.value));
  $("#itemResults").innerHTML = res.map((r) => `<div class="itemrow" tabindex="0" data-id="${r.id}">${icon(r.shortname)}<span>${esc(r.name)} <span class="muted">${esc(r.shortname)}</span></span></div>`).join("") || `<div class="empty-note">No matches.</div>`;
  $$(".itemrow").forEach((el) => { el.onclick = () => showItem(el.dataset.id); el.onkeydown = (k) => k.key === "Enter" && showItem(el.dataset.id); });
  if (res[0]) showItem(res[0].id);
});
async function showItem(id) {
  $$(".itemrow").forEach((el) => el.classList.toggle("sel", el.dataset.id == id));
  const d = await api("/api/items/" + id);
  const list = (ys) => ys && ys.length ? `<ul>${ys.map((y) => `<li>${y.quantity}× ${esc(y.name)}${y.chance && y.chance < 1 ? ` <span class="muted">(${Math.round(y.chance * 100)}% chance)</span>` : ""}</li>`).join("")}</ul>` : `<p class="muted">Not recyclable.</p>`;
  $("#itemDetail").innerHTML = `<div class="itemdetail"><h3>${icon(d.shortname)}${esc(d.name)}</h3><p class="muted">${esc(d.shortname)} · item ID ${d.id}</p>
    <h2>Craft</h2>${d.craft ? `<ul>${d.craft.ingredients.map((i) => `<li>${i.quantity}× ${esc(i.name)}</li>`).join("")}</ul><p class="muted">Workbench: ${esc(d.craft.workbench)}${d.craft.time ? " · " + esc(d.craft.time) : ""}</p>` : `<p class="muted">Can't be crafted.</p>`}
    <h2>Recycle (safe zone)</h2>${list(d.recycle)}<h2>Recycle (monument)</h2>${list(d.grecycle)}</div>`;
}

/* ---------- alerts ---------- */
function renderAlerts() {
  const sel = $("#alertFilter");
  if (sel.options.length === 1) S.alertKinds.forEach((k) => sel.add(new Option(k.label, k.key)));
  const f = sel.value;
  $("#alertLog").innerHTML = (S.log || []).filter((e) => !f || e.kind === f).slice().reverse().map((e) => `<div class="al"><time>${new Date(e.time * 1000).toLocaleString()}</time><b>${esc(e.title)}</b><div class="muted">${esc(e.text)}</div></div>`).join("") || `<div class="empty-note">No alerts yet.</div>`;
}
$("#alertFilter").addEventListener("change", renderAlerts);
$("#clearLog").addEventListener("click", () => act("/api/log/clear", {}));

/* ---------- servers & steam ---------- */
function renderServers() {
  const st = S.steam;
  $("#steamBox").innerHTML = `<div class="steam">${st.linked ? `<p><span class="pill ok">Steam linked</span> ${st.steamId ? `<span class="muted">Steam ID ${esc(st.steamId)}</span>` : ""} · push listener: ${esc(st.status)}</p>
      <p>To add a server or device: in Rust press <b>Esc → Rust+ → Pair with server</b>, or hold <b>E</b> on a smart switch, alarm or storage monitor and choose <b>Pair</b>. It shows up here within a few seconds.</p>
      <button class="ghost small" onclick="if(confirm('Unlink Steam? You will stop receiving pairing requests and alarm pushes.'))act('/api/steam/unlink',{})">Unlink Steam</button>`
    : `<h2 style="margin-top:0">Link your Steam account</h2>
      <ol><li>Click <b>Sign in with Steam</b>. Your default browser opens the official Rust+ login in a separate window.</li><li>Sign in to Steam there. The window closes by itself when it's done.</li><li>In Rust press <b>Esc → Rust+ → Pair with server</b>.</li></ol>
      <p class="muted">The login window uses its own clean browser profile, so you'll need to sign in to Steam once even if you're signed in normally. Your usual browser profile, tabs and passwords aren't touched.</p>
      ${st.status && st.status !== "not linked" ? `<p>${/^error/.test(st.status) ? '<span class="pill bad">Problem</span> ' : ""}${esc(st.status)}</p>` : ""}
      ${st.pairing ? `<button disabled>Waiting for Steam login…</button> <button class="ghost" onclick="act('/api/steam/cancel',{})">Cancel</button>` : `<button onclick="act('/api/steam/link',{})">${/^error/.test(st.status) ? "Try again" : "Sign in with Steam"}</button>`}`}</div>`;
  $("#serverList").innerHTML = (S.servers || []).map((s) => `<div class="srvrow">${s.img ? `<img src="${esc(s.img)}" alt="">` : ""}<div class="n"><b>${esc(s.name || s.ip)}</b><small>${esc(s.ip)}:${s.port} · ${s.devices} device${s.devices === 1 ? "" : "s"}</small></div>
    <label class="muted" style="display:flex;gap:5px;align-items:center;font-size:13px" title="Use if a direct connection is blocked by your network"><input type="checkbox" ${s.useProxy ? "checked" : ""} onchange="act('/api/servers/proxy',{id:'${esc(s.id)}',useProxy:this.checked})">Facepunch proxy</label>
    ${s.active ? `<span class="pill ok">Active</span><button class="ghost small" onclick="act('/api/servers/disconnect',{})">Disconnect</button>` : `<button class="small" onclick="act('/api/servers/connect',{id:'${esc(s.id)}'})">Connect</button>`}
    <button class="danger small" onclick="if(confirm('Remove this server?'))act('/api/servers/remove',{id:'${esc(s.id)}'})">Remove</button></div>`).join("") || `<div class="empty-note">No servers yet. Link Steam above, then pair from inside Rust.</div>`;
}
$("#manualForm").addEventListener("submit", (e) => {
  e.preventDefault();
  const f = e.target;
  act("/api/servers/add", { ip: f.ip.value.trim(), port: parseInt(f.port.value), playerId: f.pid.value.trim(), playerToken: f.tok.value.trim(), name: f.name.value, useProxy: f.proxy.checked }, "Server added").then(() => f.reset());
});

/* ---------- settings ---------- */
let settingsDirty = false;
function renderSettings(force) {
  if (settingsDirty && !force) return;
  const s = S.settings;
  const row = (label, input, hint = "") => `<div class="set"><label>${label}</label>${input}${hint ? `<div class="hint">${hint}</div>` : ""}</div>`;
  const num = (k, min = 1) => `<input type="number" min="${min}" name="${k}" value="${s[k]}" style="width:110px">`;
  const chk = (k) => `<input type="checkbox" name="${k}" ${s[k] ? "checked" : ""}>`;
  $("#settingsForm").innerHTML = `
    <h2>Team chat bot</h2>
    ${row("Answer chat commands", chk("chatCommands"), "Teammates can type commands like !pop in team chat.")}
    ${row("Command prefix", `<select name="prefix" style="width:80px">${["!", ".", ",", ":", ";"].map((p) => `<option ${p === s.prefix ? "selected" : ""}>${p}</option>`).join("")}</select>`)}
    ${row("Bot message tag", `<input name="botTag" value="${esc(s.botTag)}" style="width:110px">`, "Put in front of every bot message so you can tell them apart.")}
    <h2>Timings</h2>
    ${row("Oil rig crate unlock (minutes)", num("lockedCrateMinutes"))}
    ${row("Cargo egress after (minutes)", num("cargoEgressMinutes"))}
    ${row("Bradley respawn (minutes)", num("bradleyRespawnMinutes"))}
    ${row("Mark teammate AFK after (minutes)", num("afkMinutes"))}
    ${row("Warn when upkeep below (hours)", num("upkeepAlertHours", 0))}
    <h2>Event spawn timing</h2>
    <p class="muted" style="max-width:72ch">Rust picks a random delay in this range for patrol heli, cargo and the chinook drop, counting from server start (not affected by population). RustWatch times the first one from the wipe or a server restart, restarts the clock each time it sees the event, and switches to this server's real timing after a few sightings — so modded servers are handled too.</p>
    ${(S.forecasts || []).map((f) => { const w = (s.eventWindows || {})[f.kind] || [2, 4]; return row(esc(f.label) + " every", `<span class="inline" style="margin:0"><input type="number" step="0.25" min="0.25" name="ew_${f.kind}_0" value="${w[0]}" style="width:80px"> to <input type="number" step="0.25" min="0.25" name="ew_${f.kind}_1" value="${w[1]}" style="width:80px"> hours</span>`, esc(f.source)); }).join("")}
    <div class="set"><span></span><span><button type="button" class="ghost small" onclick="if(confirm('Forget the event timings RustWatch has learned for this server?'))act('/api/events/reset-learning',{},'Learned timings cleared')">Forget learned timings</button></span></div>
    <h2>Player tracker</h2>
    ${row("Player list refresh (seconds)", num("bmPollSeconds", 30))}
    ${row("BattleMetrics API key", `<input name="bmToken" type="password" value="${esc(s.bmToken)}" placeholder="optional, needs a paid plan" style="width:340px">`, "Leave empty to read the player list straight from the server (free). BattleMetrics has required a paid subscription since July 2026; with a key, tracked players are followed by BattleMetrics ID even if they change name.")}
    <h2>Kills</h2>
    ${row("Steam Web API key", `<input name="steamApiKey" type="password" value="${esc(s.steamApiKey || "")}" placeholder="free, from steamcommunity.com/dev/apikey" style="width:340px">`, `Shows teammates' kills, deaths and hours from Steam, and lets the player tracker follow people by Steam profile (works on servers that hide names). Get one free at <a href="https://steamcommunity.com/dev/apikey" target="_blank" rel="noopener">steamcommunity.com/dev/apikey</a> (any domain name works). Teammates need public game details on Steam.`)}
    <h2>Alerts</h2>
    ${row("Windows notifications", `<span>${notifyStatus()}</span>`, "Shown while the RustWatch window is open. Choose which alerts use them in the table below.")}
    ${row("Play a sound in the app", chk("sound"))}
    ${row("Discord webhook", `<input name="discordWebhook" value="${esc(s.discordWebhook)}" placeholder="https://discord.com/api/webhooks/…" style="width:100%">`, "Server settings → Integrations → Webhooks → New webhook → Copy URL.")}
    <table class="routes" style="max-width:900px;margin-top:10px"><thead><tr><th>Alert</th><th>Desktop</th><th>Team chat</th><th>Discord</th></tr></thead><tbody>
    ${S.alertKinds.map((k) => { const r = s.alerts[k.key] || {}; return `<tr><td>${esc(k.label)}</td>${["desktop", "chat", "discord"].map((c) => `<td><input type="checkbox" data-route="${k.key}" data-ch="${c}" ${r[c] ? "checked" : ""} aria-label="${esc(k.label)} ${c}"></td>`).join("")}</tr>`; }).join("")}
    </tbody></table>
    <h2 id="updBox">Updates</h2>
    ${updateBox()}
    ${row("Check for updates automatically", `<input type="checkbox" name="autoUpdate" ${s.noUpdateCheck ? "" : "checked"}>`, "Checks GitHub every 6 hours. Updates are only installed when you click Update now.")}
    <h2>Files</h2>
    ${row("Data folder", `<span><code>${esc(S.dataDir)}</code> <button type="button" class="ghost small" onclick="act('/api/datadir/open',{})">Open folder</button></span>`, "Settings and logs live here: the logs folder has one file per day for the app, alerts and team chat (last 30 days kept).")}
    ${row("Diagnostics", `<button type="button" class="ghost small" onclick="saveDiag()">Save diagnostics</button>`, "Writes a file to the logs folder describing what RustWatch sees from the server (no passwords or keys), for troubleshooting.")}
    ${row("Move data folder to", `<span class="inline" style="margin:0"><input id="newDataDir" placeholder="D:\\Games\\RustWatch" style="width:340px" aria-label="New data folder"><button type="button" class="ghost" onclick="moveData()">Move and restart</button></span>`, "Your settings and logs are copied over and RustWatch restarts. The old folder is left as it was, so you can delete it.")}
    <h2>App</h2>
    ${row("Keep running after closing the window", chk("keepRunning"), "Alerts keep coming while the window is closed. Start RustWatch again to reopen it.")}
    <div class="inline" style="margin-top:22px"><button>Save settings</button><button type="button" class="ghost" onclick="act('/api/notify/test',{},'Test sent')">Send test notification</button><button type="button" class="danger" onclick="if(confirm('Quit RustWatch? Alerts stop until you start it again.'))api('/api/quit',{})">Quit RustWatch</button><span class="muted">Version ${esc(S.version)}</span></div>`;
}
$("#settingsForm").addEventListener("input", () => { settingsDirty = true; });
$("#settingsForm").addEventListener("submit", (e) => {
  e.preventDefault();
  const f = e.target, s = structuredClone(S.settings);
  for (const k of ["chatCommands", "sound", "keepRunning"]) s[k] = f[k].checked;
  s.noUpdateCheck = !f.autoUpdate.checked;
  for (const k of ["prefix", "botTag", "bmToken", "steamApiKey", "discordWebhook"]) s[k] = f[k].value.trim();
  for (const k of ["lockedCrateMinutes", "cargoEgressMinutes", "bradleyRespawnMinutes", "afkMinutes", "upkeepAlertHours", "bmPollSeconds"]) s[k] = parseInt(f[k].value) || 0;
  s.eventWindows = s.eventWindows || {};
  for (const fc of S.forecasts || []) { const a = parseFloat(f["ew_" + fc.kind + "_0"]?.value), b = parseFloat(f["ew_" + fc.kind + "_1"]?.value); if (a > 0 && b >= a) s.eventWindows[fc.kind] = [a, b]; }
  $$("[data-route]", f).forEach((c) => { s.alerts[c.dataset.route] = s.alerts[c.dataset.route] || {}; s.alerts[c.dataset.route][c.dataset.ch] = c.checked; });
  act("/api/settings", s, "Settings saved").then(() => { settingsDirty = false; });
});

function updateBox() {
  const u = S.update || {};
  const checked = u.checked ? `checked ${ago(u.checked)}` : "not checked yet";
  let status;
  if (u.status === "downloading") status = `<span class="pill warn">Downloading ${esc(u.latest)}…</span>`;
  else if (u.status === "installing") status = `<span class="pill warn">Installing…</span>`;
  else if (u.available) status = `<span class="pill ok">Version ${esc(u.latest)} is available</span>`;
  else if (u.error) status = `<span class="pill bad">${esc(u.error)}</span>`;
  else if (u.checked) status = `<span class="pill">You're on the latest version</span>`;
  else status = `<span class="pill">${checked}</span>`;
  const notes = u.available && u.notes ? `<div class="updnotes">${esc(u.notes).replace(/\n/g, "<br>")}</div>` : "";
  return `<div class="set"><label>Installed version</label><span><b>${esc(u.current || S.version)}</b> <span class="muted">· ${checked}</span></span></div>
    <div class="set"><label>Status</label><span class="inline" style="margin:0">${status}
      ${u.available ? `<button type="button" onclick="installUpdate()" ${u.status ? "disabled" : ""}>Update now</button>` : ""}
      <button type="button" class="ghost small" onclick="act('/api/update/check',{}).catch(()=>{})">Check now</button>
      ${u.page ? `<a href="${esc(u.page)}" target="_blank" rel="noopener">What's new</a>` : ""}</span></div>
    ${notes ? `<div class="set"><span></span>${notes}</div>` : ""}
    <div class="set"><label>Update source</label><span class="muted">github.com/${esc(u.repo || "")}</span></div>`;
}
window.installUpdate = async () => {
  if (!confirm(`Install RustWatch ${S.update.latest}? It downloads the new version, checks it, and restarts. Your settings are kept.`)) return;
  try {
    toast("Updating RustWatch…", "Downloading and checking the new version.");
    await api("/api/update/install", {});
    toast("Restarting RustWatch…", "The new version opens in a few seconds.");
    setTimeout(() => window.close(), 1500);
  } catch (e) { toast("Update failed", e.message, "alarm"); refresh(); }
};

window.moveData = async () => {
  const path = $("#newDataDir").value.trim();
  if (!path) return toast("Enter a folder path", "For example D:\\Games\\RustWatch", "alarm");
  if (!confirm(`Move RustWatch's settings and logs to\n${path}\nand restart?`)) return;
  try {
    await api("/api/datadir/move", { path });
    toast("Restarting RustWatch…", "A new window opens in a few seconds.");
    setTimeout(() => window.close(), 1200);
  } catch (e) { toast("Couldn't move the data folder", e.message, "alarm"); }
};

/* ---------- timers ---------- */
$("#timerForm").addEventListener("submit", (e) => {
  e.preventDefault();
  act("/api/timers/add", { duration: e.target.d.value, message: e.target.m.value }, "Timer added").then(() => e.target.reset());
});

/* ---------- map ---------- */
const GRID = 146.25;
const MARK = { 1: "Teammate", 2: "Explosion", 3: "Shop", 4: "Chinook", 5: "Cargo ship", 6: "Locked crate", 7: "Event area", 8: "Patrol heli", 9: "Travelling vendor" };
const layers = { grid: true, monuments: true, team: true, shops: true, events: true, pins: true };
const LAYER_LABEL = { grid: "Grid", monuments: "Monuments", team: "Team", shops: "Shops", events: "Events", pins: "Map pins" };
const PIN_COLOURS = ["#e9d24a", "#3d8fe0", "#62c25a", "#d9483b", "#c25ad0", "#47c6c9"];
const mapImg = new Image();
let mapKey = "";
let pinnedTip = null;
function corrected(size) { const r = size % GRID; return r < 120 ? size - r : size + GRID - r; }
function colName(n) { let s = ""; n++; while (n > 0) { n--; s = String.fromCharCode(65 + (n % 26)) + s; n = Math.floor(n / 26); } return s; }
function outlinedText(ctx, text, x, y, fill = "#fff", size = 12, weight = 600, align = "center") {
  ctx.font = `${weight} ${size}px Bahnschrift, "Segoe UI", sans-serif`; ctx.textAlign = align; ctx.textBaseline = "middle";
  ctx.lineJoin = "round"; ctx.lineWidth = 3.5; ctx.strokeStyle = "rgba(12,14,16,.9)"; ctx.strokeText(text, x, y);
  ctx.fillStyle = fill; ctx.fillText(text, x, y);
}
function badge(ctx, x, y, r, fill, stroke = "#101214") { ctx.beginPath(); ctx.arc(x, y, r, 0, 7); ctx.fillStyle = fill; ctx.fill(); ctx.lineWidth = 2; ctx.strokeStyle = stroke; ctx.stroke(); }
// small vector glyphs (white), centred on x,y, scaled to fit radius r
const GLYPH = {
  cart(ctx, x, y, r) { const s = r / 7; ctx.beginPath(); ctx.moveTo(x - 4.5 * s, y - 3 * s); ctx.lineTo(x - 3 * s, y - 3 * s); ctx.lineTo(x - 1.8 * s, y + 1.6 * s); ctx.lineTo(x + 3.6 * s, y + 1.6 * s); ctx.lineTo(x + 4.6 * s, y - 1.8 * s); ctx.lineTo(x - 2.4 * s, y - 1.8 * s); ctx.stroke(); ctx.beginPath(); ctx.arc(x - 1.2 * s, y + 3.4 * s, 0.9 * s, 0, 7); ctx.arc(x + 3 * s, y + 3.4 * s, 0.9 * s, 0, 7); ctx.fill(); },
  ship(ctx, x, y, r) { const s = r / 7; ctx.beginPath(); ctx.moveTo(x - 5 * s, y); ctx.lineTo(x + 5 * s, y); ctx.lineTo(x + 3.5 * s, y + 3 * s); ctx.lineTo(x - 3.5 * s, y + 3 * s); ctx.closePath(); ctx.fill(); ctx.fillRect(x - 2.5 * s, y - 3.5 * s, 3 * s, 3.5 * s); ctx.fillRect(x + 1 * s, y - 2 * s, 2 * s, 2 * s); },
  heli(ctx, x, y, r) { const s = r / 7; ctx.beginPath(); ctx.ellipse(x - 0.5 * s, y + 0.8 * s, 3 * s, 2 * s, 0, 0, 7); ctx.fill(); ctx.fillRect(x + 2 * s, y + 0.2 * s, 3.5 * s, 1 * s); ctx.fillRect(x - 5.5 * s, y - 2.8 * s, 10 * s, 0.9 * s); },
  chinook(ctx, x, y, r) { const s = r / 7; ctx.fillRect(x - 4.5 * s, y - 1 * s, 9 * s, 3 * s); ctx.fillRect(x - 6 * s, y - 3 * s, 4.5 * s, 0.9 * s); ctx.fillRect(x + 1.5 * s, y - 3 * s, 4.5 * s, 0.9 * s); },
  crate(ctx, x, y, r) { const s = r / 7; ctx.strokeRect(x - 3.5 * s, y - 2.5 * s, 7 * s, 5 * s); ctx.beginPath(); ctx.moveTo(x - 3.5 * s, y - 0.5 * s); ctx.lineTo(x + 3.5 * s, y - 0.5 * s); ctx.stroke(); ctx.fillRect(x - 0.8 * s, y - 1.2 * s, 1.6 * s, 2 * s); },
  boom(ctx, x, y, r) { const s = r / 7; ctx.beginPath(); for (let i = 0; i < 16; i++) { const a = (i / 16) * Math.PI * 2, rr = (i % 2 ? 2 : 5) * s; ctx.lineTo(x + Math.cos(a) * rr, y + Math.sin(a) * rr); } ctx.closePath(); ctx.fill(); },
  van(ctx, x, y, r) { const s = r / 7; ctx.fillRect(x - 4.5 * s, y - 2.5 * s, 6 * s, 4.5 * s); ctx.fillRect(x + 1.5 * s, y - 0.5 * s, 3 * s, 2.5 * s); ctx.beginPath(); ctx.arc(x - 2.5 * s, y + 2.6 * s, 1.1 * s, 0, 7); ctx.arc(x + 2.8 * s, y + 2.6 * s, 1.1 * s, 0, 7); ctx.fill(); },
};

class MapView {
  constructor(el) {
    this.el = el; this.scale = 0; this.ox = 0; this.oy = 0; this.hits = [];
    el.innerHTML = `<canvas></canvas><div class="zoom"><button class="ghost" aria-label="Zoom in">+</button><button class="ghost" aria-label="Zoom out">−</button><button class="ghost" aria-label="Fit map" style="font-size:13px">Fit</button></div>
      <div class="legend"></div><div class="empty"></div>`;
    this.c = $("canvas", el); this.ctx = this.c.getContext("2d");
    const [zi, zo, fit] = $$(".zoom button", el);
    zi.onclick = () => this.zoomAt(1.4); zo.onclick = () => this.zoomAt(1 / 1.4); fit.onclick = () => { this.scale = 0; this.draw(); };
    $(".legend", el).addEventListener("change", (e) => { const k = e.target.dataset.layer; if (k) { layers[k] = e.target.checked; maps.overview.draw(); maps.full.draw(); } });
    el.addEventListener("wheel", (e) => { e.preventDefault(); const r = this.c.getBoundingClientRect(); this.zoomAt(e.deltaY < 0 ? 1.25 : 1 / 1.25, e.clientX - r.left, e.clientY - r.top); }, { passive: false });
    let drag = null, moved = false;
    el.addEventListener("pointerdown", (e) => { if (e.target !== this.c) return; drag = { x: e.clientX, y: e.clientY, ox: this.ox, oy: this.oy }; moved = false; el.classList.add("dragging"); this.c.setPointerCapture(e.pointerId); });
    el.addEventListener("pointermove", (e) => {
      if (drag) { const dx = e.clientX - drag.x, dy = e.clientY - drag.y; if (Math.abs(dx) + Math.abs(dy) > 3) moved = true; this.ox = drag.ox + dx; this.oy = drag.oy + dy; this.draw(); return; }
      if (!pinnedTip) this.showTip(e, this.hitAt(e));
    });
    el.addEventListener("pointerup", (e) => {
      if (drag && !moved) { const h = this.hitAt(e); pinnedTip = h ? h : null; this.showTip(e, h, !!h); }
      drag = null; el.classList.remove("dragging");
    });
    el.addEventListener("pointerleave", () => { if (!pinnedTip) $("#tip").style.display = "none"; });
    new ResizeObserver(() => this.draw()).observe(el);
  }
  zoomAt(f, cx, cy) {
    const r = this.c.getBoundingClientRect();
    cx ??= r.width / 2; cy ??= r.height / 2;
    const ns = Math.min(10, Math.max(0.15, this.scale * f));
    this.ox = cx - (cx - this.ox) * (ns / this.scale); this.oy = cy - (cy - this.oy) * (ns / this.scale);
    this.scale = ns; this.draw();
  }
  w2s(x, y) {
    const m = S.map, mg = m.oceanMargin, ms = m.mapSize;
    const px = mg + (x / ms) * (m.width - 2 * mg), py = m.height - (mg + (y / ms) * (m.height - 2 * mg));
    return [this.ox + px * this.scale, this.oy + py * this.scale];
  }
  worldPx(d) { const m = S.map; return (d / m.mapSize) * (m.width - 2 * m.oceanMargin) * this.scale; }
  legend(counts) {
    const html = Object.keys(layers).map((k) => `<label><input type="checkbox" data-layer="${k}" ${layers[k] ? "checked" : ""}>${LAYER_LABEL[k]}${counts[k] != null ? ` <span class="muted">${counts[k]}</span>` : ""}</label>`).join("");
    const lg = $(".legend", this.el);
    if (lg.dataset.html !== html) { lg.innerHTML = html; lg.dataset.html = html; }
  }
  draw() {
    if (!this.el.offsetParent) return;
    const empty = $(".empty", this.el);
    const dpr = window.devicePixelRatio || 1, r = this.el.getBoundingClientRect();
    if (this.c.width !== Math.round(r.width * dpr) || this.c.height !== Math.round(r.height * dpr)) { this.c.width = r.width * dpr; this.c.height = r.height * dpr; }
    const ctx = this.ctx; ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    if (!S || !S.map) { ctx.clearRect(0, 0, r.width, r.height); empty.textContent = S && S.active ? "Loading map…" : "Pair and connect to a server to see the live map."; empty.style.display = "grid"; return; }
    empty.style.display = "none";
    const m = S.map;
    ctx.fillStyle = /^#[0-9a-f]{6}/i.test(m.background || "") ? m.background.slice(0, 7) : "#12404d";
    ctx.fillRect(0, 0, r.width, r.height);
    const key = (S.active && S.active.id) + ":" + m.width;
    if (mapKey !== key) { mapKey = key; mapImg.src = "/api/map.jpg?k=" + encodeURIComponent(key); mapImg.onload = () => { maps.overview.draw(); maps.full.draw(); }; }
    if (!this.scale) { this.scale = Math.min(r.width / m.width, r.height / m.height); this.ox = (r.width - m.width * this.scale) / 2; this.oy = (r.height - m.height * this.scale) / 2; }
    if (mapImg.complete && mapImg.naturalWidth) { ctx.imageSmoothingQuality = "high"; ctx.drawImage(mapImg, this.ox, this.oy, m.width * this.scale, m.height * this.scale); }
    this.hits = [];
    const ms = corrected(m.mapSize);
    const counts = { shops: (S.vending || []).length, events: (S.markers || []).filter((k) => k.type !== 1).length, pins: (S.notes || []).length };

    if (layers.grid) {
      const n = Math.floor(ms / GRID);
      const [x0, y0] = this.w2s(0, ms), cell = this.worldPx(GRID);
      ctx.strokeStyle = "rgba(0,0,0,.32)"; ctx.lineWidth = 1; ctx.beginPath();
      for (let i = 0; i <= n; i++) { ctx.moveTo(x0 + i * cell, y0); ctx.lineTo(x0 + i * cell, y0 + n * cell); ctx.moveTo(x0, y0 + i * cell); ctx.lineTo(x0 + n * cell, y0 + i * cell); }
      ctx.stroke();
      if (cell > 20) {
        ctx.fillStyle = "rgba(0,0,0,.6)"; ctx.font = `600 ${Math.max(8, Math.min(13, cell / 3.2))}px Bahnschrift, sans-serif`; ctx.textAlign = "left"; ctx.textBaseline = "top";
        for (let cx = 0; cx < n; cx++) for (let cy = 0; cy < n; cy++) ctx.fillText(colName(cx) + cy, x0 + cx * cell + 2, y0 + cy * cell + 2);
      }
    }
    if (layers.monuments) {
      const size = Math.max(10, Math.min(14, 10 + this.scale * 2));
      for (const mo of m.monuments || []) {
        if (/^(DungeonBase|train_tunnel)/i.test(mo.token)) continue;
        const [x, y] = this.w2s(mo.x, mo.y), name = m.monumentNames[mo.token] || mo.token;
        ctx.save(); ctx.translate(x, y - 9); ctx.rotate(Math.PI / 4);
        ctx.fillStyle = "rgba(239,231,214,.95)"; ctx.strokeStyle = "rgba(12,14,16,.9)"; ctx.lineWidth = 1.5; ctx.fillRect(-3.5, -3.5, 7, 7); ctx.strokeRect(-3.5, -3.5, 7, 7); ctx.restore();
        outlinedText(ctx, name, x, y + 4, "#f3ecdc", size);
      }
    }
    if (layers.events) for (const mk of S.markers || []) {
      if (mk.type !== 7) continue;
      const [x, y] = this.w2s(mk.x, mk.y), c = mk.color1 || [1, 0.3, 0.3, 1];
      const rgb = `${Math.round(c[0] * 255)},${Math.round(c[1] * 255)},${Math.round(c[2] * 255)}`;
      ctx.beginPath(); ctx.arc(x, y, Math.max(6, this.worldPx(mk.radius || 50)), 0, 7);
      ctx.fillStyle = `rgba(${rgb},${mk.alpha || 0.25})`; ctx.fill(); ctx.strokeStyle = `rgba(${rgb},.9)`; ctx.lineWidth = 1.5; ctx.stroke();
      this.hits.push({ x, y, r: Math.max(8, this.worldPx(mk.radius || 50)), tip: () => `<b>${esc(mk.name || "Event area")}</b> · ${esc(mk.grid)}` });
    }
    if (layers.shops) for (const v of S.vending || []) {
      const [x, y] = this.w2s(v.x, v.y), rr = this.scale > 1.2 ? 8 : 6.5;
      const empty = v.outOfStock || !(v.orders || []).some((o) => o.amountInStock > 0);
      badge(ctx, x, y, rr, empty ? "#6f7477" : "#5aa83c");
      ctx.strokeStyle = "#fff"; ctx.fillStyle = "#fff"; ctx.lineWidth = 1.3; GLYPH.cart(ctx, x, y, rr);
      if (this.scale > 1.8) outlinedText(ctx, v.name, x, y + rr + 8, "#e3f3d6", 11, 500);
      this.hits.push({ x, y, r: rr + 3, tip: () => `<b>${esc(v.name)}</b> · ${esc(v.grid)}${empty ? ' <span class="muted">(empty)</span>' : ""}<br>${(v.orders || []).map((o) => `<span class="${o.amountInStock ? "" : "muted"}">${o.quantity}× ${esc(itemName(o.itemId))}${o.itemIsBlueprint ? " BP" : ""} → ${o.costPerItem} ${esc(itemName(o.currencyId))} · ${o.amountInStock} left</span>`).join("<br>") || "No orders"}` });
    }
    if (layers.pins) for (const n of S.notes || []) {
      const [x, y] = this.w2s(n.x, n.y);
      if (n.type === 0) {
        ctx.strokeStyle = "#101214"; ctx.lineWidth = 5; ctx.beginPath(); ctx.moveTo(x - 6, y - 6); ctx.lineTo(x + 6, y + 6); ctx.moveTo(x + 6, y - 6); ctx.lineTo(x - 6, y + 6); ctx.stroke();
        ctx.strokeStyle = "#e5483b"; ctx.lineWidth = 2.6; ctx.stroke();
        this.hits.push({ x, y, r: 9, tip: () => `<b>Your last death</b> · ${esc(gridOf(n.x, n.y))}` });
      } else {
        const col = PIN_COLOURS[n.colour] || PIN_COLOURS[0];
        ctx.beginPath(); ctx.moveTo(x, y); ctx.bezierCurveTo(x - 7, y - 9, x - 7, y - 19, x, y - 19); ctx.bezierCurveTo(x + 7, y - 19, x + 7, y - 9, x, y);
        ctx.fillStyle = col; ctx.fill(); ctx.strokeStyle = "#101214"; ctx.lineWidth = 1.5; ctx.stroke();
        ctx.beginPath(); ctx.arc(x, y - 13, 2.4, 0, 7); ctx.fillStyle = "#101214"; ctx.fill();
        if (n.label) outlinedText(ctx, n.label, x, y - 26, "#fff", 12);
        this.hits.push({ x, y: y - 12, r: 10, tip: () => `<b>${esc(n.label || "Map pin")}</b> · ${esc(gridOf(n.x, n.y))}${n.leader ? " (leader pin)" : ""}` });
      }
    }
    if (layers.events) for (const mk of S.markers || []) {
      if (mk.type === 1 || mk.type === 3 || mk.type === 7) continue;
      const [x, y] = this.w2s(mk.x, mk.y);
      const spec = { 2: ["#c8402f", "boom"], 4: ["#3b4a57", "chinook"], 5: ["#2c5d84", "ship"], 6: ["#c9962c", "crate"], 8: ["#a8322b", "heli"], 9: ["#7a5a2e", "van"] }[mk.type] || ["#555", "boom"];
      const rr = 11;
      if (mk.type === 5 || mk.type === 8 || mk.type === 4) { // heading tick
        const a = ((mk.rotation || 0) - 90) * Math.PI / 180;
        ctx.beginPath(); ctx.moveTo(x + Math.cos(a) * (rr + 7), y + Math.sin(a) * (rr + 7)); ctx.lineTo(x + Math.cos(a + 0.45) * rr, y + Math.sin(a + 0.45) * rr); ctx.lineTo(x + Math.cos(a - 0.45) * rr, y + Math.sin(a - 0.45) * rr); ctx.closePath();
        ctx.fillStyle = "#f2e9d8"; ctx.fill(); ctx.strokeStyle = "#101214"; ctx.lineWidth = 1; ctx.stroke();
      }
      badge(ctx, x, y, rr, spec[0], "#f2e9d8");
      ctx.fillStyle = "#fff"; ctx.strokeStyle = "#fff"; ctx.lineWidth = 1.4; GLYPH[spec[1]](ctx, x, y, rr - 1);
      this.hits.push({ x, y, r: rr + 2, tip: () => `<b>${MARK[mk.type] || "Marker"}</b> · ${esc(mk.grid)}` });
    }
    if (layers.team) for (const t of S.team || []) {
      const [x, y] = this.w2s(t.x, t.y);
      if (!t.isAlive) {
        if (!t.isOnline) continue;
        ctx.strokeStyle = "#101214"; ctx.lineWidth = 5; ctx.beginPath(); ctx.moveTo(x - 5, y - 5); ctx.lineTo(x + 5, y + 5); ctx.moveTo(x + 5, y - 5); ctx.lineTo(x - 5, y + 5); ctx.stroke();
        ctx.strokeStyle = "#e5483b"; ctx.lineWidth = 2.4; ctx.stroke();
      } else {
        const col = !t.isOnline ? "#9aa0a4" : t.afk ? "#e0ad3e" : "#7ee0a6";
        ctx.save(); ctx.translate(x, y); ctx.rotate((t.rotation || 0) * Math.PI / 180);
        ctx.beginPath(); ctx.moveTo(0, -9); ctx.lineTo(6.5, 6.5); ctx.lineTo(0, 3); ctx.lineTo(-6.5, 6.5); ctx.closePath();
        ctx.fillStyle = col; ctx.fill(); ctx.strokeStyle = "#101214"; ctx.lineWidth = 1.6; ctx.stroke(); ctx.restore();
      }
      outlinedText(ctx, t.name + (t.isOnline ? "" : " (offline)"), x, y - 15, t.isOnline ? "#fff" : "#c9cdd0", 12);
      this.hits.push({ x, y, r: 10, tip: () => `<b>${esc(t.name)}</b> · ${esc(t.grid)}<br>${!t.isOnline ? "Offline (sleeping)" : !t.isAlive ? "Dead" : t.afk ? "AFK " + t.afkFor : "Online"}` });
    }
    this.legend(counts);
  }
  hitAt(e) {
    const r = this.c.getBoundingClientRect(), mx = e.clientX - r.left, my = e.clientY - r.top;
    let best = null, bd = 1e9;
    for (const p of this.hits) { const d = Math.hypot(p.x - mx, p.y - my); if (d <= p.r && d < bd) { best = p; bd = d; } }
    return best;
  }
  showTip(e, h, pinned) {
    const tip = $("#tip");
    if (!h) { tip.style.display = "none"; return; }
    tip.innerHTML = h.tip() + (pinned ? `<div class="muted" style="font-size:11px;margin-top:4px">Click the map to close</div>` : "");
    tip.style.display = "block";
    tip.style.left = Math.min(window.innerWidth - tip.offsetWidth - 10, e.clientX + 14) + "px";
    tip.style.top = Math.min(window.innerHeight - tip.offsetHeight - 10, e.clientY + 14) + "px";
  }
}
function gridOf(x, y) {
  const ms = corrected(S.map.mapSize), n = Math.floor(ms / GRID);
  return colName(Math.floor(x / GRID)) + Math.max(0, Math.min(n - 1, n - 1 - Math.floor(y / GRID)));
}
const maps = { overview: new MapView($("#mapOverview")), full: new MapView($("#mapFull")) };

/* ---------- boot ---------- */
refresh();
connectStream();
setInterval(refresh, 15000);
