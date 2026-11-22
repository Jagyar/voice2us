package main

import (
	"net/http/httptest"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/Jagyar/voice2us/internal/update"

	"github.com/Jagyar/voice2us/internal/rdv"
)

// Запуск по реальному пути (buildUI → loadPrefs) с настройками, какие остаются
// от прошлых версий: сохранённое имя, друг из 1.x. Раньше здесь была паника
// (обработчик имени обращался к ещё не созданному менеджеру статусов).
func TestStartupWithSavedPrefs(t *testing.T) {
	ts := httptest.NewServer(rdv.NewServer().Handler())
	defer ts.Close()

	a := test.NewApp()
	p := a.Preferences()
	p.SetString("name", "Дмитрий")
	p.SetString("rdvURL", ts.URL)
	p.SetString("micDev", "что-то несуществующее")
	p.SetString("friend", `{"name":"Аня","role":97,"key":"`+
		"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="+`","peer":{"p":["1.2.3.4:5"]}}`)

	w := a.NewWindow("Voice2Us")
	s := newAppState(a, w)
	s.buildUI()
	s.loadPrefs()
	defer s.shutdown()

	if len(s.friends) != 1 || s.friends[0].Name != "Аня" {
		t.Fatalf("друг из 1.x не подхватился: %+v", s.friends)
	}
	if s.nameEntry.Text != "Дмитрий" {
		t.Fatal("имя не загрузилось")
	}
}

func TestStartupFreshInstall(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("Voice2Us")
	s := newAppState(a, w)
	s.buildUI()
	s.loadPrefs()
	s.shutdown()
}

func TestUpdateBanner(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("Voice2Us")
	s := newAppState(a, w)
	s.buildUI()
	old := version
	version = "2.0.1"
	defer func() { version = old }()

	s.onUpdateResult(&update.Release{Tag: "v2.1.0", URL: "https://github.com/x/y/releases/tag/v2.1.0"}, nil, false)
	if len(s.updateBox.Objects) != 1 {
		t.Fatal("плашка должна появиться")
	}
	s.prefs.SetString(prefSkipVersion, "v2.1.0")
	s.hideUpdateBanner()
	s.onUpdateResult(&update.Release{Tag: "v2.1.0", URL: "u"}, nil, false)
	if len(s.updateBox.Objects) != 0 {
		t.Fatal("после «Позже» плашка этой версии не возвращается")
	}
	s.onUpdateResult(&update.Release{Tag: "v2.2.0", URL: "https://github.com/x/y/releases/tag/v2.2.0"}, nil, false)
	if len(s.updateBox.Objects) != 1 {
		t.Fatal("более новая версия должна снова показаться")
	}
	s.onUpdateResult(&update.Release{Tag: "v2.0.1", URL: "u"}, nil, false)
	if len(s.updateBox.Objects) != 0 {
		t.Fatal("актуальная версия — плашка скрыта")
	}
}

// Ошибка запуска сессии не должна оставлять интерфейс в «занято» навсегда.
func TestFailedStartReturnsToIdle(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("Voice2Us")
	s := newAppState(a, w)
	s.buildUI()
	s.engine = nil
	s.friends = []*friend{{ID: "1", Name: "Аня"}}
	s.phase = phaseCalling
	s.callFriend = s.friends[0]
	s.startSession(nil, false)
	if s.phase != phaseIdle || s.callFriend != nil {
		t.Fatalf("после ошибки старта должен быть phaseIdle, а не %v", s.phase)
	}
	if s.statusLabel.Text == "" {
		t.Fatal("пользователь должен увидеть причину")
	}
}

func TestConnectingStrip(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("Voice2Us")
	s := newAppState(a, w)
	s.buildUI()
	s.showConnecting("Соединяемся…")
	if len(s.connectBox.Objects) != 1 {
		t.Fatal("плашка «Соединяемся… Отмена» должна появиться")
	}
	s.hideConnecting()
	if len(s.connectBox.Objects) != 0 {
		t.Fatal("плашка должна исчезнуть")
	}
}
