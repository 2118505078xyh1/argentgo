# Install the Windows argentgo binary from a release zip.
#
#   powershell -ExecutionPolicy Bypass -File scripts/install-win.ps1 `
#       [-ZipPath dist/release/argentgo_0.1.1_windows_amd64.zip] `
#       [-InstallDir "$env:LOCALAPPDATA\argentgo\bin"]
#
# Only the binary is installed; sessions/config under ~/.argentgo are untouched.

param(
    [string]$ZipPath = '',
    [string]$InstallDir = ''
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot

if (-not $InstallDir) { $InstallDir = Join-Path $env:LOCALAPPDATA 'argentgo/bin' }
$InstallDir = [IO.Path]::GetFullPath($InstallDir)

if (-not $ZipPath) {
    $releaseDir = Join-Path $repo 'dist/release'
    $zips = @(Get-ChildItem -LiteralPath $releaseDir -Filter 'argentgo_*_windows_amd64.zip' -File -ErrorAction SilentlyContinue)
    if ($zips.Count -eq 0) {
        throw "no Windows zip in $releaseDir; run scripts/build-release.ps1 or pass -ZipPath"
    }
    if ($zips.Count -gt 1) {
        throw "multiple Windows zips in $releaseDir; pass -ZipPath to choose one"
    }
    $ZipPath = $zips[0].FullName
}
if (-not (Test-Path -LiteralPath $ZipPath)) { throw "zip not found: $ZipPath" }

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$tmp = Join-Path ([IO.Path]::GetTempPath()) ('argent-install-' + [guid]::NewGuid().ToString('N'))
try {
    Expand-Archive -LiteralPath $ZipPath -DestinationPath $tmp -Force
    $exe = Get-ChildItem -LiteralPath $tmp -Filter 'argentgo.exe' -File -Recurse | Select-Object -First 1
    if (-not $exe) { throw "zip has no argentgo.exe: $ZipPath" }
    $target = Join-Path $InstallDir 'argentgo.exe'
    Copy-Item -LiteralPath $exe.FullName -Destination $target -Force
} finally {
    if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Recurse -Force }
}

Write-Host "Installed: $target" -ForegroundColor Green
if (($env:Path -split ';') -notcontains $InstallDir) {
    Write-Host "Add this directory to your user PATH to run argentgo from any shell:" -ForegroundColor Yellow
    Write-Host "  $InstallDir" -ForegroundColor Yellow
}
