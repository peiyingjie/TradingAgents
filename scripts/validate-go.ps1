param([string]$Compiler)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$previousCC = $env:CC
$previousPath = $env:PATH
Push-Location $repo
try {
    if (-not $Compiler) {
        $localCompiler = Join-Path (Split-Path -Parent $repo) '.go-toolchain/llvm-mingw-20260922-ucrt-x86_64/bin/clang.exe'
        if (Test-Path -LiteralPath $localCompiler) { $Compiler = $localCompiler }
    }
    if ($Compiler) {
        $env:CC = (Resolve-Path -LiteralPath $Compiler).Path
        $env:PATH = (Split-Path -Parent $env:CC) + [IO.Path]::PathSeparator + $env:PATH
    }
    & go version
    & go env CC CGO_ENABLED
    foreach ($check in @(
        @{Name='build'; Arguments=@('build', './...')},
        @{Name='test'; Arguments=@('test', './...')},
        @{Name='race'; Arguments=@('test', '-race', './...')},
        @{Name='vet'; Arguments=@('vet', './...')}
    )) {
        Write-Host ('Running Go ' + $check.Name)
        & go @($check.Arguments)
        if ($LASTEXITCODE -ne 0) { throw ('Go ' + $check.Name + ' failed: ' + $LASTEXITCODE) }
    }
    & go run ./cmd/tradingagents --help
    if ($LASTEXITCODE -ne 0) { throw 'CLI help failed' }
    & go run ./cmd/tradingagents backtest --help
    if ($LASTEXITCODE -ne 0) { throw 'Backtest help failed' }
} finally {
    $env:CC = $previousCC
    $env:PATH = $previousPath
    Pop-Location
}
