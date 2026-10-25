# -*- coding: utf-8 -*-
Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"

!ifndef REPO_ROOT
  !error "Build with scripts/build-installer.ps1 to provide the verified inputs."
!endif
; Override only for isolated installer acceptance tests, never on release builds.
!ifndef PRODUCT_KEY
  !define PRODUCT_KEY "Software\OpenListFS"
!endif
!ifndef UNINSTALL_KEY
  !define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\OpenListFS"
!endif
!ifndef MENU_FOLDER
  !define MENU_FOLDER "OpenListFS"
!endif

Name "OpenListFS"
OutFile "${OUTPUT_FILE}"
InstallDir "$LOCALAPPDATA\Programs\OpenListFS"
InstallDirRegKey HKCU "${PRODUCT_KEY}" "InstallDir"
RequestExecutionLevel user
AllowSkipFiles off
SetCompressor /SOLID lzma
ShowInstDetails show
ShowUninstDetails show
VIProductVersion "${FILE_VERSION}"
VIAddVersionKey /LANG=1033 "ProductName" "OpenListFS"
VIAddVersionKey /LANG=1033 "FileDescription" "OpenListFS installer with official WinFsp"
VIAddVersionKey /LANG=1033 "FileVersion" "${PRODUCT_VERSION}"
VIAddVersionKey /LANG=1033 "ProductVersion" "${PRODUCT_VERSION}"
VIAddVersionKey /LANG=1033 "LegalCopyright" "OpenListFS contributors"

!define MUI_ABORTWARNING
!define MUI_ICON "${REPO_ROOT}\assets\icon.ico"
!define MUI_UNICON "${REPO_ROOT}\assets\icon.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\openlistfs-gui.exe"
!define MUI_FINISHPAGE_RUN_NOTCHECKED
!define MUI_FINISHPAGE_RUN_TEXT "Launch OpenListFS"
!define MUI_WELCOMEPAGE_TEXT "This setup installs OpenListFS for your Windows account.$\r$\n$\r$\nIt includes the official WinFsp filesystem driver. A compatible WinFsp installation is reused. If installation or repair is needed, Windows will request administrator approval.$\r$\n$\r$\nClose OpenListFS and unmount its drives before continuing."
!insertmacro MUI_PAGE_WELCOME
!define MUI_PAGE_CUSTOMFUNCTION_LEAVE ValidateInstallDir
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Var DependencyStatus
Var MsiExitCode
Var DependencyLog
Var CheckOutput
Var ExistingInstallDir

!macro RequireClosedBinary BINARY
  ${If} ${FileExists} "${BINARY}"
    ; Append mode opens for read/write while preserving all existing bytes.
    ; Running executables cannot be opened for writing on Windows.
    ClearErrors
    FileOpen $0 "${BINARY}" a
    ${If} ${Errors}
      MessageBox MB_OK|MB_ICONSTOP "OpenListFS files are in use or cannot be replaced. Close OpenListFS, unmount its drives, and check directory permissions before running setup again."
      SetErrorLevel 2
      Abort
    ${EndIf}
    FileClose $0
  ${EndIf}
!macroend

Function .onInit
  SetShellVarContext current
  ${IfNot} ${RunningX64}
    MessageBox MB_OK|MB_ICONSTOP "OpenListFS requires 64-bit Windows."
    SetErrorLevel 13
    Abort
  ${EndIf}
  ReadRegStr $ExistingInstallDir HKCU "${PRODUCT_KEY}" "InstallDir"
FunctionEnd

Function ValidateInstallDir
  ; Upgrades retain the registered path and portable configuration next to it.
  ; Changing an existing path would strand profiles and leave a second copy.
  ${If} $ExistingInstallDir != ""
  ${AndIf} $INSTDIR != $ExistingInstallDir
    MessageBox MB_OK|MB_ICONEXCLAMATION "OpenListFS is already installed at $ExistingInstallDir. Upgrade in that directory to preserve your profiles. To relocate, back up your profiles and uninstall the old copy first."
    StrCpy $INSTDIR $ExistingInstallDir
    Abort
  ${EndIf}
FunctionEnd

Function EnsureWinFsp
  InitPluginsDir
  SetOutPath "$PLUGINSDIR"
  File /oname=winfsp-check.exe "${BIN_DIR}\winfsp-check.exe"
  SetOutPath "$PLUGINSDIR\dependencies"
  File /oname=winfsp.msi "${WINFSP_MSI}"
  SetOutPath "$PLUGINSDIR"
  ; nsExec suppresses a console window; --quiet avoids unnecessary output.
  nsExec::ExecToStack '"$PLUGINSDIR\winfsp-check.exe" --install-plan --quiet'
  Pop $DependencyStatus
  Pop $CheckOutput
  ${If} $DependencyStatus == "error"
  ${OrIf} $DependencyStatus == "timeout"
    MessageBox MB_OK|MB_ICONSTOP "Could not inspect WinFsp. OpenListFS setup has stopped."
    SetErrorLevel 12
    Abort
  ${EndIf}
  ${If} $DependencyStatus == 0
    DetailPrint "Compatible WinFsp found. Reusing the existing installation."
    Return
  ${ElseIf} $DependencyStatus == 14
    MessageBox MB_OK|MB_ICONSTOP "The existing WinFsp installation cannot safely be repaired with the bundled version. Setup will not downgrade or remove shared WinFsp. Repair the installed WinFsp version from https://winfsp.dev/rel/ and run setup again."
    SetErrorLevel 14
    Abort
  ${ElseIf} $DependencyStatus == 13
    MessageBox MB_OK|MB_ICONSTOP "WinFsp does not support this Windows architecture. OpenListFS setup has stopped."
    SetErrorLevel 13
    Abort
  ${ElseIf} $DependencyStatus != 10
  ${AndIf} $DependencyStatus != 11
  ${AndIf} $DependencyStatus != 12
    MessageBox MB_OK|MB_ICONSTOP "WinFsp inspection failed (code $DependencyStatus). OpenListFS setup has stopped."
    SetErrorLevel 12
    Abort
  ${EndIf}

  StrCpy $DependencyLog "$TEMP\OpenListFS-WinFsp-install.log"
  ${If} $DependencyStatus == 12
    DetailPrint "Repairing WinFsp. Windows may request administrator approval."
  ${ElseIf} $DependencyStatus == 11
    DetailPrint "Upgrading WinFsp. Windows may request administrator approval."
  ${Else}
    DetailPrint "Installing WinFsp. Windows may request administrator approval."
  ${EndIf}
  ; The checker verifies the embedded MSI hash and refuses downgrades again
  ; immediately before invoking only msiexec with runas. The app installer
  ; continues under the original user's identity.
  nsExec::ExecToStack '"$PLUGINSDIR\winfsp-check.exe" --install --quiet'
  Pop $MsiExitCode
  Pop $CheckOutput
  ${If} $MsiExitCode == "error"
  ${OrIf} $MsiExitCode == "timeout"
    MessageBox MB_OK|MB_ICONSTOP "Could not start WinFsp installation. OpenListFS setup has stopped."
    SetErrorLevel 12
    Abort
  ${EndIf}
  ${If} $MsiExitCode == 3010
    SetRebootFlag true
    DetailPrint "WinFsp installation succeeded. Restart Windows before mounting a drive."
  ${ElseIf} $MsiExitCode == 0
    DetailPrint "WinFsp installation succeeded."
  ${ElseIf} $MsiExitCode == 1602
    MessageBox MB_OK|MB_ICONSTOP "WinFsp installation was cancelled. The filesystem driver is required to mount drives. OpenListFS setup has stopped."
    SetErrorLevel 1602
    Abort
  ${Else}
    DetailPrint "$CheckOutput"
    MessageBox MB_OK|MB_ICONSTOP "WinFsp installation failed (code $MsiExitCode). Check $DependencyLog and run setup again. OpenListFS setup has stopped."
    SetErrorLevel 12
    Abort
  ${EndIf}
  nsExec::ExecToStack '"$PLUGINSDIR\winfsp-check.exe" --quiet'
  Pop $DependencyStatus
  Pop $CheckOutput
  ${If} $DependencyStatus == "error"
  ${OrIf} $DependencyStatus == "timeout"
    MessageBox MB_OK|MB_ICONSTOP "Could not verify WinFsp after installation. Check $DependencyLog and run setup again."
    SetErrorLevel 12
    Abort
  ${EndIf}
  ${If} $DependencyStatus != 0
    ${If} $MsiExitCode == 3010
      MessageBox MB_OK|MB_ICONINFORMATION "WinFsp requires a Windows restart. OpenListFS will be installed, but mounting is unavailable until after restart."
    ${Else}
      MessageBox MB_OK|MB_ICONSTOP "WinFsp is still unavailable after installation (code $DependencyStatus). Check $DependencyLog and repair WinFsp before running setup again."
      SetErrorLevel 12
      Abort
    ${EndIf}
  ${EndIf}
FunctionEnd

Section "OpenListFS" SEC_APP
  ; Also enforce the path policy during silent /D= installations.
  ${If} $ExistingInstallDir != ""
  ${AndIf} $INSTDIR != $ExistingInstallDir
    ${If} ${Silent}
      SetErrorLevel 2
      Quit
    ${EndIf}
    Call ValidateInstallDir
  ${EndIf}
  ; Check before replacing any existing application files.
  !insertmacro RequireClosedBinary "$INSTDIR\openlistfs-gui.exe"
  !insertmacro RequireClosedBinary "$INSTDIR\openlistfs.exe"
  !insertmacro RequireClosedBinary "$INSTDIR\winfsp-check.exe"
  Call EnsureWinFsp
  SetOutPath "$INSTDIR"
  SetOverwrite on
  ; Explicit allowlist: never include developer .env or profiles.json.
  File /oname=openlistfs-gui.exe "${GUI_BINARY}"
  File "${BIN_DIR}\openlistfs.exe"
  File "${BIN_DIR}\winfsp-check.exe"
  File /oname=THIRD_PARTY_NOTICES.md "${THIRD_PARTY_NOTICES}"
  SetOutPath "$INSTDIR\dependencies"
  File /oname=winfsp.msi "${WINFSP_MSI}"
  File /oname=winfsp.json "${WINFSP_MANIFEST}"
  File /oname=WinFsp-License.txt "${WINFSP_LICENSE}"
  SetOutPath "$INSTDIR"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr HKCU "${PRODUCT_KEY}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "OpenListFS"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${PRODUCT_VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\openlistfs-gui.exe"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '$\"$INSTDIR\uninstall.exe$\"'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
  CreateDirectory "$SMPROGRAMS\${MENU_FOLDER}"
  CreateShortcut "$SMPROGRAMS\${MENU_FOLDER}\OpenListFS.lnk" "$INSTDIR\openlistfs-gui.exe"
  CreateShortcut "$SMPROGRAMS\${MENU_FOLDER}\Uninstall.lnk" "$INSTDIR\uninstall.exe"
SectionEnd

Section "Uninstall"
  SetShellVarContext current
  !insertmacro RequireClosedBinary "$INSTDIR\openlistfs-gui.exe"
  !insertmacro RequireClosedBinary "$INSTDIR\openlistfs.exe"
  !insertmacro RequireClosedBinary "$INSTDIR\winfsp-check.exe"
  ; Remove only known application files. Keep user profiles, .env and any
  ; other user files. Never invoke WinFsp's uninstaller or remove its driver.
  ClearErrors
  Delete "$INSTDIR\openlistfs-gui.exe"
  ${If} ${Errors}
    MessageBox MB_OK|MB_ICONSTOP "Close OpenListFS and unmount its drives before uninstalling, then run the uninstaller again."
    SetErrorLevel 2
    Abort
  ${EndIf}
  Delete "$INSTDIR\openlistfs.exe"
  Delete "$INSTDIR\winfsp-check.exe"
  Delete "$INSTDIR\THIRD_PARTY_NOTICES.md"
  Delete "$INSTDIR\dependencies\winfsp.msi"
  Delete "$INSTDIR\dependencies\winfsp.json"
  Delete "$INSTDIR\dependencies\WinFsp-License.txt"
  RMDir "$INSTDIR\dependencies"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\${MENU_FOLDER}\OpenListFS.lnk"
  Delete "$SMPROGRAMS\${MENU_FOLDER}\Uninstall.lnk"
  RMDir "$SMPROGRAMS\${MENU_FOLDER}"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
  DeleteRegKey HKCU "${PRODUCT_KEY}"
  ReadRegStr $0 HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "OpenListFS"
  ${If} $0 == '$\"$INSTDIR\openlistfs-gui.exe$\" --minimized --automount'
    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "OpenListFS"
  ${EndIf}
  DetailPrint "OpenListFS removed. User configuration and shared WinFsp were retained."
SectionEnd
