param([string]$Compiler)

# No Python runtime or real service credentials are required. HTTP tests use
# httptest or stub transports; dependency downloads may still need the network.
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$savedEnv = @{}
function Set-TestEnvironment([string]$Name, [string]$Value) {
    if (-not $savedEnv.ContainsKey($Name)) {
        $savedEnv[$Name] = [Environment]::GetEnvironmentVariable($Name, 'Process')
    }
    [Environment]::SetEnvironmentVariable($Name, $Value, 'Process')
}
function Invoke-GoCheck([string]$Name, [string[]]$Arguments) {
    Write-Host "Running $Name"
    $log = Join-Path $outputDir ($Name + '.log')
    & go @Arguments 2>&1 | Out-File -LiteralPath $log -Encoding utf8
    if ($LASTEXITCODE -ne 0) {
        Get-Content -LiteralPath $log -Tail 30
        throw "$Name failed ($LASTEXITCODE); see $log"
    }
}
function Read-TestSummary([string]$Path) {
    $events = @(Get-Content -LiteralPath $Path | ForEach-Object { $_ | ConvertFrom-Json })
    $passes = @($events | Where-Object { $_.Action -eq 'pass' -and $_.Test })
    [ordered]@{
        packagesPassed = @($events | Where-Object { $_.Action -eq 'pass' -and -not $_.Test }).Count
        topLevelTestsPassed = @($passes | Where-Object { $_.Test -notmatch '/' }).Count
        testsAndSubtestsPassed = $passes.Count
        failures = @($events | Where-Object { $_.Action -eq 'fail' }).Count
        testsSkipped = @($events | Where-Object { $_.Action -eq 'skip' -and $_.Test }).Count
        providerRoutes = @($passes | Where-Object { $_.Test -like 'TestSmokeAllRegisteredProviders/*' }).Count
        dataRoutesAndHelpers = @($passes | Where-Object { $_.Test -like 'TestSmokeAllDataVendorRoutes/*' }).Count
    }
}

Push-Location $repo
try {
    $outputDir = Join-Path $repo 'reports/go-audit'
    New-Item -ItemType Directory -Force -Path $outputDir | Out-Null
    @{status='running'} | ConvertTo-Json | Set-Content (Join-Path $outputDir 'summary.json') -Encoding utf8
    # Clear ambient application overrides and replace every known credential.
    foreach ($entry in @(Get-ChildItem Env:)) {
        if ($entry.Name -like 'TRADINGAGENTS_*' -or $entry.Name -like 'AWS_*') {
            Set-TestEnvironment $entry.Name ''
        } elseif ($entry.Name -match 'API_KEY$') {
            Set-TestEnvironment $entry.Name 'offline-test-key'
        }
    }
    $source = Get-Content (Join-Path $repo 'internal/llm/client.go') -Raw
    $keys = @([regex]::Matches($source, '"([A-Z_]+API_KEY)"') | ForEach-Object { $_.Groups[1].Value })
    foreach ($key in $keys + @('GEMINI_API_KEY', 'FRED_API_KEY', 'ALPHA_VANTAGE_API_KEY')) {
        Set-TestEnvironment $key 'offline-test-key'
    }
    Set-TestEnvironment 'AWS_ACCESS_KEY_ID' 'offline-access'
    Set-TestEnvironment 'AWS_SECRET_ACCESS_KEY' 'offline-secret'
    Set-TestEnvironment 'AWS_SESSION_TOKEN' 'offline-session'
    Set-TestEnvironment 'AWS_BEARER_TOKEN_BEDROCK' 'offline-token'
    Set-TestEnvironment 'AWS_EC2_METADATA_DISABLED' 'true'
    Set-TestEnvironment 'AWS_CONFIG_FILE' (Join-Path $outputDir 'unused-aws-config')
    Set-TestEnvironment 'AWS_SHARED_CREDENTIALS_FILE' (Join-Path $outputDir 'unused-aws-credentials')
    if (-not $Compiler) {
        $localCompiler = Join-Path (Split-Path -Parent $repo) '.go-toolchain/llvm-mingw-20260922-ucrt-x86_64/bin/clang.exe'
        if (Test-Path -LiteralPath $localCompiler) { $Compiler = $localCompiler }
    }
    if ($Compiler) {
        $Compiler = (Resolve-Path -LiteralPath $Compiler).Path
        Set-TestEnvironment 'CC' $Compiler
        Set-TestEnvironment 'PATH' ((Split-Path -Parent $Compiler) + [IO.Path]::PathSeparator + $env:PATH)
    }
    Invoke-GoCheck 'build' @('build', './...')
    Invoke-GoCheck 'test' @('test', '-count=1', '-timeout=5m', '-coverprofile=reports/go-audit/coverage.out', '-json', './...')
    Invoke-GoCheck 'race' @('test', '-race', '-count=1', '-timeout=5m', '-json', './...')
    Invoke-GoCheck 'vet' @('vet', './...')
    Invoke-GoCheck 'coverage' @('tool', 'cover', '-func=reports/go-audit/coverage.out')
    New-Item -ItemType Directory -Force -Path (Join-Path $repo 'bin') | Out-Null
    $binary = Join-Path $repo 'bin/tradingagents.exe'
    Invoke-GoCheck 'binary' @('build', '-o', $binary, './cmd/tradingagents')
    & $binary --help | Out-File (Join-Path $outputDir 'cli-help.log') -Encoding utf8
    if ($LASTEXITCODE -ne 0) { throw 'CLI help failed' }
    & $binary backtest --help | Out-File (Join-Path $outputDir 'backtest-help.log') -Encoding utf8
    if ($LASTEXITCODE -ne 0) { throw 'Backtest help failed' }
    $summary = [ordered]@{
        status = 'passed'
        completedAt = (Get-Date).ToString('o')
        goVersion = (& go version)
        credentials = 'dummy values; no .env modified'
        test = (Read-TestSummary (Join-Path $outputDir 'test.log'))
        race = (Read-TestSummary (Join-Path $outputDir 'race.log'))
        coverage = (Get-Content (Join-Path $outputDir 'coverage.log') -Tail 1).Trim()
        checks = @('build', 'uncached tests', 'uncached race', 'vet', 'binary build', 'CLI help', 'backtest help')
    }
    $summary | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $outputDir 'summary.json') -Encoding utf8
    $summary | ConvertTo-Json -Depth 5 | Write-Host
} catch {
    @{status='failed'; message=$_.Exception.Message} | ConvertTo-Json | Set-Content (Join-Path $outputDir 'summary.json') -Encoding utf8
    throw
} finally {
    foreach ($name in $savedEnv.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedEnv[$name], 'Process')
    }
    Pop-Location
}
