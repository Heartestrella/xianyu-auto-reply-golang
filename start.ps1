$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root
if (-not (Test-Path 'frontend/dist/index.html')) {
  Push-Location frontend
  try { npm ci; npm run build } finally { Pop-Location }
}
if (Test-Path 'xianyu-go.exe') {
  & (Join-Path $root 'xianyu-go.exe')
} else {
  go run ./cmd/server
}
