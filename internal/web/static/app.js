/* Classy Cash — progressive enhancement. No inline script: the CSP allows only this file. */
(function () {
  "use strict";
  var csrf = (document.querySelector('meta[name="csrf"]') || {}).content || "";
  function $(s, r) { return (r || document).querySelector(s); }
  function $$(s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); }
  function post(url, body) {
    return fetch(url, { method: "POST", credentials: "same-origin", headers: { "X-CSRF-Token": csrf, "Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded" }, body: body || "" })
      .then(function (r) { return r.json(); });
  }

  // sidebar
  $$("[data-toggle-sidebar]").forEach(function (b) {
    b.addEventListener("click", function () {
      var sb = $("#sidebar"), bd = $(".sidebar-backdrop"), open = !sb.classList.contains("open");
      sb.classList.toggle("open", open); bd.hidden = !open;
      $$("[data-toggle-sidebar][aria-expanded]").forEach(function (t) { t.setAttribute("aria-expanded", open); });
    });
  });
  $$("[data-autosubmit]").forEach(function (s) { s.addEventListener("change", function () { s.form.submit(); }); });

  // dialogs
  $$("[data-open]").forEach(function (b) { b.addEventListener("click", function () { var d = document.getElementById(b.dataset.open); if (d && d.showModal) d.showModal(); }); });
  document.addEventListener("click", function (e) {
    var c = e.target.closest("[data-close]"); if (c) { var d = c.closest("dialog"); if (d) d.close(); }
    var r = e.target.closest("[data-reload]"); if (r) location.reload();
    if (e.target.matches("dialog.modal")) e.target.close(); // click on the backdrop
  });

  // confirmations for destructive actions
  document.addEventListener("submit", function (e) {
    var t = e.target.closest("[data-confirm]");
    if (t && !window.confirm(t.getAttribute("data-confirm"))) e.preventDefault();
  });

  // forms: Polish inline validation, show/hide password, and a guard against double submits
  function message(el) {
    var v = el.validity;
    if (v.customError) return el.validationMessage;
    if (v.valueMissing) return el.type === "checkbox" ? "Zaznacz to pole, aby kontynuować." : "To pole jest wymagane.";
    if (v.typeMismatch) return el.type === "email" ? "Podaj poprawny adres e-mail, np. imie@domena.pl." : "Wpisz poprawną wartość.";
    if (v.tooShort) return "Minimum " + el.minLength + " znaków (wpisano " + el.value.length + ").";
    if (v.patternMismatch) return el.title || "Nieprawidłowy format.";
    if (v.rangeUnderflow || v.rangeOverflow) return "Wartość jest poza dozwolonym zakresem.";
    return el.validationMessage;
  }
  function errorBox(el, make) {
    var f = el.closest(".field") || el.parentNode, box = $(".field-error", f);
    if (!box && make) { box = document.createElement("p"); box.className = "field-error"; box.setAttribute("role", "alert"); f.appendChild(box); }
    if (box && !box.id) box.id = (el.id || el.name || "f") + "-err";
    return box;
  }
  function check(el) {
    if (el.disabled || !el.willValidate || el.closest("[hidden]")) return true;
    var other = el.name === "password_confirm" ? $('[name="password"]', el.form) : null;
    if (el.name === "password_confirm") el.setCustomValidity(other && el.value && other.value !== el.value ? "Hasła nie są takie same." : "");
    var ok = el.checkValidity(), box = errorBox(el, !ok);
    if (ok) { el.removeAttribute("aria-invalid"); if (box) { box.textContent = ""; box.hidden = true; } el.removeAttribute("aria-describedby"); return true; }
    el.setAttribute("aria-invalid", "true"); box.hidden = false; box.textContent = message(el); el.setAttribute("aria-describedby", box.id);
    return false;
  }
  function controls(f) { return $$("input:not([type=hidden]):not([type=submit]):not([type=button]), select, textarea", f); }
  $$("form").forEach(function (f) {
    f.noValidate = true; // our messages replace the browser bubbles
    controls(f).forEach(function (el) {
      el.addEventListener("blur", function () { if (el.value !== "" || el.dataset.touched) check(el); });
      el.addEventListener("input", function () { el.dataset.touched = "1"; if (el.getAttribute("aria-invalid") === "true" || el.name === "password" || el.name === "password_confirm") check(el); });
      el.addEventListener("change", function () { check(el); });
    });
  });
  $$("input[type=password]").forEach(function (inp) {
    var wrap = document.createElement("div"), b = document.createElement("button");
    wrap.className = "pw"; inp.parentNode.insertBefore(wrap, inp); wrap.appendChild(inp);
    b.type = "button"; b.className = "pw-toggle"; b.textContent = "Pokaż"; b.setAttribute("aria-label", "Pokaż hasło"); b.setAttribute("aria-pressed", "false");
    b.addEventListener("click", function () {
      var show = inp.type === "password"; inp.type = show ? "text" : "password";
      b.textContent = show ? "Ukryj" : "Pokaż"; b.setAttribute("aria-label", show ? "Ukryj hasło" : "Pokaż hasło"); b.setAttribute("aria-pressed", String(show));
    });
    wrap.appendChild(b);
  });
  document.addEventListener("submit", function (e) {
    var f = e.target; if (!f || f.tagName !== "FORM" || e.defaultPrevented) return;
    var bad = controls(f).filter(function (el) { return !check(el); });
    if (bad.length) { e.preventDefault(); bad[0].focus(); return; }
    if ((f.method || "").toLowerCase() === "get" || f.target === "_blank" || f.hasAttribute("data-keep-enabled")) return;
    var btns = $$("button[type=submit], button:not([type]), input[type=submit]", f);
    setTimeout(function () {
      btns.forEach(function (b) { b.disabled = true; });
      if (e.submitter && e.submitter.tagName === "BUTTON") e.submitter.setAttribute("aria-busy", "true");
      setTimeout(unlock, 15000);
    }, 0);
    function unlock() { btns.forEach(function (b) { b.disabled = false; b.removeAttribute("aria-busy"); }); }
    window.addEventListener("pageshow", unlock, { once: true });
  });
  var firstBad = $(".field-error:not([hidden])");
  if (firstBad && !$("[data-wizard]")) { var fi = $("input, select, textarea", firstBad.closest(".field") || firstBad.parentNode); if (fi) { fi.setAttribute("aria-invalid", "true"); fi.focus(); } }

  // copy buttons
  $$("[data-copy-from]").forEach(function (b) {
    b.addEventListener("click", function () {
      var src = $(b.dataset.copyFrom); if (!src) return;
      var text = src.textContent.trim(), done = function () { var o = b.textContent; b.textContent = "Skopiowano ✓"; setTimeout(function () { b.textContent = o; }, 1500); };
      if (navigator.clipboard) navigator.clipboard.writeText(text).then(done, done); else { var t = document.createElement("textarea"); t.value = text; document.body.appendChild(t); t.select(); document.execCommand("copy"); t.remove(); done(); }
    });
  });

  // print buttons
  $$("[data-print]").forEach(function (b) { b.addEventListener("click", function () { window.print(); }); });

  // select / deselect all students
  $$("[data-toggle-all]").forEach(function (b) {
    b.addEventListener("click", function () {
      var boxes = $$("." + b.dataset.toggleAll), uncheck = b.textContent.indexOf("Odznacz") >= 0;
      boxes.forEach(function (c) { c.checked = !uncheck; }); b.textContent = uncheck ? "Zaznacz wszystkich" : "Odznacz wszystkich";
    });
  });

  // onboarding wizard
  $$("[data-wizard]").forEach(function (f) {
    var steps = $$("[data-step]", f), dots = $$(".step-dot", f), i = 0;
    function show(n) { i = n; steps.forEach(function (s, k) { s.hidden = k !== n; }); dots.forEach(function (d, k) { d.classList.toggle("active", k === n); }); var first = $("input", steps[n]); if (first) first.focus(); }
    $$("[data-next]", f).forEach(function (b) { b.addEventListener("click", function () {
      var bad = $$("input, select, textarea", steps[i]).filter(function (x) { return !check(x); });
      if (bad.length) { bad[0].focus(); return; } show(i + 1); }); });
    $$("[data-prev]", f).forEach(function (b) { b.addEventListener("click", function () { show(i - 1); }); });
    var firstErr = steps.findIndex ? steps.findIndex(function (s) { return $(".field-error", s); }) : -1;
    show(firstErr > 0 ? firstErr : 0);
  });

  // dashboard: refresh cards every 30 s and page the ledger
  var host = $("[data-cards-host]");
  if (host) setInterval(function () {
    if (document.hidden) return;
    fetch(host.dataset.url, { credentials: "same-origin" }).then(function (r) { return r.ok ? r.text() : null; }).then(function (h) { if (h) host.innerHTML = h; });
  }, 30000);
  var more = $("[data-ledger-more]"), body = $("[data-ledger]");
  if (more && body) more.addEventListener("click", function () {
    var page = parseInt(body.dataset.next, 10); more.disabled = true;
    fetch("/treasurer/ledger?page=" + page, { credentials: "same-origin" }).then(function (r) {
      var has = r.headers.get("X-Has-More") === "1"; return r.text().then(function (h) { return { h: h, has: has }; });
    }).then(function (x) { body.insertAdjacentHTML("beforeend", x.h); body.dataset.next = page + 1; more.disabled = false; if (!x.has) more.parentNode.innerHTML = '<span class="small muted">Koniec historii finansowej</span>'; });
  });

  // manual income: show the consequence before saving
  $$("form[data-preview=income]").forEach(function (f) {
    var amount = $('[name="amount_pln"]', f), sel = $("select", f), out = $("[data-consequence]", f);
    function update() {
      var opt = sel.options[sel.selectedIndex], due = opt && opt.dataset.amount, v = parseFloat((amount.value || "").replace(",", "."));
      if (!sel.value) { out.textContent = "Wpłata trafi do kasy klasy jako przychód ogólny."; return; }
      if (!isFinite(v)) { out.textContent = "Wpisz kwotę, aby zobaczyć skutek księgowania."; return; }
      out.textContent = v >= parseFloat(due) ? "Składka zostanie oznaczona jako opłacona, a kasa wzrośnie o " + v.toFixed(2).replace(".", ",") + " zł." : "Wpłata jest niższa niż składka (" + due.replace(".", ",") + " zł) — pozycja zostanie oznaczona jako „częściowo”.";
    }
    amount.addEventListener("input", update); sel.addEventListener("change", update); update();
  });

  // parent search suggestions
  var members = $("#members");
  if (members) $$('input[type=email][list=members]').forEach(function (inp) {
    var t; inp.addEventListener("input", function () {
      clearTimeout(t); if (inp.value.length < 3) return;
      t = setTimeout(function () { fetch("/treasurer/users/search?q=" + encodeURIComponent(inp.value), { credentials: "same-origin" }).then(function (r) { return r.json(); }).then(function (list) {
        members.innerHTML = ""; list.forEach(function (u) { var o = document.createElement("option"); o.value = u.email; o.label = u.name; members.appendChild(o); }); }); }, 250);
    });
  });

  // demo countdown
  $$("[data-countdown]").forEach(function (el) {
    var at = parseInt(el.dataset.countdown, 10) * 1000;
    function tick() { var s = Math.max(0, Math.round((at - Date.now()) / 1000)), m = Math.floor(s / 60); el.textContent = m + ":" + String(s % 60).padStart(2, "0"); if (s === 0) { setTimeout(function () { location.reload(); }, 3000); } }
    tick(); setInterval(tick, 1000);
  });

  // parent fast payment dialog
  var fp = $("[data-fastpay]"), dlg = $("#fastpay");
  if (fp && dlg) {
    var parts = { loading: "[data-fp-loading]", empty: "[data-fp-empty]", pay: "[data-fp-pay]", done: "[data-fp-done]", error: "[data-fp-error]" }, poll;
    function view(name) { Object.keys(parts).forEach(function (k) { $(parts[k], dlg).hidden = k !== name; }); }
    dlg.addEventListener("close", function () { clearInterval(poll); });
    fp.addEventListener("click", function () {
      view("loading"); dlg.showModal();
      post("/parent/pay").then(function (r) {
        if (!r.ok) return view("error");
        if (r.nothing) return view("empty");
        $("[data-fp-amount]", dlg).textContent = r.amount; $("[data-fp-code]", dlg).textContent = r.code;
        $("[data-fp-phone]", dlg).textContent = r.blik_phone || "— skontaktuj się ze skarbnikiem —";
        if (r.account) { $("[data-fp-account]", dlg).textContent = r.account; $("[data-fp-account-row]", dlg).hidden = false; }
        view("pay");
        poll = setInterval(function () { fetch("/parent/pay/" + r.id + "/status", { credentials: "same-origin" }).then(function (x) { return x.json(); }).then(function (s) { if (s.paid) { clearInterval(poll); view("done"); } }); }, 5000);
      }).catch(function () { view("error"); });
    });
  }
})();
