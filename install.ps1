# eve-wallets installer for Windows (PowerShell 5.1 and PowerShell 7).
#
#   irm https://raw.githubusercontent.com/escorbuto-petoruti/eve-wallets/main/install.ps1 | iex
#   .\install.ps1
#
# Environment:
#   VERSION                   release tag to install (default: the latest release)
#   INSTALL_DIR               where to put eve-wallets.exe
#                             (default: %LOCALAPPDATA%\eve-wallets\bin)
#   EVE_WALLETS_RELEASE_BASE  releases URL (default: the GitHub releases page);
#                             <base>/latest redirects to <base>/tag/<tag> and
#                             assets live under <base>/download/<tag>/
#   EVE_WALLETS_TEST_ARCH     test only: pretend the machine architecture is this
#   EVE_WALLETS_TEST_FAIL_REPLACE  test only: make the exe replacement fail
#
# The installer never needs administrator rights and never changes your PATH.

function Install-EveWallets {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'

    # Windows PowerShell 5.1 may default to protocols GitHub refuses.
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    } catch { }

    $repoBase = $env:EVE_WALLETS_RELEASE_BASE
    if (-not $repoBase) { $repoBase = 'https://github.com/escorbuto-petoruti/eve-wallets/releases' }
    $repoBase = $repoBase.TrimEnd('/')

    $installDir = $env:INSTALL_DIR
    if (-not $installDir) {
        if ($env:LOCALAPPDATA) {
            $installDir = Join-Path $env:LOCALAPPDATA 'eve-wallets\bin'
        } else {
            $installDir = Join-Path $HOME 'eve-wallets\bin'
        }
    }

    # Architecture: only amd64 has a Windows release asset.
    $archRaw = $env:EVE_WALLETS_TEST_ARCH
    if (-not $archRaw) {
        try { $archRaw = [string][System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture } catch { }
    }
    if (-not $archRaw) {
        $archRaw = $env:PROCESSOR_ARCHITEW6432
        if (-not $archRaw) { $archRaw = $env:PROCESSOR_ARCHITECTURE }
    }
    if ($archRaw -notin @('X64', 'AMD64', 'x64', 'amd64')) {
        throw "unsupported architecture '$archRaw': releases only provide windows amd64. Build from source, see the README."
    }

    $tag = $env:VERSION
    if (-not $tag) {
        Write-Host 'looking up the latest release'
        $final = $null
        try {
            $req = [System.Net.HttpWebRequest]::Create("$repoBase/latest")
            $req.AllowAutoRedirect = $true
            $resp = $req.GetResponse()
            $final = $resp.ResponseUri.AbsoluteUri
            $resp.Close()
        } catch {
            throw "could not look up the latest release at $repoBase/latest ($($_.Exception.Message))"
        }
        $tag = ($final.TrimEnd('/') -split '/')[-1]
        if ($tag -notmatch '^v\d') {
            throw "could not tell the latest release from $final (is there a published release?)"
        }
    }
    if ($tag -notmatch '^v\d') { $tag = "v$tag" }
    $version = $tag.Substring(1)
    $archive = "eve-wallets_${version}_windows_amd64.zip"
    $url = "$repoBase/download/$tag"

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("eve-wallets-install-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "downloading $archive ($tag)"
        foreach ($name in @($archive, 'checksums.txt')) {
            try {
                Invoke-WebRequest -UseBasicParsing -Uri "$url/$name" -OutFile (Join-Path $tmp $name)
            } catch {
                throw "download failed: $url/$name ($($_.Exception.Message))"
            }
        }

        $want = $null
        foreach ($line in Get-Content -LiteralPath (Join-Path $tmp 'checksums.txt')) {
            $parts = $line.Trim() -split '\s+', 2
            if ($parts.Count -eq 2 -and ($parts[1] -eq $archive -or $parts[1] -eq "*$archive")) {
                $want = $parts[0].ToLowerInvariant()
                break
            }
        }
        if (-not $want) { throw "checksums.txt has no entry for $archive" }
        $got = (Get-FileHash -LiteralPath (Join-Path $tmp $archive) -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($got -ne $want) {
            throw "checksum mismatch for $archive (expected $want, got $got): nothing was installed"
        }
        Write-Host 'checksum ok'

        Add-Type -AssemblyName System.IO.Compression.FileSystem
        $newExe = Join-Path $tmp 'eve-wallets.exe'
        $zip = [IO.Compression.ZipFile]::OpenRead((Join-Path $tmp $archive))
        try {
            $entry = $zip.Entries | Where-Object { $_.FullName -eq 'eve-wallets.exe' } | Select-Object -First 1
            if (-not $entry) { throw 'the archive does not contain eve-wallets.exe' }
            [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $newExe, $true)
        } finally {
            $zip.Dispose()
        }

        try {
            New-Item -ItemType Directory -Force -Path $installDir | Out-Null
        } catch {
            throw "could not create $installDir ($($_.Exception.Message))"
        }
        $target = Join-Path $installDir 'eve-wallets.exe'
        $staged = Join-Path $installDir ".eve-wallets.new.$PID.exe"
        try {
            Copy-Item -LiteralPath $newExe -Destination $staged -Force
        } catch {
            throw "could not write to $installDir ($($_.Exception.Message))"
        }
        $aside = Join-Path $installDir ".eve-wallets.old.$PID.exe"
        $movedAside = $false
        try {
            try {
                # Renaming the current exe fails fast when it is running/locked,
                # before any existing backup is touched.
                if (Test-Path -LiteralPath $target) {
                    Move-Item -LiteralPath $target -Destination $aside -Force
                    $movedAside = $true
                }
                if ($env:EVE_WALLETS_TEST_FAIL_REPLACE) { throw 'simulated failure' }
                Move-Item -LiteralPath $staged -Destination $target -Force
            } catch {
                $reason = $_.Exception.Message
                if ($movedAside) {
                    try { Move-Item -LiteralPath $aside -Destination $target -Force; $movedAside = $false } catch { }
                }
                throw "could not replace $target ($reason). If eve-wallets is running, stop it (Ctrl+C in its window, or Stop-Process -Name eve-wallets) and run the installer again."
            }
            if ($movedAside) {
                try {
                    Move-Item -LiteralPath $aside -Destination "$target.bak-prev" -Force
                    $movedAside = $false
                } catch {
                    Write-Warning "installed, but could not keep the previous binary as $target.bak-prev ($($_.Exception.Message))"
                }
            }
        } finally {
            if (Test-Path -LiteralPath $staged) { Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue }
            if ($movedAside -and (Test-Path -LiteralPath $aside)) { Remove-Item -LiteralPath $aside -Force -ErrorAction SilentlyContinue }
        }
        Write-Host "installed $target ($version)"
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }

    Write-Host ''
    Write-Host 'Next steps:'
    $onPath = $false
    foreach ($p in ($env:Path -split [IO.Path]::PathSeparator)) {
        if ($p.TrimEnd('\', '/') -eq $installDir.TrimEnd('\', '/')) { $onPath = $true }
    }
    if (-not $onPath) {
        Write-Host "  - add $installDir to your PATH (new terminals pick it up), e.g.:"
        Write-Host "      [Environment]::SetEnvironmentVariable('Path', '$installDir;' + [Environment]::GetEnvironmentVariable('Path', 'User'), 'User')"
    }
    Write-Host '  - run: eve-wallets.exe serve, then open http://localhost:8088 and sign in with EVE SSO'
    Write-Host '  - to update later: run this installer again'
}

# Inside a function and a try/catch so that `irm ... | iex` never closes the
# caller's shell: `exit` is only used when this runs as a script file.
try {
    Install-EveWallets
} catch {
    [Console]::Error.WriteLine("install.ps1: $($_.Exception.Message)")
    $global:LASTEXITCODE = 1
    if ($MyInvocation.MyCommand.Path) { exit 1 }
}
