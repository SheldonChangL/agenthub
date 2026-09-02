<#
    AgentHub - Windows real-host acceptance (issue #21)

    WHY THIS EXISTS
    ---------------
    On Windows a file mode is not a permission: Go's Chmod only sets the
    read-only attribute and cannot produce an owner-only ACL. So AgentHub
    encrypts its Ed25519 node key with DPAPI, bound to the current Windows
    user. That code compiles in CI for windows/amd64 and windows/arm64, but it
    has never once run on a real Windows machine. A cross-build proves the
    source is portable; it proves nothing about DPAPI. This script is the
    missing evidence, and we have no Windows host to produce it ourselves.

    WHAT IT DOES TO YOUR MACHINE - please read, this is not "read-only"
    ------------------------------------------------------------------
      * clones this public repo and compiles it in a temp folder, deleted at
        the end
      * runs the test suite
      * starts the node several times, each bound to 127.0.0.1 only. Nothing
        is served to your network and no firewall prompt should appear.
      * writes into Go's shared caches (%USERPROFILE%\go\pkg\mod and
        %LocalAppData%\go-build). These are NOT deleted afterwards and may
        grow by a few hundred MB. `go clean -modcache` clears them.
      * downloads Go module dependencies from the network
      * by default it does NOT read your Claude or Codex data. Every node is
        pointed at empty folders. Pass -ScanRealProviders to include that
        check; the script tells you exactly what it would read first.
      * leaves one folder on your Desktop for the two manual checks, and
        tells you so at the end

    It never needs administrator rights, installs nothing, registers no
    service, and modifies no system setting.

    WHAT IT NEEDS
    -------------
      * Windows 10 or later, on NTFS
      * Go 1.27 or later        https://go.dev/dl/
      * git                     https://git-scm.com/download/win
      * about five minutes

    HOW TO RUN
    ----------
      powershell -ExecutionPolicy Bypass -File .\windows-acceptance.ps1

    Please use Windows PowerShell (powershell.exe, version 5.1) rather than
    PowerShell 7 if you have the choice: the DPAPI check below needs a class
    that ships with 5.1 and is not always present in 7. The script still runs
    under 7, but that one check will report SKIP, and it is the most important
    one here.

    Please send back the WHOLE output, including the environment lines at the
    top and any FAIL. A failure is the point of running this, not a problem
    with your machine.
#>

param(
    [switch]$ScanRealProviders
)

$ErrorActionPreference = 'Stop'
$script:Passed = 0
$script:Failed = 0
$script:Skipped = 0
$script:Nodes = @()

function Report {
    param([string]$Name, [string]$Result, [string]$Detail = '')
    switch ($Result) {
        'PASS' { $script:Passed++;  Write-Host "  PASS  $Name" -ForegroundColor Green }
        'FAIL' { $script:Failed++;  Write-Host "  FAIL  $Name" -ForegroundColor Red }
        'SKIP' { $script:Skipped++; Write-Host "  SKIP  $Name" -ForegroundColor Yellow }
        'INFO' {                    Write-Host "  ..    $Name" -ForegroundColor Gray }
    }
    if ($Detail) { Write-Host "        $Detail" -ForegroundColor DarkGray }
}

function Section { param([string]$Title) Write-Host "`n=== $Title ===" -ForegroundColor Cyan }

# Native commands write progress to stderr as a matter of course - "Cloning
# into 'repo'...", "go: downloading ...". Under $ErrorActionPreference='Stop'
# a redirected stderr line from a native command becomes a TERMINATING error in
# Windows PowerShell 5.1, which would kill this script on the very first git
# call. So every external command goes through here, with the preference
# lowered for the duration and the exit code reported separately.
function Invoke-Native {
    param([scriptblock]$Command)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    # Cleared first: a command that does not exist raises a non-terminating
    # error and never touches $LASTEXITCODE, so without this the PREVIOUS
    # command's exit code comes back as though this one had succeeded - and a
    # machine with no git would fail later, confusingly, at Set-Location.
    $global:LASTEXITCODE = $null
    try {
        $output = & $Command 2>&1 | ForEach-Object { "$_" }
        return @{ Output = ($output -join "`n"); Code = $LASTEXITCODE }
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Show-Tail {
    param([string]$Text, [int]$Lines = 20)
    if (-not $Text) { return }
    $Text -split "`n" | Select-Object -Last $Lines | ForEach-Object {
        Write-Host "        $_" -ForegroundColor DarkGray
    }
}

# ---------------------------------------------------------------- environment

Section 'Environment'
Write-Host "  OS         : $([System.Environment]::OSVersion.VersionString)"
Write-Host "  PowerShell : $($PSVersionTable.PSVersion) ($($PSVersionTable.PSEdition))"
Write-Host "  Arch       : $env:PROCESSOR_ARCHITECTURE"
Write-Host "  User       : $env:USERNAME"
Write-Host "  Machine    : $env:COMPUTERNAME"

$go = Invoke-Native { go version }
if ($null -eq $go.Code -or $go.Code -ne 0 -or -not $go.Output) {
    Write-Host "`nGo is not on PATH. Install Go 1.27+ from https://go.dev/dl/ and run this again." -ForegroundColor Red
    exit 1
}
Write-Host "  Go         : $($go.Output)"

$git = Invoke-Native { git --version }
if ($null -eq $git.Code -or $git.Code -ne 0) {
    Write-Host "`ngit is not on PATH. Install it from https://git-scm.com/download/win and run this again." -ForegroundColor Red
    exit 1
}
Write-Host "  Git        : $($git.Output)"

$work = Join-Path $env:TEMP ("agenthub-acceptance-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Path $work -Force | Out-Null
Write-Host "  Workspace  : $work"

# Get-Volume lives in a module that is not always reachable, and -ErrorAction
# does not suppress a command-not-found, so this must be a real try/catch.
$filesystem = $null
try {
    $driveLetter = (Get-Item $work).PSDrive.Name
    $filesystem = (Get-Volume -DriveLetter $driveLetter -ErrorAction SilentlyContinue).FileSystemType
} catch { $filesystem = $null }
Write-Host "  Filesystem : $(if ($filesystem) { $filesystem } else { 'unknown' })"
if ($filesystem -and $filesystem -ne 'NTFS') {
    Write-Host "  NOTE: some checks below need NTFS and will skip." -ForegroundColor Yellow
}

# Every node is pointed at these, so nothing reads your real provider data
# unless you asked for it with -ScanRealProviders.
$emptyClaude = Join-Path $work 'empty-claude'
$emptyCodex = Join-Path $work 'empty-codex'
New-Item -ItemType Directory -Path $emptyClaude -Force | Out-Null
New-Item -ItemType Directory -Path $emptyCodex -Force | Out-Null

$keepDir = Join-Path ([Environment]::GetFolderPath('Desktop')) 'agenthub-acceptance'

try {

# ------------------------------------------------------------------- build

Section 'Build from source'
Push-Location $work
$clone = Invoke-Native { git clone --depth 1 https://github.com/SheldonChangL/agenthub.git repo }
if ($null -eq $clone.Code -or $clone.Code -ne 0) {
    Report 'clone the repository' 'FAIL'
    Show-Tail $clone.Output
    throw 'clone failed; nothing further can run'
}
Set-Location (Join-Path $work 'repo')
$head = (Invoke-Native { git rev-parse --short HEAD }).Output
Report 'clone the repository' 'PASS' "at commit $head"

$build = Invoke-Native { go build ./... }
if ($build.Code -eq 0) { Report 'go build ./...' 'PASS' } else { Report 'go build ./...' 'FAIL'; Show-Tail $build.Output }

$vet = Invoke-Native { go vet ./... }
if ($vet.Code -eq 0) { Report 'go vet ./...' 'PASS' } else { Report 'go vet ./...' 'FAIL'; Show-Tail $vet.Output }

# ------------------------------------------------------------------- tests

Section 'Test suite on Windows'
$test = Invoke-Native { go test -count=1 ./... }
if ($test.Code -eq 0) { Report 'go test ./...' 'PASS' } else { Report 'go test ./...' 'FAIL'; Show-Tail $test.Output 30 }

$race = Invoke-Native { go test -race -count=1 ./internal/identity/ }
if ($race.Code -eq 0) {
    Report 'go test -race ./internal/identity/' 'PASS'
} elseif ($race.Output -match 'requires cgo|gcc|C compiler|not supported') {
    Report 'go test -race ./internal/identity/' 'SKIP' 'the race detector is unavailable on this toolchain or architecture'
} else {
    Report 'go test -race ./internal/identity/' 'FAIL'
    Show-Tail $race.Output
}

$exe = Join-Path $work 'agenthub-node.exe'
$buildNode = Invoke-Native { go build -o $exe ./cmd/agenthub-node }
if ($null -eq $buildNode.Code -or $buildNode.Code -ne 0) {
    Report 'build agenthub-node.exe' 'FAIL'
    Show-Tail $buildNode.Output
    throw 'cannot build the node; nothing further can run'
}
Report 'build agenthub-node.exe' 'PASS'

# ------------------------------------------------------------- node control

function New-Node {
    param([string]$Db, [int]$Port, [string]$ClaudeRoot = $emptyClaude, [string]$CodexRoot = $emptyCodex)
    $info = New-Object System.Diagnostics.ProcessStartInfo
    $info.FileName = $exe
    $info.Arguments = "-db `"$Db`" -listen 127.0.0.1:$Port -peer-listen 127.0.0.1:$($Port + 1) " +
                      "-claude-root `"$ClaudeRoot`" -codex-root `"$CodexRoot`""
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $process = [System.Diagnostics.Process]::Start($info)
    # Read both pipes asynchronously: a full pipe buffer would otherwise block
    # the node before it ever answers.
    $node = @{
        Process = $process
        Out     = $process.StandardOutput.ReadToEndAsync()
        Err     = $process.StandardError.ReadToEndAsync()
        Port    = $Port
        Alive   = $false
    }
    $script:Nodes += $node
    return $node
}

function Wait-Node {
    param($Node, [int]$Seconds = 40)
    for ($i = 0; $i -lt ($Seconds * 4); $i++) {
        if ($Node.Process.HasExited) { return $false }
        try {
            $null = Invoke-WebRequest -Uri "http://127.0.0.1:$($Node.Port)/healthz" -TimeoutSec 2 -UseBasicParsing
            $Node.Alive = $true
            return $true
        } catch { }
        Start-Sleep -Milliseconds 250
    }
    return $false
}

function Stop-Node {
    param($Node)
    if ($Node.Process -and -not $Node.Process.HasExited) {
        try { $Node.Process.Kill() } catch { }
        $Node.Process.WaitForExit(5000) | Out-Null
    }
    $text = ''
    try { $text = ($Node.Out.Result + $Node.Err.Result) } catch { }
    return $text
}

function Start-NodeAndWait {
    param([string]$Db, [int]$Port, [string]$ClaudeRoot = $emptyClaude, [string]$CodexRoot = $emptyCodex)
    $node = New-Node -Db $Db -Port $Port -ClaudeRoot $ClaudeRoot -CodexRoot $CodexRoot
    $null = Wait-Node $node
    return $node
}

# ------------------------------------------------------- the key on disk

Section 'The node key on disk'
$dataDir = Join-Path $work 'data'
New-Item -ItemType Directory -Path $dataDir -Force | Out-Null
$dbPath = Join-Path $dataDir 'agenthub.db'
$keyPath = Join-Path $dataDir 'node.key'

$first = Start-NodeAndWait -Db $dbPath -Port 17462
if (-not $first.Alive) {
    Show-Tail (Stop-Node $first)
    Report 'the node starts and creates an identity' 'FAIL' 'it never answered on loopback'
    throw 'the node did not start'
}
$firstFingerprint = (Invoke-RestMethod -Uri 'http://127.0.0.1:17462/v1/node' -TimeoutSec 5).fingerprint
$null = Stop-Node $first

# The field is `omitempty`, so a missing fingerprint arrives as $null and would
# compare equal to another $null later. Demand the real shape now: six groups
# of four uppercase hex digits, as Fingerprint() in keypair.go produces.
if ($firstFingerprint -match '^([0-9A-F]{4} ){5}[0-9A-F]{4}$') {
    Report 'the node starts and creates an identity' 'PASS' "fingerprint $firstFingerprint"
} else {
    Report 'the node starts and creates an identity' 'FAIL' `
        "the node answered but reported no usable fingerprint: '$firstFingerprint'"
    throw 'no fingerprint to compare against'
}

if (-not (Test-Path $keyPath)) {
    Report 'node.key exists' 'FAIL' 'no key file was written'
    throw 'no key file'
}
Report 'node.key exists' 'PASS'
$bytes = [System.IO.File]::ReadAllBytes($keyPath)

# Layout, from keyfile_format.go:
#   magic "AHNK" (4) | scheme (1) | payload length (4, big endian) | payload
$magic = [System.Text.Encoding]::ASCII.GetString([byte[]]($bytes[0..3]))
if ($magic -eq 'AHNK') {
    Report 'node.key carries the versioned header' 'PASS' "magic '$magic', scheme byte $($bytes[4])"
} else {
    Report 'node.key carries the versioned header' 'FAIL' "first four bytes are '$magic', expected 'AHNK'"
}

if ($bytes[4] -eq 1) {
    Report 'node.key declares the DPAPI scheme' 'PASS' 'scheme 1 = DPAPI, bound to the current Windows user'
} else {
    Report 'node.key declares the DPAPI scheme' 'FAIL' "scheme byte is $($bytes[4]), expected 1"
}

# Reversing the slice turns the big-endian length into the little-endian order
# BitConverter expects.
$declaredLength = [BitConverter]::ToUInt32([byte[]]($bytes[8..5]), 0)
$payload = [byte[]]($bytes[9..($bytes.Length - 1)])
if ($declaredLength -eq $payload.Length) {
    Report 'the header length matches the payload' 'PASS' "$declaredLength bytes of protected payload"
} else {
    Report 'the header length matches the payload' 'FAIL' `
        "header declares $declaredLength bytes, file carries $($payload.Length)"
}

# ------------------------------------------------------- DPAPI, for real
#
# Everything above would still pass if protectSeed were a no-op that framed the
# raw seed: the file would not be 32 bytes, it would start with AHNK, and the
# scheme byte would be 1. The only way to know DPAPI actually ran is to ask
# Windows to undo it, with the same entropy the Go code uses.

Section 'The payload is a real DPAPI blob'
# PowerShell 7 ships this class under its own assembly name; 5.1 has it in
# System.Security. Try both, so the most important check in this script runs
# under either host instead of skipping.
$haveProtectedData = $false
foreach ($assembly in 'System.Security.Cryptography.ProtectedData', 'System.Security') {
    if ($haveProtectedData) { continue }
    try {
        Add-Type -AssemblyName $assembly -ErrorAction Stop
        $null = [System.Security.Cryptography.ProtectedData]
        $haveProtectedData = $true
    } catch { }
}

if (-not $haveProtectedData) {
    Report 'Windows can decrypt the payload' 'SKIP' `
        'System.Security.Cryptography.ProtectedData is unavailable here; please re-run under powershell.exe (5.1)'
    Report 'the plaintext seed is absent from the file' 'SKIP' 'depends on the check above'
    Report 'the blob is bound to AgentHub-specific entropy' 'SKIP' 'depends on the check above'
} else {
    # keyEntropy in keystore_windows.go
    $entropy = [System.Text.Encoding]::ASCII.GetBytes('agenthub.node.key.v1')
    $scope = [System.Security.Cryptography.DataProtectionScope]::CurrentUser
    $seed = $null
    try {
        $seed = [System.Security.Cryptography.ProtectedData]::Unprotect($payload, $entropy, $scope)
    } catch {
        $seed = $null
        $unprotectError = $_.Exception.Message
    }

    # CryptUnprotectData ignores the scope flag, so a blob protected for the
    # LOCAL_MACHINE scope would decrypt here too. What this proves is that the
    # payload is a genuine DPAPI blob carrying AgentHub's entropy - that it is
    # bound to the USER is what manual check (a) at the end proves.
    $decrypted = $false
    if ($null -eq $seed) {
        Report 'Windows can decrypt the payload' 'FAIL' `
            "DPAPI refused the payload this machine just wrote: $unprotectError"
    } elseif ($seed.Length -ne 32) {
        Report 'Windows can decrypt the payload' 'FAIL' `
            "DPAPI returned $($seed.Length) bytes, expected a 32-byte Ed25519 seed"
    } else {
        $decrypted = $true
        Report 'Windows can decrypt the payload' 'PASS' `
            'DPAPI returned exactly 32 bytes, so the payload is a real DPAPI blob'
    }

    # The seed must not also be sitting in the file in the clear. This is the
    # check that a no-op "encryption" cannot survive.
    if ($decrypted) {
        $fileHex = ($bytes | ForEach-Object { $_.ToString('x2') }) -join ''
        $seedHex = ($seed | ForEach-Object { $_.ToString('x2') }) -join ''
        if ($fileHex.Contains($seedHex)) {
            Report 'the plaintext seed is absent from the file' 'FAIL' `
                'the decrypted seed appears verbatim inside node.key, so the file is not protected'
        } else {
            Report 'the plaintext seed is absent from the file' 'PASS' `
                'the decrypted seed does not appear anywhere in the file'
        }
        [Array]::Clear($seed, 0, $seed.Length)
    } else {
        Report 'the plaintext seed is absent from the file' 'SKIP' 'nothing decrypted to compare against'
    }

    # Entropy is what stops a blob lifted from this file being decrypted by a
    # caller who does not know it came from AgentHub.
    #
    # Gated on the decryption above having worked: if the payload is not a DPAPI
    # blob at all then decrypting it WITHOUT the entropy fails too, and an
    # ungated check would report that failure as a pass.
    if (-not $decrypted) {
        Report 'the blob is bound to AgentHub-specific entropy' 'SKIP' `
            'nothing decrypted with the entropy, so binding to it cannot be tested'
    } else {
        $withoutEntropy = $null
        try { $withoutEntropy = [System.Security.Cryptography.ProtectedData]::Unprotect($payload, $null, $scope) } catch { }
        if ($null -eq $withoutEntropy) {
            Report 'the blob is bound to AgentHub-specific entropy' 'PASS' 'decryption without the entropy is refused'
        } else {
            Report 'the blob is bound to AgentHub-specific entropy' 'FAIL' `
                'the payload decrypts with no entropy, so any process could read it'
            [Array]::Clear($withoutEntropy, 0, $withoutEntropy.Length)
        }
    }
}

# --------------------------------------------------------- reload

Section 'Reload by the same user'
$second = Start-NodeAndWait -Db $dbPath -Port 17472
if (-not $second.Alive) {
    Show-Tail (Stop-Node $second)
    Report 'the same user can decrypt the key after a restart' 'FAIL' 'the node did not come back up'
} else {
    $again = (Invoke-RestMethod -Uri 'http://127.0.0.1:17472/v1/node' -TimeoutSec 5).fingerprint
    $null = Stop-Node $second
    if ($again -eq $firstFingerprint) {
        Report 'the same user can decrypt the key after a restart' 'PASS' "fingerprint is still $again"
    } else {
        Report 'the same user can decrypt the key after a restart' 'FAIL' `
            "fingerprint changed from '$firstFingerprint' to '$again'; a new identity would break every pairing"
    }
}

# ------------------------------------------------------------ damaged files

Section 'A damaged key fails closed'
$goodKey = [System.IO.File]::ReadAllBytes($keyPath)

# "It did not start" is not evidence: a port clash or a locked database would
# also produce that. A refusal counts only if the process exited non-zero AND
# said why, in the words the code actually uses.
function Test-Damaged {
    param([string]$Name, [byte[]]$Content, [string]$Expect, [string]$Why)
    [System.IO.File]::WriteAllBytes($keyPath, $Content)
    try {
        $node = New-Node -Db $dbPath -Port 17482
        $started = Wait-Node $node -Seconds 20
        $log = Stop-Node $node
        if ($started) {
            Report $Name 'FAIL' "the node started anyway - $Why"
        } elseif ($node.Process.ExitCode -eq 0) {
            Report $Name 'FAIL' "the node exited 0 rather than refusing - $Why"
        } elseif ($log -match [regex]::Escape($Expect)) {
            $line = ($log -split "`n" | Where-Object { $_ -match [regex]::Escape($Expect) } | Select-Object -First 1)
            Report $Name 'PASS' (($line -replace '\s+', ' ').Trim())
        } else {
            Report $Name 'FAIL' `
                "it refused, but not for the expected reason. Wanted '$Expect', got: $((($log -split "`n" | Select-Object -Last 2) -join ' | ').Trim())"
        }
    } finally {
        [System.IO.File]::WriteAllBytes($keyPath, $goodKey)
    }
}

# Four of these are caught by the platform-independent framing in
# keyfile_format.go. Only the flipped bit reaches DPAPI itself, and it is the
# one that proves Windows rejects a blob it did not produce.
$formatRefusal = 'unrecognised node key format'
$dpapiRefusal = 'could not be decrypted for the current Windows user'

$truncated = [byte[]]($goodKey[0..([Math]::Floor($goodKey.Length / 2))])
Test-Damaged 'a truncated key is refused (framing)' $truncated $formatRefusal `
    'a half-written key must not be treated as valid'

$oversized = [byte[]]::new($goodKey.Length + 4096)
[Array]::Copy($goodKey, $oversized, $goodKey.Length)
Test-Damaged 'an oversized key is refused (framing)' $oversized $formatRefusal `
    'trailing bytes must not be ignored'

$wrongMagic = [byte[]]::new($goodKey.Length)
[Array]::Copy($goodKey, $wrongMagic, $goodKey.Length)
$wrongMagic[0] = [byte][char]'X'
Test-Damaged 'a key with the wrong header is refused (framing)' $wrongMagic $formatRefusal `
    'a file this build did not write must not be adopted'

Test-Damaged 'an empty key file is refused (framing)' ([byte[]]::new(0)) $formatRefusal `
    'an empty file must not be read as "no key yet" and silently replaced'

$corrupt = [byte[]]::new($goodKey.Length)
[Array]::Copy($goodKey, $corrupt, $goodKey.Length)
$corrupt[$corrupt.Length - 1] = [byte](($corrupt[$corrupt.Length - 1] -bxor 0xFF) -band 0xFF)
Test-Damaged 'a corrupted blob is refused by DPAPI' $corrupt $dpapiRefusal `
    'a flipped bit must not silently produce a new identity'

# A reparse point where the key should be.
#
# readKeyFile Lstats before opening, so a link is refused rather than followed.
# The case that actually exercises that is a SYMLINK TO A VALID KEY: following
# it would succeed and the node would start happily, so only the Lstat can
# refuse it. A junction to a directory is a weaker test - a plain Stat would
# reject a directory too - so it is the fallback, and the result says which one
# ran. Creating a file symlink needs Developer Mode or elevation; a junction
# needs neither.
Section 'A reparse point in place of the key'
$linkKind = ''
$symlinkTarget = Join-Path $work 'real-node.key'
[System.IO.File]::WriteAllBytes($symlinkTarget, $goodKey)
$junctionTarget = Join-Path $work 'junction-target'
New-Item -ItemType Directory -Path $junctionTarget -Force | Out-Null

Remove-Item $keyPath -Force -ErrorAction SilentlyContinue
try {
    New-Item -ItemType SymbolicLink -Path $keyPath -Target $symlinkTarget -ErrorAction Stop | Out-Null
    $linkKind = 'a symlink to a valid key'
} catch {
    try {
        New-Item -ItemType Junction -Path $keyPath -Target $junctionTarget -ErrorAction Stop | Out-Null
        $linkKind = 'a junction to a directory (weaker: no Developer Mode for a file symlink)'
    } catch { $linkKind = '' }
}

$reparseName = 'a reparse point in place of node.key is refused'
if (-not $linkKind) {
    Report $reparseName 'SKIP' 'this machine allowed neither a file symlink nor a junction'
} else {
    $node = New-Node -Db $dbPath -Port 17486
    $started = Wait-Node $node -Seconds 20
    $log = Stop-Node $node
    # Only the Lstat refusal counts. main.go wraps every key error as
    # "load node key: ...", so matching on "node key" would also accept a
    # loader that followed the link and merely failed to open or read it.
    $expected = 'is not a regular file'
    if ($started) {
        Report $reparseName 'FAIL' "the node started with $linkKind where the key should be"
    } elseif ($node.Process.ExitCode -eq 0) {
        Report $reparseName 'FAIL' 'the node exited 0 rather than refusing'
    } elseif ($log -match [regex]::Escape($expected)) {
        $line = ($log -split "`n" | Where-Object { $_ -match [regex]::Escape($expected) } | Select-Object -First 1)
        Report $reparseName 'PASS' "tested with $linkKind - $(($line -replace '\s+', ' ').Trim())"
    } else {
        Report $reparseName 'FAIL' `
            "it refused, but not via the Lstat check. Wanted '$expected', got: $((($log -split "`n" | Select-Object -Last 2) -join ' | ').Trim())"
    }
}

# Remove-Item follows or mishandles reparse points; Delete removes the link
# itself. Directory::Delete covers a junction, File::Delete a file symlink.
try { [System.IO.Directory]::Delete($keyPath) } catch { }
try { [System.IO.File]::Delete($keyPath) } catch { }
if (Test-Path $keyPath) { Remove-Item $keyPath -Force -Recurse -ErrorAction SilentlyContinue }
[System.IO.File]::WriteAllBytes($keyPath, $goodKey)

# ------------------------------------------------------- concurrent starts

Section 'Concurrent starts agree on one identity'
if ($filesystem -and $filesystem -ne 'NTFS') {
    Report 'concurrent starts converge on one identity' 'SKIP' 'the hard-link install path needs NTFS'
} else {
    # One directory, so one node.key and one race; three databases, because
    # Open() sets no busy timeout and three processes sharing a database would
    # collide during migration long before they reached the key.
    $raceDir = Join-Path $work 'concurrent'
    New-Item -ItemType Directory -Path $raceDir -Force | Out-Null
    $racePorts = 17492, 17494, 17496
    $raceNodes = @()
    foreach ($port in $racePorts) {
        $raceNodes += New-Node -Db (Join-Path $raceDir "agenthub-$port.db") -Port $port
    }
    $fingerprints = @()
    foreach ($node in $raceNodes) {
        if (Wait-Node $node -Seconds 40) {
            try {
                $fingerprints += (Invoke-RestMethod -Uri "http://127.0.0.1:$($node.Port)/v1/node" -TimeoutSec 5).fingerprint
            } catch { }
        }
    }
    $raceLogs = @()
    foreach ($node in $raceNodes) { $raceLogs += (Stop-Node $node) }

    $distinct = @($fingerprints | Sort-Object -Unique)
    if ($fingerprints.Count -ne $racePorts.Count) {
        # Not a SKIP: a node that died in the race is exactly the failure this
        # check exists to catch.
        Report 'concurrent starts converge on one identity' 'FAIL' `
            "only $($fingerprints.Count) of $($racePorts.Count) nodes came up"
        Show-Tail ($raceLogs -join "`n") 10
    } elseif ($distinct.Count -eq 1) {
        Report 'concurrent starts converge on one identity' 'PASS' `
            "all $($fingerprints.Count) nodes reported $($distinct[0])"
    } else {
        Report 'concurrent starts converge on one identity' 'FAIL' `
            "got $($distinct.Count) different identities: $($distinct -join ', ')"
    }
}

# ------------------------------------------------------------- provider scan

Section 'Provider discovery'
$claudeRoot = Join-Path $env:USERPROFILE '.claude'
$codexRoot = Join-Path $env:USERPROFILE '.codex'
$haveClaude = Test-Path (Join-Path $claudeRoot 'projects')
$haveCodex = Test-Path (Join-Path $codexRoot 'sessions')

if (-not $ScanRealProviders) {
    Report 'discovery against installed providers' 'SKIP' `
        'not run by default. It would read session metadata (titles, working directories) from your Claude and Codex folders. To include it: -ScanRealProviders'
} elseif (-not $haveClaude -and -not $haveCodex) {
    Report 'discovery against installed providers' 'SKIP' 'neither ~/.claude nor ~/.codex is present on this machine'
} else {
    Report 'about to read your real provider data' 'INFO' `
        "reading session metadata from $claudeRoot and $codexRoot (titles, working directories; no conversation content)"
    $scanDir = Join-Path $work 'scan'
    New-Item -ItemType Directory -Path $scanDir -Force | Out-Null
    $node = Start-NodeAndWait -Db (Join-Path $scanDir 'agenthub.db') -Port 17502 `
        -ClaudeRoot $claudeRoot -CodexRoot $codexRoot
    if (-not $node.Alive) {
        Show-Tail (Stop-Node $node)
        Report 'discovery against installed providers' 'FAIL' 'the node did not start'
    } else {
        $sessions = Invoke-RestMethod -Uri 'http://127.0.0.1:17502/v1/sessions?pageSize=1' -TimeoutSec 15
        $heartbeat = Invoke-RestMethod -Uri 'http://127.0.0.1:17502/v1/heartbeat' -TimeoutSec 15
        $null = Stop-Node $node
        $found = $sessions.pagination.totalItems
        Report 'discovery against installed providers' 'PASS' "$found session(s) found"

        # Only meaningful if there was something that could have leaked.
        if ($found -eq 0) {
            Report 'discovered sessions stay private by default' 'SKIP' 'no sessions were found, so nothing could have been published'
        } elseif (-not $heartbeat.payload.PSObject.Properties['sessions']) {
            Report 'discovered sessions stay private by default' 'FAIL' `
                'the heartbeat payload has no sessions field at all; this script is asserting against the wrong shape'
        } elseif (@($heartbeat.payload.sessions).Count -eq 0) {
            Report 'discovered sessions stay private by default' 'PASS' `
                "$found session(s) on disk, none in the heartbeat"
        } else {
            Report 'discovered sessions stay private by default' 'FAIL' `
                "$(@($heartbeat.payload.sessions).Count) session(s) would be published without the owner choosing"
        }

        # The signature is where the node key is actually used on the wire.
        if ($heartbeat.signature) {
            Report 'the heartbeat is signed with the protected key' 'PASS' "signature present, $($heartbeat.signature.Length) chars"
        } else {
            Report 'the heartbeat is signed with the protected key' 'FAIL' 'the heartbeat carries no signature'
        }
    }
}

# ---------------------------------------------------- cross-user / machine

Section 'Cross-user and cross-machine (manual)'
New-Item -ItemType Directory -Path $keepDir -Force | Out-Null
Copy-Item $keyPath (Join-Path $keepDir 'node.key') -Force
Copy-Item $exe (Join-Path $keepDir 'agenthub-node.exe') -Force
Report 'the files for the two manual checks were placed on your Desktop' 'INFO' $keepDir
Write-Host @"
        These two checks are the whole point of DPAPI, and one machine with one
        account cannot prove them. Both are optional.

        (a) ANOTHER USER on this machine
            - log in as a different Windows user
            - copy the folder 'agenthub-acceptance' from the first user's
              Desktop to somewhere that user can read
            - open PowerShell there and run:
                  .\agenthub-node.exe -db .\agenthub.db
            - EXPECTED: it refuses to start, with a message containing
              "could not be decrypted for the current Windows user".
              If it starts and prints a fingerprint, that is a FAIL - please
              say so, and press Ctrl+C to stop it (a node that started will
              keep running and hold the console).

        (b) ANOTHER WINDOWS MACHINE
            - copy the same folder to a different computer and run the same
              command
            - EXPECTED: the same refusal.

        Please delete the folder afterwards. It is a throwaway test identity,
        not a real secret, but there is no reason to leave it around.
"@ -ForegroundColor DarkGray

} catch {
    # Without this the summary never prints and "send the whole output back"
    # produces a bare stack trace with no PASS/FAIL lines at all.
    Report 'the script stopped early' 'FAIL' $_.Exception.Message
    Write-Host "        $($_.ScriptStackTrace)" -ForegroundColor DarkGray
} finally {
    foreach ($node in $script:Nodes) {
        if ($node.Process -and -not $node.Process.HasExited) {
            try { $node.Process.Kill() } catch { }
        }
    }
    Pop-Location -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 500
    $removed = $true
    try { Remove-Item $work -Recurse -Force -ErrorAction Stop } catch { $removed = $false }
}

Section 'Summary'
Write-Host "  passed  : $script:Passed" -ForegroundColor Green
Write-Host "  failed  : $script:Failed" -ForegroundColor $(if ($script:Failed -gt 0) { 'Red' } else { 'Gray' })
Write-Host "  skipped : $script:Skipped" -ForegroundColor Yellow
Write-Host ''
if ($removed) {
    Write-Host "  The temporary workspace was deleted." -ForegroundColor DarkGray
} else {
    Write-Host "  The temporary workspace could NOT be deleted; please remove it by hand:" -ForegroundColor Yellow
    Write-Host "    $work" -ForegroundColor Yellow
}
Write-Host "  Left on your Desktop for the manual checks: $keepDir" -ForegroundColor DarkGray
Write-Host "  Go's module and build caches were written to and are NOT cleaned up." -ForegroundColor DarkGray
Write-Host ''
if ($script:Failed -gt 0) {
    Write-Host 'Please send this whole output back. A failure here is exactly what running it was for.' -ForegroundColor Yellow
} else {
    Write-Host 'Please send this whole output back, including the environment lines at the top.' -ForegroundColor Gray
}
