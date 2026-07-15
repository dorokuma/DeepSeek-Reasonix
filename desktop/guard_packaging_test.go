package main

import (
	"os"
	"strings"
	"testing"
)

func TestDesktopPackagesUseGuardAsDefaultLauncher(t *testing.T) {
	buildData, err := os.ReadFile("../scripts/desktop-build.sh")
	if err != nil {
		t.Fatal(err)
	}
	build := string(buildData)
	for _, want := range []string{
		`cp "$guard_out" "$app/Contents/MacOS/$GUARDNAME"`,
		`Set :CFBundleExecutable $GUARDNAME`,
		`cp "$portable" "$staging/$BINNAME.exe"`,
		`-H windowsgui`,
		`cp "$launcher_out" "$staging/${APPNAME}.exe"`,
		`cp "$guard_out" "$staging/$GUARDNAME.exe"`,
	} {
		if !strings.Contains(build, want) {
			t.Errorf("desktop-build.sh missing guard launcher contract %q", want)
		}
	}

	linuxData, err := os.ReadFile("build/linux/reasonix.desktop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(linuxData), "Exec=reasonix-guard launch --detach") {
		t.Error("Linux desktop entry does not launch through Reasonix Guard")
	}

	windowsData, err := os.ReadFile("build/windows/installer/project.nsi")
	if err != nil {
		t.Fatal(err)
	}
	windows := string(windowsData)
	for _, want := range []string{
		`CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${REASONIX_LAUNCHER}" "launch --detach"`,
		`CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${REASONIX_LAUNCHER}" "launch --detach"`,
	} {
		if !strings.Contains(windows, want) {
			t.Errorf("Windows installer missing guard shortcut contract %q", want)
		}
	}
}
