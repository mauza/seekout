/* landing page: signup / signin against PocketBase, then hand off to /app/ */
"use strict";

const pb = new PocketBase("/");
pb.beforeSend = (url, options) => {
  options.cache = "no-store";
  return { url, options };
};
const $ = (id) => document.getElementById(id);

let mode = "signup";

function toast(message, kind = "") {
  const el = document.createElement("div");
  el.className = `toast ${kind}`.trim();
  el.textContent = message;
  $("toasts").append(el);
  setTimeout(() => {
    el.classList.add("leaving");
    setTimeout(() => el.remove(), 200);
  }, 4000);
}

function setMode(next) {
  mode = next;
  const signup = mode === "signup";
  $("auth-title").textContent = signup ? "Create your account" : "Welcome back";
  $("auth-submit").textContent = signup ? "Create account" : "Sign in";
  $("auth-toggle").textContent = signup ? "Have an account? Sign in" : "Need an account? Sign up";
  $("auth-confirm-field").hidden = !signup;
  $("auth-confirm").required = signup;
  $("auth-password").autocomplete = signup ? "new-password" : "current-password";
  $("auth-error").textContent = "";
}

function openAuth(next) {
  setMode(next);
  $("auth").hidden = false;
  $("auth-email").focus();
}

function closeAuth() {
  $("auth").hidden = true;
}

for (const btn of document.querySelectorAll("[data-auth-open]")) {
  btn.addEventListener("click", () => openAuth(btn.dataset.authOpen));
}
for (const btn of document.querySelectorAll("[data-auth-close]")) {
  btn.addEventListener("click", closeAuth);
}
$("auth").addEventListener("click", (e) => {
  if (e.target === $("auth")) closeAuth();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !$("auth").hidden) closeAuth();
});
$("auth-toggle").addEventListener("click", () => setMode(mode === "signup" ? "signin" : "signup"));

// PocketBase nests validation details under data.<field>.message; surface the
// specific reason instead of a generic "Failed to create record".
function authError(err) {
  const data = err?.response?.data;
  if (data && typeof data === "object") {
    const first = Object.entries(data)[0];
    if (first) {
      const [field, detail] = first;
      const message = detail && typeof detail === "object" ? detail.message : String(detail);
      return `${field}: ${message}`;
    }
  }
  return err?.message || "Something went wrong.";
}

$("auth-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const email = $("auth-email").value.trim();
  const password = $("auth-password").value;
  const submit = $("auth-submit");
  $("auth-error").textContent = "";
  submit.disabled = true;

  try {
    if (mode === "signup") {
      if (password !== $("auth-confirm").value) {
        throw new Error("Passwords do not match.");
      }
      await pb.collection("users").create({
        email,
        password,
        passwordConfirm: $("auth-confirm").value,
      });
    }
    await pb.collection("users").authWithPassword(email, password);
    location.href = "/app/";
  } catch (err) {
    $("auth-error").textContent = authError(err);
    submit.disabled = false;
  }
});

// Already signed in? Offer the app instead of making them log in again.
(async () => {
  if (!pb.authStore.isValid) return;
  try {
    await pb.collection("users").authRefresh();
  } catch {
    pb.authStore.clear();
    return;
  }
  // one "Open app" is enough — drop the secondary sign-in affordance
  for (const btn of document.querySelectorAll('[data-auth-open="signin"]')) {
    btn.hidden = true;
  }
  for (const btn of document.querySelectorAll('[data-auth-open="signup"]')) {
    btn.textContent = "Open app";
    btn.onclick = () => (location.href = "/app/");
  }
  toast(`Signed in as ${pb.authStore.record.email}`, "ok");
})();
