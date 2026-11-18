package sysutil

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
)

// AutoStartFlag, sistemle başlatıldığında uygulamaya verilen bayraktır.
const AutoStartFlag = "-autostart"

const agentLabel = "com.mustafaomereser.remotedesk"

func agentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist")
}

// AutoStart, oturum açılınca başlatma (LaunchAgent) kurulu mu.
func AutoStart() bool {
	_, err := os.Stat(agentPath())
	return err == nil
}

// SetAutoStart, ~/Library/LaunchAgents altına bir LaunchAgent yazar ya da siler.
// Uygulama paketi (.app) varsa "open -a" ile açılır (Dock'ta normal uygulama gibi görünür).
func SetAutoStart(on bool) error {
	p := agentPath()
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	var args []string
	if b := appBundle(); b != "" {
		args = []string{"/usr/bin/open", "-a", b, "--args", AutoStartFlag}
	} else {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		args = []string{exe, AutoStartFlag}
	}
	var sb strings.Builder
	for _, a := range args {
		fmt.Fprintf(&sb, "\t\t<string>%s</string>\n", html.EscapeString(a))
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + agentLabel + `</string>
	<key>ProgramArguments</key>
	<array>
` + sb.String() + `	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(plist), 0o644)
}
