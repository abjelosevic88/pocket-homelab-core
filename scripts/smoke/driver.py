#!/usr/bin/env python3
"""Install, check and remove every catalog app through Pocket Core's API (run inside the throwaway host)."""
import http.cookiejar, json, subprocess, sys, time, urllib.request

B = sys.argv[1]
jar = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

def call(method, path, body=None, timeout=60):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(B + path, data=data, method=method, headers={"Origin": B, "Content-Type": "application/json"})
    try:
        with op.open(req, timeout=timeout) as r:
            return r.status, json.loads(r.read() or b"{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read() or b"{}")
        except Exception:
            return e.code, {}

call("POST", "/api/v1/auth/setup", {"email": "smoke@example.test", "password": "smoke-test-password", "name": "Smoke", "box_name": "Smoke"})
print(call("POST", "/api/v1/auth/login", {"email": "smoke@example.test", "password": "smoke-test-password"})[0], flush=True)
_, cat = call("GET", "/api/v1/installer?refresh=1", timeout=120)
apps = [a for a in cat["apps"] if not a.get("installed")]
only = sys.argv[2:]
if only:
    apps = [a for a in apps if a["slug"] in only]
print(f"{len(apps)} apps to try", flush=True)

def wait(job_id, limit):
    end = time.time() + limit
    while time.time() < end:
        _, j = call("GET", f"/api/v1/installer/jobs/{job_id}")
        if j.get("state") != "running":
            return j
        time.sleep(3)
    return {"state": "timeout", "log": j.get("log", [])}

results = []
for a in apps:
    slug, t0 = a["slug"], time.time()
    r = {"slug": slug, "version": a["version"], "requires": a.get("requires", {})}
    if a.get("available") is False:
        r.update(state="skipped", error="no image for this platform")
        results.append(r); print(json.dumps(r), flush=True); continue
    code, j = call("POST", "/api/v1/installer/install", {"slug": slug, "name": a["name"]})
    if code >= 300:
        r.update(state="refused", error=j.get("error", str(code)))
    else:
        j = wait(j["id"], 600)
        res = j.get("result") or {}
        r.update(state=j.get("state"), error=j.get("error", ""), warnings=res.get("warnings", []), log=j.get("log", [])[-4:],
                 port=res.get("port"), registered=any("Registered with Pocket Core" in l for l in j.get("log", [])))
        if r["registered"]:
            code, u = call("POST", f"/api/v1/apps/{slug}/uninstall")
            if code < 300:
                u = wait(u["id"], 300)
                r["removed"] = u.get("state") == "done"
                if not r["removed"]:
                    r["remove_error"] = u.get("error", "")
            else:
                r["removed"] = False; r["remove_error"] = u.get("error", str(code))
        elif r["state"] == "done":
            # Running but never registered: take it off the server by hand.
            subprocess.run(f"cd /root/pocket/apps/pocket-{slug} 2>/dev/null && docker compose down -v --remove-orphans", shell=True, capture_output=True)
    r["seconds"] = round(time.time() - t0)
    results.append(r)
    print(json.dumps(r), flush=True)
    subprocess.run("docker image prune -af >/dev/null 2>&1; docker volume prune -af >/dev/null 2>&1", shell=True)

json.dump(results, open("/root/results.json", "w"), indent=2)
ok = [r["slug"] for r in results if r.get("registered") and r.get("removed")]
print(f"DONE: {len(ok)}/{len(results)} installed, registered and removed cleanly", flush=True)
