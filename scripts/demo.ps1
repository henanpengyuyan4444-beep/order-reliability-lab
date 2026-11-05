param([int]$Port=8080)
$ErrorActionPreference='Stop'
$taskBase = "http://127.0.0.1:$Port"
$taskTag = [Guid]::NewGuid().ToString('N').Substring(0,8)
foreach ($taskScenario in @('normal','commit_timeout','transient','reject','query_unavailable','malformed')) {
    $taskHeaders = @{'Idempotency-Key'="demo-$taskTag-$taskScenario";'X-Request-ID'="req-$taskTag-$taskScenario"}
    $taskBody = @{amount_minor=1200;scenario=$taskScenario} | ConvertTo-Json
    $taskOrder = Invoke-RestMethod -Uri "$taskBase/orders" -Method Post -Headers $taskHeaders -ContentType 'application/json' -Body $taskBody
    $taskDuplicate = Invoke-RestMethod -Uri "$taskBase/orders" -Method Post -Headers $taskHeaders -ContentType 'application/json' -Body $taskBody
    if ($taskOrder.id -ne $taskDuplicate.id) { throw 'duplicate order ID' }
    $taskObserved = @()
    $taskDeadline = (Get-Date).AddSeconds(20)
    do {
        $taskOrder = Invoke-RestMethod -Uri "$taskBase/orders/$($taskOrder.id)"
        if ($taskOrder.status -notin $taskObserved) { $taskObserved += $taskOrder.status }
        if ($taskOrder.status -in @('succeeded','rejected','needs_review')) { break }
        Start-Sleep -Milliseconds 30
    } while ((Get-Date) -lt $taskDeadline)
    $taskEffects = Invoke-RestMethod -Uri "$taskBase/lab/effects/$($taskOrder.id)"
    [pscustomobject]@{scenario=$taskScenario;id=$taskOrder.id;request_id=$taskOrder.request_id;observed=($taskObserved -join ' -> ');status=$taskOrder.status;attempts=$taskOrder.attempts;effects=$taskEffects.effects;posts=$taskEffects.post_attempts} | ConvertTo-Json -Compress
    $taskExpectedStatus = if ($taskScenario -eq 'reject') {'rejected'} elseif ($taskScenario -eq 'query_unavailable') {'needs_review'} else {'succeeded'}
    $taskExpectedEffects = if ($taskScenario -eq 'reject') {0} else {1}
    if ($taskOrder.status -ne $taskExpectedStatus -or $taskEffects.effects -ne $taskExpectedEffects) { throw "Unexpected outcome for $taskScenario" }
}
