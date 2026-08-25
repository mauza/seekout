/* seekout web UI — framework-free, talks to PocketBase REST + realtime. */
"use strict";

const pb = new PocketBase("/");

const $ = (id) => document.getElementById(id);

const state = {
  sources: [],
  alerters: [],
  seekers: [],
  subscribed: false,
};

const OPS = ["=", "!=", ">", "<", ">=", "<=", "in", "contains"];

// ---------------------------------------------------------------- auth ----

function showApp() {
  $("login").hidden = true;
  $("nav").hidden = false;
  switchTab("alerts");
  refreshAlerts();
  refreshSeekerDeps();
  refreshSources();
  refreshAlerters();
  subscribeAlerts();
}

function showLogin() {
  $("login").hidden = false;
  $("nav").hidden = true;
  for (const t of ["alerts", "seekers", "sources", "alerters"]) {
    $("tab-" + t).hidden = true;
  }
}

$("login-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  $("login-error").textContent = "";
  try {
    await pb.collection("users").authWithPassword($("login-email").value, $("login-password").value);
    showApp();
  } catch (err) {
    $("login-error").textContent = err.message;
  }
});

$("logout").addEventListener("click", () => {
  pb.authStore.clear();
  showLogin();
});

// ---------------------------------------------------------------- tabs ----

for (const btn of document.querySelectorAll("#nav [data-tab]")) {
  btn.addEventListener("click", () => switchTab(btn.dataset.tab));
}

function switchTab(name) {
  for (const btn of document.querySelectorAll("#nav [data-tab]")) {
    btn.classList.toggle("active", btn.dataset.tab === name);
  }
  for (const t of ["alerts", "seekers", "sources", "alerters"]) {
    $("tab-" + t).hidden = t !== name;
  }
}

// --------------------------------------------------------------- alerts ----

async function refreshAlerts() {
  const alerts = await pb.collection("alerts").getList(1, 100, { sort: "-created" });
  const list = $("alerts-list");
  list.innerHTML = "";
  for (const a of alerts.items) list.prepend(alertEl(a));
  if (!alerts.items.length) list.innerHTML = '<p class="dim">No alerts yet.</p>';
}

function alertEl(a) {
  const div = document.createElement("div");
  div.className = "card alert-item";
  const when = new Date(a.created).toLocaleString();
  const delivered = (a.delivered || [])
    .map((d) => `<span class="badge${d.ok ? "" : " err"}">${esc(d.name || d.alerter)}${d.ok ? "" : ": " + esc(d.error || "failed")}</span>`)
    .join("");
  div.innerHTML = `
    <a href="${esc(a.url || "#")}" target="_blank" rel="noopener">${esc(a.title || "(untitled)")}</a>
    ${delivered}
    <div class="alert-time">${esc(when)}</div>`;
  return div;
}

function subscribeAlerts() {
  if (state.subscribed) return;
  state.subscribed = true;
  pb.collection("alerts").subscribe("*", (e) => {
    if (e.action === "create") {
      const list = $("alerts-list");
      const empty = list.querySelector("p.dim");
      if (empty) empty.remove();
      list.prepend(alertEl(e.record));
    }
  });
}

// --------------------------------------------------- seekers & conditions ----

async function refreshSeekerDeps() {
  state.sources = (await pb.collection("sources").getFullList({ sort: "name" }));
  state.alerters = (await pb.collection("alerters").getFullList({ sort: "name" }));
  refreshSeekers();
}

async function refreshSeekers() {
  state.seekers = (await pb.collection("seekers").getFullList({ sort: "name" }));
  const list = $("seekers-list");
  list.innerHTML = "";
  for (const s of state.seekers) {
    const conds = (s.conditions || [])
      .map((c) => `${esc(c.field)} ${esc(c.op)} ${esc(String(c.value))}`)
      .join(" &amp; ");
    const div = document.createElement("div");
    div.className = "card";
    div.innerHTML = `
      <strong>${esc(s.name)}</strong>
      <span class="badge">${esc(s.kind)}</span>
      ${s.enabled ? "" : '<span class="badge err">disabled</span>'}
      <div class="dim">${conds || "matches everything"}</div>
      <div class="row">
        <button data-edit="${s.id}">edit</button>
        <button data-del="${s.id}" class="link">delete</button>
      </div>`;
    div.querySelector("[data-edit]").addEventListener("click", () => openSeekerForm(s));
    div.querySelector("[data-del]").addEventListener("click", async () => {
      if (confirm(`Delete seeker "${s.name}"?`)) {
        await pb.collection("seekers").delete(s.id);
        refreshSeekers();
      }
    });
    list.append(div);
  }
  if (!state.seekers.length) list.innerHTML = '<p class="dim">No seekers yet.</p>';
}

$("new-seeker").addEventListener("click", () => openSeekerForm(null));

function openSeekerForm(s) {
  $("seeker-form").hidden = false;
  $("seeker-form-title").textContent = s ? "Edit seeker" : "New seeker";
  $("seeker-id").value = s ? s.id : "";
  $("seeker-name").value = s ? s.name : "";
  $("seeker-kind").value = s ? s.kind : "any";
  $("seeker-enabled").checked = s ? s.enabled : true;
  $("seeker-error").textContent = "";

  $("seeker-sources").innerHTML = state.sources.map((src) => `
    <label class="check-list"><input type="checkbox" name="src" value="${src.id}"
      ${s && (s.sources || []).includes(src.id) ? "checked" : ""}> ${esc(src.name)}</label>`).join("")
    || '<span class="dim">no sources yet</span>';

  $("seeker-alerters").innerHTML = state.alerters.map((a) => `
    <label class="check-list"><input type="checkbox" name="alt" value="${a.id}"
      ${s && (s.alerters || []).includes(a.id) ? "checked" : ""}> ${esc(a.name)}</label>`).join("")
    || '<span class="dim">no alerters yet</span>';

  const conds = s && s.conditions ? s.conditions : [];
  $("seeker-conditions").innerHTML = "";
  for (const c of conds) addConditionRow(c);
}

$("add-condition").addEventListener("click", () => addConditionRow({ field: "", op: "=", value: "" }));

function addConditionRow(c) {
  const row = document.createElement("div");
  row.className = "cond-row";
  row.innerHTML = `
    <input placeholder="field (e.g. price, title, attrs.beds)" value="${esc(c.field)}">
    <select>${OPS.map((op) => `<option${op === c.op ? " selected" : ""}>${op}</option>`).join("")}</select>
    <input placeholder="value" value="${esc(String(c.value ?? ""))}">
    <button type="button" class="link">✕</button>`;
  row.querySelector("button").addEventListener("click", () => row.remove());
  $("seeker-conditions").append(row);
}

$("seeker-cancel").addEventListener("click", () => ($("seeker-form").hidden = true));

$("seeker-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  $("seeker-error").textContent = "";

  const conditions = [...$("seeker-conditions").querySelectorAll(".cond-row")]
    .map((row) => {
      const [field, op, value] = row.querySelectorAll("input, select");
      return { field: field.value.trim(), op: op.value, value: value.value.trim() };
    })
    .filter((c) => c.field);

  const data = {
    user: pb.authStore.record.id,
    name: $("seeker-name").value,
    kind: $("seeker-kind").value,
    sources: [...$("seeker-sources").querySelectorAll("input:checked")].map((i) => i.value),
    alerters: [...$("seeker-alerters").querySelectorAll("input:checked")].map((i) => i.value),
    conditions,
    enabled: $("seeker-enabled").checked,
  };

  try {
    const id = $("seeker-id").value;
    if (id) {
      await pb.collection("seekers").update(id, data);
    } else {
      await pb.collection("seekers").create(data);
    }
    $("seeker-form").hidden = true;
    refreshSeekers();
  } catch (err) {
    $("seeker-error").textContent = err.message;
  }
});

// --------------------------------------------------------------- sources ----

async function refreshSources() {
  state.sources = (await pb.collection("sources").getFullList({ sort: "name" }));
  const list = $("sources-list");
  list.innerHTML = "";
  for (const s of state.sources) {
    const div = document.createElement("div");
    div.className = "card";
    div.innerHTML = `
      <strong>${esc(s.name)}</strong>
      <span class="badge">${esc(s.type)}</span>
      <span class="badge">${esc(s.kind)}</span>
      ${s.enabled ? "" : '<span class="badge err">disabled</span>'}
      ${s.last_error ? `<span class="badge err">${esc(s.last_error)}</span>` : ""}
      <div class="dim">${esc(s.config?.url || "")} — every ${s.interval_minutes}m
        ${s.last_polled ? "— last polled " + new Date(s.last_polled).toLocaleString() : "— never polled"}</div>
      <div class="row">
        <button data-poll="${s.id}">poll now</button>
        <button data-toggle="${s.id}" class="link">${s.enabled ? "disable" : "enable"}</button>
        <button data-del="${s.id}" class="link">delete</button>
        <span class="dim" data-status></span>
      </div>`;
    div.querySelector("[data-poll]").addEventListener("click", async (e) => {
      const status = div.querySelector("[data-status]");
      status.textContent = "polling…";
      try {
        const res = await pb.send(`/api/seekout/sources/${s.id}/poll`, { method: "POST" });
        status.textContent = res.seeded
          ? `${res.fetched} fetched, ${res.new} stored — first poll seeds without alerting`
          : `${res.fetched} fetched, ${res.new} new, ${res.matched} matched`;
      } catch (err) {
        status.textContent = err.message;
      }
    });
    div.querySelector("[data-toggle]").addEventListener("click", async () => {
      await pb.collection("sources").update(s.id, { enabled: !s.enabled });
      refreshSources();
    });
    div.querySelector("[data-del]").addEventListener("click", async () => {
      if (confirm(`Delete source "${s.name}"?`)) {
        await pb.collection("sources").delete(s.id);
        refreshSources();
      }
    });
    list.append(div);
  }
  if (!state.sources.length) list.innerHTML = '<p class="dim">No sources yet.</p>';
}

$("new-source").addEventListener("click", () => {
  $("source-form").hidden = false;
  $("source-error").textContent = "";
});
$("source-cancel").addEventListener("click", () => ($("source-form").hidden = true));

$("source-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  $("source-error").textContent = "";
  try {
    await pb.collection("sources").create({
      name: $("source-name").value,
      type: "rss",
      kind: $("source-kind").value,
      config: { url: $("source-url").value },
      interval_minutes: Number($("source-interval").value),
      enabled: true,
    });
    $("source-form").hidden = true;
    refreshSources();
  } catch (err) {
    $("source-error").textContent = err.message;
  }
});

// -------------------------------------------------------------- alerters ----

async function refreshAlerters() {
  state.alerters = (await pb.collection("alerters").getFullList({ sort: "name" }));
  const list = $("alerters-list");
  list.innerHTML = "";
  for (const a of state.alerters) {
    const div = document.createElement("div");
    div.className = "card";
    div.innerHTML = `
      <strong>${esc(a.name)}</strong>
      <span class="badge">${esc(a.type)}</span>
      ${a.enabled ? "" : '<span class="badge err">disabled</span>'}
      <div class="dim">${a.type === "ntfy" ? esc((a.config?.url || "https://ntfy.sh") + "/" + (a.config?.topic || "?")) : esc(a.config?.url || "")}</div>
      <div class="row">
        <button data-test="${a.id}">send test</button>
        <button data-del="${a.id}" class="link">delete</button>
        <span class="dim" data-status></span>
      </div>`;
    div.querySelector("[data-test]").addEventListener("click", async () => {
      const status = div.querySelector("[data-status]");
      status.textContent = "sending…";
      try {
        await pb.send(`/api/seekout/alerters/${a.id}/test`, { method: "POST" });
        status.textContent = "sent";
      } catch (err) {
        status.textContent = err.message;
      }
    });
    div.querySelector("[data-del]").addEventListener("click", async () => {
      if (confirm(`Delete alerter "${a.name}"?`)) {
        await pb.collection("alerters").delete(a.id);
        refreshAlerters();
      }
    });
    list.append(div);
  }
  if (!state.alerters.length) list.innerHTML = '<p class="dim">No alerters yet.</p>';
}

$("new-alerter").addEventListener("click", () => {
  $("alerter-form").hidden = false;
  $("alerter-error").textContent = "";
});
$("alerter-cancel").addEventListener("click", () => ($("alerter-form").hidden = true));
$("alerter-type").addEventListener("change", syncAlerterFields);

function syncAlerterFields() {
  const t = $("alerter-type").value;
  $("alerter-ntfy-fields").hidden = t !== "ntfy";
  $("alerter-webhook-fields").hidden = t !== "webhook";
}

$("alerter-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  $("alerter-error").textContent = "";
  const type = $("alerter-type").value;
  const config = type === "ntfy"
    ? {
        url: $("alerter-ntfy-url").value,
        topic: $("alerter-ntfy-topic").value,
        token: $("alerter-ntfy-token").value,
        priority: $("alerter-ntfy-priority").value,
        tags: $("alerter-ntfy-tags").value,
      }
    : { url: $("alerter-webhook-url").value };
  try {
    await pb.collection("alerters").create({
      user: pb.authStore.record.id,
      name: $("alerter-name").value,
      type,
      config,
      enabled: $("alerter-enabled").checked,
    });
    $("alerter-form").hidden = true;
    refreshAlerters();
  } catch (err) {
    $("alerter-error").textContent = err.message;
  }
});

// ---------------------------------------------------------------- util ----

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (ch) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch]));
}

// ---------------------------------------------------------------- boot ----

syncAlerterFields();
(async () => {
  // isValid only checks token expiry — the record may be gone (stale
  // browser storage, restored backup). Prove it with authRefresh.
  if (pb.authStore.isValid) {
    try {
      await pb.collection("users").authRefresh();
      showApp();
      return;
    } catch {
      pb.authStore.clear();
    }
  }
  showLogin();
})();
