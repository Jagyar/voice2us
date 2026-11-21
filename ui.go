package main

// Весь интерфейс: главный экран (обмен кодами подключения) и экран
// разговора (устройства, громкость, mute).

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/Jagyar/voice2us/internal/audio"
	"github.com/Jagyar/voice2us/internal/proto"
	"github.com/Jagyar/voice2us/internal/stun"
)

type appState struct {
	app   fyne.App
	win   fyne.Window
	prefs fyne.Preferences

	engine *audio.Engine

	conn    *net.UDPConn
	myCode  *proto.Code
	session *proto.Session
	cancel  context.CancelFunc

	reasonMu        sync.Mutex // защищает stopReason: пишут и pump, и UI-поток
	stopReason      string
	updatingDevices bool

	// главный экран
	nameEntry     *widget.Entry
	myCodeEntry   *widget.Entry
	peerCodeEntry *widget.Entry
	createBtn     *widget.Button
	connectBtn    *widget.Button
	statusLabel   *widget.Label

	// экран разговора
	peerLabel  *widget.Label
	callStatus *widget.Label
	rttLabel   *widget.Label
	micSelect  *widget.Select
	spkSelect  *widget.Select
	micSlider  *widget.Slider
	spkSlider  *widget.Slider
	micPct     *widget.Label
	spkPct     *widget.Label
	micMute    *widget.Check
	hangupBtn  *widget.Button

	root       *fyne.Container // стек: главный экран + экран разговора
	mainScreen *fyne.Container
	callScreen *fyne.Container
}

func newAppState(a fyne.App, w fyne.Window) *appState {
	engine, err := audio.NewEngine()
	if err != nil {
		engine = nil // покажем ошибку в интерфейсе, не роняя приложение
	}
	return &appState{
		app:     a,
		win:     w,
		prefs:   a.Preferences(),
		engine:  engine,
	}
}

func (s *appState) buildUI() {
	s.nameEntry = widget.NewEntry()
	s.nameEntry.SetPlaceHolder("Как вас показать другу")

	s.myCodeEntry = widget.NewMultiLineEntry()
	s.myCodeEntry.SetPlaceHolder("Нажмите «Создать код»")
	s.myCodeEntry.Wrapping = fyne.TextWrapWord
	s.myCodeEntry.Disable()

	s.peerCodeEntry = widget.NewMultiLineEntry()
	s.peerCodeEntry.SetPlaceHolder("Вставьте сюда код друга")
	s.peerCodeEntry.Wrapping = fyne.TextWrapWord

	s.createBtn = widget.NewButton("Создать код", s.onCreateCode)
	copyBtn := widget.NewButton("Скопировать код", func() {
		if txt := s.myCodeEntry.Text; txt != "" {
			s.win.Clipboard().SetContent(txt)
		}
	})
	pasteBtn := widget.NewButton("Вставить из буфера", func() {
		s.peerCodeEntry.SetText(s.win.Clipboard().Content())
	})
	s.connectBtn = widget.NewButton("Продолжить", s.onPeerCode)

	hint := widget.NewLabel("Порядок действий:\n" +
		"1. Один нажимает «Создать код» и отправляет его другу в мессенджер.\n" +
		"2. Второй вставляет этот код, жмёт «Продолжить» — появляется ответный код, отправить его обратно.\n" +
		"3. Первый вставляет ответный код и жмёт «Продолжить».")
	hint.Wrapping = fyne.TextWrapWord

	s.statusLabel = widget.NewLabel("")
	s.statusLabel.Wrapping = fyne.TextWrapWord

	s.mainScreen = container.NewVBox(
		widget.NewLabel("Ваше имя:"),
		s.nameEntry,
		widget.NewSeparator(),
		container.NewGridWithColumns(2, s.createBtn, copyBtn),
		s.myCodeEntry,
		widget.NewSeparator(),
		pasteBtn,
		s.peerCodeEntry,
		s.connectBtn,
		widget.NewSeparator(),
		hint,
		s.statusLabel,
	)

	// --- экран разговора ---
	s.peerLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	s.callStatus = widget.NewLabel("")
	s.rttLabel = widget.NewLabel("")

	s.micSelect = widget.NewSelect(nil, func(name string) {
		if s.updatingDevices || s.engine == nil || name == "" {
			return
		}
		s.prefs.SetString("micDev", name)
		go func() {
			if err := s.engine.UseCaptureByName(name); err != nil {
				fyne.Do(func() { s.callStatus.SetText("Микрофон: " + err.Error()) })
			}
		}()
	})
	s.spkSelect = widget.NewSelect(nil, func(name string) {
		if s.updatingDevices || s.engine == nil || name == "" {
			return
		}
		s.prefs.SetString("spkDev", name)
		go func() {
			if err := s.engine.UsePlaybackByName(name); err != nil {
				fyne.Do(func() { s.callStatus.SetText("Динамик: " + err.Error()) })
			}
		}()
	})

	s.micPct = widget.NewLabel("100%")
	s.micSlider = widget.NewSlider(0, 200)
	s.micSlider.OnChanged = func(v float64) {
		s.micPct.SetText(fmt.Sprintf("%.0f%%", v))
		if s.engine != nil {
			s.engine.SetMicVolume(v / 100)
		}
		s.prefs.SetFloat("micVol", v)
	}
	s.spkPct = widget.NewLabel("100%")
	s.spkSlider = widget.NewSlider(0, 200)
	s.spkSlider.OnChanged = func(v float64) {
		s.spkPct.SetText(fmt.Sprintf("%.0f%%", v))
		if s.engine != nil {
			s.engine.SetSpkVolume(v / 100)
		}
		s.prefs.SetFloat("spkVol", v)
	}

	s.micMute = widget.NewCheck("Выключить микрофон (mute)", func(v bool) {
		if s.engine != nil {
			s.engine.SetMicMuted(v)
		}
	})

	s.hangupBtn = widget.NewButton("Завершить разговор", s.hangup)

	s.callScreen = container.NewVBox(
		s.peerLabel,
		s.callStatus,
		s.rttLabel,
		widget.NewSeparator(),
		widget.NewLabel("Микрофон:"),
		s.micSelect,
		container.NewHBox(s.micSlider, s.micPct),
		s.micMute,
		widget.NewLabel("Динамик / наушники:"),
		s.spkSelect,
		container.NewHBox(s.spkSlider, s.spkPct),
		widget.NewSeparator(),
		s.hangupBtn,
	)

	s.root = container.NewStack(s.mainScreen, s.callScreen)
	s.callScreen.Hide()
	s.win.SetContent(container.NewScroll(s.root))

	s.win.SetCloseIntercept(func() {
		s.shutdown()
		s.win.Close()
	})
}

func (s *appState) loadPrefs() {
	s.nameEntry.SetText(s.prefs.String("name"))
	micVol := s.prefs.FloatWithFallback("micVol", 100)
	spkVol := s.prefs.FloatWithFallback("spkVol", 100)
	s.micSlider.Value = micVol
	s.spkSlider.Value = spkVol
	s.micSlider.Refresh()
	s.spkSlider.Refresh()
	s.micPct.SetText(fmt.Sprintf("%.0f%%", micVol))
	s.spkPct.SetText(fmt.Sprintf("%.0f%%", spkVol))
	// Слайдеры выше только красят цифры — применяем громкости к движку сразу.
	if s.engine != nil {
		s.engine.SetMicVolume(micVol / 100)
		s.engine.SetSpkVolume(spkVol / 100)
	}
}

// --- главный экран ---

func (s *appState) onCreateCode() {
	if s.engine == nil {
		s.setStatus("Аудио недоступно: не удалось инициализировать звуковой subsystem.")
		return
	}
	s.prefs.SetString("name", s.nameEntry.Text)
	s.createBtn.Disable()
	s.connectBtn.Disable()
	s.setStatus("Определяем адреса (STUN)…")

	go func() {
		warn, err := s.ensureMyCode(proto.RoleA)
		fyne.Do(func() {
			s.createBtn.Enable()
			s.connectBtn.Enable()
			if err != nil {
				s.setStatus("Ошибка: " + err.Error())
				return
			}
			codeStr, err := proto.MarshalCode(s.myCode)
			if err != nil {
				s.setStatus("Ошибка: " + err.Error())
				return
			}
			s.myCodeEntry.SetText(codeStr)
			if warn != "" {
				s.setStatus("Код готов, но: " + warn)
				return
			}
			s.setStatus("Код готов — отправьте его другу и ждите ответного кода.")
		})
	}()
}

func (s *appState) onPeerCode() {
	if s.engine == nil {
		s.setStatus("Аудио недоступно: не удалось инициализировать звуковой subsystem.")
		return
	}
	peer, err := proto.UnmarshalCode(s.peerCodeEntry.Text)
	if err != nil {
		s.setStatus("Код друга не распознан: " + err.Error())
		return
	}
	if s.myCode != nil && s.myCode.Role == peer.Role && s.myCode.Name == peer.Name {
		s.setStatus("Это похоже на ваш собственный код — нужен код друга.")
		return
	}
	s.prefs.SetString("name", s.nameEntry.Text)
	s.createBtn.Disable()
	s.connectBtn.Disable()
	s.setStatus("Определяем адреса (STUN)…")

	go func() {
		warn, err := s.ensureMyCode(proto.Opposite(peer.Role))
		fyne.Do(func() {
			s.createBtn.Enable()
			s.connectBtn.Enable()
			if err != nil {
				s.setStatus("Ошибка: " + err.Error())
				return
			}
			codeStr, _ := proto.MarshalCode(s.myCode)
			s.myCodeEntry.SetText(codeStr)
			s.startSession(peer)
			if warn != "" {
				s.setStatus(s.statusLabel.Text + " " + warn)
			}
		})
	}()
}

// ensureMyCode гарантирует, что у нас есть код нужной роли: при необходимости
// открывает UDP-сокет, находит адреса через STUN и пересоздаёт код.
// Возвращает предупреждение (пустую строку, если всё хорошо) и ошибку.
func (s *appState) ensureMyCode(role proto.Role) (string, error) {
	if s.myCode != nil && s.myCode.Role == role {
		return "", nil
	}
	if s.conn == nil {
		conn, err := net.ListenUDP("udp4", nil)
		if err != nil {
			return "", fmt.Errorf("не удалось открыть UDP-порт: %w", err)
		}
		s.conn = conn
	}

	public, lan := proto.DiscoverEndpoints(s.conn, stunServers(), 800*time.Millisecond)
	if len(public) == 0 && len(lan) == 0 {
		return "", fmt.Errorf("не удалось определить ни одного адреса — проверьте интернет")
	}
	warn := ""
	if len(public) == 0 {
		warn = "публичный адрес не определён (STUN недоступен) — связь возможна только в одной локальной сети"
	}

	name := s.nameEntry.Text
	if name == "" {
		name = "Без имени"
	}

	code := &proto.Code{
		Name: name,
		Role: role,
		Endpoints: proto.Endpoints{
			Public: public,
			LAN:    lan,
		},
	}
	if role == proto.RoleA {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return "", err
		}
		code.Key = key
	}
	s.myCode = code
	return warn, nil
}

func (s *appState) startSession(peer *proto.Code) {
	if s.session != nil {
		s.setStatus("Разговор уже идёт — сначала завершите его.")
		return
	}

	sess, err := proto.NewSession(s.conn, s.myCode, peer)
	if err != nil {
		s.setStatus("Ошибка: " + err.Error())
		return
	}

	s.setReason("")
	s.session = sess
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	if s.myCode.Role == proto.RoleB {
		s.setStatus("Ответный код показан выше — отправьте его другу. Ищем связь…")
	} else {
		s.setStatus("Соединяемся…")
	}

	go sess.Run(ctx)
	go s.pump(sess, ctx)
	go s.micPuller(sess, ctx.Done())
}

// pump перекладывает события сессии в интерфейс, а входящие кадры — в звук.
func (s *appState) pump(sess *proto.Session, ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sess.Events():
			switch ev.Kind {
			case proto.EvConnected:
				if s.engine != nil {
					s.engine.ResetBuffers() // выкинуть накопленное за время разрыва
				}
				fyne.Do(func() { s.showCall(ev.Peer) })
			case proto.EvLost:
				fyne.Do(func() {
					s.callStatus.SetText("Связь прервалась — переподключаемся…")
					s.rttLabel.SetText("")
				})
			case proto.EvStalled:
				// 90 секунд без успеха: прекращаем попытки и возвращаемся
				// на главный экран — нужен новый обмен кодами.
				s.setReason("Долго не удалось соединиться — сделайте новый обмен кодами.")
				sess.Stop()
			case proto.EvRTT:
				rtt := ev.RTTms
				fyne.Do(func() { s.rttLabel.SetText(fmt.Sprintf("Задержка: %d мс", rtt)) })
			case proto.EvPeerBye:
				s.setReason("Друг завершил разговор.")
				sess.Stop()
			case proto.EvStopped:
				s.cancel()
				reason := s.takeReason()
				// Мутации UI — только на главном потоке Fyne.
				fyne.Do(func() { s.finishCall(reason) })
				return
			}
		case frame := <-sess.InFrames():
			if s.engine != nil {
				s.engine.WriteReceived(frame)
			}
		}
	}
}

func (s *appState) micPuller(sess *proto.Session, done <-chan struct{}) {
	if s.engine == nil {
		return
	}
	frame := make([]int16, audio.FrameSamples)
	for {
		select {
		case <-done:
			return
		default:
		}
		if sess.IsConnected() && s.engine.ReadMicFrame(frame) {
			sess.SendAudio(frame)
		} else {
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// --- экран разговора ---

func (s *appState) showCall(peerName string) {
	s.peerLabel.SetText("Разговор с " + peerName)
	s.callStatus.SetText("Подключено")
	s.mainScreen.Hide()
	s.callScreen.Show()
	s.refreshDevices()
}

func (s *appState) refreshDevices() {
	if s.engine == nil {
		return
	}
	s.updatingDevices = true
	defer func() { s.updatingDevices = false }()

	caps, err1 := s.engine.Devices(audio.KindCapture)
	plays, err2 := s.engine.Devices(audio.KindPlayback)

	if names := deviceNames(caps); len(names) > 0 {
		s.micSelect.Options = names
		s.micSelect.SetSelected(pickDevice(names, caps, s.prefs.String("micDev")))
	}
	if names := deviceNames(plays); len(names) > 0 {
		s.spkSelect.Options = names
		s.spkSelect.SetSelected(pickDevice(names, plays, s.prefs.String("spkDev")))
	}

	if err1 != nil || err2 != nil {
		s.callStatus.SetText("Не удалось перечислить устройства.")
		return
	}

	micDev := s.micSelect.Selected
	spkDev := s.spkSelect.Selected
	go func() {
		if err := s.engine.UseCaptureByName(micDev); err != nil {
			fyne.Do(func() { s.callStatus.SetText("Микрофон: " + err.Error()) })
		}
		if err := s.engine.UsePlaybackByName(spkDev); err != nil {
			fyne.Do(func() { s.callStatus.SetText("Динамик: " + err.Error()) })
		}
	}()
}

func deviceNames(list []audio.Device) []string {
	names := make([]string, 0, len(list))
	for _, d := range list {
		names = append(names, d.Name)
	}
	return names
}

func pickDevice(names []string, list []audio.Device, preferred string) string {
	for _, n := range names {
		if n == preferred {
			return preferred
		}
	}
	for _, d := range list {
		if d.IsDefault {
			return d.Name
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

func (s *appState) hangup() {
	s.setReason("Разговор завершён.")
	if s.session != nil {
		s.session.Stop()
	}
}

func (s *appState) setReason(r string) {
	s.reasonMu.Lock()
	s.stopReason = r
	s.reasonMu.Unlock()
}

func (s *appState) takeReason() string {
	s.reasonMu.Lock()
	defer s.reasonMu.Unlock()
	r := s.stopReason
	s.stopReason = ""
	return r
}

// finishCall вызывается на UI-потоке, когда сессия полностью закончилась.
func (s *appState) finishCall(reason string) {
	if s.engine != nil {
		s.engine.StopDevices()
	}
	s.session = nil
	s.myCode = nil // новый разговор — новый ключ сессии и свежие адреса
	if reason == "" {
		reason = "Отключено."
	}
	s.showMain(reason)
}

func (s *appState) showMain(status string) {
	s.callScreen.Hide()
	s.mainScreen.Show()
	s.setStatus(status)
}

func (s *appState) shutdown() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.session != nil {
		s.session.Stop()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.engine != nil {
		s.engine.Close()
	}
}

func (s *appState) setStatus(text string) {
	s.statusLabel.SetText(text)
}

func stunServers() []string {
	return stun.DefaultServers
}
