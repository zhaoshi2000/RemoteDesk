; Full user-session client package. No default unattended password is installed.
#define MyAppVersion "0.4.0"
#ifndef BundleDir
#define BundleDir "..\dist\windows-amd64"
#endif
[Setup]
AppId={{234A6EAC-9C68-4FEA-B5D0-0092C1D664AF}
AppName=RemoteDesk
AppVersion={#MyAppVersion}
DefaultDirName={autopf}\RemoteDesk
DefaultGroupName=RemoteDesk
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0.17763
OutputDir=..\release
OutputBaseFilename=RemoteDeskSetup
Compression=lzma2/fast
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\RemoteDesk.exe
[Files]
Source: "{#BundleDir}\*"; DestDir: "{app}"; Excludes: "redist\*"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "{#BundleDir}\redist\vc_redist.x64.exe"; Flags: dontcopy
[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; Flags: unchecked
Name: "openssh"; Description: "检测并安装本机 OpenSSH Server（仅回环监听；不改写已有非回环配置）"
[Icons]
Name: "{group}\RemoteDesk"; Filename: "{app}\RemoteDesk.exe"
Name: "{autodesktop}\RemoteDesk"; Filename: "{app}\RemoteDesk.exe"; Tasks: desktopicon
[Run]
Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoProfile -File ""{app}\scripts\Install-OpenSSH.ps1"""; Tasks: openssh; Flags: waituntilterminated; StatusMsg: "检测 Windows OpenSSH；本机回环模式"
Filename: "{app}\RemoteDesk.exe"; Description: "打开 RemoteDesk 并初始化设备"; Flags: nowait postinstall skipifsilent runasoriginaluser
[Code]
var RuntimeRestart: Boolean;
function PrepareToInstall(var NeedsRestart: Boolean): String;
var ResultCode: Integer;
begin
 Result := '';
 ExtractTemporaryFile('vc_redist.x64.exe');
 if not Exec(ExpandConstant('{tmp}\vc_redist.x64.exe'), '/install /quiet /norestart', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
  Result := 'Cannot start the Microsoft Visual C++ runtime installer.'
 else if (ResultCode <> 0) and (ResultCode <> 3010) and (ResultCode <> 1638) then
  Result := 'Microsoft Visual C++ runtime installation failed. Code: ' + IntToStr(ResultCode);
 RuntimeRestart := ResultCode = 3010;
end;
function NeedRestart(): Boolean;
begin
 Result := RuntimeRestart;
end;
; The installer never embeds credentials or overwrites private user identities.
; OpenSSH remains optional, as Windows optional-component installation requires a working source.
