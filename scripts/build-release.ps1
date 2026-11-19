# Build cross-platform release artifacts for argent-go.
#
#   powershell -ExecutionPolicy Bypass -File scripts/build-release.ps1 `
#       [-Version 0.1.1] [-Tag v0.1.1] [-OutDir dist/release] `
#       [-BuildUI] [-SkipChecks]
#
# Produces one zip per GOOS/GOARCH plus a sha256sum-style checksums.txt.
# The version is read from internal/cli/cli.go (the single source of truth);
# -Version/-Tag only validate, they never override it, so a binary cannot
# report a different version than the tag it was released under.

param(
    [string]$Version = '',
    [string]$Tag = '',
    [string]$OutDir = '',
    [switch]$BuildUI,
    [switch]$SkipChecks
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
Push-Location $repo
try {
    # --- toolchain -------------------------------------------------------
    $go = (Get-Command go -ErrorAction SilentlyContinue | Select-Object -First 1).Source
    if (-not $go) {
        $go = Join-Path $env:USERPROFILE '.tools\go\go\bin\go.exe'
    }
    if (-not (Test-Path -LiteralPath $go)) {
        throw 'go not found: add it to PATH or install to ~/.tools/go/go/bin'
    }
    $goBin = Split-Path -Parent $go
    $gofmt = Join-Path $goBin 'gofmt.exe'
    if (-not (Test-Path -LiteralPath $gofmt)) { $gofmt = Join-Path $goBin 'gofmt' }

    # --- version (must match the source constant) ------------------------
    $cliSource = Get-Content -LiteralPath (Join-Path $repo 'internal\cli\cli.go') -Raw
    $match = [regex]::Match($cliSource, 'const\s+Version\s*=\s*"([^"]+)"')
    if (-not $match.Success) {
        throw 'cannot read Version from internal/cli/cli.go'
    }
    $sourceVersion = $match.Groups[1].Value
    if ($Version -and $Version -ne $sourceVersion) {
        throw "requested version $Version != cli.Version $sourceVersion; bump the source first"
    }
    $Version = $sourceVersion
    if ($Tag) {
        $tagVersion = $Tag -replace '^v', ''
        if ($tagVersion -ne $Version) {
            throw "tag $Tag != cli.Version $Version; bump the source before tagging"
        }
    }
    Write-Host "argent-go $Version" -ForegroundColor Cyan

    # --- output directory (clean build, no stale assets) ------------------
    if (-not $OutDir) { $OutDir = Join-Path $repo 'dist\release' }
    $OutDir = [IO.Path]::GetFullPath($OutDir)
    if (Test-Path -LiteralPath $OutDir) {
        Remove-Item -LiteralPath $OutDir -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

    # --- quality gates -----------------------------------------------------
    if (-not $SkipChecks) {
        Write-Host '== vet ==' -ForegroundColor Cyan
        & $go vet ./...
        if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
        Write-Host '== test ==' -ForegroundColor Cyan
        & $go test ./...
        if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
        Write-Host '== gofmt ==' -ForegroundColor Cyan
        $unformatted = & $gofmt -l .
        if ($LASTEXITCODE -ne 0) { throw 'gofmt failed' }
        if ($unformatted) {
            throw ("unformatted files: " + ($unformatted -join ', '))
        }
    }

    # --- embedded frontend -------------------------------------------------
    if ($BuildUI) {
        Write-Host '== build UI ==' -ForegroundColor Cyan
        & powershell -ExecutionPolicy Bypass -File (Join-Path $repo 'scripts\build-ui.ps1')
        if ($LASTEXITCODE -ne 0) { throw 'build-ui.ps1 failed' }
    }
    $index = Join-Path $repo 'internal\gui\dist\index.html'
    if (-not (Test-Path -LiteralPath $index)) {
        throw "embedded UI is missing $index (run scripts/build-ui.ps1)"
    }
    $indexBody = Get-Content -LiteralPath $index -Raw
    if ($indexBody -notmatch '/assets/') {
        throw 'embedded UI looks like the placeholder; run scripts/build-ui.ps1'
    }

    # --- cross-compile matrix ---------------------------------------------
    $targets = @(
        @{ os = 'windows'; arch = 'amd64' },
        @{ os = 'windows'; arch = 'arm64' },
        @{ os = 'linux';   arch = 'amd64' },
        @{ os = 'linux';   arch = 'arm64' },
        @{ os = 'darwin';  arch = 'amd64' },
        @{ os = 'darwin';  arch = 'arm64' }
    )

    $oldGoos = $env:GOOS
    $oldGoarch = $env:GOARCH
    $oldCgo = $env:CGO_ENABLED
    $checksumLines = [System.Collections.Generic.List[string]]::new()
    try {
        foreach ($t in $targets) {
            $os = $t.os
            $arch = $t.arch
            Write-Host "== build $os/$arch ==" -ForegroundColor Cyan
            $env:GOOS = $os
            $env:GOARCH = $arch
            $env:CGO_ENABLED = '0'

            $stage = Join-Path $OutDir ("stage_" + $os + "_" + $arch)
            if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
            New-Item -ItemType Directory -Force -Path $stage | Out-Null

            $binName = if ($os -eq 'windows') { 'argent.exe' } else { 'argent' }
            $bin = Join-Path $stage $binName
            & $go build -trimpath -ldflags '-s -w' -o $bin ./cmd/argent
            if ($LASTEXITCODE -ne 0) { throw "go build failed for $os/$arch" }
            if (-not (Test-Path -LiteralPath $bin)) { throw "missing binary for $os/$arch" }

            $zipName = "argent_${Version}_${os}_${arch}.zip"
            $zip = Join-Path $OutDir $zipName
            if (Test-Path -LiteralPath $zip) { Remove-Item -LiteralPath $zip -Force }
            Compress-Archive -LiteralPath $bin -DestinationPath $zip -CompressionLevel Optimal

            $sum = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
            $checksumLines.Add("$sum  $zipName")
            Write-Host ("  {0}  {1:N0} bytes" -f $zipName, (Get-Item -LiteralPath $zip).Length)

            # Per-asset checksum for --sha256 users; the combined list drives
            # the updater's automatic verification.
            Set-Content -LiteralPath ($zip + '.sha256') -Value "$sum  $zipName" -Encoding utf8 -NoNewline
            Remove-Item -LiteralPath $stage -Recurse -Force
        }
    }
    finally {
        $env:GOOS = $oldGoos
        $env:GOARCH = $oldGoarch
        $env:CGO_ENABLED = $oldCgo
    }

    $sumFile = Join-Path $OutDir 'checksums.txt'
    # UTF-8 without BOM and LF endings: sha256sum -c must accept it everywhere.
    [IO.File]::WriteAllText($sumFile, (($checksumLines -join "`n") + "`n"), (New-Object Text.UTF8Encoding $false))

    # --- verify the manifest we just wrote ---------------------------------
    foreach ($line in $checksumLines) {
        $parts = $line -split '\s+', 2
        $actual = (Get-FileHash -LiteralPath (Join-Path $OutDir $parts[1]) -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $parts[0]) { throw "checksum mismatch for $($parts[1])" }
    }

    Write-Host ''
    Write-Host "Release artifacts: $OutDir" -ForegroundColor Green
    Write-Host ('  {0} zips, checksums.txt' -f $targets.Count) -ForegroundColor Green
}
finally {
    Pop-Location
}
