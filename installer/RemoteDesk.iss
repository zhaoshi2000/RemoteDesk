; Full user-session client package. No default unattended password is installed.
#define MyAppVersion "0.3.0"
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
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\RemoteDesk.exe
[Files]
Source: "{#BundleDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs
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
procedure CurStepChanged(CurStep: TSetupStep);
begin
 if CurStep=ssPostInstall then
  MsgBox('设备密钥将在当前用户首次初始化时生成，不含公共默认密码。完整桌面需要交互用户会话和支持硬件编解码的 GPU；VC++ 2015-2022 x64 运行库须已安装。已有 SSH 配置若非回环限定，脚本会拒绝修改。',mbInformation,MB_OK);
end;
; User identities and configuration live outside Program Files and are preserved at uninstall.
; A SYSTEM identity-only service can be installed explicitly with the supplied service script.
