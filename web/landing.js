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
  showForm();
}

// Swaps the modal between the credential form and the "check your inbox"
// panel. `justSent` distinguishes signup (a link is already on its way) from
// a sign-in blocked by verification (the link was sent whenever they signed
// up, possibly days ago).
function showSent(email, justSent) {
  $("auth-sent-email").textContent = email;
  $("auth-sent-lead").textContent = justSent
    ? "We sent a verification link to"
    : "This address is not verified yet. Check the link we emailed";
  $("auth-sent").hidden = false;
  $("auth-error").textContent = "";
  for (const id of ["auth-email-field", "auth-password-field", "auth-confirm-field"]) {
    $(id).hidden = true;
  }
  $("auth-submit").hidden = true;
  $("auth-toggle").hidden = true;
}

function showForm() {
  $("auth-sent").hidden = true;
  $("auth-email-field").hidden = false;
  $("auth-password-field").hidden = false;
  $("auth-submit").hidden = false;
  $("auth-toggle").hidden = false;
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

let lastEmail = "";

$("auth-resend").addEventListener("click", async (e) => {
  const btn = e.currentTarget;
  btn.disabled = true;
  try {
    await pb.collection("users").requestVerification(lastEmail);
    toast("Sent another link.", "ok");
  } catch (err) {
    toast(authError(err), "err");
  }
  setTimeout(() => (btn.disabled = false), 3000);
});

$("auth-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const email = $("auth-email").value.trim();
  const password = $("auth-password").value;
  const submit = $("auth-submit");
  $("auth-error").textContent = "";
  submit.disabled = true;
  lastEmail = email;

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
      // Accounts cannot authenticate until the address is confirmed, so
      // there is no session to hand off yet — ask for the email instead.
      await pb.collection("users").requestVerification(email);
      showSent(email, true);
      return;
    }
    await pb.collection("users").authWithPassword(email, password);
    location.href = "/app/";
  } catch (err) {
    // PocketBase distinguishes the two failures: 400 is bad credentials,
    // 403 is the collection's auth rule rejecting an unverified account.
    if (err?.status === 403) {
      $("auth-title").textContent = "Verify your email first";
      showSent(email, false);
      $("auth-error").textContent = "";
    } else {
      $("auth-error").textContent = authError(err);
    }
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
