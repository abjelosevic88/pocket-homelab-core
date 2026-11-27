#!/usr/bin/env python3
"""Synchronize/check the reviewed SDK snapshot and the shared UI kit in independently buildable apps.

Go:  sdk/*.go, sdk/appkit/*.go and sdk/*.js → <app>/third_party/pocket-core/ (+ sdk.lock.json, SHA256SUMS);
     go.mod's replace and the Dockerfile's checksum step are added when missing.
UI:  sdk/ui/* → <app>/web/src/pocket-ui/ (+ SHA256SUMS). An app takes only the kit files it opted into:
     the ones already in its pocket-ui/ folder, plus any named with --ui (or --ui all).

  sync-sdk.py --app notes                    sync one app (Go snapshot + the kit files it has)
  sync-sdk.py --app notes --ui AskButton.tsx give it one more kit file
  sync-sdk.py --check [--app …] [--json]     report SDK/kit drift and the fleet style/contract guards
"""
import argparse, hashlib, json, re, sys
from pathlib import Path

p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
p.add_argument('--check', action='store_true', help='report drift and guard findings; exit 1 when there are any')
p.add_argument('--apps-root', type=Path, default=Path(__file__).resolve().parents[2])
p.add_argument('--app', action='append', default=[], help='only these apps (errors, projects…); every app when left out')
p.add_argument('--ui', action='append', default=[], help='also give the app(s) this kit file (a name in sdk/ui, or "all")')
p.add_argument('--json', action='store_true', help='with --check: print the report as JSON')
a = p.parse_args()
root = Path(__file__).resolve().parents[1]
sha = lambda b: hashlib.sha256(b).hexdigest()
sums = lambda files: ''.join(sha(body) + '  ' + name + '\n' for name, body in files.items()).encode()

# ---- the Go snapshot ----
go = {}
for pattern in ('sdk/*.go', 'sdk/appkit/*.go', 'sdk/*.js'):
    for f in sorted(root.glob(pattern)):
        if not f.name.endswith('_test.go'):
            go[str(f.relative_to(root))] = f.read_bytes()
go['go.mod'] = b'module github.com/abjelosevic88/pocket-homelab-core\n\ngo 1.26.5\n'
go['LICENSE'] = (root / 'LICENSE').read_bytes()
lock = {'source': 'https://github.com/abjelosevic88/pocket-homelab-core', 'contract': 'platform-v1', 'sha256': {k: sha(v) for k, v in go.items()}}
go['sdk.lock.json'] = (json.dumps(lock, indent=2) + '\n').encode()
go['SHA256SUMS'] = sums(go)

# ---- the UI kit ----
kit = {f.name: f.read_bytes() for f in sorted((root / 'sdk/ui').iterdir()) if f.is_file() and f.name != 'README.md'}
for name in a.ui:
    if name != 'all' and name not in kit:
        sys.exit(f'--ui {name}: not in sdk/ui (has {", ".join(kit)})')

names = [d.name.removeprefix('pocket-homelab-') for d in sorted(a.apps_root.glob('pocket-homelab-*'))
         if d.name != 'pocket-homelab-core' and ((d / 'third_party/pocket-core').is_dir() or (d / 'web/src/pocket-ui').is_dir())]
if a.app:
    unknown = set(a.app) - set(names)
    if unknown:
        sys.exit('unknown app(s): ' + ', '.join(sorted(unknown)))
    names = [n for n in names if n in a.app]


def kit_files(app: Path) -> dict:
    """The kit files this app takes: those already in pocket-ui/ plus --ui."""
    have = {f.name for f in (app / 'web/src/pocket-ui').glob('*') if f.name in kit}
    want = set(kit) if 'all' in a.ui else have | set(a.ui)
    return {n: kit[n] for n in sorted(want)}


# ---- guards: the papercuts that kept coming back (docs/appkit.md) ----
RULE = re.compile(r'([^{}]+)\{([^{}]*)\}')
KIT_COPIES = {'ui.tsx', 'Select.tsx', 'AskButton.tsx', 'ErrorBoundary.tsx', 'PocketModelSelect.tsx'}


def guards(app: Path, takes: dict) -> list:
    out = []
    src = app / 'web/src'
    if src.is_dir():
        for f in sorted(src.rglob('*.css')):
            if 'node_modules' in f.parts:
                continue
            css = re.sub(r'/\*.*?\*/', '', f.read_text(errors='replace'), flags=re.S)
            rel = str(f.relative_to(app))
            for sel, body in RULE.findall(css):
                sels = [x.strip() for x in sel.split(',')]
                if any(':hover' in x for x in sels) and re.search(r'text-decoration(-line)?\s*:\s*underline', body):
                    out.append(f'hover-underline: {rel}: {sel.strip()[:60]} (colour or background only)')
                if '.block' in sels and re.search(r'display\s*:\s*block', body):
                    out.append(f'block-beats-btn: {rel}: .block {{ display: block }} beats .btn (use .block:not(.btn))')
        for f in sorted(src.rglob('*.tsx')):
            if 'node_modules' in f.parts:
                continue
            if 'preserveAspectRatio="none"' in f.read_text(errors='replace'):
                out.append(f'stretched-svg: {f.relative_to(app)}: preserveAspectRatio="none" stretches the drawing (measure the width instead)')
        for name in takes:
            if name in KIT_COPIES and (src / 'components' / name).exists():
                out.append(f'kit-copy: web/src/components/{name} is still here next to pocket-ui/{name}')
    mod = app / 'go.mod'
    if (app / 'third_party/pocket-core').is_dir() and mod.exists() and 'replace github.com/abjelosevic88/pocket-homelab-core => ./third_party/pocket-core' not in mod.read_text():
        out.append('go.mod: no replace => ./third_party/pocket-core')
    return out


report, failed = {}, 0
for name in names:
    app = a.apps_root / ('pocket-homelab-' + name)
    snap = app / 'third_party/pocket-core'
    takes = kit_files(app) if (app / 'web/src').is_dir() else {}
    kit_target = app / 'web/src/pocket-ui'
    kit_all = dict(takes, SHA256SUMS=sums(takes)) if takes else {}
    if a.check:
        r = {'sdk': 'none', 'kit': sorted(takes), 'drift': [], 'guards': guards(app, takes)}
        if snap.is_dir():
            stale = [n for n, body in go.items() if not (snap / n).is_file() or (snap / n).read_bytes() != body]
            r['sdk'] = 'drift' if stale else 'ok'
            r['drift'] += ['third_party/pocket-core/' + n for n in stale]
        r['drift'] += ['web/src/pocket-ui/' + n for n, body in kit_all.items() if not (kit_target / n).is_file() or (kit_target / n).read_bytes() != body]
        report[name] = r
        failed += bool(r['drift'] or r['guards'])
        continue
    if snap.is_dir():
        for n, body in go.items():
            dest = snap / n
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_bytes(body)
        mod = app / 'go.mod'
        s = mod.read_text()
        s = re.sub(r'(github.com/abjelosevic88/pocket-homelab-core) v[^\s]+', r'\1 v0.18.5', s)
        if 'replace github.com/abjelosevic88/pocket-homelab-core' not in s:
            s += '\n// Reviewed SDK snapshot; see third_party/pocket-core/sdk.lock.json.\nreplace github.com/abjelosevic88/pocket-homelab-core => ./third_party/pocket-core\n'
        mod.write_text(s)
    docker = app / 'Dockerfile'
    if docker.exists():
        s = docker.read_text()
        if snap.is_dir():
            line = 'COPY third_party/pocket-core ./third_party/pocket-core\n'
            if line not in s:
                s = s.replace('COPY go.mod go.sum ./\n', 'COPY go.mod go.sum ./\n' + line)
            verify = 'RUN cd third_party/pocket-core && sha256sum -c SHA256SUMS\n'
            if verify not in s:
                s = s.replace(line, line + verify)
        if kit_all:
            web, verify = 'COPY web/ ./\n', 'RUN cd src/pocket-ui && sha256sum -c SHA256SUMS\n'
            if verify not in s and web in s:
                s = s.replace(web, web + verify, 1)
        docker.write_text(s)
    if kit_all:
        kit_target.mkdir(parents=True, exist_ok=True)
        for n, body in kit_all.items():
            (kit_target / n).write_bytes(body)

if a.check:
    if a.json:
        print(json.dumps(report, indent=2))
    else:
        for name, r in report.items():
            if not (r['drift'] or r['guards']):
                continue
            print(f'{name}: sdk {r["sdk"]}, kit {len(r["kit"])} file(s)')
            if r['drift']:
                more = len(r['drift']) - 4
                print('  drift: ' + ', '.join(x.split('/')[-1] for x in r['drift'][:4]) + (f' … +{more}' if more > 0 else ''))
            for g in r['guards']:
                print('  ' + g)
        sdk_ok = sum(r['sdk'] == 'ok' for r in report.values())
        with_sdk = sum(r['sdk'] != 'none' for r in report.values())
        with_kit = sum(bool(r['kit']) for r in report.values())
        print(f'Checked {len(report)} apps: SDK current in {sdk_ok}/{with_sdk}, UI kit in {with_kit}, {failed} with findings '
              f'({len(lock["sha256"])} SDK files, {len(kit)} kit files)')
    sys.exit(1 if failed else 0)
print(f'Synchronized {len(names)} apps against {len(lock["sha256"])} SDK files' + (f' and the {len(kit)}-file UI kit' if kit else ''))
