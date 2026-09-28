#requires -Version 5.1
#requires -RunAsAdministrator
<#
Installs Windows' official optional OpenSSH Server component and restricts a NEW setup to loopback.
Existing non-loopback SSH configurations are deliberately left unchanged and reported as an error.
This script does not create Windows accounts, passwords, or authorized_keys.
Not executed/verified on Windows in the supplied build environment.
#>
[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$Capability = 'OpenSSH.Server~~~~0.0.1.0'
$ConfigPath = Join-Path $env:ProgramData 'ssh\sshd_config'
$Sshd = Join-Path $env:WINDIR 'System32\OpenSSH\sshd.exe'
$Cap = Get-WindowsCapability -Online -Name $Capability

function Assert-LoopbackConfig {
    $Effective = & $Sshd -T -f $ConfigPath 2>&1
    if ($LASTEXITCODE -ne 0) { throw "sshd configuration validation failed: $Effective" }
    $Listeners = @($Effective | Where-Object { $_ -match '^listenaddress ' })
    if ($Listeners.Count -eq 0) { throw 'No explicit loopback SSH listeners were found.' }
    foreach ($Line in $Listeners) {
        if ($Line -notmatch '^listenaddress (127\.0\.0\.1:22|\[::1\]:22)$') {
            throw "Existing SSH is not loopback-only on port 22. It has NOT been modified: $Line"
        }
    }
}

if ($Cap.State -eq 'Installed') {
    if (-not (Test-Path -LiteralPath $ConfigPath)) { throw 'Existing OpenSSH has no configuration. Review it before allowing RemoteDesk to manage it.' }
    Assert-LoopbackConfig
    Set-Service -Name sshd -StartupType Automatic
    Start-Service -Name sshd
    Write-Output 'Existing loopback-only OpenSSH reused; configuration was not rewritten.'
    return
}
if (Test-Path -LiteralPath $ConfigPath) { throw 'A previous SSH configuration exists. Refusing to overwrite it.' }
$Result = Add-WindowsCapability -Online -Name $Capability
if ($Result.RestartNeeded) { throw 'Windows requires a restart to finish OpenSSH installation. SSH configuration has not been reported as complete.' }
# Windows may create this inbound rule while installing the capability. Do not leave it enabled.
$Rule = Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue
if ($Rule) { Disable-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' | Out-Null }
$Service = Get-Service -Name sshd -ErrorAction Stop
if ($Service.Status -eq 'Running') { Stop-Service -Name sshd -ErrorAction Stop }
$Parent = Split-Path -Parent $ConfigPath
New-Item -ItemType Directory -Path $Parent -Force | Out-Null
if (Test-Path -LiteralPath $ConfigPath) {
    Copy-Item -LiteralPath $ConfigPath -Destination ($ConfigPath + '.remotedesk-install-backup') -ErrorAction Stop
}
$Content = @'
# RemoteDesk-owned NEW OpenSSH installation: never expose port 22 outside this computer.
Port 22
ListenAddress 127.0.0.1
ListenAddress ::1
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
AuthorizedKeysFile .ssh/authorized_keys
AllowTcpForwarding no
AllowAgentForwarding no
GatewayPorts no
PermitTunnel no
X11Forwarding no
Subsystem sftp sftp-server.exe
Match Group administrators
    AuthorizedKeysFile __PROGRAMDATA__/ssh/administrators_authorized_keys
'@
[System.IO.File]::WriteAllText($ConfigPath, $Content + "`r`n", (New-Object System.Text.UTF8Encoding($false)))
$Keygen = Join-Path $env:WINDIR 'System32\OpenSSH\ssh-keygen.exe'
& $Keygen -A
if ($LASTEXITCODE -ne 0) { throw 'OpenSSH host key creation failed. sshd remains stopped.' }
& $Sshd -t -f $ConfigPath
if ($LASTEXITCODE -ne 0) { throw 'Invalid OpenSSH configuration. sshd remains stopped; the previous generated default is backed up.' }
Assert-LoopbackConfig
Set-Service -Name sshd -StartupType Automatic
Start-Service -Name sshd
$Listeners = @(Get-NetTCPConnection -State Listen -LocalPort 22 -ErrorAction Stop)
if ($Listeners.Count -eq 0) { throw 'sshd did not start listening.' }
foreach ($Listener in $Listeners) {
    if ($Listener.LocalAddress -notin @('127.0.0.1','::1')) {
        Stop-Service -Name sshd
        throw 'Unexpected non-loopback listener detected. sshd was stopped.'
    }
}
Write-Output 'OpenSSH installed and listening only on loopback. Windows/OpenSSH public-key authorization is still required.'
