$ErrorActionPreference='Stop'
. "$PSScriptRoot\env.ps1"
$taskExe=Join-Path $taskProjectRoot 'bin\lab.exe'
if (-not (Test-Path -LiteralPath $taskExe)) { throw 'Run verify.ps1 first' }
$taskPort=18089
$taskBase="http://127.0.0.1:$taskPort"
$taskData=Join-Path $taskProjectRoot ('.tools\smoke-'+[Guid]::NewGuid().ToString('N'))
$taskProcess=$null
function Start-LabProcess([int]$Count,[string]$LogName) {
    $taskRunning=Start-Process -FilePath $taskExe -ArgumentList @('-port',"$taskPort",'-workers',"$Count",'-data',$taskData) -WorkingDirectory $taskProjectRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $taskProjectRoot "evidence\$LogName.jsonl") -RedirectStandardError (Join-Path $taskProjectRoot "evidence\$LogName.stderr.txt")
    $taskReady=$false
    for($taskPoll=0;$taskPoll -lt 50;$taskPoll++) {
        $taskRunning.Refresh()
        if ($taskRunning.HasExited) { throw 'Lab failed to start; check port conflict and evidence logs' }
        try { $null=Invoke-RestMethod "$taskBase/health"; $taskReady=$true; break } catch { Start-Sleep -Milliseconds 100 }
    }
    if (-not $taskReady) { Stop-Process -Id $taskRunning.Id; throw 'Lab health check timed out' }
    return $taskRunning
}
try {
    $taskProcess=Start-LabProcess 0 'smoke-paused'
    $taskHeaders=@{'Idempotency-Key'='smoke-restart';'X-Request-ID'='req-smoke-restart'}
    $taskOrder=Invoke-RestMethod "$taskBase/orders" -Method Post -Headers $taskHeaders -ContentType 'application/json' -Body '{"amount_minor":1200}'
    if ($taskOrder.status -ne 'pending') { throw 'Worker pause failed' }
    Stop-Process -Id $taskProcess.Id
    $taskProcess.WaitForExit()
    $taskProcess=Start-LabProcess 4 'smoke-service'
    $taskDeadline=(Get-Date).AddSeconds(5)
    do {
        $taskOrder=Invoke-RestMethod "$taskBase/orders/$($taskOrder.id)"
        if ($taskOrder.status -eq 'succeeded') { break }
        Start-Sleep -Milliseconds 30
    } while((Get-Date) -lt $taskDeadline)
    $taskEffects=Invoke-RestMethod "$taskBase/lab/effects/$($taskOrder.id)"
    if ($taskOrder.status -ne 'succeeded' -or $taskEffects.effects -ne 1) { throw 'Process restart did not recover pending order' }
    @{scenario='process_kill_then_restart';status=$taskOrder.status;effects=$taskEffects.effects;posts=$taskEffects.post_attempts;request_id=$taskOrder.request_id} |
        ConvertTo-Json -Compress | Set-Content -Encoding utf8 'evidence\smoke-results.jsonl'
    & "$PSScriptRoot\demo.ps1" -Port $taskPort | Tee-Object -FilePath 'evidence\smoke-results.jsonl' -Append
    Write-Output 'Compiled executable smoke and process restart passed.'
} finally {
    if ($taskProcess) {
        $taskProcess.Refresh()
        if (-not $taskProcess.HasExited) { Stop-Process -Id $taskProcess.Id; $taskProcess.WaitForExit() }
    }
}
