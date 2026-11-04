param([switch]$TestOnly, [string]$GoExecutable='')
$ErrorActionPreference='Stop'
$savedEnvironment=@{}
foreach($key in @('GOPATH','GOCACHE','GOTOOLCHAIN','CGO_ENABLED','GOOS','GOARCH','GOAMD64')) { $savedEnvironment[$key]=[Environment]::GetEnvironmentVariable($key,'Process') }
$project=Split-Path -Parent $MyInvocation.MyCommand.Path
$sdk=Join-Path (Split-Path (Split-Path $project -Parent) -Parent) '.tools'
$go=$GoExecutable
if (!$go) {
    $installedGo=Get-Command go -ErrorAction SilentlyContinue
    if ($installedGo) { $go=$installedGo.Source }
    else {
        $go=Join-Path $sdk 'go\bin\go.exe'
        if (!(Test-Path -LiteralPath $go)) { throw 'Go SDK not found. Install Go or pass -GoExecutable.' }
        $env:GOPATH=Join-Path $sdk 'gopath'
        $env:GOCACHE=Join-Path $sdk 'gocache'
    }
}
$env:GOTOOLCHAIN='local'
$env:CGO_ENABLED='0'
Push-Location $project
try {
    $env:GOOS='windows'; $env:GOARCH='amd64'; $env:GOAMD64='v1'
    & $go test ./...
    if($LASTEXITCODE -ne 0){throw 'Tests failed'}
    if(!$TestOnly){
        & $go build -trimpath -o dist/claude-guard-monitor.exe .
        if($LASTEXITCODE -ne 0){throw 'Windows build failed'}
        $env:GOOS='linux'
        & $go build -trimpath -o dist/claude-guard-monitor-linux-amd64 .
        if($LASTEXITCODE -ne 0){throw 'Linux build failed'}
        Get-FileHash dist/claude-guard-monitor-linux-amd64 -Algorithm SHA256
    }
} finally {
    Pop-Location
    foreach($key in $savedEnvironment.Keys) { [Environment]::SetEnvironmentVariable($key,$savedEnvironment[$key],'Process') }
}
