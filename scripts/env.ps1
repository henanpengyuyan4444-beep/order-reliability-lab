$ErrorActionPreference = 'Stop'
$taskProjectRoot = Split-Path $PSScriptRoot -Parent
Set-Location -LiteralPath $taskProjectRoot
$taskBundledGo = Join-Path $taskProjectRoot '.tools\go\bin'
if (Test-Path -LiteralPath (Join-Path $taskBundledGo 'go.exe')) {
    $env:PATH = $taskBundledGo + [IO.Path]::PathSeparator + $env:PATH
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'Install Go 1.24+ from https://go.dev/dl/ and a C compiler for github.com/mattn/go-sqlite3.'
}
$env:GOTOOLCHAIN = 'local'
$env:GOPATH = Join-Path $taskProjectRoot '.tools\gopath'
$env:GOCACHE = Join-Path $taskProjectRoot '.tools\gocache'
$env:GOTMPDIR = Join-Path $taskProjectRoot '.tools\tmp'
$env:TEMP = $env:GOTMPDIR
$env:TMP = $env:GOTMPDIR
New-Item -ItemType Directory -Force -Path $env:GOTMPDIR | Out-Null
