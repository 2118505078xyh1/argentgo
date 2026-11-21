# Remove the locally installed argentgo binary.
#
#   powershell -ExecutionPolicy Bypass -File scripts/uninstall-win.ps1 `
#       [-InstallDir "$env:LOCALAPPDATA\argentgo\bin"]
#
# Sessions, config, hooks and audit logs under ~/.argentgo are preserved.

param([string]$InstallDir = '')

$ErrorActionPreference = 'Stop'

if (-not $InstallDir) { $InstallDir = Join-Path $env:LOCALAPPDATA 'argentgo/bin' }
$InstallDir = [IO.Path]::GetFullPath($InstallDir)
$exe = Join-Path $InstallDir 'argentgo.exe'

if (Test-Path -LiteralPath $exe) {
    Remove-Item -LiteralPath $exe -Force
    Write-Host "Removed: $exe" -ForegroundColor Green
} else {
    Write-Host "Nothing installed at $exe" -ForegroundColor Yellow
}

if ((Test-Path -LiteralPath $InstallDir) -and -not (Get-ChildItem -LiteralPath $InstallDir -Force)) {
    Remove-Item -LiteralPath $InstallDir -Force
    Write-Host "Removed empty directory: $InstallDir" -ForegroundColor Green
}
Write-Host 'User data under ~/.argentgo was left untouched.' -ForegroundColor Green
