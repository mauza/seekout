/* seekout app — hash-routed views over the PocketBase REST + realtime API */
"use strict";

const pb = new PocketBase("/");
// PocketBase sends no cache headers, so a GET issued right after a mutation
// can be answered from the browser cache with stale rows. Opt every request
// out explicitly.
pb.beforeSend = (url, options) => {
  options.cache = "no-store";
  return { url, options };
};
const $ = (id) => document.getElementById(id);

const OPS = ["=", "!=", ">", "<", ">=", "<=", "in", "contains"];
const LISTING_FIELDS = ["title", "price", "url", "kind", "description", "external_id", "published"];

const state = {
  sources: [],
  alerters: [],
  seekers: [],
  alerts: [],
  listings: [],
  attrKeys: [],
  unseen: 0,
  listingFilter: "",
  pollResults: {},
  // Source and alerter types with their config fields, and the listing
  // kinds, from /api/seekout/plugins: the forms below are built from it,
  // so a type registered in Go shows up here with no UI change.
  plugins: { sources: [], alerters: [], kinds: [] },
};

/* ------------------------------------------------------------- utilities -- */

const esc = (s) =>
  String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

function el(html) {
  const t = document.createElement("template");
  t.innerHTML = html.trim();
  return t.content.firstElementChild;
}

function timeAgo(iso) {
  if (!iso) return "never";
  const secs = (Date.now() - new Date(iso.replace(" ", "T") + (iso.endsWith("Z") ? "" : "Z")).getTime()) / 1000;
  if (Number.isNaN(secs)) return "unknown";
  if (secs < 45) return "just now";
  const units = [["m", 60], ["h", 3600], ["d", 86400], ["mo", 2592000]];
  let out = `${Math.round(secs / 60)}m ago`;
  for (const [suffix, size] of units) {
    if (secs >= size) out = `${Math.floor(secs / size)}${suffix} ago`;
  }
  return out;
}

function absTime(iso) {
  if (!iso) return "";
  const d = new Date(iso.replace(" ", "T") + (iso.endsWith("Z") ? "" : "Z"));
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString();
}

const money = (n) => (n > 0 ? `$${Number(n).toLocaleString()}` : "");

function toast(message, kind = "") {
  const node = el(`<div class="toast ${kind}">${esc(message)}</div>`);
  $("toasts").append(node);
  setTimeout(() => {
    node.classList.add("leaving");
    setTimeout(() => node.remove(), 200);
  }, 4200);
}

function apiError(err) {
  const data = err?.response?.data;
  if (data && typeof data === "object") {
    const first = Object.entries(data)[0];
    if (first) {
      const [field, detail] = first;
      return `${field}: ${detail && typeof detail === "object" ? detail.message : detail}`;
    }
  }
  return err?.message || "Request failed";
}

/* ---------------------------------------------------------------- modals -- */

function openModal({ title, body, submitLabel = "Save", onSubmit, wide = false }) {
  const backdrop = el(`
    <div class="modal-backdrop">
      <div class="modal" role="dialog" aria-modal="true" ${wide ? 'style="width:min(720px,100%)"' : ""}>
        <header>
          <h2>${esc(title)}</h2>
          <button class="btn btn-icon btn-ghost" data-close aria-label="Close">
            <svg viewBox="0 0 24 24" width="16" height="16"><path d="m6 6 12 12M18 6 6 18" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>
          </button>
        </header>
        <form>
          <div class="body"></div>
          <footer>
            <p class="err-text grow"></p>
            <button type="button" class="btn" data-close>Cancel</button>
            <button type="submit" class="btn btn-primary">${esc(submitLabel)}</button>
          </footer>
        </form>
      </div>
    </div>`);

  const form = backdrop.querySelector("form");
  const bodyEl = backdrop.querySelector(".body");
  const errEl = backdrop.querySelector(".err-text");
  bodyEl.append(body);

  const close = () => {
    backdrop.remove();
    document.removeEventListener("keydown", onKey);
  };
  const onKey = (e) => e.key === "Escape" && close();

  backdrop.querySelectorAll("[data-close]").forEach((b) => b.addEventListener("click", close));
  backdrop.addEventListener("click", (e) => e.target === backdrop && close());
  document.addEventListener("keydown", onKey);

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const submit = form.querySelector('[type="submit"]');
    submit.disabled = true;
    errEl.textContent = "";
    try {
      await onSubmit(bodyEl);
      close();
    } catch (err) {
      errEl.textContent = apiError(err);
      submit.disabled = false;
    }
  });

  $("modal-host").append(backdrop);
  bodyEl.querySelector("input, select, textarea")?.focus();
  return { close };
}

function confirmDialog(title, message, confirmLabel = "Delete") {
  return new Promise((resolve) => {
    const body = el(`<p class="muted">${esc(message)}</p>`);
    const { close } = openModal({
      title,
      body,
      submitLabel: confirmLabel,
      onSubmit: async () => resolve(true),
    });
    // Resolve false if dismissed without submitting.
    const host = $("modal-host").lastElementChild;
    host.addEventListener("click", (e) => {
      if (e.target.closest("[data-close]") || e.target === host) resolve(false);
    });
    document.addEventListener("keydown", function onEsc(e) {
      if (e.key === "Escape") {
        document.removeEventListener("keydown", onEsc);
        resolve(false);
      }
    });
    void close;
  });
}

function switchField(label, checked) {
  return el(`
    <label class="row" style="gap:.6rem;cursor:pointer">
      <span class="switch"><input type="checkbox" ${checked ? "checked" : ""}><span class="track"></span></span>
      <span class="small muted">${esc(label)}</span>
    </label>`);
}

/* ------------------------------------------------------------------ data -- */

async function loadAll() {
  const [sources, alerters, seekers, plugins] = await Promise.all([
    pb.collection("sources").getFullList({ sort: "name" }),
    pb.collection("alerters").getFullList({ sort: "name" }),
    pb.collection("seekers").getFullList({ sort: "name" }),
    state.plugins.kinds.length ? state.plugins : pb.send("/api/seekout/plugins", {}),
  ]);
  state.sources = sources;
  state.alerters = alerters;
  state.seekers = seekers;
  state.plugins = plugins;
}

/* ---------------------------------------------------------- plugin forms -- */

// Admins manage what every user shares: sources and invites. The API
// enforces it; this only hides controls that would fail.
const isAdmin = () => Boolean(pb.authStore.record?.admin);

const pluginInfo = (group, type) => state.plugins[group].find((p) => p.type === type);

// configForm renders one input per declared config field (internal/plugin)
// for one type. Only the selected type's fields are in the DOM, so a
// required input is never hidden (a hidden required control blocks submit
// with nothing to focus).
function configForm(info, config) {
  const box = el(`<div class="stack"></div>`);
  if (!info) return box;
  if (info.description) box.append(el(`<p class="hint">${esc(info.description)}</p>`));
  const grid = el(`<div class="cfg-grid"></div>`);
  for (const f of info.fields) {
    const v = config?.[f.key] ?? (f.type === "select" || f.type === "number" ? f.default ?? "" : "");
    const hint = f.hint || (f.required ? "" : "optional");
    const attrs = `data-cfg="${esc(f.key)}" ${f.required ? "required" : ""}`;
    let input;
    if (f.type === "select") {
      input = `<select ${attrs}>${f.options.map((o) =>
        `<option ${o === v ? "selected" : ""}>${esc(o)}</option>`).join("")}</select>`;
    } else {
      const type = { url: "url", number: "number" }[f.type] || "text";
      const range = `${f.min != null ? `min="${f.min}"` : ""} ${f.max != null ? `max="${f.max}"` : ""}`;
      input = `<input ${attrs} type="${type}" ${range} autocomplete="off" spellcheck="false"
        placeholder="${esc(f.placeholder || "")}" value="${esc(v)}">`;
    }
    grid.append(el(`
      <div class="field ${f.type === "url" ? "cfg-wide" : ""}">
        <label>${esc(f.label)}${hint ? ` <span class="hint">${esc(hint)}</span>` : ""}</label>
        ${input}
      </div>`));
  }
  box.append(grid);
  return box;
}

// readConfig returns base with the form's fields applied. Keys the form
// doesn't offer (a webhook's headers, set over the API) are kept; a field
// left blank is removed so the type's default applies.
function readConfig(box, info, base) {
  const out = { ...base };
  for (const f of info?.fields || []) {
    const raw = box.querySelector(`[data-cfg="${CSS.escape(f.key)}"]`).value.trim();
    if (raw === "") delete out[f.key];
    else out[f.key] = f.type === "number" ? Number(raw) : raw;
  }
  return out;
}

// configSummary is the one-line description on a source or alerter card.
// Secrets are left out.
function configSummary(info, config) {
  if (!info) return "";
  return info.fields
    .filter((f) => f.type !== "secret")
    .map((f) => {
      const v = config?.[f.key] ?? f.default;
      if (v === undefined || v === "") return "";
      return f.type === "number" ? `${f.label.toLowerCase()} ${v}` : String(v);
    })
    .filter(Boolean)
    .join(" · ");
}

// typePicker builds the type picker plus the config section that follows
// it, re-rendering the section when the type changes. Returns the picker
// and a read() that gives the config to save.
function typePicker(group, record) {
  const infos = state.plugins[group];
  const current = record?.type || infos[0]?.type;
  const select = el(`<select name="type">${infos.map((p) =>
    `<option value="${esc(p.type)}" ${p.type === current ? "selected" : ""}>${esc(p.type)} &mdash; ${esc(p.label)}</option>`).join("")}</select>`);
  const section = el(`<div></div>`);
  // Editing keeps the record's config (and keys the form doesn't offer)
  // as long as the type stays the same.
  const baseFor = (type) => (record && record.type === type ? record.config || {} : {});
  const render = () => section.replaceChildren(configForm(pluginInfo(group, select.value), baseFor(select.value)));
  select.addEventListener("change", render);
  render();
  return {
    select,
    section,
    read: () => readConfig(section, pluginInfo(group, select.value), baseFor(select.value)),
  };
}

async function loadAlerts() {
  const res = await pb.collection("alerts").getList(1, 100, {
    sort: "-created",
    expand: "seeker,listing",
  });
  state.alerts = res.items;
  return res.totalItems;
}

async function loadListings() {
  const opts = { sort: "-created", expand: "source" };
  if (state.listingFilter) opts.filter = `source='${state.listingFilter}'`;
  const res = await pb.collection("listings").getList(1, 100, opts);
  state.listings = res.items;
  const keys = new Set(state.attrKeys);
  for (const l of res.items) {
    // Bare keys: the matcher paths straight into attrs, so a suggested
    // "attrs.beds" would resolve to attrs["attrs"]["beds"] and never match.
    for (const k of Object.keys(l.attrs || {})) keys.add(k);
  }
  state.attrKeys = [...keys];
  return res.totalItems;
}

const sourceName = (id) => state.sources.find((s) => s.id === id)?.name || "unknown source";
const alerterName = (id) => state.alerters.find((a) => a.id === id)?.name || "deleted alerter";

/* ----------------------------------------------------------------- views -- */

function setHead(title, sub, actions = []) {
  $("page-title").textContent = title;
  $("page-sub").textContent = sub;
  $("page-actions").replaceChildren(...actions);
}

function emptyState(icon, heading, text, action) {
  const node = el(`
    <div class="empty">
      ${icon}
      <h3>${esc(heading)}</h3>
      <p>${esc(text)}</p>
    </div>`);
  if (action) node.append(action);
  return node;
}

const ICON_BELL = `<svg viewBox="0 0 24 24"><path d="M12 3a6 6 0 0 0-6 6c0 5-2 6-2 6h16s-2-1-2-6a6 6 0 0 0-6-6Z" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linejoin="round"/><path d="M10 19a2 2 0 0 0 4 0" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linecap="round"/></svg>`;
const ICON_SEARCH = `<svg viewBox="0 0 24 24"><circle cx="11" cy="11" r="6" stroke="currentColor" stroke-width="1.8" fill="none"/><path d="m15.5 15.5 4 4" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`;
const ICON_FEED = `<svg viewBox="0 0 24 24"><path d="M4 11a9 9 0 0 1 9 9M4 5a15 15 0 0 1 15 15" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linecap="round"/><circle cx="5" cy="19" r="1.6" fill="currentColor"/></svg>`;
const ICON_LIST = `<svg viewBox="0 0 24 24"><rect x="4" y="5" width="16" height="14" rx="2.5" stroke="currentColor" stroke-width="1.8" fill="none"/><path d="M8 10h8M8 14h5" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`;

function skeletons(n = 3) {
  const box = el(`<div class="view-inner"></div>`);
  for (let i = 0; i < n; i++) box.append(el(`<div class="skeleton"></div>`));
  return box;
}

/* ---- alerts ---- */

function alertCard(a) {
  const listing = a.expand?.listing;
  const seeker = a.expand?.seeker;
  const delivered = Array.isArray(a.delivered) ? a.delivered : [];
  const badges = delivered.length
    ? delivered
        .map((d) =>
          d.ok
            ? `<span class="badge badge-ok badge-dot">${esc(d.name || "alerter")}</span>`
            : `<span class="badge badge-err badge-dot" title="${esc(d.error || "failed")}">${esc(d.name || "alerter")}</span>`)
        .join("")
    : `<span class="badge">feed only</span>`;

  const card = el(`
    <article class="card card-hover alert-card">
      ${listing?.image ? `<img class="thumb" src="${esc(listing.image)}" alt="" loading="lazy" onerror="this.remove()">` : ""}
      <div class="item">
        <div>
          <a class="item-title" href="${esc(a.url || listing?.url || "#")}" target="_blank" rel="noopener">
            ${esc(a.title || "(untitled listing)")}
          </a>
          <div class="item-meta">
            ${seeker ? `<span class="badge badge-accent">${esc(seeker.name)}</span>` : ""}
            ${listing?.price > 0 ? `<span class="tnum">${esc(money(listing.price))}</span>` : ""}
            ${listing ? `<span>${esc(sourceName(listing.source))}</span>` : ""}
            <span title="${esc(absTime(a.created))}">${esc(timeAgo(a.created))}</span>
          </div>
        </div>
        <div class="item-badges">${badges}</div>
      </div>
    </article>`);
  return card;
}

async function viewAlerts() {
  setHead("Alerts", "Matches from your seekers, newest first");
  $("view").replaceChildren(skeletons());

  const total = await loadAlerts();
  state.unseen = 0;
  renderNavCount();

  const box = el(`<div class="view-inner"></div>`);
  const enabledSeekers = state.seekers.filter((s) => s.enabled).length;
  const enabledSources = state.sources.filter((s) => s.enabled).length;
  box.append(el(`
    <div class="stats">
      <div class="card stat"><div class="n tnum" id="stat-alerts">${total}</div><div class="k">alerts</div></div>
      <div class="card stat"><div class="n tnum">${enabledSeekers}</div><div class="k">active seekers</div></div>
      <div class="card stat"><div class="n tnum">${enabledSources}</div><div class="k">active sources</div></div>
    </div>`));

  if (!state.alerts.length) {
    const cta = el(`<a class="btn btn-primary" href="#/sources">Add a source</a>`);
    box.append(emptyState(ICON_BELL, "No alerts yet",
      "Once a source polls and a seeker matches something new, it shows up here the moment it happens.", cta));
  } else {
    const list = el(`<div class="stack" id="alert-list"></div>`);
    for (const a of state.alerts) list.append(alertCard(a));
    box.append(list);
  }
  $("view").replaceChildren(box);
}

/* ---- seekers ---- */

// Soft conditions trade off against each other (see internal/match); only
// ordering ops can be soft, since they say which direction is better.
const SOFT_OPS = [">", ">=", "<", "<="];

function conditionRow(cond = { field: "", op: "=", value: "" }) {
  const row = el(`
    <div class="cond-row">
      <input data-k="field" type="text" list="field-suggestions" placeholder="field" value="${esc(cond.field)}">
      <select data-k="op">${OPS.map((o) => `<option ${o === cond.op ? "selected" : ""}>${o}</option>`).join("")}</select>
      <input data-k="value" type="text" placeholder="value" value="${esc(cond.value ?? "")}">
      <div class="cond-actions">
        <label class="soft-toggle" title="Soft: may trade off against other soft conditions">
          <input type="checkbox" data-k="soft" ${cond.soft ? "checked" : ""}> soft
        </label>
        <button type="button" class="btn btn-icon btn-ghost" aria-label="Remove condition">
          <svg viewBox="0 0 24 24" width="15" height="15"><path d="m6 6 12 12M18 6 6 18" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>
        </button>
      </div>
      <div class="soft-fields" ${cond.soft ? "" : "hidden"}>
        <label>floor <span class="hint">worst ok</span>
          <input data-k="floor" type="number" step="any" value="${esc(cond.floor ?? "")}"></label>
        <label>best <span class="hint">full credit</span>
          <input data-k="best" type="number" step="any" value="${esc(cond.best ?? "")}"></label>
        <label>weight <span class="hint">default 1</span>
          <input data-k="weight" type="number" step="any" min="0" placeholder="1" value="${esc(cond.weight ?? "")}"></label>
      </div>
    </div>`);
  const q = (k) => row.querySelector(`[data-k="${k}"]`);
  const sync = () => {
    const ordering = SOFT_OPS.includes(q("op").value);
    if (!ordering) q("soft").checked = false;
    q("soft").disabled = !ordering;
    row.querySelector(".soft-fields").hidden = !q("soft").checked;
  };
  q("op").addEventListener("change", sync);
  q("soft").addEventListener("change", sync);
  sync();
  row.querySelector("button").addEventListener("click", () => row.remove());
  return row;
}

// conditionFromRow reads a row back into the stored condition shape.
function conditionFromRow(row) {
  const q = (k) => row.querySelector(`[data-k="${k}"]`);
  const c = { field: q("field").value.trim(), op: q("op").value, value: q("value").value.trim() };
  if (q("soft").checked) {
    c.soft = true;
    for (const k of ["floor", "best", "weight"]) {
      const v = q(k).value.trim();
      if (v !== "") c[k] = Number(v);
    }
  }
  return c;
}

function conditionChip(c) {
  const soft = c.soft
    ? ` <span class="dim">~ ${esc(c.floor)}…${esc(c.best)}${c.weight && c.weight !== 1 ? ` ×${esc(c.weight)}` : ""}</span>`
    : "";
  return `<span class="chip${c.soft ? " chip-soft" : ""}"><b>${esc(c.field)}</b> ${esc(c.op)} ${esc(c.value)}${soft}</span>`;
}

function checkGrid(items, selected, labelFn) {
  const grid = el(`<div class="check-grid"></div>`);
  if (!items.length) {
    grid.append(el(`<p class="dim small">none yet</p>`));
    return grid;
  }
  for (const item of items) {
    const on = selected.includes(item.id);
    const label = el(`
      <label class="check-item ${on ? "on" : ""}">
        <input type="checkbox" value="${esc(item.id)}" ${on ? "checked" : ""}>
        <span class="truncate">${esc(labelFn(item))}</span>
      </label>`);
    const input = label.querySelector("input");
    input.addEventListener("change", () => label.classList.toggle("on", input.checked));
    grid.append(label);
  }
  return grid;
}

function seekerModal(seeker) {
  const editing = Boolean(seeker);
  const conds = Array.isArray(seeker?.conditions) ? seeker.conditions : [];

  const body = el(`
    <div class="stack">
      <div class="field">
        <label>Name</label>
        <input name="name" type="text" required placeholder="cheap miata" value="${esc(seeker?.name || "")}">
      </div>
      <div class="field">
        <label>Kind</label>
        <select name="kind">
          <option value="any" ${!seeker || seeker.kind === "any" ? "selected" : ""}>any kind</option>
          ${state.plugins.kinds.map((k) => `<option value="${k}" ${seeker?.kind === k ? "selected" : ""}>${k}</option>`).join("")}
        </select>
      </div>
      <div class="field">
        <label>Conditions <span class="hint">hard ones must hold; soft ones trade off (score 0 at the value, +1 at best, −1 at floor, sum ≥ 0)</span></label>
        <div class="cond-editor"></div>
        <button type="button" class="btn btn-sm" data-add-cond>+ condition</button>
        <datalist id="field-suggestions">
          ${[...LISTING_FIELDS, ...state.attrKeys].map((f) => `<option value="${esc(f)}">`).join("")}
        </datalist>
      </div>
      <div class="field">
        <label>Sources <span class="hint">none selected = all sources</span></label>
      </div>
      <div class="field">
        <label>Alerters <span class="hint">where matches get sent</span></label>
      </div>
    </div>`);

  const editor = body.querySelector(".cond-editor");
  for (const c of conds) editor.append(conditionRow(c));
  if (!conds.length) editor.append(conditionRow());
  body.querySelector("[data-add-cond]").addEventListener("click", () => editor.append(conditionRow()));

  const fields = body.querySelectorAll(".field");
  const sourcesGrid = checkGrid(state.sources, seeker?.sources || [], (s) => s.name);
  const alertersGrid = checkGrid(state.alerters, seeker?.alerters || [], (a) => `${a.name} (${a.type})`);
  fields[3].append(sourcesGrid);
  fields[4].append(alertersGrid);

  const enabled = switchField("enabled", seeker ? seeker.enabled : true);
  body.append(enabled);

  openModal({
    title: editing ? "Edit seeker" : "New seeker",
    body,
    submitLabel: editing ? "Save changes" : "Create seeker",
    onSubmit: async () => {
      const conditions = [...editor.querySelectorAll(".cond-row")]
        .map(conditionFromRow)
        .filter((c) => c.field);

      const data = {
        user: pb.authStore.record.id,
        name: body.querySelector('[name="name"]').value.trim(),
        kind: body.querySelector('[name="kind"]').value,
        sources: [...sourcesGrid.querySelectorAll("input:checked")].map((i) => i.value),
        alerters: [...alertersGrid.querySelectorAll("input:checked")].map((i) => i.value),
        conditions,
        enabled: enabled.querySelector("input").checked,
      };
      if (editing) await pb.collection("seekers").update(seeker.id, data);
      else await pb.collection("seekers").create(data);
      toast(editing ? "Seeker saved" : "Seeker created", "ok");
      await loadAll();
      route();
    },
  });
}

async function viewSeekers() {
  const add = el(`<button class="btn btn-primary">+ New seeker</button>`);
  add.addEventListener("click", () => seekerModal(null));
  setHead("Seekers", "Rules that decide what counts as a match", [add]);

  const box = el(`<div class="view-inner"></div>`);
  if (!state.seekers.length) {
    const cta = el(`<button class="btn btn-primary">+ New seeker</button>`);
    cta.addEventListener("click", () => seekerModal(null));
    box.append(emptyState(ICON_SEARCH, "No seekers yet",
      "A seeker names what you are looking for and lists the conditions a new listing has to satisfy.", cta));
    $("view").replaceChildren(box);
    return;
  }

  for (const s of state.seekers) {
    const conds = Array.isArray(s.conditions) ? s.conditions : [];
    const card = el(`
      <article class="card card-hover">
        <div class="item">
          <div>
            <div class="item-title">
              ${esc(s.name)}
              <span class="badge">${esc(s.kind)}</span>
              ${s.enabled ? "" : `<span class="badge badge-warn">paused</span>`}
            </div>
            <div class="item-meta">
              <span>${s.sources?.length ? `${s.sources.length} source${s.sources.length > 1 ? "s" : ""}` : "all sources"}</span>
              <span>${s.alerters?.length ? s.alerters.map(alerterName).map(esc).join(", ") : "feed only"}</span>
            </div>
          </div>
          <div class="item-actions">
            <button class="btn btn-sm" data-edit>Edit</button>
            <button class="btn btn-sm btn-danger" data-del>Delete</button>
          </div>
          <div class="item-note cond-list">
            ${conds.length
              ? conds.map(conditionChip).join("")
              : `<span class="chip">matches everything</span>`}
          </div>
        </div>
      </article>`);

    card.querySelector("[data-edit]").addEventListener("click", () => seekerModal(s));
    card.querySelector("[data-del]").addEventListener("click", async () => {
      if (!(await confirmDialog("Delete seeker", `Delete "${s.name}"? Its alerts go with it.`))) return;
      try {
        await pb.collection("seekers").delete(s.id);
        toast("Seeker deleted", "ok");
        await loadAll();
        route();
      } catch (err) {
        toast(apiError(err), "err");
      }
    });
    box.append(card);
  }
  $("view").replaceChildren(box);
}

/* ---- sources ---- */

function sourceModal(source) {
  const editing = Boolean(source);
  const picker = typePicker("sources", source);
  const body = el(`
    <div class="stack">
      <div class="field">
        <label>Name</label>
        <input name="name" type="text" required placeholder="slc cars" value="${esc(source?.name || "")}">
      </div>
      <div class="field" data-type>
        <label>Source type</label>
      </div>
      <div data-config></div>
      <div class="row" style="gap:.8rem;align-items:flex-end">
        <div class="field grow">
          <label>Kind</label>
          <select name="kind">
            ${state.plugins.kinds.map((k) => `<option value="${k}" ${source?.kind === k ? "selected" : ""}>${k}</option>`).join("")}
          </select>
        </div>
        <div class="field grow">
          <label>Poll every <span class="hint">minutes</span></label>
          <input name="interval" type="number" min="1" required value="${esc(source?.interval_minutes || 30)}">
        </div>
      </div>
      <p class="hint">The first poll of a new source stores its backlog quietly &mdash; alerts start with the next one.</p>
    </div>`);
  body.querySelector("[data-type]").append(picker.select);
  body.querySelector("[data-config]").replaceWith(picker.section);

  const enabled = switchField("enabled", source ? source.enabled : true);
  body.append(enabled);

  openModal({
    title: editing ? "Edit source" : "New source",
    body,
    submitLabel: editing ? "Save changes" : "Create source",
    onSubmit: async () => {
      const data = {
        name: body.querySelector('[name="name"]').value.trim(),
        type: picker.select.value,
        kind: body.querySelector('[name="kind"]').value,
        config: picker.read(),
        interval_minutes: Number(body.querySelector('[name="interval"]').value),
        enabled: enabled.querySelector("input").checked,
      };
      if (editing) await pb.collection("sources").update(source.id, data);
      else await pb.collection("sources").create(data);
      toast(editing ? "Source saved" : "Source created", "ok");
      await loadAll();
      route();
    },
  });
}

const sourceMeta = (s) => configSummary(pluginInfo("sources", s.type), s.config);

async function viewSources() {
  const admin = isAdmin();
  const add = el(`<button class="btn btn-primary">+ New source</button>`);
  add.addEventListener("click", () => sourceModal(null));
  setHead("Sources", admin ? "Feeds seekout polls for new listings" : "Feeds seekout polls for new listings (shared; an admin manages them)",
    admin ? [add] : []);

  const box = el(`<div class="view-inner"></div>`);
  if (!state.sources.length) {
    const cta = el(`<button class="btn btn-primary">+ New source</button>`);
    cta.addEventListener("click", () => sourceModal(null));
    box.append(emptyState(ICON_FEED, "No sources yet", admin
      ? "Add an RSS or Atom feed, or the Utah MLS source — anything that lists things."
      : "Sources are shared, and an admin adds them.", admin ? cta : undefined));
    $("view").replaceChildren(box);
    return;
  }

  for (const s of state.sources) {
    const card = el(`
      <article class="card card-hover">
        <div class="item">
          <div>
            <div class="item-title">
              ${esc(s.name)}
              <span class="badge">${esc(s.type)}</span>
              <span class="badge">${esc(s.kind)}</span>
              ${s.enabled ? "" : `<span class="badge badge-warn">paused</span>`}
              ${s.seeded ? "" : `<span class="badge badge-accent" title="First poll only stores the backlog">not seeded</span>`}
            </div>
            <div class="item-meta">
              <span class="mono truncate" style="max-width:44ch">${esc(sourceMeta(s))}</span>
              <span>every ${esc(s.interval_minutes)}m</span>
              <span title="${esc(absTime(s.last_polled))}">polled ${esc(timeAgo(s.last_polled))}</span>
            </div>
          </div>
          <div class="item-actions">
            <span class="small muted" data-status>${esc(state.pollResults[s.id] || "")}</span>
            <button class="btn btn-sm" data-poll>Poll now</button>
            ${admin ? `<button class="btn btn-sm" data-edit>Edit</button>
            <button class="btn btn-sm btn-danger" data-del>Delete</button>` : ""}
          </div>
          ${s.last_error ? `<div class="item-note error-note">
            <svg viewBox="0 0 24 24" width="15" height="15" style="flex:none"><path d="M12 8v5m0 3.5v.5" stroke="currentColor" stroke-width="2" stroke-linecap="round"/><circle cx="12" cy="12" r="9" stroke="currentColor" stroke-width="1.6" fill="none"/></svg>
            <span>${esc(s.last_error)}</span></div>` : ""}
        </div>
      </article>`);

    const status = card.querySelector("[data-status]");
    const pollBtn = card.querySelector("[data-poll]");
    pollBtn.addEventListener("click", async () => {
      pollBtn.disabled = true;
      status.replaceChildren(el(`<span class="row"><span class="spinner"></span> polling…</span>`));
      try {
        const res = await pb.send(`/api/seekout/sources/${s.id}/poll`, { method: "POST" });
        state.pollResults[s.id] = res.seeded
          ? `seeded ${res.new} listing${res.new === 1 ? "" : "s"}`
          : `${res.new} new · ${res.matched} matched`;
        status.textContent = state.pollResults[s.id];
        toast(res.seeded
          ? `${s.name}: stored ${res.new} existing listing${res.new === 1 ? "" : "s"}, alerts start next poll`
          : `${s.name}: ${res.new} new, ${res.matched} matched`, "ok");
        await loadAll();
        if (location.hash === "#/sources") route();
      } catch (err) {
        status.textContent = "";
        toast(apiError(err), "err");
        await loadAll();
        route();
      } finally {
        pollBtn.disabled = false;
      }
    });

    card.querySelector("[data-edit]")?.addEventListener("click", () => sourceModal(s));
    card.querySelector("[data-del]")?.addEventListener("click", async () => {
      if (!(await confirmDialog("Delete source", `Delete "${s.name}"? Its listings and their alerts go with it.`))) return;
      try {
        await pb.collection("sources").delete(s.id);
        toast("Source deleted", "ok");
        await loadAll();
        route();
      } catch (err) {
        toast(apiError(err), "err");
      }
    });
    box.append(card);
  }
  $("view").replaceChildren(box);
}

/* ---- listings ---- */

async function viewListings() {
  const filter = el(`
    <select style="width:auto">
      <option value="">all sources</option>
      ${state.sources.map((s) => `<option value="${esc(s.id)}" ${state.listingFilter === s.id ? "selected" : ""}>${esc(s.name)}</option>`).join("")}
    </select>`);
  filter.addEventListener("change", () => {
    state.listingFilter = filter.value;
    viewListings();
  });
  setHead("Listings", "Everything ingested so far — useful for picking condition fields", [filter]);
  $("view").replaceChildren(skeletons(4));

  const total = await loadListings();
  const box = el(`<div class="view-inner"></div>`);

  if (!state.listings.length) {
    box.append(emptyState(ICON_LIST, "Nothing ingested yet",
      "Listings appear here after a source polls. Their field names are what seeker conditions match on."));
    $("view").replaceChildren(box);
    return;
  }

  box.append(el(`<p class="small dim">${total} listing${total === 1 ? "" : "s"} stored${state.listingFilter ? " in this source" : ""}</p>`));
  for (const l of state.listings) {
    box.append(el(`
      <article class="card card-hover alert-card">
        ${l.image ? `<img class="thumb" src="${esc(l.image)}" alt="" loading="lazy" onerror="this.remove()">` : ""}
        <div class="item">
          <div>
            <a class="item-title" href="${esc(l.url || "#")}" target="_blank" rel="noopener">${esc(l.title || "(untitled)")}</a>
            <div class="item-meta">
              ${l.price > 0 ? `<span class="tnum">${esc(money(l.price))}</span>` : ""}
              <span class="badge">${esc(l.kind || "other")}</span>
              <span>${esc(l.expand?.source?.name || sourceName(l.source))}</span>
              <span title="${esc(absTime(l.published || l.created))}">${esc(timeAgo(l.published || l.created))}</span>
            </div>
          </div>
          <div class="item-badges">
            ${Object.keys(l.attrs || {}).length ? `<span class="chip" title="${esc(JSON.stringify(l.attrs))}">attrs</span>` : ""}
          </div>
        </div>
      </article>`));
  }
  $("view").replaceChildren(box);
}

/* ---- alerters ---- */

function alerterModal(alerter) {
  const editing = Boolean(alerter);
  const picker = typePicker("alerters", alerter);
  const body = el(`
    <div class="stack">
      <div class="field">
        <label>Name</label>
        <input name="name" type="text" required placeholder="my phone" value="${esc(alerter?.name || "")}">
      </div>
      <div class="field" data-type>
        <label>Type</label>
      </div>
    </div>`);
  body.querySelector("[data-type]").append(picker.select);
  body.append(picker.section);

  const enabled = switchField("enabled", alerter ? alerter.enabled : true);
  body.append(enabled);

  openModal({
    title: editing ? "Edit alerter" : "New alerter",
    body,
    submitLabel: editing ? "Save changes" : "Create alerter",
    onSubmit: async () => {
      const data = {
        user: pb.authStore.record.id,
        name: body.querySelector('[name="name"]').value.trim(),
        type: picker.select.value,
        config: picker.read(),
        enabled: enabled.querySelector("input").checked,
      };
      if (editing) await pb.collection("alerters").update(alerter.id, data);
      else await pb.collection("alerters").create(data);
      toast(editing ? "Alerter saved" : "Alerter created", "ok");
      await loadAll();
      route();
    },
  });
}

async function viewAlerters() {
  const add = el(`<button class="btn btn-primary">+ New alerter</button>`);
  add.addEventListener("click", () => alerterModal(null));
  setHead("Alerters", "Where matches get delivered", [add]);

  const box = el(`<div class="view-inner"></div>`);
  if (!state.alerters.length) {
    const cta = el(`<button class="btn btn-primary">+ New alerter</button>`);
    cta.addEventListener("click", () => alerterModal(null));
    box.append(emptyState(ICON_BELL, "No alerters yet",
      "Add an ntfy topic to get push notifications, or a webhook to forward matches to Slack.", cta));
    $("view").replaceChildren(box);
    return;
  }

  for (const a of state.alerters) {
    const target = configSummary(pluginInfo("alerters", a.type), a.config);
    const card = el(`
      <article class="card card-hover">
        <div class="item">
          <div>
            <div class="item-title">
              ${esc(a.name)}
              <span class="badge badge-accent">${esc(a.type)}</span>
              ${a.enabled ? "" : `<span class="badge badge-warn">paused</span>`}
            </div>
            <div class="item-meta"><span class="mono truncate" style="max-width:48ch">${esc(target)}</span></div>
          </div>
          <div class="item-actions">
            <span class="small muted" data-status></span>
            <button class="btn btn-sm" data-test>Send test</button>
            <button class="btn btn-sm" data-edit>Edit</button>
            <button class="btn btn-sm btn-danger" data-del>Delete</button>
          </div>
        </div>
      </article>`);

    const status = card.querySelector("[data-status]");
    const testBtn = card.querySelector("[data-test]");
    testBtn.addEventListener("click", async () => {
      testBtn.disabled = true;
      status.replaceChildren(el(`<span class="row"><span class="spinner"></span> sending…</span>`));
      try {
        await pb.send(`/api/seekout/alerters/${a.id}/test`, { method: "POST" });
        status.replaceChildren(el(`<span class="badge badge-ok badge-dot">delivered</span>`));
        toast(`Test sent through ${a.name}`, "ok");
      } catch (err) {
        status.replaceChildren(el(`<span class="badge badge-err badge-dot">failed</span>`));
        toast(apiError(err), "err");
      } finally {
        testBtn.disabled = false;
      }
    });

    card.querySelector("[data-edit]").addEventListener("click", () => alerterModal(a));
    card.querySelector("[data-del]").addEventListener("click", async () => {
      if (!(await confirmDialog("Delete alerter", `Delete "${a.name}"? Seekers using it stop notifying.`))) return;
      try {
        await pb.collection("alerters").delete(a.id);
        toast("Alerter deleted", "ok");
        await loadAll();
        route();
      } catch (err) {
        toast(apiError(err), "err");
      }
    });
    box.append(card);
  }
  $("view").replaceChildren(box);
}

/* ---- invites (admins) ---- */

const inviteLink = (code) => `${location.origin}/?invite=${encodeURIComponent(code)}`;

async function copyText(text, what) {
  try {
    await navigator.clipboard.writeText(text);
    toast(`${what} copied`, "ok");
  } catch {
    toast(text);
  }
}

function inviteModal() {
  const body = el(`
    <div class="stack">
      <div class="field">
        <label>Note <span class="hint">optional, e.g. who it's for</span></label>
        <input name="note" type="text" placeholder="for Sam">
      </div>
      <p class="hint">Each invite is a one-time code: it works for exactly one signup.</p>
    </div>`);
  openModal({
    title: "New invite",
    body,
    submitLabel: "Create invite",
    onSubmit: async () => {
      const inv = await pb.collection("invites").create({ note: body.querySelector('[name="note"]').value.trim() });
      await copyText(inviteLink(inv.code), "Invite link");
      route();
    },
  });
}

async function viewInvites() {
  const add = el(`<button class="btn btn-primary">+ New invite</button>`);
  add.addEventListener("click", inviteModal);
  setHead("Invites", "One-time signup codes", [add]);

  const invites = await pb.collection("invites").getFullList({ sort: "-created" });
  const box = el(`<div class="view-inner"></div>`);
  if (!invites.length) {
    const cta = el(`<button class="btn btn-primary">+ New invite</button>`);
    cta.addEventListener("click", inviteModal);
    box.append(emptyState(ICON_BELL, "No invites yet", "Signup needs an invite code. Create one and share its link.", cta));
    $("view").replaceChildren(box);
    return;
  }
  for (const inv of invites) {
    const used = Boolean(inv.used_by);
    const card = el(`
      <article class="card card-hover">
        <div class="item">
          <div>
            <div class="item-title">
              <span class="mono">${esc(inv.code)}</span>
              ${used ? `<span class="badge">used</span>` : `<span class="badge badge-ok">unused</span>`}
            </div>
            <div class="item-meta">
              ${inv.note ? `<span>${esc(inv.note)}</span>` : ""}
              <span title="${esc(absTime(inv.created))}">created ${esc(timeAgo(inv.created))}</span>
              ${used ? `<span title="${esc(absTime(inv.used_at))}">used ${esc(timeAgo(inv.used_at))}</span>` : ""}
            </div>
          </div>
          <div class="item-actions">
            ${used ? "" : `<button class="btn btn-sm" data-copy>Copy link</button>`}
            <button class="btn btn-sm btn-danger" data-del>Delete</button>
          </div>
        </div>
      </article>`);
    card.querySelector("[data-copy]")?.addEventListener("click", () => copyText(inviteLink(inv.code), "Invite link"));
    card.querySelector("[data-del]").addEventListener("click", async () => {
      if (!(await confirmDialog("Delete invite", used
        ? "Delete this used invite from the list? The account it created stays."
        : "Delete this invite? Its code stops working."))) return;
      try {
        await pb.collection("invites").delete(inv.id);
        toast("Invite deleted", "ok");
        route();
      } catch (err) {
        toast(apiError(err), "err");
      }
    });
    box.append(card);
  }
  $("view").replaceChildren(box);
}

/* ---------------------------------------------------------------- router -- */

const ROUTES = {
  alerts: viewAlerts,
  seekers: viewSeekers,
  sources: viewSources,
  listings: viewListings,
  alerters: viewAlerters,
  invites: viewInvites,
};

function currentRoute() {
  const name = location.hash.replace(/^#\/?/, "") || "alerts";
  if (name === "invites" && !isAdmin()) return "alerts";
  return ROUTES[name] ? name : "alerts";
}

function renderNavCount() {
  const badge = $("nav-alerts-count");
  badge.hidden = state.unseen === 0;
  badge.textContent = state.unseen;
}

async function route() {
  const name = currentRoute();
  for (const a of document.querySelectorAll("#nav a")) {
    a.classList.toggle("active", a.dataset.route === name);
  }
  $("sidebar").classList.remove("open");
  try {
    await ROUTES[name]();
  } catch (err) {
    $("view").replaceChildren(el(`<div class="view-inner"><div class="error-note">${esc(apiError(err))}</div></div>`));
  }
}

window.addEventListener("hashchange", route);
$("menu-toggle").addEventListener("click", () => {
  $("sidebar").classList.toggle("open");
  document.body.classList.toggle("nav-open", $("sidebar").classList.contains("open"));
});
// tapping the page behind the mobile drawer dismisses it
document.querySelector(".main").addEventListener("click", () => {
  if (!$("sidebar").classList.contains("open")) return;
  $("sidebar").classList.remove("open");
  document.body.classList.remove("nav-open");
});
$("signout").addEventListener("click", () => {
  pb.authStore.clear();
  location.href = "/";
});

/* -------------------------------------------------------------- realtime -- */

async function subscribeAlerts() {
  await pb.collection("alerts").subscribe("*", async (e) => {
    if (e.action !== "create") return;
    let record = e.record;
    try {
      record = await pb.collection("alerts").getOne(e.record.id, { expand: "seeker,listing" });
    } catch { /* fall back to the unexpanded record */ }

    if (currentRoute() === "alerts") {
      const list = $("alert-list");
      if (list) {
        const card = alertCard(record);
        card.classList.add("alert-new");
        list.prepend(card);
        const stat = $("stat-alerts");
        if (stat) stat.textContent = Number(stat.textContent) + 1;
      } else {
        route();
      }
    } else {
      state.unseen++;
      renderNavCount();
    }
    toast(`New match: ${record.title || "listing"}`, "ok");
  }, { expand: "seeker,listing" });
}

/* ------------------------------------------------------------------ boot -- */

(async () => {
  if (!pb.authStore.isValid) {
    location.replace("/");
    return;
  }
  try {
    await pb.collection("users").authRefresh();
  } catch {
    pb.authStore.clear();
    location.replace("/");
    return;
  }

  const email = pb.authStore.record?.email || "";
  $("account-email").textContent = email;
  $("account-avatar").textContent = (email[0] || "?").toUpperCase();
  if (isAdmin()) {
    $("nav").append(el(`
      <a href="#/invites" data-route="invites">
        <svg viewBox="0 0 24 24"><path d="M4 7h16v10H4z" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linejoin="round"/><path d="m4 8 8 5 8-5" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linejoin="round"/></svg>
        <span>Invites</span>
      </a>`));
  }

  try {
    await loadAll();
    await loadListings();
  } catch (err) {
    toast(apiError(err), "err");
  }
  await route();
  subscribeAlerts().catch(() => toast("Realtime updates unavailable", "err"));
})();
