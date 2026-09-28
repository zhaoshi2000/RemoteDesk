#requires -Version 5.1
#requires -RunAsAdministrator
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$AgentExe,
    [Parameter(Mandatory=$true)][string]$StateDirectory,
    [switch]$AllowPrivateLAN,
    [switch]$InstallOpenSSH
)
$ErrorActionPreference = 'Stop'
$AgentExe = (Resolve-Path -LiteralPath $AgentExe).Path
$StateDirectory = (Resolve-Path -LiteralPath $StateDirectory).Path
foreach ($Name in @('agent.json','identity.key','trusted.json')) {
    if (-not (Test-Path -LiteralPath (Join-Path $StateDirectory $Name) -PathType Leaf)) { throw "Missing $Name. Initialize/register the agent on THIS Windows computer first." }
}
$Bytes = [System.IO.File]::ReadAllBytes((Join-Path $StateDirectory 'identity.key'))
$Prefix = [System.Text.Encoding]::ASCII.GetString($Bytes,0,[Math]::Min($Bytes.Length,20))
if (-not $Prefix.StartsWith('RD-DPAPI-MACHINE-v1')) { throw 'The identity must be initialized on this Windows computer, not copied from a Linux build/test.' }
if (Get-Service -Name 'RemoteDeskAgent' -ErrorAction SilentlyContinue) { throw 'RemoteDeskAgent already exists. No service or keys were overwritten.' }
if ($InstallOpenSSH) { & (Join-Path $PSScriptRoot 'Install-OpenSSH.ps1'); if ($LASTEXITCODE -ne 0) { throw 'OpenSSH installation step failed.' } }
# Never run a LocalSystem service from Downloads or another user-writable executable path.
$ServiceBin = Join-Path $env:ProgramFiles 'RemoteDesk\ServiceBin'
$Parent = Split-Path -Parent $ServiceBin
foreach ($Path in @($Parent,$ServiceBin)) {
    if (Test-Path -LiteralPath $Path) {
        $Item = Get-Item -LiteralPath $Path -Force
        if (-not $Item.PSIsContainer -or ($Item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw "Service program directory must be a real directory: $Path"
        }
    }
}
if (Test-Path -LiteralPath $ServiceBin) {
    if (@(Get-ChildItem -LiteralPath $ServiceBin -Force).Count -gt 0) {
        throw 'Protected ServiceBin is not empty. Refusing to overwrite an existing service binary.'
    }
}
New-Item -ItemType Directory -Path $ServiceBin -Force | Out-Null
$Acl = New-Object System.Security.AccessControl.DirectorySecurity
$Acl.SetAccessRuleProtection($true,$false)
$Admins = New-Object System.Security.Principal.SecurityIdentifier('S-1-5-32-544')
$System = New-Object System.Security.Principal.SecurityIdentifier('S-1-5-18')
$Users = New-Object System.Security.Principal.SecurityIdentifier('S-1-5-32-545')
$Acl.SetOwner($Admins)
foreach ($Principal in @($Admins,$System)) {
    $Rule = New-Object System.Security.AccessControl.FileSystemAccessRule($Principal,'FullControl','ContainerInherit,ObjectInherit','None','Allow')
    $Acl.AddAccessRule($Rule)
}
$ReadRule = New-Object System.Security.AccessControl.FileSystemAccessRule($Users,'ReadAndExecute','ContainerInherit,ObjectInherit','None','Allow')
$Acl.AddAccessRule($ReadRule)
Set-Acl -LiteralPath $ServiceBin -AclObject $Acl
$ProtectedAgent = Join-Path $ServiceBin 'remote-agent.exe'
Copy-Item -LiteralPath $AgentExe -Destination $ProtectedAgent -ErrorAction Stop
if ((Get-FileHash -LiteralPath $AgentExe -Algorithm SHA256).Hash -ne (Get-FileHash -LiteralPath $ProtectedAgent -Algorithm SHA256).Hash) {
    throw 'Service executable copy hash mismatch. No service was registered.'
}
$AgentExe = $ProtectedAgent
$BinaryPath = '"' + $AgentExe + '" service --state "' + $StateDirectory + '"'
& sc.exe create RemoteDeskAgent binPath= $BinaryPath start= auto DisplayName= 'RemoteDesk Agent'
if ($LASTEXITCODE -ne 0) { throw 'Service registration failed.' }
& sc.exe description RemoteDeskAgent 'RemoteDesk authorized device connectivity and SSH tunnel. No interactive Session-0 desktop capture.'
if ($LASTEXITCODE -ne 0) { throw 'Service description failed.' }
& sc.exe failure RemoteDeskAgent reset= 86400 actions= restart/5000/restart/15000/restart/60000
if ($LASTEXITCODE -ne 0) { throw 'Service recovery policy failed.' }
if ($AllowPrivateLAN) {
    New-NetFirewallRule -Name 'RemoteDesk-Agent-TCP' -DisplayName 'RemoteDesk authenticated peers TCP (private LAN)' -Direction Inbound -Program $AgentExe -Protocol TCP -Profile Private,Domain -Action Allow | Out-Null
    New-NetFirewallRule -Name 'RemoteDesk-Agent-UDP' -DisplayName 'RemoteDesk signed UDP probes (private LAN)' -Direction Inbound -Program $AgentExe -Protocol UDP -Profile Private,Domain -Action Allow | Out-Null
}
Start-Service -Name RemoteDeskAgent
Write-Output 'RemoteDeskAgent service installed. Inspect service health and the coordinator dashboard before enabling unattended use.'
