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
#   EVE_WALLETS_ADD_TO_PATH   1 adds the install dir to your user PATH without
#                             asking; 0 never asks nor adds. Unset: ask [y/N]
#                             (default No) in an interactive terminal, and
#                             change nothing otherwise (for example `irm | iex`
#                             automation).
#   EVE_WALLETS_ADD_SHORTCUT  1 creates the Desktop and Start menu shortcuts
#                             (named eve-wallets) without asking; 0 never asks
#                             nor creates. Unset: ask [y/N] (default No) in an
#                             interactive terminal, and create nothing
#                             otherwise. Running the installer again replaces
#                             them.
#   EVE_WALLETS_TEST_ARCH     test only: pretend the machine architecture is this
#   EVE_WALLETS_TEST_FAIL_REPLACE  test only: make the exe replacement fail
#   EVE_WALLETS_TEST_PATH_FILE     test only: a file that stands in for the user
#                             PATH value (read and written instead of the
#                             user PATH registry value; Linux has no registry)
#   EVE_WALLETS_TEST_SHORTCUT_DIR  test only: a directory where, instead of
#                             creating .lnk files (Linux cannot), the installer
#                             writes shortcuts.txt with one line per shortcut:
#                             "<folder-kind> <target> <workdir>"
#
# The installer never needs administrator rights. It only ever touches your
# user PATH (never the machine PATH), only when you opt in, and it only appends
# its own directory: other entries and the value type are left as they were.

# The real user PATH lives in HKCU\Environment. It is read and written through the registry
# so %VAR% references stay unexpanded and the value type (usually REG_EXPAND_SZ) is kept:
# [Environment]::Get/SetEnvironmentVariable would expand every entry and write plain text.
function Test-EveWindows { return ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) }

function Get-EveUserPath {
    if ($env:EVE_WALLETS_TEST_PATH_FILE) {
        if (Test-Path -LiteralPath $env:EVE_WALLETS_TEST_PATH_FILE) {
            return [string](Get-Content -Raw -LiteralPath $env:EVE_WALLETS_TEST_PATH_FILE)
        }
        return ''
    }
    if (-not (Test-EveWindows)) { return '' }
    $key = $null
    try {
        $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $false)
        if (-not $key) { return '' }
        return [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    } catch {
        throw "could not read your user PATH from the registry ($($_.Exception.Message)); nothing was changed"
    } finally {
        if ($key) { $key.Dispose() }
    }
}

function Set-EveUserPath([string]$value) {
    if ($env:EVE_WALLETS_TEST_PATH_FILE) {
        [IO.File]::WriteAllText($env:EVE_WALLETS_TEST_PATH_FILE, $value)
        return
    }
    if (-not (Test-EveWindows)) { throw 'the user PATH can only be changed on Windows' }
    $key = $null
    try {
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
        $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
        if ($key.GetValueNames() -contains 'Path') {
            $existing = $key.GetValueKind('Path')
            if ($existing -eq [Microsoft.Win32.RegistryValueKind]::String -or $existing -eq [Microsoft.Win32.RegistryValueKind]::ExpandString) {
                $kind = $existing
            }
        }
        $key.SetValue('Path', $value, $kind)
    } catch {
        throw "could not write your user PATH to the registry ($($_.Exception.Message)); it was left unchanged"
    } finally {
        if ($key) { $key.Dispose() }
    }
    # Direct registry writes do not tell running programs. Setting and removing a throwaway
    # user variable makes .NET broadcast WM_SETTINGCHANGE so new terminals see the new PATH.
    try {
        [Environment]::SetEnvironmentVariable('EVE_WALLETS_PATH_REFRESH', '1', 'User')
        [Environment]::SetEnvironmentVariable('EVE_WALLETS_PATH_REFRESH', $null, 'User')
    } catch {
        Write-Host "  (your PATH was saved, but running programs were not notified; sign out and in, or open a new terminal)"
    }
}

function Test-EveInteractive {
    try {
        if (-not [Environment]::UserInteractive) { return $false }
        if ([Console]::IsInputRedirected -or [Console]::IsOutputRedirected) { return $false }
        if ([Environment]::GetCommandLineArgs() -contains '-NonInteractive') { return $false }
        return $true
    } catch {
        return $false
    }
}

# Creates (or replaces) the eve-wallets.lnk shortcuts in the user's Desktop and Start Menu
# Programs folders, resolved with GetFolderPath so redirected folders work. They start the exe
# with no arguments, which runs the server. With EVE_WALLETS_TEST_SHORTCUT_DIR it records the
# intended shortcuts in a text file instead (the whole file is rewritten, so it is idempotent).
function New-EveShortcuts([string]$target, [string]$workDir) {
    if ($env:EVE_WALLETS_TEST_SHORTCUT_DIR) {
        New-Item -ItemType Directory -Force -Path $env:EVE_WALLETS_TEST_SHORTCUT_DIR | Out-Null
        $lines = foreach ($kind in @('Desktop', 'Programs')) { "$kind $target $workDir" }
        [IO.File]::WriteAllText((Join-Path $env:EVE_WALLETS_TEST_SHORTCUT_DIR 'shortcuts.txt'), (($lines -join "`n") + "`n"))
        return
    }
    if (-not (Test-EveWindows)) { throw 'shortcuts can only be created on Windows' }
    $shell = New-Object -ComObject WScript.Shell
    try {
        foreach ($kind in @('Desktop', 'Programs')) {
            $folder = [Environment]::GetFolderPath($kind)
            if (-not $folder) { throw "could not find your $kind folder" }
            $lnk = $shell.CreateShortcut((Join-Path $folder 'eve-wallets.lnk'))
            $lnk.TargetPath = $target
            $lnk.WorkingDirectory = $workDir
            $lnk.Description = 'eve-wallets: EVE Online wallet charts (starts the local server and opens the page)'
            $lnk.Save()
            [void][Runtime.InteropServices.Marshal]::ReleaseComObject($lnk)
        }
    } finally {
        [void][Runtime.InteropServices.Marshal]::ReleaseComObject($shell)
    }
}

# True when $dir is one of the ';'-separated entries of a user PATH value (case-insensitive,
# trailing slashes ignored), compared both as written and with %VAR% references expanded.
function Test-EveDirInPath([string]$dir, [string]$pathValue) {
    $want = $dir.TrimEnd('\', '/')
    foreach ($p in ($pathValue -split ';')) {
        $entry = $p.Trim()
        if ($entry.TrimEnd('\', '/') -ieq $want) { return $true }
        $expanded = [Environment]::ExpandEnvironmentVariables($entry)
        if ($expanded.TrimEnd('\', '/') -ieq $want) { return $true }
    }
    return $false
}

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
    $sessionSep = [IO.Path]::PathSeparator
    $onPath = $false
    foreach ($p in ($env:Path -split [regex]::Escape([string]$sessionSep))) {
        if ($p.TrimEnd('\', '/') -ieq $installDir.TrimEnd('\', '/')) { $onPath = $true }
    }
    $userPath = Get-EveUserPath
    if (Test-EveDirInPath $installDir $userPath) { $onPath = $true }
    $addHint = "to add it, set EVE_WALLETS_ADD_TO_PATH=1 (PowerShell: `$env:EVE_WALLETS_ADD_TO_PATH = '1') and run the installer again, or add the folder in Settings > Environment Variables"
    if ($onPath) {
        Write-Host "  - $installDir is already on your PATH"
    } else {
        $choice = $env:EVE_WALLETS_ADD_TO_PATH
        $add = $false
        if ($choice -eq '1') {
            $add = $true
        } elseif ($choice -ne '0' -and (Test-EveInteractive)) {
            try {
                $answer = Read-Host "Add $installDir to your user PATH? [y/N]"
                $add = ($answer -match '^\s*(y|yes)\s*$')
            } catch { $add = $false }
        }
        if ($add) {
            $trimmed = $userPath.TrimEnd(';')
            if ($trimmed) { Set-EveUserPath "$trimmed;$installDir" } else { Set-EveUserPath $installDir }
            Write-Host "  - added $installDir to your user PATH; open a new terminal to use eve-wallets from anywhere"
        } else {
            Write-Host "  - $installDir was not added to your PATH ($addHint)"
        }
    }

    $lnkHint = "to create them, set EVE_WALLETS_ADD_SHORTCUT=1 (PowerShell: `$env:EVE_WALLETS_ADD_SHORTCUT = '1') and run the installer again"
    $lnkChoice = $env:EVE_WALLETS_ADD_SHORTCUT
    $makeLnk = $false
    if ($lnkChoice -eq '1') {
        $makeLnk = $true
    } elseif ($lnkChoice -ne '0' -and (Test-EveInteractive)) {
        try {
            $answer = Read-Host 'Create shortcuts for eve-wallets on your Desktop and Start menu? [y/N]'
            $makeLnk = ($answer -match '^\s*(y|yes)\s*$')
        } catch { $makeLnk = $false }
    }
    $lnkDone = $false
    if ($makeLnk) {
        try {
            New-EveShortcuts (Join-Path $installDir 'eve-wallets.exe') $installDir
            $lnkDone = $true
            Write-Host '  - created shortcuts for eve-wallets on your Desktop and Start menu; double-click one to start it'
        } catch {
            Write-Host "  - could not create the shortcuts ($($_.Exception.Message)); the install itself is fine"
        }
    } elseif ($lnkChoice -eq '0') {
        Write-Host '  - no shortcuts created (EVE_WALLETS_ADD_SHORTCUT=0)'
    } else {
        Write-Host "  - no shortcuts created ($lnkHint)"
    }
    if ($lnkDone) {
        Write-Host '  - run: double-click the eve-wallets shortcut, or eve-wallets.exe (no arguments starts the server, same as eve-wallets serve); it opens http://localhost:8088, then sign in with EVE SSO'
    } else {
        Write-Host '  - run: double-click eve-wallets.exe (no arguments starts the server, same as eve-wallets serve; from a new terminal once it is on your PATH: eve-wallets serve); it opens http://localhost:8088, then sign in with EVE SSO'
    }
    Write-Host '  - to stop it: close its window, press Ctrl+C, or use the Quit button in the page'
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
