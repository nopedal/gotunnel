# tunnel - local test script for Windows
# Run from the gotunnel folder in PowerShell: .\test-local.ps1

$serverExe = "$PSScriptRoot\bin\tunnel-server.exe"
$clientExe = "$PSScriptRoot\bin\tunnel.exe"
$devConfig  = "$PSScriptRoot\server-dev.yaml"

if (-not (Test-Path $serverExe)) { Write-Error "tunnel-server.exe not found in bin\"; exit 1 }
if (-not (Test-Path $clientExe)) { Write-Error "tunnel.exe not found in bin\"; exit 1 }
if (-not (Test-Path $devConfig))  { Write-Error "server-dev.yaml not found"; exit 1 }

Write-Host ""
Write-Host "  tunnel local test" -ForegroundColor Cyan
Write-Host "  ------------------------------------------" -ForegroundColor DarkGray
Write-Host ""

# 1. start tunnel-server

Write-Host "  [1/3] starting tunnel-server (:7000 control, :8080 http)..." -ForegroundColor Yellow
$serverLog  = "$env:TEMP\tunnel-server.log"
$serverProc = Start-Process -FilePath $serverExe -ArgumentList "--config", $devConfig `
    -PassThru -NoNewWindow -RedirectStandardError $serverLog
Start-Sleep -Seconds 1
if ($serverProc.HasExited) {
    Write-Host "  server crashed:" -ForegroundColor Red
    Get-Content $serverLog
    exit 1
}
Write-Host "  server running  (PID $($serverProc.Id))" -ForegroundColor Green

# 2. start a local HTTP service

Write-Host "  [2/3] starting local service on port 3000..." -ForegroundColor Yellow
$localLog  = "$env:TEMP\local-http.log"
$hasPython = ($null -ne (Get-Command python -ErrorAction SilentlyContinue)) -or
             ($null -ne (Get-Command python3 -ErrorAction SilentlyContinue))
$localProc = $null
$localJob  = $null

if ($hasPython) {
    $py = if (Get-Command python3 -ErrorAction SilentlyContinue) { "python3" } else { "python" }
    $localProc = Start-Process -FilePath $py -ArgumentList "-m", "http.server", "3000" `
        -PassThru -NoNewWindow -RedirectStandardError $localLog
    Write-Host "  python http.server running (PID $($localProc.Id))" -ForegroundColor Green
} else {
    Write-Host "  python not found - using PowerShell listener" -ForegroundColor DarkYellow
    $localJob = Start-Job -ScriptBlock {
        $l = [System.Net.HttpListener]::new()
        $l.Prefixes.Add("http://+:3000/")
        $l.Start()
        while ($l.IsListening) {
            $ctx  = $l.GetContext()
            $body = [System.Text.Encoding]::UTF8.GetBytes("Hello from localhost:3000 - tunnel is working!")
            $ctx.Response.ContentLength64 = $body.Length
            $ctx.Response.OutputStream.Write($body, 0, $body.Length)
            $ctx.Response.OutputStream.Close()
        }
    }
    Write-Host "  powershell listener running (job $($localJob.Id))" -ForegroundColor Green
}
Start-Sleep -Seconds 1

# 3. connect the client

Write-Host "  [3/3] connecting tunnel client (port 3000)..." -ForegroundColor Yellow
$clientLog  = "$env:TEMP\tunnel-client.log"
$clientProc = Start-Process -FilePath $clientExe `
    -ArgumentList "connect","--server","localhost:7000","--token","devtoken123","--port","3000","--subdomain","demo" `
    -PassThru -NoNewWindow -RedirectStandardError $clientLog
Start-Sleep -Seconds 2

if ($clientProc.HasExited) {
    Write-Host "  client crashed:" -ForegroundColor Red
    Get-Content $clientLog
    Stop-Process -Id $serverProc.Id -Force -ErrorAction SilentlyContinue
    exit 1
}
Write-Host "  client running  (PID $($clientProc.Id))" -ForegroundColor Green
Write-Host ""

# fire test requests

Write-Host "  -- results ----------------------------------------" -ForegroundColor Cyan
Write-Host ""

$tests = @(
    @{ label = "Server health check    "; url = "http://localhost:8080/health";      host = "localhost"      },
    @{ label = "Active tunnels API     "; url = "http://localhost:8080/api/tunnels"; host = "localhost"      },
    @{ label = "Request through tunnel "; url = "http://localhost:8080/";            host = "demo.localhost"  },
    @{ label = "Unknown subdomain      "; url = "http://localhost:8080/";            host = "ghost.localhost" }
)

foreach ($t in $tests) {
    try {
        $result  = Invoke-WebRequest -Uri $t.url -Headers @{ Host = $t.host } -UseBasicParsing -TimeoutSec 5
        $code    = $result.StatusCode
        $color   = if ($code -lt 400) { "Green" } else { "Yellow" }
        $preview = ($result.Content -replace '\s+', ' ').Trim()
        if ($preview.Length -gt 80) { $preview = $preview.Substring(0, 80) + "..." }
        Write-Host ("  {0} -> {1}" -f $t.label, $code) -ForegroundColor $color
        Write-Host ("    {0}" -f $preview) -ForegroundColor DarkGray
    } catch {
        Write-Host ("  {0} -> ERROR: {1}" -f $t.label, $_.Exception.Message) -ForegroundColor Red
    }
    Write-Host ""
}

# pump a few more requests so the live log shows activity
1..3 | ForEach-Object {
    try {
        Invoke-WebRequest "http://localhost:8080/page$_" -Headers @{ Host = "demo.localhost" } `
            -UseBasicParsing -TimeoutSec 3 | Out-Null
    } catch {}
    Start-Sleep -Milliseconds 200
}

Start-Sleep -Milliseconds 500

Write-Host "  -- client live log --------------------------------" -ForegroundColor Cyan
Write-Host ""
if (Test-Path $clientLog) { Get-Content $clientLog -Encoding UTF8 }

Write-Host ""
Write-Host "  -- cleanup ----------------------------------------" -ForegroundColor Cyan
Write-Host ""

Stop-Process -Id $clientProc.Id -Force -ErrorAction SilentlyContinue
Stop-Process -Id $serverProc.Id -Force -ErrorAction SilentlyContinue
if ($localProc) { Stop-Process -Id $localProc.Id -Force -ErrorAction SilentlyContinue }
if ($localJob)  { Stop-Job $localJob; Remove-Job $localJob }

Write-Host "  done." -ForegroundColor DarkGray
Write-Host ""
Write-Host "  To run manually, open 3 PowerShell windows:" -ForegroundColor White
Write-Host "    1  .\bin\tunnel-server.exe --config server-dev.yaml" -ForegroundColor DarkGray
Write-Host "    2  python -m http.server 3000" -ForegroundColor DarkGray
Write-Host "    3  .\bin\tunnel.exe connect --server localhost:7000 --token devtoken123 --port 3000" -ForegroundColor DarkGray
Write-Host ""
