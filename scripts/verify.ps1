param([switch]$Race)
. "$PSScriptRoot\env.ps1"
New-Item -ItemType Directory -Force -Path 'evidence' | Out-Null
go version | Tee-Object -FilePath 'evidence\environment.txt'
gcc --version | Select-Object -First 1 | Tee-Object -FilePath 'evidence\environment.txt' -Append
Get-Location | Out-String | Tee-Object -FilePath 'evidence\environment.txt' -Append
$taskGoFiles = Get-ChildItem cmd,internal -Filter '*.go' -Recurse | ForEach-Object FullName
$taskFormat = & gofmt -l $taskGoFiles
if ($taskFormat) { throw ('Run gofmt on: ' + ($taskFormat -join ', ')) }
go mod verify 2>&1 | Tee-Object -FilePath 'evidence\module-verify.txt'
if ($LASTEXITCODE -ne 0) { throw 'go mod verify failed' }
go vet ./... 2>&1 | Tee-Object -FilePath 'evidence\vet.txt'
if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
$taskTestArgs = @('test','-count=1','-v','-cover','./...')
$taskLog = 'evidence\tests.txt'
if ($Race) { $taskTestArgs = @('test','-race','-count=1','-v','./...'); $taskLog = 'evidence\race.txt' }
& go @taskTestArgs 2>&1 | Tee-Object -FilePath $taskLog
if ($LASTEXITCODE -ne 0) { throw 'go test failed; inspect evidence logs' }
New-Item -ItemType Directory -Force -Path 'bin' | Out-Null
go build -trimpath -ldflags '-s -w' -o 'bin\lab.exe' ./cmd/lab
if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
Write-Output 'Verification passed. This validates software, not the learner.'
