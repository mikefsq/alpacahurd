<#
.SYNOPSIS
    Build and install helper for alpacahurd on Windows.

.DESCRIPTION
    Pass a target as the first argument (default "all"). Use "help" to list targets.

.EXAMPLE
    .\make.ps1 deps-head
    .\make.ps1
    .\make.ps1 install

.NOTES
    If the script is blocked by execution policy, run it as:
      powershell -ExecutionPolicy Bypass -File .\make.ps1 <target>
#>
param([string]$Target = "all")

$ErrorActionPreference = "Stop"

$Bin        = "alpacahurd.exe"
$TaskName   = "alpacahurd"
$InstallDir = Join-Path $env:ProgramData "alpacahurd"
$ExeDst     = Join-Path $InstallDir $Bin
$Config     = Join-Path $InstallDir "hurd.json"
$DevicesDir = Join-Path $InstallDir "devices.d"
$StateDir   = Join-Path $InstallDir "state"
$LogDir     = Join-Path $InstallDir "logs"

# Run a native command and fail on a nonzero exit.
function Invoke-Native {
    param([string]$File, [string[]]$Arguments)
    & $File @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$File $($Arguments -join ' ') exited $LASTEXITCODE" }
}

function Assert-Admin {
    $principal = New-Object Security.Principal.WindowsPrincipal(
        [Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)) {
        throw "this target needs an elevated prompt (Run as Administrator)"
    }
}

function Target-Help {
    @"
Usage: .\make.ps1 <target>

  all        Build the orchestrator with simulators (default)
  build      Build alpacahurd.exe; hardware drivers run as separate binaries
  clean      Remove the built binary and dist/
  deps-head  Update mikefsq dependencies to their latest main commits
  fat        Build alpacahurd.exe with the drivers listed in hurd.conf
  gen        Regenerate driver imports from hurd.conf
  help       Show available targets
  install    Install binary, config, startup task, and firewall rule (admin)
  test       Run the Go test suite
  tidy       Regenerate imports, update dependencies to main, and tidy modules
  uninstall  Remove task, firewall rule, and binary; keep config (admin)
"@ | Write-Host
}

function Target-DepsHead {
    $previousGoWork = $env:GOWORK
    try {
        $env:GOWORK = "off"
        $self = & go list -m
        if ($LASTEXITCODE -ne 0) { throw "go list -m exited $LASTEXITCODE" }
        $modules = @(
            [regex]::Matches((Get-Content -Raw go.mod), 'github.com/mikefsq/[a-zA-Z0-9./-]+') |
                ForEach-Object { $_.Value } |
                Where-Object { $_ -ne $self } |
                Sort-Object -Unique
        )
        if ($modules.Count -eq 0) {
            Write-Host "deps-head: no github.com/mikefsq dependencies in go.mod"
            return
        }
        $modules | ForEach-Object { Write-Host "  $_" }
        $getArgs = @("get") + @($modules | ForEach-Object { "${_}@main" })
        Invoke-Native go $getArgs
    } finally {
        $env:GOWORK = $previousGoWork
    }
}

function Target-Gen  { Invoke-Native go @("run", ".\internal\gendrivers") }

function Target-Tidy {
    Target-Gen
    Target-DepsHead
    Invoke-Native go @("mod", "tidy")
    Write-Host "updated dependencies and tidied go.mod/go.sum"
}

function Target-Build {
    $env:CGO_ENABLED = "0"
    Invoke-Native go @("build", "-o", $Bin, ".")
    Write-Host "built .\$Bin (bare: sim drivers only)"
}

function Target-Fat {
    $env:CGO_ENABLED = "0"
    Invoke-Native go @("build", "-tags", "fat", "-o", $Bin, ".")
    Write-Host "built .\$Bin (fat: hurd.conf drivers compiled in)"
}

function Target-All  { Target-Build }

function Target-Test { Invoke-Native go @("test", "./...") }

function Target-Clean {
    Remove-Item -Force -ErrorAction SilentlyContinue $Bin
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue dist
}

function Target-Install {
    Assert-Admin
    if (-not (Test-Path $Bin)) { Target-All }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -Force $Bin $ExeDst
    Write-Host "installed binary -> $ExeDst"

    if (Test-Path $Config) {
        Write-Host "keeping existing config $Config"
    } else {
        $example = (& $ExeDst -example | Out-String)
        # The JSON loader requires UTF-8 without a BOM.
        [System.IO.File]::WriteAllText($Config, $example)
        Write-Host "installed server config -> $Config"
    }
    # Preserve existing device files.
    Invoke-Native $ExeDst @("-example-devices", $DevicesDir)
    Write-Host "device files -> $DevicesDir\   *** EDIT THESE for your hardware ***"
    New-Item -ItemType Directory -Force -Path (Join-Path $StateDir "devices"), $LogDir | Out-Null

    Invoke-Native $ExeDst @("-check", "-config", $Config)

    # ALPACA_SYSTEM_SERVICE selects service paths for the startup task.
    [Environment]::SetEnvironmentVariable("ALPACA_SYSTEM_SERVICE", "true", "Machine")
    $action    = New-ScheduledTaskAction -Execute $ExeDst -Argument "-config `"$Config`""
    $trigger   = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -LogonType ServiceAccount -RunLevel Highest
    $settings  = New-ScheduledTaskSettingsSet -StartWhenAvailable `
                    -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
                    -ExecutionTimeLimit ([TimeSpan]::Zero)
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
        -Principal $principal -Settings $settings -Force | Out-Null
    Write-Host "registered startup task '$TaskName'"

    if (-not (Get-NetFirewallRule -DisplayName $TaskName -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule -DisplayName $TaskName -Direction Inbound `
            -Program $ExeDst -Action Allow -Profile Any | Out-Null
        Write-Host "added firewall rule '$TaskName'"
    }

    Start-ScheduledTask -TaskName $TaskName
    Write-Host ""
    Write-Host "done. edit $Config then: Restart-ScheduledTask -TaskName $TaskName"
}

function Target-Uninstall {
    Assert-Admin
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
    Remove-NetFirewallRule -DisplayName $TaskName -ErrorAction SilentlyContinue
    Remove-Item -Force -ErrorAction SilentlyContinue $ExeDst
    Write-Host "removed task, firewall rule, and binary. config kept in $InstallDir"
}

Push-Location $PSScriptRoot
try {
    switch ($Target.ToLower()) {
        "help"      { Target-Help }
        "deps-head" { Target-DepsHead }
        "gen"       { Target-Gen }
        "tidy"      { Target-Tidy }
        "build"     { Target-Build }
        "fat"       { Target-Gen; Target-Fat }
        "all"       { Target-All }
        "test"      { Target-Test }
        "install"   { Target-Install }
        "uninstall" { Target-Uninstall }
        "clean"     { Target-Clean }
        default     { Write-Host "unknown target '$Target'`n"; Target-Help; exit 1 }
    }
} finally {
    Pop-Location
}
