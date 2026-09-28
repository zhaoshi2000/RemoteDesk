#requires -Version 5.1
#requires -RunAsAdministrator
[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$Service = Get-Service -Name RemoteDeskAgent -ErrorAction SilentlyContinue
if ($Service) {
    if ($Service.Status -ne 'Stopped') { Stop-Service -Name RemoteDeskAgent -ErrorAction Stop }
    & sc.exe delete RemoteDeskAgent
    if ($LASTEXITCODE -ne 0) { throw 'Service removal failed.' }
}
foreach ($Name in @('RemoteDesk-Agent-TCP','RemoteDesk-Agent-UDP')) {
    if (Get-NetFirewallRule -Name $Name -ErrorAction SilentlyContinue) { Remove-NetFirewallRule -Name $Name }
}
Write-Output 'RemoteDesk service/rules removed. Device identity, configuration, and OpenSSH were preserved intentionally.'
