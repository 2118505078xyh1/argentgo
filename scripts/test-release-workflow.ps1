# Local, offline validation harness for .github/workflows/release.yml.
#
#   powershell -ExecutionPolicy Bypass -File scripts/test-release-workflow.ps1
#   powershell -ExecutionPolicy Bypass -File scripts/test-release-workflow.ps1 -FullBuild
#
# Two halves:
#   1. Static assertions over release.yml as TEXT. They prove the workflow is
#      wired correctly (gated job graph, tag trigger, least privilege, token
#      wiring, calls to the local build/verify scripts). They do not run Actions.
#   2. A local reproduction of the workflow's "Verify release artifacts" step:
#      produce a build-release-shaped artifact in a temp dir (fabricated by
#      default; the real builder with -FullBuild), then run
#      scripts/verify-release.ps1 against it exactly as the workflow does and
#      require exit 0. The temp dir is removed in finally.
#
# No network, no GitHub, no token value is ever read or printed. The token is
# only asserted as a literal placeholder string inside the YAML. This is a
# partial, honest validation: a real publish still needs a v* tag push and the
# automatically provided GITHUB_TOKEN on a GitHub-hosted runner.
# Standard-library PowerShell only.

param(
    # Opt in to running the real scripts/build-release.ps1 instead of a
    # fabricated artifact. Requires a working Go toolchain and cached modules;
    # it is off by default so the harness stays fully offline and deterministic.
    [switch]$FullBuild
)

$ErrorActionPreference = 'Stop'

$repo         = Split-Path -Parent $PSScriptRoot
$workflow     = Join-Path $repo '.github/workflows/release.yml'
$verifyScript = Join-Path $repo 'scripts/verify-release.ps1'
$buildScript  = Join-Path $repo 'scripts/build-release.ps1'
$cliSource    = Join-Path $repo 'internal/cli/cli.go'

$script:PassCount = 0
$script:FailCount = 0
$script:Failures  = [System.Collections.Generic.List[string]]::new()

function Write-Section([string]$Title) {
    Write-Host ''
    Write-Host "== $Title ==" -ForegroundColor Cyan
}

function Assert-Condition([string]$Name, [bool]$Condition, [string]$Hint = '') {
    if ($Condition) {
        $script:PassCount++
        Write-Host "  PASS  $Name"
    }
    else {
        $script:FailCount++
        $script:Failures.Add($Name)
        if ($Hint) { Write-Host "  FAIL  $Name -- $Hint" -ForegroundColor Red }
        else       { Write-Host "  FAIL  $Name" -ForegroundColor Red }
    }
}

# Native commands may write to stderr (e.g. a missing PyYAML module) or exit
# non-zero; with ErrorActionPreference=Stop, Windows PowerShell 5.1 turns that
# stderr into a terminating error. Capture it without aborting the harness.
function Invoke-NativeCaptured([string]$Exe, [string[]]$Arguments) {
    $old = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $captured = & $Exe @Arguments 2>&1
        return [pscustomobject]@{ Output = @($captured); ExitCode = $LASTEXITCODE }
    }
    finally {
        $ErrorActionPreference = $old
    }
}

# Return only the lines belonging to a top-level job (two-space indent), so
# scoped checks cannot accidentally match a different job's configuration.
function Get-JobBlock([string]$Text, [string]$JobName) {
    $lines = $Text -split "\r?`n"
    $start = -1
    for ($i = 0; $i -lt $lines.Count; $i++) {
        if ($lines[$i] -match "^  ${JobName}:\s*$") { $start = $i; break }
    }
    if ($start -lt 0) { return '' }
    $end = $lines.Count
    for ($j = $start + 1; $j -lt $lines.Count; $j++) {
        if ($lines[$j] -match '^  [A-Za-z_][A-Za-z0-9_-]*:') { $end = $j; break }
    }
    return (($lines[$start..($end - 1)]) -join "`n")
}

Write-Host 'Release workflow local validation (offline; no token values are read or printed)'

# ---------------------------------------------------------------------------
# 1. Static structure of release.yml
# ---------------------------------------------------------------------------
Write-Section 'Static workflow structure (release.yml parsed as text)'

if (-not (Test-Path -LiteralPath $workflow -PathType Leaf)) {
    Write-Host "Cannot find workflow: $workflow" -ForegroundColor Red
    exit 1
}
$yaml = Get-Content -LiteralPath $workflow -Raw

Assert-Condition 'declares job: verify' ($yaml -match '(?m)^  verify:\s*$')
Assert-Condition 'declares job: race'   ($yaml -match '(?m)^  race:\s*$')
Assert-Condition 'declares job: release'($yaml -match '(?m)^  release:\s*$')
Assert-Condition "tag trigger is v* (tags: ['v*'])" ($yaml -match "(?m)^\s*tags:\s*\[\s*['`"]?v\*['`"]?\s*\]")
Assert-Condition 'top-level permissions block present' ($yaml -match '(?m)^permissions:\s*$')
Assert-Condition 'least-privilege default is contents: read' ($yaml -match '(?m)^\s*contents:\s*read\b')

$verifyBlock  = Get-JobBlock $yaml 'verify'
$raceBlock    = Get-JobBlock $yaml 'race'
$releaseBlock = Get-JobBlock $yaml 'release'

Assert-Condition 'release job block located for scoped assertions' ($releaseBlock.Length -gt 0) 'job extraction failed'
if ($releaseBlock.Length -eq 0) {
    Write-Host 'RESULT: FAIL (cannot scope release-job assertions)' -ForegroundColor Red
    exit 1
}

Assert-Condition 'verify job runs gofmt' ($verifyBlock -match 'gofmt\s+-l')
Assert-Condition 'verify job runs go vet' ($verifyBlock -match 'go\s+vet')
Assert-Condition 'verify job runs go test' ($verifyBlock -match 'go\s+test\b')
Assert-Condition "verify builds with CGO_ENABLED='0'" ($verifyBlock -match "CGO_ENABLED:\s*'0'")
Assert-Condition 'race job runs go test -race' ($raceBlock -match 'go\s+test\s+-race')
# 竞态门禁必须有上限超时（与 ci.yml / matrix.yml 对齐），否则挂起的测试会一直阻塞作业。
Assert-Condition 'race job sets -timeout on go test -race' ($raceBlock -match 'go\s+test\s+-race[^\r\n]*-timeout\s+\d+m')
Assert-Condition "race enables CGO_ENABLED='1' for the detector" ($raceBlock -match "CGO_ENABLED:\s*'1'")

# All remaining assertions are scoped to the release job block.
Assert-Condition 'release needs: [verify, race]' ($releaseBlock -match '(?m)^\s*needs:\s*\[\s*verify\s*,\s*race\s*\]')
Assert-Condition 'release grants permissions: contents: write' (($releaseBlock -match '(?m)^\s*permissions:\s*$') -and ($releaseBlock -match '(?m)^\s*contents:\s*write\b'))
Assert-Condition 'release references secrets.GITHUB_TOKEN (literal only; value never read or printed)' ($releaseBlock -match '\$\{\{\s*secrets\.GITHUB_TOKEN\s*\}\}')
Assert-Condition 'release sets GH_TOKEN from that secret' ($releaseBlock -match '(?m)^\s*GH_TOKEN:\s*\$\{\{\s*secrets\.GITHUB_TOKEN\s*\}\}')
Assert-Condition 'release calls scripts/build-release.ps1' ($releaseBlock -match 'build-release\.ps1')
Assert-Condition 'release calls scripts/verify-release.ps1' ($releaseBlock -match 'verify-release\.ps1')
Assert-Condition 'release calls scripts/check-release-consistency.ps1' ($releaseBlock -match 'check-release-consistency\.ps1')
Assert-Condition 'release calls scripts/extract-release-notes.ps1' ($releaseBlock -match 'extract-release-notes\.ps1')
Assert-Condition 'release runs gh release create' ($releaseBlock -match 'gh\s+release\s+create')
Assert-Condition 'gh release create pins --verify-tag' ($releaseBlock -match '--verify-tag')
Assert-Condition 'release verifies published assets with gh release view' ($releaseBlock -match 'gh\s+release\s+view')

# ---------------------------------------------------------------------------
# 2. Optional YAML parse. Informational only: a missing PyYAML is not a failure.
# ---------------------------------------------------------------------------
Write-Section 'Optional YAML parse (python + PyYAML)'

$pythonCmd = (Get-Command python -ErrorAction SilentlyContinue | Select-Object -First 1).Source
if (-not $pythonCmd) {
    Write-Host '  SKIP  python not on PATH; textual assertions above are authoritative'
}
else {
    # Single quotes inside the Python code survive Windows PowerShell 5.1's
    # native-argument quoting; double quotes would be stripped from the string.
    $pyCode = "import sys, yaml; yaml.safe_load(open(sys.argv[1], encoding='utf-8')); print('yaml-ok')"
    $py     = Invoke-NativeCaptured $pythonCmd @('-c', $pyCode, $workflow)
    $pyMsg  = ($py.Output | Out-String).Trim()
    if ($py.ExitCode -eq 0) {
        $script:PassCount++
        Write-Host '  PASS  python yaml.safe_load parsed release.yml'
    }
    elseif ($pyMsg -match 'No module named .?yaml|ModuleNotFoundError') {
        Write-Host '  SKIP  PyYAML not installed; textual assertions above are authoritative'
    }
    else {
        Assert-Condition 'python yaml.safe_load parsed release.yml' $false $pyMsg
    }
}

# ---------------------------------------------------------------------------
# 3. Local reproduction of the workflow's artifact-verification step.
# ---------------------------------------------------------------------------
Write-Section 'Local pipeline reproduction (offline; temp dir cleaned in finally)'

$version = ''
if (Test-Path -LiteralPath $cliSource -PathType Leaf) {
    $m = [regex]::Match((Get-Content -LiteralPath $cliSource -Raw), 'const\s+Version\s*=\s*"([^"]+)"')
    if ($m.Success) { $version = $m.Groups[1].Value }
}
Assert-Condition 'read Version constant from internal/cli/cli.go' ([bool]$version)
if (-not $version) { $version = '0.0.0-local' }

$pwshCmd = (Get-Command pwsh -ErrorAction SilentlyContinue | Select-Object -First 1).Source
if (-not $pwshCmd) { $pwshCmd = (Get-Command powershell -ErrorAction SilentlyContinue | Select-Object -First 1).Source }
Assert-Condition 'found a PowerShell host to run the release scripts' ([bool]$pwshCmd)
if (-not $pwshCmd) {
    Write-Host 'RESULT: FAIL (no PowerShell host)' -ForegroundColor Red
    exit 1
}

$tmpRoot     = Join-Path ([IO.Path]::GetTempPath()) ('argent-relcheck-' + [guid]::NewGuid().ToString('N'))
$artifactDir = $null

try {
    New-Item -ItemType Directory -Force -Path $tmpRoot | Out-Null

    if ($FullBuild) {
        $artifactDir = Join-Path $tmpRoot 'dist/release'
        Write-Host '  -> running scripts/build-release.ps1 -SkipChecks into a temp dir (real build)'
        $b     = Invoke-NativeCaptured $pwshCmd @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $buildScript, '-Tag', "v$version", '-OutDir', $artifactDir, '-SkipChecks')
        $bLast = ($b.Output | Select-Object -Last 1)
        Assert-Condition 'build-release.ps1 exited 0' ($b.ExitCode -eq 0) "exit=$($b.ExitCode); $bLast"
    }
    else {
        $artifactDir = Join-Path $tmpRoot 'artifacts'
        New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null
        $stage    = Join-Path $tmpRoot 'fabricated-binaries'
        New-Item -ItemType Directory -Force -Path $stage | Out-Null

        # Same shape as build-release.ps1 output: one windows/amd64 zip holding
        # both binaries plus a sha256sum-style checksums.txt entry.
        $zipName = "argentgo_${version}_windows_amd64.zip"
        foreach ($bin in @('argentgo.exe', 'argentgo-native.exe')) {
            [IO.File]::WriteAllBytes((Join-Path $stage $bin), [Text.Encoding]::ASCII.GetBytes("fake $bin for local release validation`n"))
        }
        $zip = Join-Path $artifactDir $zipName
        Compress-Archive -LiteralPath @((Join-Path $stage 'argentgo.exe'), (Join-Path $stage 'argentgo-native.exe')) -DestinationPath $zip -CompressionLevel Optimal
        $sum = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
        [IO.File]::WriteAllText((Join-Path $artifactDir 'checksums.txt'), "$sum  $zipName`n", (New-Object Text.UTF8Encoding $false))
        Assert-Condition 'fabricated build-release-shaped artifacts in temp dir' ((Test-Path -LiteralPath $zip -PathType Leaf) -and (Test-Path -LiteralPath (Join-Path $artifactDir 'checksums.txt') -PathType Leaf))
    }

    Write-Host "  -> running verify-release.ps1 -ArtifactDir <temp> -Version $version (same as the workflow)"
    $v = Invoke-NativeCaptured $pwshCmd @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $verifyScript, '-ArtifactDir', $artifactDir, '-Version', $version)
    foreach ($line in $v.Output) { Write-Host "     $line" }
    Assert-Condition 'verify-release.ps1 exited 0 on local artifacts' ($v.ExitCode -eq 0) "exit=$($v.ExitCode)"
}
finally {
    if (Test-Path -LiteralPath $tmpRoot) {
        Remove-Item -LiteralPath $tmpRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# ---------------------------------------------------------------------------
# 4. Summary
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host '===== release workflow local validation summary ====='
Write-Host ("  PASS: {0}   FAIL: {1}" -f $script:PassCount, $script:FailCount)

if ($script:FailCount -gt 0) {
    Write-Host '  Failed assertions:' -ForegroundColor Red
    foreach ($failure in $script:Failures) { Write-Host "    - $failure" -ForegroundColor Red }
    Write-Host 'RESULT: FAIL' -ForegroundColor Red
    Write-Host 'Scope: LOCAL PARTIAL validation only; nothing was published.' -ForegroundColor Yellow
    exit 1
}

Write-Host 'RESULT: PASS' -ForegroundColor Green
Write-Host 'Scope: local wiring + artifact verification only. A real publish still requires a v* tag push and GITHUB_TOKEN on GitHub Actions.' -ForegroundColor Yellow
exit 0
