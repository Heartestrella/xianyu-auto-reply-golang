@echo off
setlocal
cd /d "%~dp0"
if not exist "frontend\dist\index.html" (
  pushd frontend
  call npm ci || exit /b 1
  call npm run build || exit /b 1
  popd
)
if exist "xianyu-go.exe" (
  xianyu-go.exe
) else (
  go run ./cmd/server
)
