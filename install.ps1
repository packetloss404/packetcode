[CmdletBinding()]
param(
    [string]$Version = "latest",
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA "Programs\PacketCode\bin"),

    # Abort unless the Sigstore signature on checksums.txt verifies. Off by
    # default only because cosign is rarely installed on Windows and releases
    # before v0.6.0 are unsigned; turn it on for anything unattended.
    [switch]$RequireSignature
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$repository = "packetloss404/packetcode"
$binary = "packetcode.exe"
$architecture = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()) {
    "x64" { "amd64" }
    "arm64" { "arm64" }
    default { throw "packetcode: unsupported Windows architecture: $_" }
}

if ($Version -eq "latest") {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repository/releases/latest"
    $Version = [string]$release.tag_name
}
if ([string]::IsNullOrWhiteSpace($Version)) {
    throw "packetcode: could not determine a release version"
}

$archive = "packetcode-windows-$architecture.zip"
$releaseBase = "https://github.com/$repository/releases/download/$Version"
$temporaryRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("packetcode-install-" + [guid]::NewGuid().ToString("N"))

try {
    New-Item -ItemType Directory -Path $temporaryRoot | Out-Null
    $archivePath = Join-Path $temporaryRoot $archive
    $checksumsPath = Join-Path $temporaryRoot "checksums.txt"

    Write-Host "Downloading packetcode $Version for windows/$architecture..."
    Invoke-WebRequest -Uri "$releaseBase/$archive" -OutFile $archivePath
    Invoke-WebRequest -Uri "$releaseBase/checksums.txt" -OutFile $checksumsPath

    # Check the checksum file before believing what it says.
    #
    # Matching the archive against checksums.txt proves the download was not
    # corrupted. It proves nothing about substitution: whoever could serve a
    # modified archive could serve the checksums.txt that matches it, and this
    # script would confirm the forgery. The signature is what ends the chain
    # somewhere an attacker does not control.
    $cosign = Get-Command cosign -ErrorAction SilentlyContinue
    if (-not $cosign) {
        if ($RequireSignature) {
            throw "packetcode: -RequireSignature was given but cosign is not installed (https://docs.sigstore.dev/cosign/installation/)"
        }
        Write-Host "  note: cosign is not installed, so the checksum file's signature was not checked."
        Write-Host "        Install cosign and re-run with -RequireSignature to verify properly."
    }
    else {
        $signaturePath = Join-Path $temporaryRoot "checksums.txt.sig"
        $certificatePath = Join-Path $temporaryRoot "checksums.txt.pem"
        $haveSignature = $true
        foreach ($pair in @(@("$releaseBase/checksums.txt.sig", $signaturePath),
                            @("$releaseBase/checksums.txt.pem", $certificatePath))) {
            try {
                Invoke-WebRequest -Uri $pair[0] -OutFile $pair[1] -ErrorAction Stop
            }
            catch {
                $haveSignature = $false
                break
            }
        }

        if (-not $haveSignature) {
            if ($RequireSignature) {
                throw "packetcode: -RequireSignature was given but $Version publishes no signature"
            }
            Write-Host "  note: $Version has no signature (releases before v0.6.0 are unsigned)."
        }
        else {
            $identity = "^https://github.com/$([regex]::Escape($repository))/\.github/workflows/release\.yml@refs/tags/"
            & $cosign.Source verify-blob $checksumsPath `
                --signature $signaturePath `
                --certificate $certificatePath `
                --certificate-identity-regexp $identity `
                --certificate-oidc-issuer "https://token.actions.githubusercontent.com" 2>&1 | Out-Null
            if ($LASTEXITCODE -ne 0) {
                # Always fatal. A signature that is present and does not verify
                # is a failed guarantee, not a missing one, and must never be
                # softened into the "unsigned release" path above.
                throw "packetcode: the signature on checksums.txt did NOT verify. Refusing to install - this is what a substituted release looks like."
            }
            Write-Host "  signature verified: checksums.txt was produced by $repository's release workflow."
        }
    }

    $checksumLine = Get-Content -LiteralPath $checksumsPath |
        Where-Object { $_ -match "^\s*([0-9a-fA-F]{64})\s+\*?$([regex]::Escape($archive))\s*$" } |
        Select-Object -First 1
    if (-not $checksumLine) {
        throw "packetcode: checksum for $archive was not found in checksums.txt"
    }
    $expectedHash = ([regex]::Match($checksumLine, "^\s*([0-9a-fA-F]{64})")).Groups[1].Value.ToLowerInvariant()
    $actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
    if ($actualHash -ne $expectedHash) {
        throw "packetcode: checksum mismatch for $archive (expected $expectedHash, got $actualHash)"
    }

    $expanded = Join-Path $temporaryRoot "expanded"
    Expand-Archive -LiteralPath $archivePath -DestinationPath $expanded
    $source = Join-Path $expanded $binary
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
        throw "packetcode: $binary was not found in $archive"
    }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    $destination = Join-Path $InstallDir $binary
    Copy-Item -LiteralPath $source -Destination $destination -Force

    $installedVersion = & $destination --version
    if ($LASTEXITCODE -ne 0) {
        throw "packetcode: installed binary failed its version probe"
    }
    Write-Host "$installedVersion"
    Write-Host "Installed to $destination"
    if (($env:PATH -split [System.IO.Path]::PathSeparator) -notcontains $InstallDir) {
        Write-Warning "$InstallDir is not on PATH. Add this directory to PATH to run packetcode by name."
    }
}
finally {
    if (Test-Path -LiteralPath $temporaryRoot) {
        Remove-Item -LiteralPath $temporaryRoot -Recurse -Force
    }
}
