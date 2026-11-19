param(
  [Parameter(Mandatory = $true)][string]$Url,
  [string]$Browser = ""
)

$ErrorActionPreference = "Stop"

# Every artefact lands under the OS temp directory, never in the checkout.
$work = Join-Path ([IO.Path]::GetTempPath()) ("argent-e2e-" + $PID)
New-Item -ItemType Directory -Force -Path $work | Out-Null

$uri = [Uri]$Url
$query = $uri.Query.TrimStart([char]'?')
$tokenPart = ($query -split '&' | Where-Object { $_ -like 'token=*' } | Select-Object -First 1)
if (-not $tokenPart) { throw "URL must contain ?token=<hex>" }
$token = [Uri]::UnescapeDataString($tokenPart.Substring(6))
$base = "$($uri.Scheme)://$($uri.Authority)"
$headers = @{ Authorization = "Bearer $token" }

function Check([string]$name, [string]$path, [bool]$rpc = $false) {
  if ($rpc) {
    $response = Invoke-WebRequest -Uri "$base/rpc/$path" -Method Post -Headers $headers -Body "{}" -ContentType "application/json" -UseBasicParsing
  } else {
    $response = Invoke-WebRequest -Uri "$base$path" -Headers $headers -UseBasicParsing
  }
  if ($response.StatusCode -ne 200) { throw "$name returned $($response.StatusCode)" }
  Write-Host "ok  $name"
  return $response.Content
}

Check "health" "/healthz" | Out-Null
Check "get_state" "get_state" $true | Out-Null
Check "list_workflows" "list_workflows" $true | Out-Null
Check "usage_summary" "usage_summary" $true | Out-Null

# One SSE frame proves the event stream is wired. A short timeout keeps the
# script bounded when no turn is running (the server sends keepalives).
try {
  $events = Invoke-WebRequest -Uri "$base/events?token=$token" -TimeoutSec 3 -UseBasicParsing
  if ($events.StatusCode -eq 200) { Write-Host "ok  events" }
} catch {
  $status = $_.Exception.Response.StatusCode.value__
  if ($status -and $status -ne 200) { throw "events returned $status" }
  Write-Host "ok  events (stream closed by timeout as expected)"
}

if (-not $Browser) {
  $candidates = @(
    "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe",
    "$env:ProgramFiles\Google\Chrome\Application\chrome.exe",
    "$env:LOCALAPPDATA\Google\Chrome\Application\chrome.exe"
  )
  $Browser = $candidates | Where-Object { Test-Path $_ } | Select-Object -First 1
}
if ($Browser) {
  $shot = Join-Path $work "screenshot.png"
  & $Browser "--headless=new" "--disable-gpu" "--hide-scrollbars" "--window-size=1440,900" "--screenshot=$shot" $Url 2>$null | Out-Null
  if (Test-Path $shot) { Write-Host "screenshot: $shot" } else { Write-Host "screenshot: browser wrote no file" }
} else {
  Write-Host "screenshot: no Edge/Chrome found; pass -Browser to capture one"
}

Write-Host "workdir: $work"
