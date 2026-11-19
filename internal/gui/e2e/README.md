# GUI smoke / screenshot capture

`run.ps1` drives a **running** `argent gui` instance through its HTTP bridge and
optionally asks the system Edge/Chrome for a headless screenshot. It writes only
to the OS temporary directory, so it is safe to run from a clean checkout.

```powershell
# terminal 1: start the GUI (prints http://127.0.0.1:<port>/?token=<hex>)
argui gui --C .

# terminal 2: pass that URL verbatim
powershell -File internal/gui/e2e/run.ps1 -Url 'http://127.0.0.1:1234/?token=...'
```

Steps verified: `/healthz` → `get_state` → `list_workflows` → `usage_summary`
→ one `/events` frame. With `-Browser` (or an auto-detected Edge/Chrome) the
script also writes `<temp>/argent-e2e-<pid>/screenshot.png` and prints the path.
No npm package, browser automation driver or network service is used.
