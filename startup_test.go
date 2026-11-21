package main

import (
	"net/http/httptest"
	"testing"

	"fyne.io/fyne/v2/test"

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
