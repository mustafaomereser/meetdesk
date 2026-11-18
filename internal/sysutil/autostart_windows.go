//go:build windows

package sysutil

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runName = "Meetdesk"
const legacyRunName = "RemoteDesk" // eski sürümlerin kaydı

// AutoStartFlag, sistemle başlatıldığında uygulamaya verilen bayraktır (pencere simge
// durumunda açılır).
const AutoStartFlag = "-autostart"

// AutoStart, uygulama oturum açılınca kendiliğinden başlıyor mu (ve kayıt bu exe'yi mi
// gösteriyor).
func AutoStart() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runName)
	if err != nil {
		if v, _, err = k.GetStringValue(legacyRunName); err != nil {
			return false
		}
	}
	exe, err := os.Executable()
	return err == nil && strings.Contains(strings.ToLower(v), strings.ToLower(exe))
}

// SetAutoStart, oturum açılınca başlatmayı açar/kapatır (yönetici izni gerekmez: kullanıcının
// kendi "Run" anahtarı).
func SetAutoStart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	k.DeleteValue(legacyRunName)
	if !on {
		if err := k.DeleteValue(runName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runName, `"`+exe+`" `+AutoStartFlag)
}
