param(
  [string]$BaseUrl = "http://host.docker.internal:8080",
  [string]$Duration = "1m"
)

$levels = @(
  @{ Name = "LOW"; VUsPerRole = 5 },
  @{ Name = "MEDIUM"; VUsPerRole = 25 },
  @{ Name = "HIGH"; VUsPerRole = 50 }
)

$scriptDirectory = (Resolve-Path $PSScriptRoot).Path

foreach ($level in $levels) {
  $totalVUs = $level.VUsPerRole * 3
  Write-Host ""
  Write-Host "===== $($level.Name): $($level.VUsPerRole) VU per role ($totalVUs total) =====" -ForegroundColor Cyan

  docker run --rm `
    --mount "type=bind,source=$scriptDirectory,target=/scripts,readonly" `
    grafana/k6 run `
    -e "BASE_URL=$BaseUrl" `
    -e "VUS_PER_ROLE=$($level.VUsPerRole)" `
    -e "DURATION=$Duration" `
    /scripts/report.js

  if ($LASTEXITCODE -ne 0) {
    throw "Load level $($level.Name) failed or missed a threshold. Stopping before the next level."
  }

  Read-Host "Screenshot this level's k6 summary, then press Enter to continue"
}

Write-Host "All three load levels passed their k6 thresholds." -ForegroundColor Green
