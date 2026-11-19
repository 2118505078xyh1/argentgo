# Download the Go toolchain from a mirror with parallel ranged requests.
#
#   powershell -ExecutionPolicy Bypass -File scripts\fetch-go.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\fetch-go.ps1 -Version 1.27.4 -Parts 8
#
# Each range is a separate curl.exe process writing `<zip>.partNN`. Re-running the
# script re-fetches only the ranges that are still incomplete, and the assembled
# zip is checked against the official sha256 from `dl/?mode=json`. Progress is
# printed once per second as a percentage.

param(
    [string]$Version = '1.27.4',
    [int]$Parts = 8,
    [string[]]$Mirrors = @(
        'https://mirrors.aliyun.com/golang',
        'https://mirrors.ustc.edu.cn/golang',
        'https://golang.google.cn/dl'
    ),
    [string]$Root = (Join-Path $env:USERPROFILE '.tools'),
    [string]$Dest = '',
    [switch]$KeepZip
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

if (-not $Dest) { $Dest = Join-Path $Root 'go' }
$dl = Join-Path $Root 'go-dl'
New-Item -ItemType Directory -Force -Path $dl | Out-Null

$name = "go$Version.windows-amd64.zip"
$zip = Join-Path $dl $name

# ---- pick a mirror -------------------------------------------------------

$url = $null
$size = 0
foreach ($mirror in $Mirrors) {
    $candidate = '{0}/{1}' -f $mirror.TrimEnd('/'), $name
    try {
        $head = Invoke-WebRequest -Method Head -Uri $candidate -UseBasicParsing -TimeoutSec 20
        $len = [long]$head.Headers['Content-Length']
        if ($head.StatusCode -eq 200 -and $len -gt 0) {
            $url = $candidate
            $size = $len
            break
        }
    } catch {
        Write-Host ("skip {0}" -f $candidate)
    }
}
if (-not $url) { throw "no mirror served $name" }

Write-Host ("source: {0}" -f $url)
Write-Host ("size:   {0:N1} MB" -f ($size / 1MB))
Write-Host ("parts:  {0}" -f $Parts)

# ---- split into ranges ---------------------------------------------------

$chunk = [long][math]::Ceiling($size / $Parts)
$jobs = @()
for ($i = 0; $i -lt $Parts; $i++) {
    $start = [long]$i * $chunk
    if ($start -ge $size) { break }
    $end = [long][math]::Min($start + $chunk - 1, $size - 1)
    $jobs += [pscustomobject]@{
        Start = $start
        End   = $end
        Want  = $end - $start + 1
        Path  = (Join-Path $dl ('{0}.part{1:D2}' -f $name, $i))
        Proc  = $null
    }
}

function Get-Have([object]$job) {
    if (-not (Test-Path -LiteralPath $job.Path)) { return 0 }
    return [math]::Min([long](Get-Item -LiteralPath $job.Path).Length, [long]$job.Want)
}

function Start-Range([object]$job) {
    if (Test-Path -LiteralPath $job.Path) { Remove-Item -LiteralPath $job.Path -Force }
    $curlArgs = @(
        '-sS', '-L', '--fail',
        '--retry', '5', '--retry-delay', '2', '--connect-timeout', '20',
        '--range', ('{0}-{1}' -f $job.Start, $job.End),
        '-o', ('"{0}"' -f $job.Path),
        $url
    )
    $job.Proc = Start-Process -FilePath 'curl.exe' -ArgumentList $curlArgs -WindowStyle Hidden -PassThru
}

for ($attempt = 1; $attempt -le 4; $attempt++) {
    $pending = @($jobs | Where-Object { (Get-Have $_) -lt $_.Want })
    if ($pending.Count -eq 0) { break }

    if ($attempt -gt 1) {
        Write-Host ("retry {0}: {1} incomplete part(s)" -f ($attempt - 1), $pending.Count)
        Start-Sleep -Seconds 2
    }
    foreach ($job in $pending) { Start-Range $job }

    $last = [DateTime]::MinValue
    while ($true) {
        $sum = 0
        $done = 0
        foreach ($job in $jobs) {
            $have = Get-Have $job
            $sum += $have
            if ($have -ge $job.Want) { $done++ }
        }
        $now = Get-Date
        if (($now - $last).TotalSeconds -ge 1) {
            Write-Host ("{0,6:N1}%  {1,7:N1} / {2,7:N1} MB  parts {3}/{4}" -f `
                    ($sum * 100 / $size), ($sum / 1MB), ($size / 1MB), $done, $jobs.Count)
            $last = $now
        }
        $running = @($pending | Where-Object { $_.Proc -and -not $_.Proc.HasExited })
        if ($running.Count -eq 0) { break }
        Start-Sleep -Milliseconds 500
    }
}

$missing = @($jobs | Where-Object { (Get-Have $_) -lt $_.Want })
if ($missing.Count -gt 0) {
    throw ("{0} part(s) still incomplete after retries" -f $missing.Count)
}

# ---- assemble ------------------------------------------------------------

if (Test-Path -LiteralPath $zip) { Remove-Item -LiteralPath $zip -Force }
$target = [System.IO.File]::Open($zip, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write)
try {
    foreach ($job in $jobs) {
        $source = [System.IO.File]::OpenRead($job.Path)
        try { $source.CopyTo($target) } finally { $source.Dispose() }
    }
} finally {
    $target.Dispose()
}

$got = (Get-Item -LiteralPath $zip).Length
if ($got -ne $size) { throw "assembled size $got != expected $size" }
Write-Host ("100.0%  {0:N1} MB  -> {1}" -f ($got / 1MB), $zip)

foreach ($job in $jobs) { Remove-Item -LiteralPath $job.Path -Force -ErrorAction SilentlyContinue }

$hash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLower()
Write-Host "sha256: $hash"

try {
    $meta = Invoke-RestMethod -Uri 'https://golang.google.cn/dl/?mode=json' -TimeoutSec 20
    $expect = @($meta | ForEach-Object { $_.files } | Where-Object { $_.filename -eq $name })[0].sha256
    if ($expect) {
        if ($expect -ne $hash) { throw "sha256 mismatch: expected $expect" }
        Write-Host "sha256 verified against dl/?mode=json"
    }
} catch [System.Management.Automation.RuntimeException] {
    throw
} catch {
    Write-Host "sha256 check skipped (metadata unavailable)"
}

# ---- extract -------------------------------------------------------------

if (Test-Path -LiteralPath $Dest) { Remove-Item -LiteralPath $Dest -Recurse -Force }
New-Item -ItemType Directory -Force -Path $Dest | Out-Null
Write-Host "extracting to $Dest"
Expand-Archive -LiteralPath $zip -DestinationPath $Dest -Force

if (-not $KeepZip) { Remove-Item -LiteralPath $zip -Force }

$go = Join-Path $Dest 'go\bin\go.exe'
Write-Host ("go: {0}" -f $go)
& $go version
