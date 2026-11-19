# One-click Windows release for argent-go.
#
#   powershell -ExecutionPolicy Bypass -File scripts/publish-win.ps1
#   powershell -ExecutionPolicy Bypass -File scripts/publish-win.ps1 -DryRun
#
# Preconditions (all checked by the script before anything is built or pushed):
#   - clean main checkout
#   - internal/cli/cli.go Version already bumped
#   - CHANGELOG.md has a `## v<Version>` section
#   - no local or remote tag for v<Version>
#   - tracked files pass the privacy scan
#
# It validates locally, pushes main and the v<Version> tag, then waits for the
# GitHub Actions workflow to publish the Windows-only Release (zip + checksums)
# and prints its URL. No local gh CLI or personal token is needed: the workflow
# uses Actions' GITHUB_TOKEN. A tag without a Release is a hard failure.
#
# -DryRun runs every validation and the local build, then stops before pushing.
# -KeepArtifacts keeps dist/publish-win after a successful publish.

param(
    [switch]$BuildUI,
    [switch]$SkipChecks,
    [switch]$DryRun,
    [switch]$KeepArtifacts,
    [switch]$SkipPrivacy
)

$ErrorActionPreference = 'Stop'

function Invoke-RepoScript {
    param([string]$Path, [string[]]$ScriptArgs = @())
    $exe = (Get-Command pwsh -ErrorAction SilentlyContinue | Select-Object -First 1).Source
    if (-not $exe) { $exe = (Get-Command powershell -ErrorAction SilentlyContinue | Select-Object -First 1).Source }
    if (-not $exe) { throw 'pwsh or powershell not found' }
    $prefix = @('-NoProfile')
    if ($exe -notmatch 'pwsh') { $prefix += @('-ExecutionPolicy', 'Bypass') }
    & $exe @prefix -File $Path @ScriptArgs | Out-Host
}

$repo = Split-Path -Parent $PSScriptRoot
Push-Location $repo
try {
    # --- A07: clean main before any build ---------------------------------
    $branch = (& git rev-parse --abbrev-ref HEAD).Trim()
    if ($branch -ne 'main') {
        throw "publish-win must run on main (currently $branch)"
    }
    $dirty = (@(& git status --porcelain) -join "`n").Trim()
    if ($dirty) {
        throw "working tree is not clean:`n$dirty"
    }

    # --- A08/A09: version, tag and CHANGELOG agree ------------------------
    $cliSource = Get-Content -LiteralPath (Join-Path $repo 'internal/cli/cli.go') -Raw
    $versionMatch = [regex]::Match($cliSource, 'const\s+Version\s*=\s*"([^"]+)"')
    if (-not $versionMatch.Success) {
        throw 'cannot read Version from internal/cli/cli.go'
    }
    $version = $versionMatch.Groups[1].Value
    $tag = "v$version"

    $changelog = Get-Content -LiteralPath (Join-Path $repo 'CHANGELOG.md') -Raw
    if ($changelog -notmatch "(?m)^##\s+v$([regex]::Escape($version))\b") {
        throw "CHANGELOG.md has no section for v$version"
    }

    $localTag = (@(& git tag -l $tag) -join "`n").Trim()
    if ($localTag) {
        throw "tag $tag already exists locally"
    }
    $remoteTag = (@(& git ls-remote --tags origin "refs/tags/$tag") -join "`n").Trim()
    if ($remoteTag) {
        throw "tag $tag already exists on origin"
    }

    # --- A19/A18: reusable consistency + privacy gates --------------------
    $checker = Join-Path $repo 'scripts/check-release-consistency.ps1'
    if (Test-Path -LiteralPath $checker) {
        Invoke-RepoScript -Path $checker -ScriptArgs @('-Tag', $tag)
        if ($LASTEXITCODE -ne 0) {
            throw 'Version/tag/CHANGELOG consistency check failed'
        }
    }
    if (-not $SkipPrivacy) {
        $scan = Join-Path $repo 'scripts/privacy-scan.ps1'
        if (Test-Path -LiteralPath $scan) {
            Write-Host '== privacy scan ==' -ForegroundColor Cyan
            Invoke-RepoScript -Path $scan
            if ($LASTEXITCODE -ne 0) {
                throw 'privacy scan found potential secrets; fix or pass -SkipPrivacy'
            }
        }
    }

    # --- local build (Windows amd64 only, one zip) -------------------------
    $outDir = Join-Path $repo 'dist/publish-win'
    Write-Host "== validate and build Windows $version ==" -ForegroundColor Cyan
    $buildArgs = @('-Tag', $tag, '-OutDir', $outDir)
    if ($BuildUI) { $buildArgs += '-BuildUI' }
    if ($SkipChecks) { $buildArgs += '-SkipChecks' }
    Invoke-RepoScript -Path (Join-Path $repo 'scripts/build-release.ps1') -ScriptArgs $buildArgs
    if ($LASTEXITCODE -ne 0) {
        throw 'build-release.ps1 failed'
    }

    if ($DryRun) {
        Write-Host "Dry run: validated v$version and built $outDir." -ForegroundColor Yellow
        Write-Host "Would push main and tag $tag; nothing was pushed." -ForegroundColor Yellow
        return
    }

    Write-Host '== push main and tag ==' -ForegroundColor Cyan
    & git push origin main
    if ($LASTEXITCODE -ne 0) { throw 'git push main failed' }
    & git tag -a $tag -m "Release $tag (Windows)"
    if ($LASTEXITCODE -ne 0) { throw 'git tag failed' }
    & git push origin $tag
    if ($LASTEXITCODE -ne 0) { throw 'git push tag failed' }

    $remote = (& git remote get-url origin).Trim()
    if ($remote -notmatch '([^/:]+)/([^/]+?)(?:\.git)?$') {
        throw "cannot parse owner/repo from origin: $remote"
    }
    $slug = "$($Matches[1])/$($Matches[2])"

    # --- A02/A03: a tag is not a release; require published assets ---------
    Write-Host "== waiting for GitHub Release $tag ==" -ForegroundColor Cyan
    $release = $null
    $deadline = (Get-Date).AddMinutes(10)
    while ((Get-Date) -lt $deadline) {
        try {
            $candidate = Invoke-RestMethod `
                -Uri "https://api.github.com/repos/$slug/releases/tags/$tag" `
                -Headers @{ 'User-Agent' = 'argent-publish' } `
                -TimeoutSec 15
            $assets = @($candidate.assets)
            $hasZip = @($assets | Where-Object { $_.name -like 'argent_*_windows_amd64.zip' }).Count -gt 0
            $hasSums = @($assets | Where-Object { $_.name -eq 'checksums.txt' }).Count -gt 0
            if ($candidate.html_url -and $hasZip -and $hasSums) {
                $release = $candidate
                break
            }
        } catch {
            $release = $null
        }
        Start-Sleep -Seconds 10
    }

    if (-not $release -or -not $release.html_url) {
        throw "Tag $tag was pushed, but no GitHub Release with a Windows zip and checksums appeared within 10 minutes. Check the release workflow; a tag alone is not a release."
    }
    Write-Host "Release published: $($release.html_url)" -ForegroundColor Green

    # --- A10: clean local artifacts after a successful publish ------------
    if ($KeepArtifacts) {
        Write-Host "Artifacts kept at $outDir" -ForegroundColor Green
    } else {
        if (Test-Path -LiteralPath $outDir) {
            Remove-Item -LiteralPath $outDir -Recurse -Force
        }
        Write-Host "Removed local artifacts: $outDir" -ForegroundColor Green
    }
}
finally {
    Pop-Location
}
