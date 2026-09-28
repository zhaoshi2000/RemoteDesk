; Inno Setup source for the COMMAND-LINE AGENT milestone, not the finished Qt remote desktop.
; Not compiled in the supplied environment. It deliberately does not enroll devices or install
; privileged services until the operator initializes a local identity and authorizes peers.
#define MyAppName "RemoteDesk Agent Engineering Preview"
#define MyAppVersion "0.1.0"
[Setup]
AppId={{8EDC05A3-4490-4C4F-A5B1-804D4E926681}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
DefaultDirName={autopf}\RemoteDesk
DefaultGroupName=RemoteDesk
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
OutputDir=..\dist\installer
OutputBaseFilename=RemoteDeskAgentSetup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
[Files]
Source: "..\dist\windows-amd64\remote-agent.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\scripts\Install-AgentService.ps1"; DestDir: "{app}\scripts"; Flags: ignoreversion
Source: "..\scripts\Install-OpenSSH.ps1"; DestDir: "{app}\scripts"; Flags: ignoreversion
Source: "..\scripts\Uninstall-AgentService.ps1"; DestDir: "{app}\scripts"; Flags: ignoreversion
Source: "..\docs\RUNBOOK.md"; DestDir: "{app}"; Flags: ignoreversion
[Icons]
Name: "{group}\RemoteDesk operation guide"; Filename: "{app}\RUNBOOK.md"
[UninstallRun]
Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoProfile -File ""{app}\scripts\Uninstall-AgentService.ps1"""; Flags: runhidden waituntilterminated; RunOnceId: "RemoveRemoteDeskAgentService"
