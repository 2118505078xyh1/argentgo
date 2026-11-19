# Build the Windows release artifact for argentgo (the version the operator
# runs locally). Other platforms are intentionally out of scope.
#
#   powershell -ExecutionPolicy Bypass -File scripts/build-release.ps1 `
#       [-Version 0.1.1] [-Tag v0.1.1] [-OutDir dist/release] `
#       [-SkipChecks]
#
# Produces one Windows zip plus a sha256sum-style checksums.txt.
# The version is read from internal/cli/cli.go (the single source of truth);
# -Version/-Tag only validate, they never override it, so a binary cannot
# report a different version than the tag it was released under.

param(
    [string]$Version = '',
    [string]$Tag = '',
    [string]$OutDir = '',
    [switch]$SkipChecks
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
Push-Location $repo
try {
    # --- toolchain -------------------------------------------------------
    # Forward slashes work in PowerShell on both Windows and Linux; the
    # release workflow runs this script with pwsh on ubuntu-latest.
    $go = (Get-Command go -ErrorAction SilentlyContinue | Select-Object -First 1).Source
    if (-not $go) {
        $go = Join-Path $env:USERPROFILE '.tools/go/go/bin/go'
        if (Test-Path -LiteralPath ($go + '.exe')) { $go = $go + '.exe' }
    }
    if (-not (Test-Path -LiteralPath $go)) {
        throw 'go not found: add it to PATH or install to ~/.tools/go/go/bin'
    }
    $goBin = Split-Path -Parent $go
    $gofmt = Join-Path $goBin 'gofmt'
    if (Test-Path -LiteralPath ($gofmt + '.exe')) { $gofmt = $gofmt + '.exe' }
    $pwsh = (Get-Command pwsh -ErrorAction SilentlyContinue | Select-Object -First 1).Source
    if (-not $pwsh) { $pwsh = (Get-Command powershell -ErrorAction SilentlyContinue | Select-Object -First 1).Source }

    # --- version (must match the source constant) ------------------------
    $cliSource = Get-Content -LiteralPath (Join-Path $repo 'internal/cli/cli.go') -Raw
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
    Write-Host "argentgo $Version" -ForegroundColor Cyan

    # --- output directory (clean build, no stale assets) ------------------
    if (-not $OutDir) { $OutDir = Join-Path $repo 'dist/release' }
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
        # gofmt compares line endings, and a Windows clone with the default
        # core.autocrlf=true checks out CRLF. Check a LF-normalized copy so
        # this gate measures formatting, not git checkout policy, and never
        # mutates the operator's working tree.
        Write-Host '== gofmt ==' -ForegroundColor Cyan
        $goFiles = @(& git ls-files '*.go')
        $fmtRoot = Join-Path ([IO.Path]::GetTempPath()) ('argentgofmt-' + [guid]::NewGuid().ToString('N'))
        $unformatted = @()
        try {
            foreach ($file in $goFiles) {
                $src = Join-Path $repo $file
                if (-not (Test-Path -LiteralPath $src)) { continue }
                $dest = Join-Path $fmtRoot $file
                $parent = Split-Path -Parent $dest
                if (-not (Test-Path -LiteralPath $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
                $text = [Text.Encoding]::UTF8.GetString([IO.File]::ReadAllBytes($src)).Replace("`r`n", "`n")
                [IO.File]::WriteAllText($dest, $text, (New-Object Text.UTF8Encoding $false))
            }
            $unformatted = @(& $gofmt -l $fmtRoot)
            $gofmtExit = $LASTEXITCODE
        }
        finally {
            if (Test-Path -LiteralPath $fmtRoot) { Remove-Item -LiteralPath $fmtRoot -Recurse -Force }
        }
        if ($gofmtExit -ne 0) { throw 'gofmt failed' }
        if ($unformatted) {
            $names = @($unformatted | ForEach-Object { $_.Substring($fmtRoot.Length + 1) })
            throw ("unformatted files: " + ($names -join ', '))
        }
    }

    # Windows amd64 only. The operator runs the Windows build; other targets
    # are intentionally out of scope for the one-click publisher.
    $targets = @(
        @{ os = 'windows'; arch = 'amd64' }
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
            # windowsgui makes the exe a GUI-subsystem binary: no black console
            # window when launched from a desktop shortcut. CLI output still
            # works when started from an existing terminal.
            & $go build -trimpath -ldflags '-s -w -H=windowsgui' -o $bin ./cmd/argent
            if ($LASTEXITCODE -ne 0) { throw "go build failed for $os/$arch" }
            if (-not (Test-Path -LiteralPath $bin)) { throw "missing binary for $os/$arch" }

            # The MyGo native frontend is the default desktop client. Build it
            # beside argent.exe; it drives the engine over --rpc.
            $nativeBin = Join-Path $stage 'argent-native.exe'
            & $go build -trimpath -ldflags '-s -w -H=windowsgui' -o $nativeBin ./native
            if ($LASTEXITCODE -ne 0) { throw "native frontend build failed for $os/$arch" }
            if (-not (Test-Path -LiteralPath $nativeBin)) { throw "missing native binary for $os/$arch" }

            $zipName = "argent_${Version}_${os}_${arch}.zip"
            $zip = Join-Path $OutDir $zipName
            if (Test-Path -LiteralPath $zip) { Remove-Item -LiteralPath $zip -Force }
            Compress-Archive -LiteralPath @($bin, $nativeBin) -DestinationPath $zip -CompressionLevel Optimal

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

    $zips = @(Get-ChildItem -LiteralPath $OutDir -Filter '*.zip' -File)
    if ($zips.Count -ne $targets.Count) {
        throw "expected $($targets.Count) zip(s) in $OutDir, found $($zips.Count)"
    }

    Write-Host ''
    Write-Host "Release artifacts: $OutDir" -ForegroundColor Green
    Write-Host ('  {0} zips, checksums.txt' -f $targets.Count) -ForegroundColor Green
}
finally {
    Pop-Location
}
