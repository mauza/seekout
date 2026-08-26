/* /verify — confirms the token from the verification email */
"use strict";

const pb = new PocketBase("/");
pb.beforeSend = (url, options) => {
  options.cache = "no-store";
  return { url, options };
};
const $ = (id) => document.getElementById(id);

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

function show(state, title, text) {
  $("verify-icon").dataset.state = state;
  $("verify-title").textContent = title;
  $("verify-text").textContent = text;
}

$("resend-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const submit = $("resend-submit");
  submit.disabled = true;
  try {
    await pb.collection("users").requestVerification($("resend-email").value.trim());
    // PocketBase answers 204 whether or not the address exists, so this
    // wording must not imply the account was found.
    toast("If that address has an account, a new link is on its way.", "ok");
    $("resend-form").hidden = true;
  } catch (err) {
    toast(err?.message || "Could not send the email.", "err");
    submit.disabled = false;
  }
});

(async () => {
  // The token rides in the fragment so it stays out of server and proxy logs.
  const token = location.hash.replace(/^#/, "").trim();
  if (!token) {
    show("err", "Nothing to verify", "This link is missing its token. Request a new one below.");
    $("resend-form").hidden = false;
    return;
  }

  try {
    await pb.collection("users").confirmVerification(token);
  } catch (err) {
    const status = err?.status;
    show(
      "err",
      status === 400 ? "This link has expired" : "Verification failed",
      status === 400
        ? "Verification links expire a few hours after they are sent. Send yourself a fresh one."
        : err?.message || "Something went wrong. Try a fresh link.",
    );
    $("resend-form").hidden = false;
    return;
  }

  show("ok", "Email verified", "Your account is ready. Sign in to start watching listings.");
  $("verify-actions").hidden = false;

  // confirmVerification refreshes an existing session in place; if the tab
  // that signed up is still authenticated, go straight to the app.
  if (pb.authStore.isValid) {
    $("verify-continue").textContent = "Open app";
    $("verify-continue").href = "/app/";
  }
})();
