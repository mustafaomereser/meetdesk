package desktop

// macOS: bağlanılan bilgisayarın ekranında çizim katmanı henüz yok; çizimler izleyicilerin
// ekranında görünür.

func inkAdd(id string, laser bool, color string, width int, dx, dy, dw, dh int, pts []int, end bool) {
}
func inkRemove(id string) {}
func inkClear()           {}

// clickRipple: macOS'ta engellenen tıklama halkası henüz yok.
func clickRipple(x, y int) {}

// macOS: işaret dairesi ve gizlilik perdesi henüz yok.
func ping(x, y int, color string)            {}
func setPrivacy(on bool, onExit func()) bool { return !on }
