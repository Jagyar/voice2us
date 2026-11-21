// Voice2Us — голосовой чат с друзьями: звук идёт напрямую между машинами
// по UDP (ChaCha20-Poly1305, несжатый PCM). Крошечный сервер-«встречалка»
// нужен только чтобы друзья нашли друг друга и видели, кто онлайн.
package main

import (
	"fmt"
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	"github.com/Jagyar/voice2us/internal/audio"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--devices" {
		listDevices()
		return
	}

	a := app.NewWithID("ru.jagyar.voice2us")
	a.Settings().SetTheme(newAppTheme())
	w := a.NewWindow("Voice2Us")
	w.Resize(fyne.NewSize(420, 760))
	w.CenterOnScreen()

	s := newAppState(a, w)
	s.buildUI()
	s.loadPrefs()
	s.startBackground()
	w.ShowAndRun()
}

// listDevices prints audio endpoints and exits — handy for debugging
// without opening the GUI.
func listDevices() {
	e, err := audio.NewEngine()
	if err != nil {
		fmt.Fprintln(os.Stderr, "не удалось инициализировать аудио:", err)
		os.Exit(1)
	}
	defer e.Close()

	caps, err := e.Devices(audio.KindCapture)
	if err != nil {
		fmt.Fprintln(os.Stderr, "перечисление микрофонов:", err)
		os.Exit(1)
	}
	fmt.Println("Микрофоны:")
	for _, d := range caps {
		if d.IsDefault {
			fmt.Printf("  * %s (по умолчанию)\n", d.Name)
		} else {
			fmt.Printf("    %s\n", d.Name)
		}
	}

	plays, err := e.Devices(audio.KindPlayback)
	if err != nil {
		fmt.Fprintln(os.Stderr, "перечисление устройств вывода:", err)
		os.Exit(1)
	}
	fmt.Println("Динамики и наушники:")
	for _, d := range plays {
		if d.IsDefault {
			fmt.Printf("  * %s (по умолчанию)\n", d.Name)
		} else {
			fmt.Printf("    %s\n", d.Name)
		}
	}
}
