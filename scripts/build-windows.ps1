#requires -Version 5.1
[CmdletBinding()]
param([switch]$WithNative)
$ErrorActionPreference='Stop'
Set-Location (Join-Path $PSScriptRoot '..')
& go test -count=1 ./...
if ($LASTEXITCODE -ne 0) { throw 'Go tests failed' }
& go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'Go vet failed' }
New-Item -ItemType Directory -Force -Path 'dist\windows-amd64' | Out-Null
$env:CGO_ENABLED='0';$env:GOOS='windows';$env:GOARCH='amd64'
foreach ($Name in @('remote-server','remote-agent')) {
    & go build -trimpath '-ldflags=-s -w -buildid=' -o "dist/windows-amd64/$Name.exe" "./cmd/$Name"
    if ($LASTEXITCODE -ne 0) { throw "Build failed: $Name" }
}
if ($WithNative) {
    & cmake -S native -B build/native -A x64
    if ($LASTEXITCODE -ne 0) { throw 'Native configuration failed. Install the Windows SDK and MSVC toolchain.' }
    & cmake --build build/native --config Release
    if ($LASTEXITCODE -ne 0) { throw 'Native build failed' }
    & ctest --test-dir build/native -C Release --output-on-failure
    if ($LASTEXITCODE -ne 0) { throw 'Native tests failed' }
}
