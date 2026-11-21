package main

// Уведомление о новых версиях: при запуске и раз в час приложение спрашивает
// GitHub о последнем релизе. Только плашка со ссылкой — ничего не скачивается
// и не устанавливается само.

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Jagyar/voice2us/internal/update"
)

const (
	prefUpdateCheck = "updateCheck" // bool, по умолчанию включено
	prefSkipVersion = "skipVersion" // версия, о которой нажали «Позже»
	updateEvery     = time.Hour
)

// startUpdateChecks: первая проверка вскоре после запуска, дальше раз в час.
func (s *appState) startUpdateChecks(quit <-chan struct{}) {
	go func() {
		select {
		case <-time.After(3 * time.Second):
		case <-quit:
			return
		}
		for {
			fyne.Do(func() { s.checkUpdates(false) })
			select {
			case <-time.After(updateEvery):
			case <-quit:
				return
			}
		}
	}()
}

// checkUpdates запрашивает последний релиз. manual — нажали «Проверить
// сейчас»: тогда результат показывается всегда, даже «версия последняя».
func (s *appState) checkUpdates(manual bool) {
	if !manual && !s.prefs.BoolWithFallback(prefUpdateCheck, true) {
		return
	}
	if !update.Parse(version) {
		if manual {
			s.updateResult.SetText("Это сборка для разработки — обновления не проверяются.")
		}
		return
	}
	if manual {
		s.updateResult.SetText("Проверяем…")
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		rel, err := update.Latest(ctx, &http.Client{Timeout: 8 * time.Second}, update.LatestAPI, "Voice2Us/"+version)
		fyne.Do(func() { s.onUpdateResult(rel, err, manual) })
	}()
}

func (s *appState) onUpdateResult(rel *update.Release, err error, manual bool) {
	switch {
	case err != nil:
		if manual {
			s.updateResult.SetText("Не удалось проверить: " + err.Error())
		}
	case !update.Newer(version, rel.Tag):
		if manual {
			s.updateResult.SetText("У вас последняя версия (v" + version + ").")
		}
		s.hideUpdateBanner()
	case !manual && s.prefs.String(prefSkipVersion) == rel.Tag:
		// о ней уже нажали «Позже»
	default:
		if manual {
			s.updateResult.SetText("Доступна версия " + rel.Tag + ".")
		}
		s.showUpdateBanner(rel)
	}
}

func (s *appState) hideUpdateBanner() {
	s.updateBox.Objects = nil
	s.updateBox.Refresh()
}

func (s *appState) showUpdateBanner(rel *update.Release) {
	title := widget.NewLabelWithStyle("Доступна версия "+rel.Tag, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	sub := widget.NewLabel("Сейчас установлена v" + version)
	sub.Importance = widget.LowImportance

	download := widget.NewButtonWithIcon("Скачать", theme.DownloadIcon(), func() {
		if u, err := url.Parse(rel.URL); err == nil && u.Scheme == "https" {
			_ = s.app.OpenURL(u)
		}
	})
	download.Importance = widget.HighImportance
	later := widget.NewButton("Позже", func() {
		s.prefs.SetString(prefSkipVersion, rel.Tag)
		s.hideUpdateBanner()
	})

	s.updateBox.Objects = []fyne.CanvasObject{
		newCard(container.NewVBox(
			title, sub,
			container.NewGridWithColumns(2, download, later),
		)),
	}
	s.updateBox.Refresh()
}
