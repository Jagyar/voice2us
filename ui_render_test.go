package main

// Отрисовка экранов в PNG для визуальной проверки редизайна без дисплея:
//
//	V2U_SHOTS=/tmp/shots go test -run TestRenderScreens .
//
// Без переменной окружения тест пропускается.

import (
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/software"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"github.com/Jagyar/voice2us/internal/update"
)

// forced фиксирует вариант темы (светлый/тёмный) независимо от настроек ОС.
type forced struct {
	fyne.Theme
	v fyne.ThemeVariant
}

func (f forced) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return f.Theme.Color(n, f.v)
}

func TestRenderScreens(t *testing.T) {
	dir := os.Getenv("V2U_SHOTS")
	if dir == "" {
		t.Skip("V2U_SHOTS не задан")
	}
	_ = os.MkdirAll(dir, 0o755)

	for _, v := range []struct {
		name string
		v    fyne.ThemeVariant
	}{{"dark", theme.VariantDark}, {"light", theme.VariantLight}} {
		a := test.NewApp()
		a.Settings().SetTheme(forced{newAppTheme(), v.v})
		w := a.NewWindow("Voice2Us")
		s := newAppState(a, w)
		s.engine = nil
		s.buildUI()
		s.pres = newPresence()
		s.nameEntry.SetText("Дмитрий")
		s.micSelect.Options = []string{"MacBook Pro Microphone", "AirPods Pro"}
		s.micSelect.Selected = "AirPods Pro"
		s.spkSelect.Options = []string{"MacBook Pro Speakers", "AirPods Pro"}
		s.spkSelect.Selected = "AirPods Pro"
		s.micSlider.Value, s.spkSlider.Value = 100, 130
		s.micPct.SetText("100%")
		s.spkPct.SetText("130%")

		s.friends = []*friend{
			{ID: "1", Name: "Аня"}, {ID: "2", Name: "Борис"}, {ID: "3", Name: "Саша"}, {ID: "4", Name: "Катя"},
		}
		s.serverKnown, s.serverOK = true, true
		s.statuses = map[string]peerStatus{
			"1": {Online: true, State: stIdle},
			"2": {Online: false},
			"3": {Online: true, State: stInCall},
			"4": {Online: true, State: stCalling},
		}
		s.updateFooter()
		s.renderFriends()
		s.setStatus("Разговор завершён.")
		version = "2.0.1"
		s.showUpdateBanner(&update.Release{Tag: "v2.1.0", URL: "https://github.com/x/y"})
		shot(t, w, filepath.Join(dir, v.name+"-main.png"), v.v, a, 420, 880)

		s.friends = nil
		s.renderFriends()
		shot(t, w, filepath.Join(dir, v.name+"-empty.png"), v.v, a, 420, 760)

		s.showCall("Аня")
		s.rtt = 24
		s.updateCallStatus()
		shot(t, w, filepath.Join(dir, v.name+"-call.png"), v.v, a, 420, 560)
	}
}

func shot(t *testing.T, w fyne.Window, path string, v fyne.ThemeVariant, a fyne.App, width, height float32) {
	t.Helper()
	body := w.Content().(*container.Scroll).Content
	img := software.Render(container.NewGridWrap(fyne.NewSize(width, height), body), a.Settings().Theme())
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
