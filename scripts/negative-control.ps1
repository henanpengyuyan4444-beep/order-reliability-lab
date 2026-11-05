$ErrorActionPreference='Stop'
. "$PSScriptRoot\env.ps1"
$taskOriginal=Join-Path $taskProjectRoot 'internal\lab\store.go'
$taskBefore=(Get-FileHash -LiteralPath $taskOriginal).Hash
$taskCopy=Join-Path $taskProjectRoot ('.tools\mutation-'+[Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $taskCopy | Out-Null
foreach($taskItem in @('cmd','internal','go.mod','go.sum')) {
    Copy-Item -LiteralPath (Join-Path $taskProjectRoot $taskItem) -Destination $taskCopy -Recurse
}
$taskMutationFile=Join-Path $taskCopy 'internal\lab\store.go'
$taskSource=Get-Content -Raw -LiteralPath $taskMutationFile
$taskAnchor='if o.Amount != amount || o.Scenario != scenario {'
if (-not $taskSource.Contains($taskAnchor)) { throw 'Expected guard changed; review mutation before running' }
$taskSource.Replace($taskAnchor,'if false { // deliberately broken isolated negative control') | Set-Content -Encoding utf8 -LiteralPath $taskMutationFile
Push-Location -LiteralPath $taskCopy
try {
    $taskOutput=go test ./internal/lab -run '^TestKeyConflict$' -v -count=1 2>&1
    $taskExit=$LASTEXITCODE
} finally { Pop-Location }
$taskOutput | Set-Content -Encoding utf8 -LiteralPath (Join-Path $taskProjectRoot 'evidence\negative-control.txt')
if ($taskExit -eq 0 -or -not ($taskOutput -match 'want conflict, got 200')) { throw 'Negative control did not detect the intended defect' }
if ((Get-FileHash -LiteralPath $taskOriginal).Hash -ne $taskBefore) { throw 'Original source unexpectedly changed' }
@{utc=[DateTime]::UtcNow.ToString('o');expected_exit='nonzero';actual_exit=$taskExit;detected='missing idempotency conflict guard';original_unchanged=$true} |
    ConvertTo-Json | Set-Content -Encoding utf8 -LiteralPath 'evidence\negative-control-result.json'
Write-Output 'Negative control detected the deliberate defect; original source unchanged.'
