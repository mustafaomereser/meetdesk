// Package tray: macOS'ta menü çubuğu simgesi henüz yok.
package tray

type Item struct {
	Label    string
	Checked  bool
	Disabled bool
	Bold     bool
	OnClick  func()
}

type Options struct {
	Icon    []byte
	Tooltip string
	OnClick func()
	Menu    func() []Item
}

func Start(o Options) bool      { return false }
func SetTooltip(s string)       {}
func Notify(title, text string) {}
func Stop()                     {}
