# Flake guard: surface wall-clock instability in the packages most prone to it.
#
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/flake-guard.ps1 `
#       [-Count 5] [-Packages internal/tui, internal/hooks] [-Race]
#
# Each package is run with `go test -count=N`, which bypasses the Go test cache
# and executes every test N times in one invocation. A single timing-dependent
# failure therefore fails the whole run and is reported with the package name
# and the captured test output. Output is captured in memory: no temp files are
# created and nothing is written to disk.
#
# -Race is OFF by default: repetition plus the race detector is slow, and the
# CI race gate already covers concurrency. Enable it deliberately; it needs a
# C toolchain (CGO_ENABLED=1), e.g. gcc on Linux or MinGW on Windows.

param(
    [ValidateRange(1, 1000)]
    [int]$Count = 5,
    [string[]]$Packages = @(
        'internal/tui',
        'internal/hooks',
        'internal/jobs',
        'internal/agent'
    ),
    [switch]$Race
)

# Native-command stderr is merged with 2>&1 below; keep the error preference at
# the default so PowerShell 5.1 does not turn a Go stderr line into an exception.
$ErrorActionPreference = 'Continue'

# Resolve package paths against the repository root, not the caller's cwd.
$repo = Split-Path -Parent $PSScriptRoot
Push-Location $repo

$exitCode = 0
try {
    $timer = [System.Diagnostics.Stopwatch]::StartNew()

    # `powershell -File ... -Packages a,b` hands the script a single string,
    # while an in-session `& script.ps1 -Packages a,b` yields a real array.
    # Accept both forms so the documented command line behaves predictably.
    $normalized = [System.Collections.Generic.List[string]]::new()
    foreach ($p in $Packages) {
        foreach ($part in ([string]$p).Split(',')) {
            $part = $part.Trim()
            if ($part) { $normalized.Add($part) }
        }
    }
    $Packages = $normalized.ToArray()

    if ($Race) {
        # The race detector needs cgo; do not inherit a disabled value.
        $env:CGO_ENABLED = '1'
    }

    Write-Host ("flake-guard: {0} package(s), count={1}, race={2}, timeout=300s" -f $Packages.Count, $Count, [bool]$Race)

    $failed = [System.Collections.Generic.List[string]]::new()

    foreach ($pkg in $Packages) {
        # Module-relative path: ./internal/<pkg>. A bare 'internal/tui' would be
        # resolved as an import path instead of a local directory.
        $testArgs = @('test')
        if ($Race) { $testArgs += '-race' }
        $testArgs += @("./$pkg", "-count=$Count", '-timeout', '300s')

        Write-Host ''
        Write-Host ("==> go {0}" -f ($testArgs -join ' '))

        # Merge stderr into stdout to capture FAIL lines. Native commands never
        # throw on a non-zero exit in PowerShell, so inspect $LASTEXITCODE.
        $lines = & go @testArgs 2>&1
        $code = $LASTEXITCODE

        if ($code -ne 0) {
            # Report exactly which package run failed, with its output.
            foreach ($line in $lines) { Write-Host $line }
            $failed.Add($pkg)
            Write-Host ("FAIL {0}: go test exited {1}" -f $pkg, $code)
        }
        else {
            Write-Host ("PASS {0}: {1} run(s) stable" -f $pkg, $Count)
        }
    }

    $timer.Stop()

    $failedPackages = if ($failed.Count -gt 0) { $failed -join ', ' } else { 'none' }
    Write-Host ''
    Write-Host ("flake-guard: failed package(s): {0}" -f $failedPackages)
    Write-Host ("flake-guard total runtime: {0:N1}s" -f $timer.Elapsed.TotalSeconds)

    if ($failed.Count -gt 0) {
        [Console]::Error.WriteLine("flake-guard FAILED: unstable package(s): $failedPackages")
        $exitCode = 1
    }
    else {
        Write-Host "flake-guard PASS: no flakes detected."
    }
}
finally {
    Pop-Location
}

exit $exitCode
