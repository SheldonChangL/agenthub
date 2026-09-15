Unicode true

####
## Please note: Template replacements don't work in this file. They are provided with default defines like
## mentioned underneath.
## If the keyword is not defined, "wails_tools.nsh" will populate them with the values from ProjectInfo.
## If they are defined here, "wails_tools.nsh" will not touch them. This allows to use this project.nsi manually
## from outside of Wails for debugging and development of the installer.
##
## For development first make a wails nsis build to populate the "wails_tools.nsh":
## > wails build --target windows/amd64 --nsis
## Then you can call makensis on this file with specifying the path to your binary:
## For a AMD64 only installer:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe
## For a ARM64 only installer:
## > makensis -DARG_WAILS_ARM64_BINARY=..\..\bin\app.exe
## For a installer with both architectures:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app-amd64.exe -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe
####
## The following information is taken from the ProjectInfo file, but they can be overwritten here.
####
## !define INFO_PROJECTNAME    "MyProject" # Default "{{.Name}}"
## !define INFO_COMPANYNAME    "MyCompany" # Default "{{.Info.CompanyName}}"
## !define INFO_PRODUCTNAME    "MyProduct" # Default "{{.Info.ProductName}}"
## !define INFO_PRODUCTVERSION "1.0.0"     # Default "{{.Info.ProductVersion}}"
## !define INFO_COPYRIGHT      "Copyright" # Default "{{.Info.Copyright}}"
###
## !define PRODUCT_EXECUTABLE  "Application.exe"      # Default "${INFO_PROJECTNAME}.exe"
## !define UNINST_KEY_NAME     "UninstKeyInRegistry"  # Default "${INFO_COMPANYNAME}${INFO_PRODUCTNAME}"
####
## !define REQUEST_EXECUTION_LEVEL "admin"            # Default "admin"  see also https://nsis.sourceforge.io/Docs/Chapter4.html
####
## Include the wails tools
####
!include "wails_tools.nsh"

# The version information for this two must consist of 4 parts
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

# Enable HiDPI support. https://nsis.sourceforge.io/Reference/ManifestDPIAware
ManifestDPIAware true

!include "MUI.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
# !define MUI_WELCOMEFINISHPAGE_BITMAP "resources\leftimage.bmp" #Include this to add a bitmap on the left side of the Welcome Page. Must be a size of 164x314
!define MUI_FINISHPAGE_NOAUTOCLOSE # Wait on the INSTFILES page so the user can take a look into the details of the installation steps
!define MUI_ABORTWARNING # This will warn the user if they exit from the installer.

!insertmacro MUI_PAGE_WELCOME # Welcome to the installer page.
# The skill section below writes into the user's own ~/.claude, which is not
# this application's to change. A components page is how that gets asked.
!insertmacro MUI_PAGE_COMPONENTS
# !insertmacro MUI_PAGE_LICENSE "resources\eula.txt" # Adds a EULA page to the installer
!insertmacro MUI_PAGE_DIRECTORY # In which folder install page.
!insertmacro MUI_PAGE_INSTFILES # Installing page.
!insertmacro MUI_PAGE_FINISH # Finished installation page.

!insertmacro MUI_UNPAGE_INSTFILES # Uinstalling page

!insertmacro MUI_LANGUAGE "English" # Set the Language of the installer

## The following two statements can be used to sign the installer and the uninstaller. The path to the binaries are provided in %1
#!uninstfinalize 'signtool --file "%1"'
#!finalize 'signtool --file "%1"'

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe" # Name of the installer's file.
!ifdef WAILS_INSTALL_SCOPE
  !if "${WAILS_INSTALL_SCOPE}" == "user"
    InstallDir "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"
  !else
    InstallDir "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}"
  !endif
!else
  InstallDir "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}"
!endif # Default installing folder ($PROGRAMFILES is Program Files folder).
ShowInstDetails show # This will always show the installation details.

Function .onInit
   !insertmacro wails.checkArchitecture
FunctionEnd

Section "AgentHub" SecCore
    SectionIn RO
    !insertmacro wails.setShellContext

    !insertmacro wails.webview2runtime

    # A running agenthub-node.exe holds its own file open, and Windows refuses
    # to overwrite a running executable — so on a reinstall or an upgrade the
    # copy below fails unless the old one is stopped first. /F because the node
    # has no window to close politely; it keeps no unflushed state of its own.
    # Both are expected to fail on a first install, where there is nothing to
    # stop, which is why neither result is checked.
    nsExec::Exec 'taskkill /F /IM agenthub-node.exe'
    Pop $0
    nsExec::Exec 'taskkill /F /IM agenthub-desktop.exe'
    Pop $0

    SetOutPath $INSTDIR

    !insertmacro wails.files

    # wails.files installs the app executable and nothing else, but the app
    # resolves ah, agenthub-node and agenthub-mcp beside its own executable and
    # deliberately does not fall back to PATH (desktop/build/bundle-binaries.sh
    # says why). Without these three the installed app comes up and every
    # service and MCP action answers "ah was not found".
    #
    # They are taken from build/bin, where the postBuildHook has just put them,
    # so `wails build -platform windows/amd64 -nsis` is still one command.
    File "/oname=ah.exe" "..\..\bin\ah.exe"
    File "/oname=agenthub-node.exe" "..\..\bin\agenthub-node.exe"
    File "/oname=agenthub-mcp.exe" "..\..\bin\agenthub-mcp.exe"

    # The agenthub-watch skill, kept beside the app rather than installed into
    # Claude's configuration: this location belongs to this application, so
    # writing it needs nobody's permission. Copying it into ~/.claude is the
    # optional section below, because that directory is the user's.
    SetOutPath "$INSTDIR\skills\agenthub-watch"
    File "..\..\..\..\.claude\skills\agenthub-watch\SKILL.md"
    SetOutPath $INSTDIR

    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols

    # agenthub-node is the part that has to keep running; the desktop app is
    # only its front end. `ah service install` is what registers it — on
    # Windows a Task Scheduler job that runs at logon as this user (#65) — and
    # it also starts the node, so there is nothing to launch separately here.
    #
    # Through ah rather than by writing the registration here, because the same
    # command has to work for someone who never ran this installer, and two
    # implementations of one registration drift.
    nsExec::ExecToStack '"$INSTDIR\ah.exe" service install'
    Pop $0
    Pop $1
    DetailPrint "ah service install: $1"
    ${If} $0 != 0
        # Registration failed. The install itself is fine — every file is in
        # place and the app runs — so this does not abort it; what is missing
        # is only the part that starts the node at the next logon, and a
        # Startup shortcut still does that much. It cannot do Restart or
        # Status, which is why it is the fallback and not the plan.
        #
        # Current user, not all users: node.key is sealed with DPAPI against
        # the user who created it, so a copy started as anyone else cannot open
        # it. SetShellVarContext current pins $SMSTARTUP to this user's own
        # Startup folder even when the install scope is machine-wide.
        #
        # Minimised rather than hidden: agenthub-node is a console program, and
        # hiding it would need the binary built with -H windowsgui, which also
        # takes away the only place a Windows owner can see why it will not
        # start. `ah service install` avoids the window a different way — the
        # task runs `ah service run-node`, which starts the node detached and
        # returns — but that route is exactly what has just failed.
        SetShellVarContext current
        CreateShortCut "$SMSTARTUP\${INFO_PRODUCTNAME} Node.lnk" "$INSTDIR\agenthub-node.exe" "" "" 0 SW_SHOWMINIMIZED
        !insertmacro wails.setShellContext
        Exec '"$INSTDIR\agenthub-node.exe"'
        MessageBox MB_ICONEXCLAMATION|MB_OK "AgentHub is installed and will run.$\r$\n$\r$\nRegistering the node with Task Scheduler did not succeed, so it has been set to start from your Startup folder instead. The app's background-service panel will not be able to restart it or report its status.$\r$\n$\r$\nWhat the command said:$\r$\n$1"
    ${EndIf}

    !insertmacro wails.writeUninstaller
SectionEnd

# Unchecked by default, and deliberately: ~/.claude holds the user's own skills,
# and an installer that adds to it without being asked is indistinguishable from
# one that overwrites something they wrote. The marker file is what lets the
# uninstaller tell its own copy from theirs.
Section /o "agenthub-watch skill for Claude Code" SecSkill
    SetShellVarContext current
    SetOutPath "$PROFILE\.claude\skills\agenthub-watch"
    File "..\..\..\..\.claude\skills\agenthub-watch\SKILL.md"
    FileOpen $0 "$PROFILE\.claude\skills\agenthub-watch\.installed-by-agenthub" w
    FileWrite $0 "Written by the AgentHub installer. Delete this file to keep the skill$\r$\n"
    FileWrite $0 "when AgentHub is uninstalled.$\r$\n"
    FileClose $0
    SetOutPath $INSTDIR
    !insertmacro wails.setShellContext
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
    !insertmacro MUI_DESCRIPTION_TEXT ${SecCore} "The AgentHub desktop app, the node that keeps running, and the ah and agenthub-mcp command line tools."
    !insertmacro MUI_DESCRIPTION_TEXT ${SecSkill} "Copies the agenthub-watch skill into $PROFILE\.claude\skills so Claude Code can use it anywhere on this machine. Leave this unticked to keep your own .claude directory untouched; a copy is always installed beside the app either way."
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Section "uninstall"
    !insertmacro wails.setShellContext

    # RMDir /r cannot remove a directory holding a running executable, so an
    # uninstall with the node still running leaves the install directory and
    # its binaries behind while reporting success.
    # Before the files go: `ah service uninstall` ends the task and removes the
    # registration, and it needs ah.exe to still be there to do it. A failure
    # is not allowed to stop the uninstall — a machine that cannot deregister
    # is still a machine the owner asked to be rid of this — so the result is
    # only printed.
    nsExec::ExecToStack '"$INSTDIR\ah.exe" service uninstall'
    Pop $0
    Pop $1
    DetailPrint "ah service uninstall: $1"

    nsExec::Exec 'taskkill /F /IM agenthub-node.exe'
    Pop $0
    nsExec::Exec 'taskkill /F /IM agenthub-desktop.exe'
    Pop $0

    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # Remove the WebView2 DataPath

    RMDir /r $INSTDIR

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"

    # Only the copy this installer wrote, identified by the marker it left. A
    # skill directory without the marker is the user's own — possibly a newer
    # one they cloned or edited — and removing it would be deleting their work
    # to tidy up after ours.
    IfFileExists "$PROFILE\.claude\skills\agenthub-watch\.installed-by-agenthub" 0 +2
        RMDir /r "$PROFILE\.claude\skills\agenthub-watch"

    # Either this installer's fallback wrote it, or an installer from before
    # the Task Scheduler registration existed did. `ah service install` removes
    # it too; both deleting it is idempotent, and neither of them running is
    # what leaves two nodes starting at logon.
    SetShellVarContext current
    Delete "$SMSTARTUP\${INFO_PRODUCTNAME} Node.lnk"
    Delete "$SMSTARTUP\AgentHub Node.lnk"
    !insertmacro wails.setShellContext

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    !insertmacro wails.deleteUninstaller
SectionEnd
