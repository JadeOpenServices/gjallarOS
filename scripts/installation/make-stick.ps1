# gjallarOS USB stick maker for Windows (Windows PowerShell 5.1).
#
#   irm https://github.com/JadeOpenServices/gjallarOS/releases/latest/download/make-stick.ps1 | iex
#   powershell -ExecutionPolicy Bypass -File make-stick.ps1 [-Release vYYYY.MM.N]
#
# Downloads the installer stick image, checks its signature against the key
# below, writes it to a USB stick the user picks, reads it back and adds a
# small GJLOGS partition for install logs. Only the write runs with admin
# rights. The elevated code is text from this script, saved next to the
# downloads; the new powershell.exe reads it once into memory and runs it
# only if its SHA-256 matches the hash on its command line. No Add-Type,
# nothing in %TEMP% (loophole L73). The elevated side opens the downloads
# itself and refuses links and pipes (L74), checks again that the disk is the
# stick the user picked (L75) and writes a prebuilt GJLOGS image (L76). Only
# its own success codes count as success: a process that Windows Security or
# anything else ends early never reads as a written stick (L83).
#
# Everything sits in one function that the last line calls, so a cut-off
# download runs nothing. The function never calls exit: under irm | iex
# that would close the user's PowerShell window.

param([string]$Release = '')

function Invoke-GjallarMakeStick {
    param([string]$Release = '')

    $ErrorActionPreference = 'Stop'
    Set-StrictMode -Version 2.0

    $releases = 'https://github.com/JadeOpenServices/gjallarOS/releases'
    # RSA public key for release signatures (RSA-PSS, SHA-256, salt 32), as
    # base64 modulus and exponent: Windows PowerShell 5.1 has no PEM import.
    # Its fingerprint is published apart from the release (Plane 350, Q5).
    $keyModulus = '@GJALLAR_RELEASE_KEY_MODULUS@'
    $keyExponent = '@GJALLAR_RELEASE_KEY_EXPONENT@'
    $imgSize = 33554432
    $isoMax = 2000000000

    # Native calls shared by both sides. P/Invoke is built in memory with
    # Reflection.Emit; Add-Type would compile through files in %TEMP%.
    $native = @'
function New-GjNative {
    $name = 'GjNative' + [Guid]::NewGuid().ToString('N')
    $asm = [AppDomain]::CurrentDomain.DefineDynamicAssembly((New-Object Reflection.AssemblyName $name), 'Run')
    $tb = $asm.DefineDynamicModule($name, $false).DefineType('K32', 'Public, Class')
    $h = [Microsoft.Win32.SafeHandles.SafeFileHandle]
    $b = [byte[]]
    $ri = [int].MakeByRefType()
    $defs = @(
        @('CreateFileW', $h, @([string], [int], [int], [IntPtr], [int], [int], [IntPtr])),
        @('DeviceIoControl', [bool], @($h, [int], $b, [int], $b, [int], $ri, [IntPtr])),
        @('ReadFile', [bool], @($h, $b, [int], $ri, [IntPtr])),
        @('WriteFile', [bool], @($h, $b, [int], $ri, [IntPtr])),
        @('SetFilePointerEx', [bool], @($h, [long], [IntPtr], [int])),
        @('FlushFileBuffers', [bool], @($h)),
        @('GetFileType', [int], @($h)),
        @('GetFileInformationByHandle', [bool], @($h, $b)),
        @('GetFinalPathNameByHandleW', [int], @($h, $b, [int], [int]))
    )
    $dll = [Runtime.InteropServices.DllImportAttribute]
    $fields = [Reflection.FieldInfo[]]@($dll.GetField('SetLastError'), $dll.GetField('CharSet'), $dll.GetField('CallingConvention'))
    foreach ($d in $defs) {
        $m = $tb.DefineMethod($d[0], 'Public, Static, PinvokeImpl', $d[1], [Type[]]$d[2])
        $m.SetImplementationFlags('PreserveSig')
        $m.SetCustomAttribute((New-Object Reflection.Emit.CustomAttributeBuilder($dll.GetConstructor([Type[]]@([string])),
            [object[]]@('kernel32.dll'), $fields,
            [object[]]@($true, [Runtime.InteropServices.CharSet]::Unicode, [Runtime.InteropServices.CallingConvention]::Winapi))))
    }
    $tb.CreateType()
}
$K = New-GjNative

function Get-GjError { [Runtime.InteropServices.Marshal]::GetLastWin32Error() }

# Open a downloaded file for reading without following a link at its last
# name, and refuse anything but one plain file on a disk: no reparse
# point, no hard link, no pipe or device. Others may only read it while
# the handle is open, so it cannot change or grow (L74).
function Open-GjFile([string]$path, [long]$size) {
    $h = $K::CreateFileW($path, -2147483648, 1, [IntPtr]::Zero, 3, 0x08200000, [IntPtr]::Zero)
    if ($h.IsInvalid) { return $null }
    $info = New-Object byte[] 52
    $ok = $K::GetFileType($h) -eq 1 -and $K::GetFileInformationByHandle($h, $info)
    if ($ok) {
        $attr = [BitConverter]::ToUInt32($info, 0)
        $len = ([long][BitConverter]::ToUInt32($info, 32) -shl 32) + [BitConverter]::ToUInt32($info, 36)
        $ok = ($attr -band 0x410) -eq 0 -and [BitConverter]::ToUInt32($info, 40) -eq 1 -and $len -eq $size
    }
    if ($ok) {
        # The whole path as Windows resolved it must be the one we were
        # given, so no folder on the way was a junction.
        $buf = New-Object byte[] 65536
        $n = $K::GetFinalPathNameByHandleW($h, $buf, 32767, 0)
        $ok = $n -gt 0 -and $n -lt 32767 -and [Text.Encoding]::Unicode.GetString($buf, 0, 2 * $n) -ceq $path
    }
    if (-not $ok) { $h.Dispose(); return $null }
    $h
}

function Get-GjFinalPath([string]$path) {
    $h = $K::CreateFileW($path, -2147483648, 1, [IntPtr]::Zero, 3, 0x00200000, [IntPtr]::Zero)
    if ($h.IsInvalid) { return $null }
    try {
        $buf = New-Object byte[] 65536
        $n = $K::GetFinalPathNameByHandleW($h, $buf, 32767, 0)
        if ($n -le 0 -or $n -ge 32767) { return $null }
        [Text.Encoding]::Unicode.GetString($buf, 0, 2 * $n)
    } finally { $h.Dispose() }
}

# Fills the first count bytes of buf. A short read goes on through a
# second buffer, as ReadFile has no offset into the buffer.
function Read-GjAll($h, [byte[]]$buf, [int]$count) {
    $got = 0
    $part = $buf
    while ($got -lt $count) {
        $n = 0
        if (-not $K::ReadFile($h, $part, $count - $got, [ref]$n, [IntPtr]::Zero)) { throw "read failed ($(Get-GjError))" }
        if ($n -eq 0) { throw 'read ended early' }
        if ($got -gt 0) { [Array]::Copy($part, 0, $buf, $got, $n) }
        $got += $n
        if ($got -lt $count -and [object]::ReferenceEquals($part, $buf)) { $part = New-Object byte[] $count }
    }
}

function Get-GjDisks([string]$where) {
    $s = New-Object Management.ManagementObjectSearcher('root\Microsoft\Windows\Storage', "SELECT * FROM MSFT_Disk $where")
    @($s.Get())
}
'@

    $state = @{ LogFile = ''; Work = '' }
    function Write-Log([string]$text) {
        if (-not $state.LogFile) { return }
        try {
            [IO.File]::AppendAllText($state.LogFile, ('{0} {1}' -f [DateTime]::UtcNow.ToString('HH:mm:ss'), $text) + "`r`n")
        } catch { }
    }
    function Stop-Maker([string]$text, [int]$code = 1) {
        $e = New-Object Exception $text
        $e.Data['GjCode'] = $code
        throw $e
    }
    function Remove-Work {
        if ($state.Work -and (Test-Path -LiteralPath $state.Work)) {
            Remove-Item -LiteralPath $state.Work -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
    function Get-Bytes([long]$n) { '{0:0.0} GB' -f ([Math]::Floor($n / 100000000) / 10) }
    function Get-Clean([string]$s) {
        $s = ($s -replace '[^A-Za-z0-9 ._()+-]', '').Trim()
        if ($s.Length -gt 40) { $s = $s.Substring(0, 40) }
        $s
    }

    $local = [Environment]::GetFolderPath('LocalApplicationData')
    if ($local) {
        try {
            $dir = Join-Path $local 'gjallarOS'
            [void][IO.Directory]::CreateDirectory($dir)
            $state.LogFile = Join-Path $dir 'make-stick.log'
            [IO.File]::AppendAllText($state.LogFile, "`r`nmake-stick $([DateTime]::UtcNow.ToString('yyyy-MM-dd HH:mm:ss')) UTC`r`n")
        } catch { $state.LogFile = '' }
    }
    Write-Log 'GJA-3001 maker.run.started os=windows'

    $code = 0
    try {
        if ($Release -and $Release -notmatch '^v[0-9]{4}\.[0-9]{2}\.[0-9]+$') {
            Stop-Maker 'A release name looks like v2026.10.1.' 2
        }
        # A release build puts the key in; an unbuilt copy still holds the
        # placeholders, which are not base64.
        foreach ($k in @($keyModulus, $keyExponent)) {
            if ($k -notmatch '^[A-Za-z0-9+/]+={0,2}$') {
                Stop-Maker 'This copy of the maker has no release key. Download make-stick.ps1 from a gjallarOS release.'
            }
        }
        if ($PSVersionTable.PSVersion.Major -ne 5 -or [Environment]::OSVersion.Platform -ne 'Win32NT') {
            Stop-Maker 'Run the maker in Windows PowerShell 5.1 (powershell.exe), the one that comes with Windows.'
        }
        . ([ScriptBlock]::Create($native))

        Write-Host 'gjallarOS USB stick maker'
        Write-Host ''
        Write-Host 'This downloads the gjallarOS installer, checks its signature and writes'
        Write-Host 'it to a USB stick. Everything on that stick is erased.'
        Write-Host ''
        if (-not $Release) {
            $r = Read-Host 'Press Enter to use the newest stable release, or type A for advanced options'
            if ($r -match '^[aA]$') {
                while ($true) {
                    $r = Read-Host 'Release name, for example v2026.10.1 (Enter for stable)'
                    if (-not $r -or $r -match '^v[0-9]{4}\.[0-9]{2}\.[0-9]+$') { break }
                    Write-Host 'A release name looks like v2026.10.1.'
                }
                $Release = $r
            }
        }
        $base = if ($Release) { "$releases/download/$Release" } else { "$releases/latest/download" }

        if (-not $local) { Stop-Maker 'Windows did not name a local app data folder for the download.' }
        $state.Work = Join-Path $local ('gjallarOS\download-' + [Guid]::NewGuid().ToString('N').Substring(0, 12))
        [void][IO.Directory]::CreateDirectory($state.Work)

        $proto = [Net.ServicePointManager]::SecurityProtocol
        if ($proto -ne [Net.SecurityProtocolType]::SystemDefault -and -not ($proto -band [Net.SecurityProtocolType]::Tls12)) {
            [Net.ServicePointManager]::SecurityProtocol = $proto -bor [Net.SecurityProtocolType]::Tls12
        }

        # https only, also after redirects; stops past the size cap; resumes
        # a cut-off download.
        function Get-Download([string]$url, [string]$file, [long]$max, [bool]$progress) {
            $path = Join-Path $state.Work $file
            for ($try = 1; $try -le 3; $try++) {
                try {
                    $u = $url
                    for ($hop = 0; ; $hop++) {
                        if ($hop -gt 8 -or -not $u.StartsWith('https://')) { throw "redirect to $u" }
                        $have = if (Test-Path -LiteralPath $path) { (Get-Item -LiteralPath $path).Length } else { 0 }
                        $req = [Net.HttpWebRequest]::Create($u)
                        $req.AllowAutoRedirect = $false
                        $req.UserAgent = 'gjallarOS-make-stick'
                        if ($have -gt 0) { $req.AddRange([long]$have) }
                        try { $resp = $req.GetResponse() } catch {
                            # Method calls wrap the WebException.
                            $ex = $_.Exception
                            while ($ex -and $ex -isnot [Net.WebException]) { $ex = $ex.InnerException }
                            if (-not $ex -or -not $ex.Response -or [int]$ex.Response.StatusCode -ne 416) { throw }
                            $resp = $ex.Response
                        }
                        $st = [int]$resp.StatusCode
                        if ($st -ge 300 -and $st -lt 400) {
                            $u = [string](New-Object Uri((New-Object Uri $u), $resp.Headers['Location']))
                            $resp.Close()
                            continue
                        }
                        break
                    }
                    try {
                        if ($st -eq 416) { return }
                        if ($st -eq 200) { $have = 0 } elseif ($st -ne 206) { throw "HTTP $st" }
                        $total = $have + $resp.ContentLength
                        if ($resp.ContentLength -lt 0 -or $total -gt $max) { throw 'too large' }
                        $mode = if ($have -gt 0) { 'Append' } else { 'Create' }
                        $out = New-Object IO.FileStream($path, $mode, 'Write', 'None')
                        try {
                            $in = $resp.GetResponseStream()
                            $buf = New-Object byte[] 1048576
                            $last = -1
                            while (($n = $in.Read($buf, 0, $buf.Length)) -gt 0) {
                                $have += $n
                                if ($have -gt $max) { throw 'too large' }
                                $out.Write($buf, 0, $n)
                                if ($progress -and $total -gt 0) {
                                    $pc = [int][Math]::Floor(100 * $have / $total)
                                    if ($pc -ne $last) { Write-Host -NoNewline ("`r  {0,3} %" -f $pc); $last = $pc }
                                }
                            }
                        } finally { $out.Dispose() }
                        if ($progress) { Write-Host '' }
                        return
                    } finally { $resp.Close() }
                } catch {
                    if ($try -eq 3) { Stop-Maker "Download failed three times: $url" }
                    Start-Sleep -Seconds 3
                }
            }
        }
        function Stop-BadDownload([string]$what) {
            Remove-Work
            Write-Log 'GJA-3012 maker.signature.bad'
            Stop-Maker "$what is not the file we published. Nothing was written. Please report this line with the release name."
        }
        function Get-Sha256([string]$path) {
            $sha = [Security.Cryptography.SHA256]::Create()
            $f = [IO.File]::OpenRead($path)
            try { -join ($sha.ComputeHash($f) | ForEach-Object { $_.ToString('x2') }) } finally { $f.Dispose(); $sha.Dispose() }
        }

        Write-Host 'Downloading the release checksums...'
        Get-Download "$base/SHA256SUMS" 'SHA256SUMS' 65536 $false
        Get-Download "$base/SHA256SUMS.sig" 'SHA256SUMS.sig' 4096 $false
        $sums = [IO.File]::ReadAllBytes((Join-Path $state.Work 'SHA256SUMS'))
        $good = $false
        try {
            $rsa = New-Object Security.Cryptography.RSACng
            $p = New-Object Security.Cryptography.RSAParameters
            $p.Modulus = [Convert]::FromBase64String($keyModulus)
            $p.Exponent = [Convert]::FromBase64String($keyExponent)
            $rsa.ImportParameters($p)
            $good = $rsa.VerifyData($sums, [IO.File]::ReadAllBytes((Join-Path $state.Work 'SHA256SUMS.sig')),
                [Security.Cryptography.HashAlgorithmName]::SHA256, [Security.Cryptography.RSASignaturePadding]::Pss)
        } catch { $good = $false }
        if (-not $good) { Stop-BadDownload 'SHA256SUMS' }

        $isoName = ''; $isoSha = ''; $imgSha = ''; $isoTag = ''
        $text = [Text.Encoding]::ASCII.GetString($sums)
        if ($text -notmatch "^([^\r\n]*\n)+\z") { Stop-BadDownload 'SHA256SUMS' }
        foreach ($line in $text.Substring(0, $text.Length - 1).Split("`n")) {
            if ($line -cnotmatch '^([0-9a-f]{64})  ([A-Za-z0-9._-]+)$') { Stop-BadDownload 'SHA256SUMS' }
            $sum = $Matches[1]; $name = $Matches[2]
            if ($name -ceq 'gjlogs.img') {
                if ($imgSha) { Stop-BadDownload 'SHA256SUMS' }
                $imgSha = $sum
            } elseif ($name -ceq 'make-stick.sh' -or $name -ceq 'make-stick.ps1') {
            } elseif (-not $isoName -and $name -cmatch '^gjallarOS-installer-(v[0-9]{4}\.[0-9]{2}\.[0-9]+)\.iso$') {
                $isoName = $name; $isoSha = $sum; $isoTag = $Matches[1]
            } else { Stop-BadDownload 'SHA256SUMS' }
        }
        if (-not $isoName -or -not $imgSha) { Stop-BadDownload 'SHA256SUMS' }
        if ($Release -and $isoTag -cne $Release) { Stop-BadDownload 'SHA256SUMS' }

        Write-Host "Downloading gjallarOS installer $isoTag..."
        Get-Download "$base/$isoName" 'installer.iso' $isoMax $true
        Get-Download "$base/gjlogs.img" 'gjlogs.img' $imgSize $false
        Write-Log 'GJA-3010 maker.download.finished'

        $isoPath = Join-Path $state.Work 'installer.iso'
        $imgPath = Join-Path $state.Work 'gjlogs.img'
        $isoSize = (Get-Item -LiteralPath $isoPath).Length
        if ((Get-Item -LiteralPath $imgPath).Length -ne $imgSize) { Stop-BadDownload 'gjlogs.img' }
        if ($isoSize -le 0 -or $isoSize % 512 -ne 0) { Stop-BadDownload $isoName }
        Write-Host 'Checking the download...'
        if ((Get-Sha256 $isoPath) -cne $isoSha) { Stop-BadDownload $isoName }
        if ((Get-Sha256 $imgPath) -cne $imgSha) { Stop-BadDownload 'gjlogs.img' }
        Write-Log 'GJA-3011 maker.signature.verified'
        Write-Host 'Signature and checksums are correct.'

        # USB disks that are not the boot or system disk.
        while ($true) {
            $all = Get-GjDisks 'WHERE BusType = 7'
            $sticks = @($all | Where-Object { -not $_.IsBoot -and -not $_.IsSystem -and [long]$_.Size -ge $isoSize } |
                Sort-Object { [int]$_.Number })
            if ($sticks.Count -gt 0) { break }
            $small = @($all | Where-Object { -not $_.IsBoot -and -not $_.IsSystem }).Count
            Write-Host ''
            if ($small -gt 0) {
                Write-Host "The USB sticks plugged in are too small. The installer needs $(Get-Bytes $isoSize)."
            } else { Write-Host 'No USB stick found.' }
            $r = Read-Host 'Plug in a stick and press Enter to look again, or type Q to stop'
            if ($r -match '^[qQ]$') { Stop-Maker 'Nothing was written.' 130 }
        }
        Write-Host ''
        Write-Host 'USB sticks:'
        for ($i = 0; $i -lt $sticks.Count; $i++) {
            $label = Get-Clean ([string]$sticks[$i].FriendlyName)
            if (-not $label) { $label = 'USB stick' }
            Write-Host ('  {0}  {1,-40} {2,10}   serial {3}' -f ($i + 1), $label, (Get-Bytes $sticks[$i].Size), (Get-Clean ([string]$sticks[$i].SerialNumber)))
        }
        while ($true) {
            $r = Read-Host 'Type the number of the stick to erase and write (Enter to stop)'
            if (-not $r) { Stop-Maker 'Nothing was written.' 130 }
            if ($r -match '^[1-9][0-9]?$' -and [int]$r -le $sticks.Count) { $pick = $sticks[[int]$r - 1]; break }
            Write-Host "Type one of the numbers 1 to $($sticks.Count)."
        }
        Write-Log "GJA-3020 maker.disk.chosen disks=$($sticks.Count)"
        $label = Get-Clean ([string]$pick.FriendlyName)
        if (-not $label) { $label = 'USB stick' }

        Write-Host ''
        Write-Host "You picked $label, $(Get-Bytes $pick.Size)."
        Write-Host "The maker will erase everything on it, write gjallarOS installer $isoTag"
        Write-Host 'and add a small GJLOGS partition where the installer can save its log.'
        $r = Read-Host 'Type ERASE to go on, or press Enter to stop'
        if ($r -cne 'ERASE') { Stop-Maker 'Nothing was written.' 130 }

        Write-Host ''
        Write-Host 'The write needs admin rights, and Windows asks for them next.'
        Write-Host '  Yes: a second window writes the stick and checks it. Leave it open.'
        Write-Host '  No:  nothing is written and the download is deleted.'
        Write-Host 'When the write is done, Windows may offer to format the stick.'
        Write-Host 'Choose Cancel there: formatting erases the installer again.'
        [void](Read-Host 'Press Enter to continue')

        # The elevated side gets the final paths Windows resolved, so it can
        # refuse a path that was redirected after this point.
        $isoFinal = Get-GjFinalPath $isoPath
        $imgFinal = Get-GjFinalPath $imgPath
        if (-not $isoFinal -or -not $imgFinal) { Stop-Maker 'The download folder changed. Nothing was written. Run the maker again.' }

        # Elevated part. Arguments only, all base64 or digits: disk number,
        # UniqueId, size, ISO path, ISO size, ISO hash, image path, image
        # hash. Exit codes: 40 done with GJLOGS, 41 done without GJLOGS (the
        # layout leaves no slot), 42 done without GJLOGS (no room); any other
        # code is a failure, 0 and 1 too (L83). 10 bad arguments, 11 not the picked stick, 12 a volume on it is in
        # use, 13 too small, 14 a download is not a plain file, 15 a
        # download does not match its hash, 16 not 512-byte sectors,
        # 21 read-back mismatch, 22 write failed (both wipe the first MiB).
        $elevated = $native + @'

function Stop-Gj([int]$c) { [Environment]::Exit($c) }
function Get-Arg([string]$v) { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($v)) }
if ($args.Count -ne 8) { Stop-Gj 10 }
$b64 = '^[A-Za-z0-9+/]{4,}={0,2}$'
if ($args[0] -notmatch '^[0-9]{1,3}$' -or $args[1] -notmatch $b64 -or $args[2] -notmatch '^[1-9][0-9]{0,15}$' -or
    $args[3] -notmatch $b64 -or $args[4] -notmatch '^[1-9][0-9]{0,11}$' -or $args[5] -cnotmatch '^[0-9a-f]{64}$' -or
    $args[6] -notmatch $b64 -or $args[7] -cnotmatch '^[0-9a-f]{64}$') { Stop-Gj 10 }
$num = [int]$args[0]; $uid = Get-Arg $args[1]; $size = [long]$args[2]
$isoPath = Get-Arg $args[3]; $isoSize = [long]$args[4]; $isoSha = $args[5]
$imgPath = Get-Arg $args[6]; $imgSha = $args[7]
$imgSize = 33554432; $mib = 1048576; $chunk = 4 * $mib
if ($isoSize % 512 -ne 0) { Stop-Gj 10 }

function Test-Disk {
    $d = @(Get-GjDisks "WHERE Number = $num")
    $d.Count -eq 1 -and [string]$d[0].UniqueId -ceq $uid -and [long]$d[0].Size -eq $size -and
        [int]$d[0].BusType -eq 7 -and -not $d[0].IsBoot -and -not $d[0].IsSystem
}
# A control code without output gives a bool. An empty byte array would
# read as false and turn a lock that worked into "in use".
function Invoke-Ioctl($h, [int]$code) {
    $n = 0
    [bool]$K::DeviceIoControl($h, $code, $null, 0, $null, 0, [ref]$n, [IntPtr]::Zero)
}
function Get-Ioctl($h, [int]$code, [int]$outSize) {
    $out = New-Object byte[] $outSize
    $n = 0
    if (-not $K::DeviceIoControl($h, $code, $null, 0, $out, $outSize, [ref]$n, [IntPtr]::Zero)) { return $null }
    , $out
}
function Get-Hash($h, [long]$at, [long]$count, [string]$what) {
    if (-not $K::SetFilePointerEx($h, $at, [IntPtr]::Zero, 0)) { throw 'seek failed' }
    $sha = [Security.Cryptography.SHA256]::Create()
    $buf = New-Object byte[] $chunk
    $left = $count; $last = -1
    while ($left -gt 0) {
        $n = [int][Math]::Min($chunk, $left)
        Read-GjAll $h $buf $n
        [void]$sha.TransformBlock($buf, 0, $n, $null, 0)
        $left -= $n
        if ($what) {
            $pc = [int][Math]::Floor(100 * ($count - $left) / $count)
            if ($pc -ne $last) { Write-Host -NoNewline ("`r$what {0,3} %" -f $pc); $last = $pc }
        }
    }
    [void]$sha.TransformFinalBlock((New-Object byte[] 0), 0, 0)
    if ($what) { Write-Host '' }
    -join ($sha.Hash | ForEach-Object { $_.ToString('x2') })
}
function Write-At($disk, $src, [long]$at, [long]$count) {
    if (-not $K::SetFilePointerEx($src, 0, [IntPtr]::Zero, 0)) { throw 'seek failed' }
    if (-not $K::SetFilePointerEx($disk, $at, [IntPtr]::Zero, 0)) { throw 'seek failed' }
    $buf = New-Object byte[] $chunk
    $left = $count; $last = -1
    while ($left -gt 0) {
        $n = [int][Math]::Min($chunk, $left)
        Read-GjAll $src $buf $n
        $w = 0
        if (-not $K::WriteFile($disk, $buf, $n, [ref]$w, [IntPtr]::Zero) -or $w -ne $n) { throw "write failed ($(Get-GjError))" }
        $left -= $n
        $pc = [int][Math]::Floor(100 * ($count - $left) / $count)
        if ($pc -ne $last) { Write-Host -NoNewline ("`rWriting   {0,3} %" -f $pc); $last = $pc }
    }
    Write-Host ''
}

Write-Host 'gjallarOS USB stick maker: writing the stick. Do not unplug it.'
if (-not (Test-Disk)) { Stop-Gj 11 }
$iso = Open-GjFile $isoPath $isoSize
$img = Open-GjFile $imgPath $imgSize
if (-not $iso -or -not $img) { Stop-Gj 14 }
if ((Get-Hash $iso 0 $isoSize 'Checking ') -cne $isoSha -or (Get-Hash $img 0 $imgSize '') -cne $imgSha) { Stop-Gj 15 }

# Lock and dismount every volume with an extent on this disk, and keep the
# locks until the end. One lock that fails stops the write.
$locks = New-Object Collections.ArrayList
$vols = New-Object Management.ManagementObjectSearcher('root\cimv2', 'SELECT DeviceID FROM Win32_Volume')
foreach ($v in @($vols.Get())) {
    $id = ([string]$v.DeviceID).TrimEnd('\')
    if ($id -notmatch '^\\\\\?\\Volume\{[0-9a-fA-F-]{36}\}$') { continue }
    $vh = $K::CreateFileW($id, -1073741824, 3, [IntPtr]::Zero, 3, 0, [IntPtr]::Zero)
    if ($vh.IsInvalid) { continue }
    $ext = Get-Ioctl $vh 0x00560000 776
    $mine = $false
    if ($ext) {
        $cnt = [BitConverter]::ToUInt32($ext, 0)
        for ($i = 0; $i -lt [Math]::Min($cnt, 32); $i++) {
            if ([BitConverter]::ToUInt32($ext, 8 + 24 * $i) -eq $num) { $mine = $true }
        }
    }
    if (-not $mine) { $vh.Dispose(); continue }
    if (-not (Invoke-Ioctl $vh 0x00090018) -or -not (Invoke-Ioctl $vh 0x00090020)) { $vh.Dispose(); Stop-Gj 12 }
    [void]$locks.Add($vh)
}

$disk = $K::CreateFileW("\\.\PhysicalDrive$num", -1073741824, 3, [IntPtr]::Zero, 3, -2147483648, [IntPtr]::Zero)
if ($disk.IsInvalid) { Stop-Gj 12 }
$devnum = Get-Ioctl $disk 0x002D1080 12
$geo = Get-Ioctl $disk 0x000700A0 256
if (-not $devnum -or -not $geo -or [BitConverter]::ToUInt32($devnum, 4) -ne $num -or
    [BitConverter]::ToInt64($geo, 24) -ne $size -or -not (Test-Disk)) { Stop-Gj 11 }
if ([BitConverter]::ToUInt32($geo, 20) -ne 512) { Stop-Gj 16 }
if ($size -lt $isoSize) { Stop-Gj 13 }
$off = [long][Math]::Ceiling($isoSize / $mib) * $mib
$logs = if ($size -ge $off + $imgSize) { 'room' } else { 'too-small' }

# Environment.Exit skips finally blocks, so a failure after the write
# started sets rc, and the wipe below runs before the exit.
$rc = 0
try {
    Write-At $disk $iso 0 $isoSize
    if ($logs -eq 'room') { Write-At $disk $img $off $imgSize }
    if (-not $K::FlushFileBuffers($disk)) { throw 'flush failed' }
    if ((Get-Hash $disk 0 $isoSize 'Reading back') -cne $isoSha) { $rc = 21; throw 'mismatch' }
    if ($logs -eq 'room' -and (Get-Hash $disk $off $imgSize '') -cne $imgSha) { $rc = 21; throw 'mismatch' }

    if ($logs -eq 'room') {
        # MBR slot 3 for GJLOGS, only on the layout the ISO is built with:
        # boot signature, slots 3 and 4 empty, no GPT, slots 1 and 2 end
        # before the image.
        $mbr = New-Object byte[] 1024
        if (-not $K::SetFilePointerEx($disk, 0, [IntPtr]::Zero, 0)) { throw 'seek failed' }
        Read-GjAll $disk $mbr 1024
        $ok = $mbr[510] -eq 0x55 -and $mbr[511] -eq 0xaa -and
            [Text.Encoding]::ASCII.GetString($mbr, 512, 8) -cne 'EFI PART'
        for ($i = 478; $i -lt 510; $i++) { if ($mbr[$i] -ne 0) { $ok = $false } }
        foreach ($o in 446, 462) {
            if ($mbr[$o + 4] -ne 0 -and
                [long][BitConverter]::ToUInt32($mbr, $o + 8) + [BitConverter]::ToUInt32($mbr, $o + 12) -gt $off / 512) { $ok = $false }
        }
        if ($ok) {
            $entry = [byte[]](@(0x00, 0xfe, 0xff, 0xff, 0x0e, 0xfe, 0xff, 0xff) +
                [BitConverter]::GetBytes([uint32]($off / 512)) + [BitConverter]::GetBytes([uint32]($imgSize / 512)))
            $sector = New-Object byte[] 512
            [Array]::Copy($mbr, $sector, 512)
            [Array]::Copy($entry, 0, $sector, 478, 16)
            $w = 0
            if (-not $K::SetFilePointerEx($disk, 0, [IntPtr]::Zero, 0) -or
                -not $K::WriteFile($disk, $sector, 512, [ref]$w, [IntPtr]::Zero) -or $w -ne 512 -or
                -not $K::FlushFileBuffers($disk)) { throw 'MBR write failed' }
            $back = New-Object byte[] 512
            if (-not $K::SetFilePointerEx($disk, 0, [IntPtr]::Zero, 0)) { throw 'seek failed' }
            Read-GjAll $disk $back 512
            for ($i = 0; $i -lt 512; $i++) { if ($back[$i] -ne $sector[$i]) { $rc = 21 } }
            if ($rc) { throw 'mismatch' }
            $logs = 'created'
        } else { $logs = 'layout' }
    }
} catch {
    if (-not $rc) { $rc = 22 }
}
if ($rc) {
    # A failed or partial write must not boot.
    $z = New-Object byte[] $mib
    $w = 0
    if ($K::SetFilePointerEx($disk, 0, [IntPtr]::Zero, 0)) { [void]$K::WriteFile($disk, $z, $mib, [ref]$w, [IntPtr]::Zero) }
    [void]$K::FlushFileBuffers($disk)
}
foreach ($l in $locks) { $l.Dispose() }
[void](Invoke-Ioctl $disk 0x00070140)
$disk.Dispose()
if ($rc) { Stop-Gj $rc }
switch ($logs) { 'created' { Stop-Gj 40 } 'layout' { Stop-Gj 41 } default { Stop-Gj 42 } }
'@

        function ConvertTo-B64([string]$s) { [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($s)) }
        # The elevated code goes into the download folder as a plain file.
        # The elevated side reads it into memory once and runs those bytes
        # only if their SHA-256 matches, so a change to the file after this
        # point stops the write (code 15); a link swapped in at the path can
        # only stop it too. A packed, encoded copy on the command line looks
        # like a malware stager to Windows Security, which then ends the
        # process before its first line (L83).
        $raw = [Text.Encoding]::UTF8.GetBytes($elevated)
        $sha = [Security.Cryptography.SHA256]::Create()
        $bodySha = -join ($sha.ComputeHash($raw) | ForEach-Object { $_.ToString('x2') })
        $sha.Dispose()
        $bodyPath = Join-Path $state.Work 'write-stick.txt'
        [IO.File]::WriteAllBytes($bodyPath, $raw)
        $argList = @([string]$pick.Number, (ConvertTo-B64 ([string]$pick.UniqueId)), [string]$pick.Size,
            (ConvertTo-B64 $isoFinal), [string]$isoSize, $isoSha, (ConvertTo-B64 $imgFinal), $imgSha) |
            ForEach-Object { "'$_'" }
        # The elevated process loads no module from the user's folders:
        # autoload is off before the first command, and the one module the
        # write needs comes from the PowerShell install folder by full path.
        # FileStream refuses pipes and devices, and others may only read the
        # file while it is open.
        $boot = '$PSModuleAutoLoadingPreference = ''None''; $ErrorActionPreference = ''Stop''; ' +
            'Import-Module -Name ($PSHOME + ''\Modules\Microsoft.PowerShell.Utility\Microsoft.PowerShell.Utility.psd1''); ' +
            '$f = New-Object IO.FileStream(''' + $bodyPath.Replace("'", "''") + ''', ''Open'', ''Read'', ''Read''); ' +
            'if ($f.Length -ne ' + $raw.Length + ') { [Environment]::Exit(15) }; ' +
            '$b = New-Object byte[] ' + $raw.Length + '; $o = 0; ' +
            'while ($o -lt $b.Length) { $n = $f.Read($b, $o, $b.Length - $o); if ($n -le 0) { [Environment]::Exit(15) }; $o += $n }; ' +
            '$f.Dispose(); $s = [Security.Cryptography.SHA256]::Create(); ' +
            'if ((-join ($s.ComputeHash($b) | ForEach-Object { $_.ToString(''x2'') })) -cne ''' + $bodySha + ''') { [Environment]::Exit(15) }; ' +
            '& ([ScriptBlock]::Create([Text.Encoding]::UTF8.GetString($b))) ' + ($argList -join ' ') + '; [Environment]::Exit(1)'
        $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($boot))
        $ps = Join-Path ([Environment]::GetFolderPath('System')) 'WindowsPowerShell\v1.0\powershell.exe'

        while ($true) {
            # Process.Start, not Start-Process: Windows PowerShell's Start-Process
            # turns the UAC "No" into an InvalidOperationException that drops the
            # Win32Exception and its code 1223.
            $psi = New-Object Diagnostics.ProcessStartInfo
            $psi.FileName = $ps
            $psi.Arguments = "-NoProfile -NoLogo -NonInteractive -EncodedCommand $encoded"
            $psi.UseShellExecute = $true
            $psi.Verb = 'runas'
            try {
                $proc = [Diagnostics.Process]::Start($psi)
                $proc.WaitForExit()
            } catch {
                # 1223: the user said no at the UAC prompt.
                $ex = $_.Exception
                while ($ex -and -not ($ex -is [ComponentModel.Win32Exception] -and $ex.NativeErrorCode -eq 1223)) { $ex = $ex.InnerException }
                if (-not $ex) { throw }
                Write-Log 'GJA-3021 maker.elevation.declined'
                Write-Host ''
                Stop-Maker 'No admin rights for the write. Nothing was written and the download is deleted.'
            }
            $rc = $proc.ExitCode
            if ($rc -ne 12) { break }
            Write-Log 'GJA-3022 maker.disk.busy'
            Write-Host ''
            Write-Host 'Windows could not lock the stick: a program has a file on it open, often a'
            Write-Host 'File Explorer window. Nothing was written.'
            $r = Read-Host 'Close the programs that show the stick, then press Enter to try again, or type Q to stop'
            if ($r -match '^[qQ]$') { Stop-Maker 'Nothing was written.' 130 }
        }
        if ($rc -in 21, 40, 41, 42) { Write-Log 'GJA-3030 maker.write.finished' }
        switch ($rc) {
            { $_ -in 40, 41, 42 } { break }
            11 { Stop-Maker 'The stick changed after you picked it. Nothing was written. Run the maker again.' }
            13 { Stop-Maker "The stick is too small. The installer needs $(Get-Bytes $isoSize). Nothing was written." }
            14 { Stop-Maker 'The downloaded files changed or are not plain files. Nothing was written. Run the maker again.' }
            15 { Stop-Maker 'The downloaded files changed after the check. Nothing was written. Run the maker again.' }
            16 { Stop-Maker 'This stick uses a sector size the maker does not support. Nothing was written. Try another stick.' }
            21 {
                Write-Log 'GJA-3032 maker.readback.mismatch'
                Stop-Maker 'The stick did not read back what was written. Its start was wiped so it does not boot. Try another stick.'
            }
            22 { Stop-Maker 'The write failed. Its start was wiped so the stick does not boot. Run the maker again or try another stick.' }
            default {
                Write-Log "GJA-3033 maker.write.ended code=$rc"
                Stop-Maker ("The write did not finish (code $rc), so the stick may not start. If Windows Security " +
                    'stopped it, its Protection history names the reason. Run the maker again.')
            }
        }
        Write-Log 'GJA-3031 maker.readback.verified'
        switch ($rc) {
            40 { Write-Log 'GJA-3040 maker.logs.created' }
            41 {
                Write-Log 'GJA-3041 maker.logs.skipped reason=layout'
                Write-Host 'No GJLOGS partition: this image leaves no free slot for it. The installer offers other drives for its log.'
            }
            42 {
                Write-Log 'GJA-3041 maker.logs.skipped reason=too-small'
                Write-Host 'No GJLOGS partition: the stick has no room after the image. The installer offers other drives for its log.'
            }
        }
        Write-Host ''
        Write-Host "The stick is ready: gjallarOS installer $isoTag, read back and checked."
        Write-Host 'If Windows offers to format the stick, choose Cancel.'
        Write-Host 'Start the computer you want to install on from this stick.'
    } catch {
        $code = if ($_.Exception.Data.Contains('GjCode')) { [int]$_.Exception.Data['GjCode'] } else { 1 }
        if ($code -eq 130) {
            Write-Host $_.Exception.Message
        } else {
            Write-Host "ERROR: $($_.Exception.Message)" -ForegroundColor Red
            Write-Log "ERROR: $($_.Exception.Message)"
            Write-Log 'GJA-3098 maker.run.failed'
        }
    } finally {
        Remove-Work
        Write-Log "GJA-3099 maker.run.finished code=$code"
    }
}

Invoke-GjallarMakeStick -Release $Release
