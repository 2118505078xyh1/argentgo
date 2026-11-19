# Build the SolidJS UI and publish it into internal/gui/dist for go:embed.
#
#   powershell -ExecutionPolicy Bypass -File scripts/build-ui.ps1
#
# The copy replaces dist wholesale: stale assets from an earlier build must not
# survive into the embedded frontend.

$ErrorActionPreference = 'Stop'

$repo = Split-Path -Parent $PSScriptRoot
$ui = Join-Path $repo 'ui'
$source = Join-Path $ui 'dist'
$target = Join-Path $repo 'internal\gui\dist'

function Invoke-BunStep {
    param([string[]]$Arguments)
    $label = 'bun ' + ($Arguments -join ' ')
    Write-Host $label -ForegroundColor Cyan
    & bun @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$label failed with exit code $LASTEXITCODE"
    }
}

Push-Location $ui
try {
    if (-not (Test-Path -LiteralPath (Join-Path $ui 'node_modules'))) {
        Invoke-BunStep @('install')
    } else {
        Write-Host 'node_modules present; skipping bun install'
    }
    Invoke-BunStep @('run', 'typecheck')
    Invoke-BunStep @('run', 'test')
    Invoke-BunStep @('run', 'build')
} finally {
    Pop-Location
}

if (-not (Test-Path -LiteralPath (Join-Path $source 'index.html'))) {
    throw "build output is missing $source\index.html"
}

if (Test-Path -LiteralPath $target) {
    Remove-Item -LiteralPath $target -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $target | Out-Null
Get-ChildItem -LiteralPath $source -Force | Copy-Item -Destination $target -Recurse -Force

$files = @(Get-ChildItem -LiteralPath $target -Recurse -File)
$index = Get-Item -LiteralPath (Join-Path $target 'index.html')
Write-Host ('UI published: {0} file(s), index.html {1:N0} bytes -> {2}' -f $files.Count, $index.Length, $target) -ForegroundColor Green
