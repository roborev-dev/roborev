# roborev installer for Windows
# Usage: powershell -ExecutionPolicy ByPass -c "irm https://roborev.io/install.ps1 | iex"

$ErrorActionPreference = 'Stop'

$repo = 'roborev-dev/roborev'
$binaryName = 'roborev.exe'

function Write-Info($msg) { Write-Host $msg -ForegroundColor Green }
function Write-Warn($msg) { Write-Host $msg -ForegroundColor Yellow }
function Write-Err($msg) { Write-Host $msg -ForegroundColor Red }

function Get-Architecture {
    # Check if running on ARM64
    if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') {
        return 'arm64'
    }

    # Check using .NET for more reliable detection
    try {
        $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
        switch ($arch.ToString()) {
            'X64' { return 'amd64' }
            'X86' { return '386' }
            'Arm64' { return 'arm64' }
            default { return 'amd64' }
        }
    } catch {
        # Fallback for older PowerShell
        if ([System.Environment]::Is64BitOperatingSystem) {
            return 'amd64'
        } else {
            return '386'
        }
    }
}

function Get-LatestVersion {
    $url = "https://api.github.com/repos/$repo/releases/latest"
    try {
        $response = Invoke-RestMethod -Uri $url -UseBasicParsing
        return $response.tag_name
    } catch {
        throw "Failed to fetch latest version: $_"
    }
}

function Get-InstallDir {
    # Use ROBOREV_INSTALL_DIR if set, otherwise default to ~/.roborev/bin
    if ($env:ROBOREV_INSTALL_DIR) {
        return $env:ROBOREV_INSTALL_DIR
    }
    return Join-Path $env:USERPROFILE '.roborev\bin'
}

function Add-ToPath($dir) {
    $currentPath = [Environment]::GetEnvironmentVariable('Path', 'User')

    if ($currentPath -split ';' | Where-Object { $_ -eq $dir }) {
        Write-Info "Directory already in PATH"
        return $false
    }

    $newPath = "$currentPath;$dir"
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')

    # Also update current session
    $env:Path = "$env:Path;$dir"

    return $true
}

function Install-Roborev {
    Write-Info "Installing roborev..."
    Write-Host ""

    $arch = Get-Architecture
    Write-Info "Platform: windows/$arch"

    # Currently only amd64 is built for Windows
    if ($arch -ne 'amd64') {
        Write-Warn "Note: Only amd64 binaries are currently available."
        Write-Warn "Attempting to use amd64 binary (may work via emulation on ARM64)."
        $arch = 'amd64'
    }

    $version = Get-LatestVersion
    Write-Info "Latest version: $version"

    $versionNum = $version.TrimStart('v')
    $archiveName = "roborev_${versionNum}_windows_${arch}.tar.gz"
    $downloadUrl = "https://github.com/$repo/releases/download/$version/$archiveName"

    $installDir = Get-InstallDir
    Write-Info "Install directory: $installDir"
    Write-Host ""

    # Create install directory
    if (-not (Test-Path $installDir)) {
        New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    }

    # Create temp directory
    $tmpDir = Join-Path $env:TEMP "roborev-install-$(Get-Random)"
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

    try {
        $archivePath = Join-Path $tmpDir $archiveName

        Write-Info "Downloading $archiveName..."
        Invoke-WebRequest -Uri $downloadUrl -OutFile $archivePath -UseBasicParsing

        Write-Info "Extracting..."
        # Windows 10+ has tar built-in
        tar -xzf $archivePath -C $tmpDir

        # Move binary to install dir
        $binaryPath = Join-Path $tmpDir $binaryName
        $destPath = Join-Path $installDir $binaryName

        # Remove existing binary if present
        if (Test-Path $destPath) {
            Remove-Item $destPath -Force
        }

        Move-Item $binaryPath $destPath -Force

        Write-Host ""
        Write-Info "Installation complete!"
        Write-Host ""

        # Add to PATH
        if (-not $env:ROBOREV_NO_MODIFY_PATH) {
            $pathUpdated = Add-ToPath $installDir
            if ($pathUpdated) {
                Write-Info "Added $installDir to PATH"
                Write-Warn "Restart your terminal for PATH changes to take effect."
                Write-Host ""
            }
        }

        # Verify installation
        Write-Host "Get started:"
        Write-Host "  cd your-repo"
        Write-Host "  roborev init"
        Write-Host ""
        Write-Host "For AI agent skills (Claude Code, Codex):"
        Write-Host "  roborev skills install"

    } finally {
        # Cleanup temp directory
        if (Test-Path $tmpDir) {
            Remove-Item $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

# Run installer
Install-Roborev
