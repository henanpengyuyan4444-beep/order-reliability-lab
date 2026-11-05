param([int]$Workers=4,[int]$Port=8080,[string]$Data='data')
. "$PSScriptRoot\env.ps1"
go run -ldflags '-s -w' ./cmd/lab -workers $Workers -port $Port -data $Data
if ($LASTEXITCODE -ne 0) { throw 'lab exited with an error' }
