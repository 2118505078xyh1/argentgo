# CI guard against binary bloat and startup-time regressions.
#
#   pwsh -File scripts/check-binary-size.ps1 [-Binary <path>] [-MaxBytes <n>]
#
# Builds a trimmed Windows/amd64 binary (unless -Binary is given), asserts its
# byte size is <= MaxBytes, and times `--version` as a non-blocking warning.
# The size baseline is generous (current trimmed build ~19 MiB, gate at 28 MiB)
# so routine growth is fine but a regression like #94/#95 (whole-file SHA-256
# on each token estimate, unbounded checkpoint cache) would be caught.

param(
    [string]$Binary = '',
    # 28 MiB: ~19 MiB current trimmed binary plus headroom for feature growth.
    [long]$MaxBytes = 29360128
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
Push-Location $repo
try {
    $built = $false
    if (-not $Binary) {
        $Binary = Join-Path ([IO.Path]::GetTempPath()) ('argent-size-' + [guid]::NewGuid().ToString('N') + '.exe')
        $env:CGO_ENABLED = '0'
        & go build -trimpath -ldflags '-s -w' -o $Binary ./cmd/argent
        if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
        $built = $true
    }
    if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) { throw "binary not found: $Binary" }

    $size = (Get-Item -LiteralPath $Binary).Length
    Write-Host "binary size: $size bytes (limit $MaxBytes)"
    if ($size -gt $MaxBytes) {
        throw "binary size $size exceeds limit $MaxBytes; check for bloat regressions (#94/#95)"
    }

    # Startup smoke: --version must return quickly. This is a warning, not a
    # hard gate, because CI runners vary; a gross regression still surfaces.
    if ($IsWindows -or $env:OS -eq 'Windows_NT') {
        $sw = [Diagnostics.Stopwatch]::StartNew()
        $null = (& $Binary '--version' 2>&1 | Out-String)
        $sw.Stop()
        Write-Host "--version wall time: $($sw.ElapsedMilliseconds) ms (informational)"
        if ($sw.ElapsedMilliseconds -gt 5000) {
            Write-Host 'WARNING: --version took >5s; investigate startup path' -ForegroundColor Yellow
        }
    }

    Write-Host 'binary size / startup gate OK'
}
finally {
    if ($built -and (Test-Path -LiteralPath $Binary)) { Remove-Item -LiteralPath $Binary -Force }
    Pop-Location
}
