<#
.SYNOPSIS
  srm one-line installer for Windows x64. The Windows analog of install.sh.

.DESCRIPTION
  Run from an elevated PowerShell (Administrator):

      irm https://raw.githubusercontent.com/erlete/srm/stable/install.ps1 | iex

  It downloads the latest published windows-x64 release artifact, verifies its
  sha256, installs it to %ProgramFiles%\srm\srm.exe, puts that directory on the
  machine PATH, and creates %ProgramData%\srm granting the runner service account
  (NETWORK SERVICE) read access so the detached ephemeral supervisor can read
  config.yaml and the GitHub App key without any manual icacls step.

  Override via environment before piping to iex:
    $env:SRM_VERSION     = 'v1.5.0'              # install a specific tag (default: latest)
    $env:SRM_INSTALL_DIR = 'D:\tools\srm'        # install dir (binary goes to <dir>\srm.exe)
    $env:SRM_LOCAL_EXE   = 'C:\path\srm.exe' # install THIS file (skip download + checksum):
                                                 # offline / air-gapped / pre-release testing

  srm manages Windows Services and ACLs, so the installer requires elevation -
  the Windows counterpart of install.sh's sudo. Unlike `curl | sudo bash`, a
  piped PowerShell script cannot self-elevate (there is no script file to relaunch),
  so an unelevated run stops with guidance rather than failing halfway.
#>

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# GitHub serves the API and release assets over TLS 1.2+. Windows PowerShell 5.1
# (the stock shell) still defaults to TLS 1.0, so the one-liner would fail to
# connect without this; PowerShell 7 already negotiates 1.2+, so the OR is a no-op there.
[Net.ServicePointManager]::SecurityProtocol = `
    [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$Repo       = 'erlete/srm'
$Version    = $env:SRM_VERSION
$LocalExe   = $env:SRM_LOCAL_EXE
$InstallDir = if ($env:SRM_INSTALL_DIR) { $env:SRM_INSTALL_DIR } else { Join-Path $env:ProgramFiles 'srm' }
$DataDir    = Join-Path $env:ProgramData 'srm'

function Die($msg) { Write-Error "srm-install: $msg"; exit 1 }
function Info($msg) { Write-Host "srm-install: $msg" }

# Platform guard: the port targets Windows x64 only (mirrors install.sh's OS/arch check).
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    Die "unsupported platform (srm targets Windows x64 only)"
}
$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -ne 'AMD64') {
    Die "unsupported arch '$arch' (srm targets x64). For ARM64, install x64 via emulation or build from source."
}

# Elevation guard. Installing to Program Files, writing the machine PATH, and
# ACL-granting %ProgramData%\srm all require Administrator.
$isAdmin = ([Security.Principal.WindowsPrincipal] `
    [Security.Principal.WindowsIdentity]::GetCurrent()
    ).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Die "must run elevated - open PowerShell as Administrator and re-run, e.g.`n  Start-Process powershell -Verb RunAs"
}

$tmp = Join-Path $env:TEMP ("srm-install-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null

try {
    # --- obtain the verified binary to install ---
    # Two sources: a locally-built exe (SRM_LOCAL_EXE - offline / pre-release testing,
    # installs the file as-is) or the published, checksum-verified release asset.
    $tmpExe = Join-Path $tmp 'srm.exe'

    if ($LocalExe) {
        if (-not (Test-Path $LocalExe)) { Die "SRM_LOCAL_EXE not found: $LocalExe" }
        Info "installing local binary $LocalExe (skipping download + checksum)"
        Copy-Item $LocalExe $tmpExe -Force
    } else {
        # Resolve the version: the latest release tag unless pinned via SRM_VERSION.
        if (-not $Version) {
            Info "resolving the latest release"
            try {
                $rel = Invoke-RestMethod -UseBasicParsing -Headers @{ 'User-Agent' = 'srm-install' } `
                    "https://api.github.com/repos/$Repo/releases/latest"
                $Version = $rel.tag_name
            } catch {
                Die "could not resolve the latest release tag from GitHub: $($_.Exception.Message)"
            }
            if (-not $Version) { Die "could not resolve the latest release tag from GitHub" }
        }

        $asset  = "srm-$Version-windows-x64.exe"
        $base   = "https://github.com/$Repo/releases/download/$Version"
        $tmpSha = Join-Path $tmp 'srm.exe.sha256'

        Info "downloading $asset ($Version)"
        try {
            Invoke-WebRequest -UseBasicParsing "$base/$asset"        -OutFile $tmpExe
            Invoke-WebRequest -UseBasicParsing "$base/$asset.sha256" -OutFile $tmpSha
        } catch {
            Die "download failed: $($_.Exception.Message)"
        }

        # Verify integrity. The .sha256 is `<hash>  <filename>` (sha256sum format), so
        # compare by the hash value rather than relying on the filename. Get-FileHash
        # returns uppercase hex and sha256sum lowercase, hence the case-insensitive compare.
        Info "verifying checksum"
        $want = ((Get-Content $tmpSha -Raw).Trim() -split '\s+')[0]
        if (-not $want) { Die "could not read the published checksum" }
        $have = (Get-FileHash $tmpExe -Algorithm SHA256).Hash
        if ($have -ine $want) { Die "checksum mismatch (want $want, got $have) - aborting" }
    }

    # --- install ---
    # Stage the verified binary, then swap it in by rename. An in-place overwrite
    # would fail if a running process holds the exe (an ephemeral supervisor service
    # runs <InstallDir>\srm.exe). Windows allows RENAMING a running executable (only
    # deletion/overwrite is blocked), so renaming the old binary aside leaves any
    # running service on the old file and drops the new one cleanly into place - the
    # Windows counterpart of install.sh's atomic mv.
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    $target = Join-Path $InstallDir 'srm.exe'
    $prev   = Join-Path $InstallDir 'srm.prev.exe'
    if (Test-Path $target) {
        Remove-Item $prev -Force -ErrorAction SilentlyContinue
        try {
            Move-Item $target $prev -Force   # keeps a running service on the old inode
        } catch {
            Info "could not back up the existing binary (likely in use); overwriting in place"
        }
    }
    Copy-Item $tmpExe $target -Force

    # Machine-wide data dir: config.yaml, secrets.age, the App key, the agent cache,
    # and ephemeral control files live here (the analog of /etc/srm + /var/lib/srm).
    # Grant the runner service account (NETWORK SERVICE, well-known SID S-1-5-20)
    # inheritable Read+Execute so the DETACHED ephemeral supervisor service - which
    # runs as that low-privilege account with no console - can read config.yaml and
    # the GitHub App private key on its own. This is the production replacement for
    # the manual icacls grants used during live testing. The grant is read-only:
    # the agent-tarball cache stays admin-write-only (its integrity boundary is
    # WRITE, which this does not confer), and the per-slot control dirs get their
    # own Modify grant at `runners ephemeral` create time.
    Info "creating $DataDir"
    New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
    & icacls $DataDir /grant '*S-1-5-20:(OI)(CI)RX' /T /C /Q | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Info "warning: icacls grant on $DataDir returned $LASTEXITCODE - the ephemeral supervisor may need a manual grant"
    }

    # Put the install dir on the machine PATH so `srm` runs from any shell. Exact
    # entry match (not a substring) so re-running the installer stays idempotent.
    $machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
    $entries = @($machinePath -split ';' | Where-Object { $_ -ne '' })
    if ($entries -notcontains $InstallDir) {
        Info "adding $InstallDir to the machine PATH"
        [Environment]::SetEnvironmentVariable('Path', ($machinePath.TrimEnd(';') + ';' + $InstallDir), 'Machine')
    }
    # Make `srm` resolve in THIS session too (the machine PATH change only reaches
    # new shells); harmless if already present.
    if (@($env:Path -split ';') -notcontains $InstallDir) {
        $env:Path = $env:Path.TrimEnd(';') + ';' + $InstallDir
    }

    $installed = (& $target version) 2>$null
    if (-not $installed) { $installed = 'srm' }
    Info "installed $installed to $target"
    Write-Host "next: run 'srm' (first run guides setup), or 'srm init'."
    Write-Host "      open a NEW terminal if 'srm' is not yet found on PATH."
}
finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
