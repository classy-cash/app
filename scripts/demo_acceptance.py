#!/usr/bin/env python3
"""Black-box acceptance for a running Classy Cash demo (any seed, any wrapper).

    python3 scripts/demo_acceptance.py --base http://127.0.0.1:18096 \\
        [--reset-cmd "python3 reset.py --root /srv/classy-cash-demo --ready-url http://127.0.0.1:18081/ready"] \\
        [--restart-cmd "docker compose -p classycash-demo -f /srv/classy-cash-demo/compose.yaml restart app"]

Needs only HTTP access (use an SSH tunnel or a local proxy for a loopback-only service). Without --reset-cmd the
run is read-only except for one reversible-by-reset visitor edit; --reset-cmd performs ONE real reset.

It checks what docs/INTEGRATION.md §5 promises: safe demo mode, quick-login accounts that work, role
permissions (a treasurer panel, a denied ordinary user), every payment state visible, no ghost "Anulowano" rows,
captured mail only, disabled webhook, and -- with --reset-cmd -- that a visitor edit and session disappear and the
service stays up. --restart-cmd additionally proves pending payments survive the app's 24 h expiry job.
Exit status 1 when any check fails. Python 3.9+, standard library only.
"""
import argparse
import http.cookiejar
import re
import shlex
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


class Demo:
    def __init__(self, base):
        self.base = base.rstrip("/")

    def client(self):
        # An https BASE_URL makes cookies Secure; a loopback tunnel is plain http, so allow both here.
        policy = http.cookiejar.DefaultCookiePolicy(secure_protocols=("http", "https"))
        return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar(policy)), NoRedirect)

    def req(self, c, path, data=None):
        body = urllib.parse.urlencode(data).encode() if data is not None else None
        try:
            r = c.open(urllib.request.Request(self.base + path, data=body), timeout=8)
            return r.status, r.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()

    def token(self, c):
        for p in ("/login", "/profile"):
            m = re.search(r'name="csrf" content="([^"]+)"', self.req(c, p)[1])
            if m:
                return m.group(1)
        raise RuntimeError("no CSRF token")

    def login(self, email):
        c = self.client()
        st, _ = self.req(c, "/demo/login", {"email": email, "_csrf": self.token(c)})
        return c if st == 303 else None


def text(html):
    return re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", html)).replace("\xa0", " ")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--base", required=True)
    ap.add_argument("--reset-cmd")
    ap.add_argument("--restart-cmd")
    ap.add_argument("--reset-cwd", default=".")
    a = ap.parse_args()
    d, failed = Demo(a.base), []

    def check(name, ok, extra=""):
        print(("PASS " if ok else "FAIL ") + name + (" — " + extra if extra else ""))
        if not ok:
            failed.append(name)

    anon = d.client()
    st, ready = d.req(anon, "/ready")
    check("/ready is 200 in safe demo mode", st == 200 and '"mode":"demo"' in ready and '"mail_driver":"capture"' in ready and '"bank_webhook":"disabled"' in ready, ready.strip()[:160])
    st, login = d.req(anon, "/login")
    emails = re.findall(r'action="/demo/login">.*?name="email" value="([^"]+)"', login, re.S)
    check("login page shows the demo banner, countdown and >= 3 quick-login accounts", "Tryb demo" in login and "data-countdown" in login and len(emails) >= 3, f"{len(emails)} accounts")
    check("quick-login refuses an unlisted account", d.login("intruder@example.test") is None)
    check("real bank webhook is off", d.req(d.client(), "/api/bank-mail", {"x": "1"})[0] == 503)

    sessions = {}
    for e in emails:
        c = d.login(e)
        check(f"quick login works for {e}", c is not None)
        if c:
            sessions[e] = c
    managers = [e for e, c in sessions.items() if d.req(c, "/treasurer/dashboard")[0] == 200]
    outsiders = [e for e, c in sessions.items() if d.req(c, "/treasurer/dashboard")[0] == 403]
    check("at least one account manages a class and at least one is an ordinary parent", bool(managers) and bool(outsiders), f"managers={len(managers)} parents={len(outsiders)}")
    if not managers:
        sys.exit(1)
    boss = sessions[managers[0]]
    for p in ("/treasurer/dashboard", "/treasurer/payments", "/treasurer/contributions", "/treasurer/students", "/treasurer/reports", "/treasurer/reports/outstanding", "/treasurer/members", "/treasurer/settings"):
        st, body = d.req(boss, p)
        check(f"manager page {p}", st == 200 and "<no value>" not in body and "%!" not in body, str(st))
    for e in outsiders:
        check(f"parent {e} cannot open reports", d.req(sessions[e], "/treasurer/reports")[0] == 403)
        check(f"parent {e} sees own area", d.req(sessions[e], "/parent")[0] == 200)

    admins = [e for e, c in sessions.items() if d.req(c, "/admin/mail")[0] == 200]
    page = text(d.req(boss, "/treasurer/payments")[1])
    for label in ("Opłacona", "Oczekuje", "Wygasła", "Nieudana", "Zwrócona", "Nieprzypisana", "Do sprawdzenia"):
        check(f"state '{label}' is visible", label in page)
    check("no ghost 'Anulowano' rows (contribution slots without an obligation)", "Anulowano" not in text(d.req(boss, "/treasurer/contributions")[1]))
    m = re.search(r"Suma w kasie\s*(-?[−\-]?\d[\d ]*(?:,\d+)? zł)", text(d.req(boss, "/treasurer/dashboard")[1]))
    print("INFO cash card:", m.group(1) if m else "?")
    if admins:
        mail = d.req(sessions[admins[0]], "/admin/mail")[1]
        check("mail is only captured (nothing 'Wysłana')", "Wysłana" not in mail)

    if a.restart_cmd:  # the app's housekeeping (24 h payment expiry) runs at start-up
        subprocess.run(shlex.split(a.restart_cmd), check=True, capture_output=True)
        for _ in range(60):
            try:
                urllib.request.urlopen(d.base + "/health", timeout=1)
                break
            except Exception:
                time.sleep(0.5)
        boss = d.login(managers[0])
        check("a pending payment survives the expiry job", "Oczekuje" in text(d.req(boss, "/treasurer/payments")[1]), "seed pending payments must be < 24 h old at reset time")

    if a.reset_cmd:
        # Only the visitor edit below touches data; the reset must undo it.
        marker = "ZNIKNIE-PO-RESECIE"
        d.req(boss, "/treasurer/manual-transactions", {"transaction_type": "expense", "amount_pln": "1", "expense_description": marker, "_csrf": d.token(boss)})
        check("visitor edit is visible before the reset", marker in d.req(boss, "/treasurer/dashboard")[1])
        fails, stop = [], []

        def poll():
            while not stop:
                try:
                    urllib.request.urlopen(d.base + "/health", timeout=1)
                except Exception:
                    fails.append(time.time())
                time.sleep(0.05)

        t = threading.Thread(target=poll)
        t.start()
        t0 = time.time()
        r = subprocess.run(shlex.split(a.reset_cmd), cwd=a.reset_cwd, capture_output=True, text=True)
        took = time.time() - t0
        time.sleep(0.5)
        stop.append(1)
        t.join()
        check("reset command succeeds", r.returncode == 0, (r.stdout + r.stderr).strip()[-160:])
        unavailable = (max(fails) - min(fails) + 0.05) if fails else 0.0
        check("downtime during reset is bounded (< 10 s)", unavailable < 10, f"reset took {took:.1f}s, unavailable ~{unavailable:.1f}s")
        check("visitor session is invalid after the reset", d.req(boss, "/treasurer/dashboard")[0] == 303)
        fresh = d.login(managers[0])
        check("visitor edit is gone after the reset", fresh is not None and marker not in d.req(fresh, "/treasurer/dashboard")[1])

    print("RESULT:", ("FAILED: " + "; ".join(failed)) if failed else "ALL PASSED")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
