<#
.SYNOPSIS
    Build and install helper for alpacahurd on Windows — the Makefile equivalent.

.DESCRIPTION
    Pass a target as the first argument (default "all"):
      help        show this list
      workspace   (re)write go.work over the sibling checkouts (pre-release only)
      gen         regenerate drivers_gen.go from hurd.conf
      build       build alpacahurd.exe
      all         gen + build (default)
      test        run the test suite
      install     install the binary + config and register a startup task (admin)
      uninstall   remove the task and firewall rule (admin; config is kept)
      clean       remove the built binary

.EXAMPLE
    .\make.ps1 workspace
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

# Library sibling checkouts the workspace overlays. The driver modules are added
# recursively from ..\goalpaca-devices, so only the libraries are listed here.
# Keep in sync with the Makefile's WS_DIRS.
$Libs = @(
    "..\goalpaca", "..\lx200", "..\goindi", "..\astrocam", "..\goasi",
    "..\oasis-astro", "..\optec", "..\pegasus-astro", "..\astromi.ch", "..\unihedron"
)

# run a native command and fail the script on a non-zero exit (make-style).
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
alpacahurd make.ps1 targets:
  help        show this list
  workspace   (re)write go.work over the sibling checkouts (pre-release only)
  gen         regenerate drivers_gen.go from hurd.conf
  build       build $Bin
  all         gen + build (default)
  test        run the test suite
  install     install binary + config and register a startup task (admin)
  uninstall   remove the task and firewall rule (admin; config kept)
  clean       remove the built binary

Pre-release: run '.\make.ps1 workspace' once, with the sibling repos checked out
next to this one. Once the modules are published this step is unnecessary and a
plain 'go build' resolves everything from the module proxy.
"@ | Write-Host
}

function Target-Workspace {
    Remove-Item -Force -ErrorAction SilentlyContinue go.work, go.work.sum
    Invoke-Native go @("work", "init", ".")
    $devices = "..\goalpaca-devices"
    if (Test-Path $devices) {
        Invoke-Native go @("work", "use", "-r", $devices)   # every driver module under it
    } else {
        Write-Warning "missing (skipped): $devices - clone it next to alpacahurd"
    }
    foreach ($d in $Libs) {
        if (Test-Path $d) { Invoke-Native go @("work", "use", $d) }
        else { Write-Warning "missing (skipped): $d - clone it next to alpacahurd" }
    }
    Write-Host "go.work written over the present siblings"
}

function Target-Gen  { Invoke-Native go @("run", ".\internal\gendrivers") }

function Target-Build {
    # The Windows transports are pure Go; no C toolchain required.
    $env:CGO_ENABLED = "0"
    Invoke-Native go @("build", "-o", $Bin, ".")
    Write-Host "built .\$Bin"
}

function Target-All  { Target-Gen; Target-Build }

function Target-Test { Invoke-Native go @("test", "./...") }

function Target-Clean { Remove-Item -Force -ErrorAction SilentlyContinue $Bin }

function Target-Install {
    Assert-Admin
    if (-not (Test-Path $Bin)) { Target-All }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -Force $Bin $ExeDst
    Write-Host "installed binary -> $ExeDst"

    if (Test-Path $Config) {
        Write-Host "keeping existing config $Config"
    } else {
        # Seed a starter config from the binary itself (every compiled-in driver,
        # disabled). WriteAllText emits UTF-8 with no BOM, which the JSON loader needs.
        $example = (& $ExeDst -example | Out-String)
        [System.IO.File]::WriteAllText($Config, $example)
        Write-Host "installed starter config -> $Config   *** EDIT THIS for your hardware ***"
    }

    # Validate the config before registering the task (the ExecStartPre equivalent).
    Invoke-Native $ExeDst @("-check", "-config", $Config)

    # Startup task as SYSTEM, restart on failure, no run-time limit: the Windows
    # analogue of the systemd service / launchd daemon.
    $action    = New-ScheduledTaskAction -Execute $ExeDst -Argument "-config `"$Config`""
    $trigger   = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -LogonType ServiceAccount -RunLevel Highest
    $settings  = New-ScheduledTaskSettingsSet -StartWhenAvailable `
                    -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
                    -ExecutionTimeLimit ([TimeSpan]::Zero)
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
        -Principal $principal -Settings $settings -Force | Out-Null
    Write-Host "registered startup task '$TaskName'"

    # Allow the binary through the firewall (covers every Alpaca port + UDP 32227
    # discovery), so a rule per port is unnecessary.
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
        "workspace" { Target-Workspace }
        "gen"       { Target-Gen }
        "build"     { Target-Build }
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
