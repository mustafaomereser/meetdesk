//go:build darwin

package capture

// Program gizleme şimdilik yalnızca Windows'ta.
const HideSupported = false

type RunningApp struct {
	Exe   string `json:"exe"`
	Title string `json:"title"`
}

type HideRule struct {
	Exe   string
	Title string
}

func SetHideOwn(bool)            {}
func SetHidden([]HideRule)       {}
func CleanTitle(t string) string { return t }
func Apps() []RunningApp         { return nil }
