package main

// Интерфейс: главный экран (друзья, приглашение, звук, настройки) и экран
// разговора. Звонки и приглашения — в calls.go и pairflow.go.

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Jagyar/voice2us/internal/audio"
	"github.com/Jagyar/voice2us/internal/proto"
	"github.com/Jagyar/voice2us/internal/rdv"
	"github.com/Jagyar/voice2us/internal/stun"
)

// version подставляется при сборке: -ldflags "-X main.version=2.0.0".
var version = "dev"

// Адрес «встречалки» по умолчанию; меняется в настройках приложения.
const defaultRendezvous = "http://159.194.244.147:8787"

type phase int

const (
	phaseIdle     phase = iota
	phaseCalling        // мы звоним, ждём ответа
	phaseIncoming       // нам звонят, показан диалог
	phaseSession        // идёт соединение или разговор
)

type appState struct {
	app   fyne.App
	win   fyne.Window
	prefs fyne.Preferences

	engine *audio.Engine

	conn     *net.UDPConn
	myCode   *proto.Code
	peerCode *proto.Code
	session  *proto.Session
	cancel   context.CancelFunc

	reasonMu        sync.Mutex // stopReason пишут и pump, и UI-поток
	stopReason      string
	updatingDevices bool

	// друзья и «встречалка» (всё, кроме pres, — только UI-поток)
	rdvc        *rdv.Client
	pres        *presence
	friends     []*friend
	statuses    map[string]peerStatus
	serverKnown bool
	serverOK    bool

	// состояние звонка
	phase      phase
	callFriend *friend
	callID     string
	callTimer  *time.Timer
	callDlg    dialog.Dialog
	keepStop   context.CancelFunc
	keepDone   chan struct{}
	handled    map[string]bool
	sockBusy   bool // UDP-сокет занят STUN-запросами/приглашением: сессию на нём пока не запускать
	incomingEP proto.Endpoints

	inCall    bool
	muted     bool
	connected bool
	callStart time.Time
	rtt       int64
	tickStop  chan struct{}
	quit      chan struct{}

	// главный экран
	friendsBox    *fyne.Container
	statusLabel   *widget.Label
	footerLabel   *widget.Label
	nameEntry     *widget.Entry
	urlEntry      *widget.Entry
	urlResult     *widget.Label
	myCodeEntry   *widget.Entry
	peerCodeEntry *widget.Entry
	createBtn     *widget.Button
	connectBtn    *widget.Button

	// звук (виден на обоих экранах)
	micSelect *widget.Select
	spkSelect *widget.Select
	micSlider *widget.Slider
	spkSlider *widget.Slider
	micPct    *widget.Label
	spkPct    *widget.Label

	// экран разговора
	callHead   *fyne.Container
	callStatus *widget.Label
	muteBtn    *widget.Button

	mainTop    *fyne.Container
	callTop    *fyne.Container
	mainBottom *fyne.Container
}

func newAppState(a fyne.App, w fyne.Window) *appState {
	engine, err := audio.NewEngine()
	if err != nil {
		engine = nil // покажем ошибку в интерфейсе, не роняя приложение
	}
	return &appState{
		app:      a,
		win:      w,
		prefs:    a.Preferences(),
		engine:   engine,
		statuses: map[string]peerStatus{},
		handled:  map[string]bool{},
		quit:     make(chan struct{}),
	}
}

func section(title string) fyne.CanvasObject {
	l := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	l.Importance = widget.LowImportance
	return l
}

func heading(text string, align fyne.TextAlign) *widget.RichText {
	return widget.NewRichText(&widget.TextSegment{
		Text: text,
		Style: widget.RichTextStyle{
			SizeName:  theme.SizeNameHeadingText,
			TextStyle: fyne.TextStyle{Bold: true},
			Alignment: align,
		},
	})
}

func (s *appState) buildUI() {
	s.buildMain()
	s.buildSound()
	s.buildCall()

	soundCard := s.soundCard()
	body := container.NewVBox(
		container.NewStack(s.mainTop, s.callTop),
		soundCard,
		s.mainBottom,
	)
	s.callTop.Hide()
	s.win.SetContent(container.NewScroll(container.New(layout.NewCustomPaddedLayout(14, 14, 16, 16), body)))

	s.win.SetCloseIntercept(func() {
		s.shutdown()
		s.win.Close()
	})
}

// --- главный экран ---

func (s *appState) buildMain() {
	s.friendsBox = container.NewVBox()

	inviteBtn := widget.NewButtonWithIcon("Пригласить друга", theme.ContentAddIcon(), s.onInvite)
	inviteBtn.Importance = widget.HighImportance
	joinBtn := widget.NewButtonWithIcon("У меня есть код", theme.LoginIcon(), s.onJoin)

	s.mainTop = container.NewVBox(
		heading("Voice2Us", fyne.TextAlignLeading),
		section("Друзья"),
		s.friendsBox,
		container.NewGridWithColumns(2, inviteBtn, joinBtn),
		widget.NewSeparator(),
	)

	// Настройки и запасной ручной обмен кодами.
	s.nameEntry = widget.NewEntry()
	s.nameEntry.SetPlaceHolder("Как вас показать друзьям")
	s.nameEntry.OnChanged = func(t string) {
		s.prefs.SetString("name", t)
		s.pres.SetName(t)
	}

	s.urlEntry = widget.NewEntry()
	s.urlEntry.SetPlaceHolder(defaultRendezvous)
	s.urlResult = widget.NewLabel("")
	s.urlResult.Importance = widget.LowImportance
	s.urlResult.Wrapping = fyne.TextWrapWord
	checkBtn := widget.NewButton("Сохранить и проверить", s.onSaveServer)

	profile := container.NewVBox(
		widget.NewLabel("Ваше имя"), s.nameEntry,
		widget.NewLabel("Сервер-«встречалка»"), s.urlEntry,
		checkBtn, s.urlResult,
	)

	s.myCodeEntry = widget.NewMultiLineEntry()
	s.myCodeEntry.SetPlaceHolder("Нажмите «Создать код»")
	s.myCodeEntry.Wrapping = fyne.TextWrapWord
	s.myCodeEntry.Disable()
	s.peerCodeEntry = widget.NewMultiLineEntry()
	s.peerCodeEntry.SetPlaceHolder("Вставьте сюда код друга")
	s.peerCodeEntry.Wrapping = fyne.TextWrapWord
	s.createBtn = widget.NewButton("Создать код", s.onCreateCode)
	copyBtn := widget.NewButton("Скопировать", func() {
		if txt := s.myCodeEntry.Text; txt != "" {
			s.win.Clipboard().SetContent(txt)
		}
	})
	pasteBtn := widget.NewButton("Вставить из буфера", func() {
		s.peerCodeEntry.SetText(s.win.Clipboard().Content())
	})
	s.connectBtn = widget.NewButton("Продолжить", s.onPeerCode)
	hint := widget.NewLabel("Если сервер недоступен: один создаёт код и отправляет другу, " +
		"второй вставляет его и отправляет ответный код обратно, первый вставляет ответ.")
	hint.Wrapping = fyne.TextWrapWord
	hint.Importance = widget.LowImportance
	manual := container.NewVBox(
		hint,
		container.NewGridWithColumns(2, s.createBtn, copyBtn),
		s.myCodeEntry,
		pasteBtn, s.peerCodeEntry, s.connectBtn,
	)

	adv := widget.NewAccordion(
		widget.NewAccordionItem("Профиль и сервер", profile),
		widget.NewAccordionItem("Обмен кодами вручную", manual),
	)

	s.statusLabel = widget.NewLabel("")
	s.statusLabel.Wrapping = fyne.TextWrapWord
	s.statusLabel.Importance = widget.LowImportance
	s.footerLabel = widget.NewLabel("")
	s.footerLabel.Importance = widget.LowImportance
	s.footerLabel.Alignment = fyne.TextAlignCenter

	s.mainBottom = container.NewVBox(adv, s.statusLabel, s.footerLabel)
}

// --- звук ---

func (s *appState) buildSound() {
	s.micSelect = widget.NewSelect(nil, func(name string) {
		if s.updatingDevices || s.engine == nil || name == "" {
			return
		}
		s.prefs.SetString("micDev", name)
		if !s.inCall {
			return // до разговора только запоминаем выбор: микрофон не открываем
		}
		go func() {
			if err := s.engine.UseCaptureByName(name); err != nil {
				fyne.Do(func() { s.deviceErr("Микрофон: " + err.Error()) })
			}
		}()
	})
	s.spkSelect = widget.NewSelect(nil, func(name string) {
		if s.updatingDevices || s.engine == nil || name == "" {
			return
		}
		s.prefs.SetString("spkDev", name)
		if !s.inCall {
			return
		}
		go func() {
			if err := s.engine.UsePlaybackByName(name); err != nil {
				fyne.Do(func() { s.deviceErr("Динамик: " + err.Error()) })
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
}

func (s *appState) soundCard() fyne.CanvasObject {
	refresh := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), s.refreshDevices)
	refresh.Importance = widget.LowImportance
	head := container.NewBorder(nil, nil, section("Звук"), refresh)
	return container.NewVBox(
		head,
		newCard(container.NewVBox(
			widget.NewLabel("Микрофон"),
			s.micSelect,
			container.NewBorder(nil, nil, nil, s.micPct, s.micSlider),
			widget.NewLabel("Динамик или наушники"),
			s.spkSelect,
			container.NewBorder(nil, nil, nil, s.spkPct, s.spkSlider),
		)),
	)
}

// --- экран разговора ---

func (s *appState) buildCall() {
	s.callHead = container.NewVBox()
	s.callStatus = widget.NewLabel("")
	s.callStatus.Alignment = fyne.TextAlignCenter
	s.callStatus.Importance = widget.LowImportance

	s.muteBtn = widget.NewButtonWithIcon("Выключить микрофон", theme.VolumeUpIcon(), func() {
		s.setMuted(!s.muted)
	})
	hangup := widget.NewButtonWithIcon("Завершить", theme.CancelIcon(), s.hangup)
	hangup.Importance = widget.DangerImportance

	s.callTop = container.NewVBox(
		layout.NewSpacer(),
		s.callHead,
		s.callStatus,
		container.NewGridWithColumns(2, s.muteBtn, hangup),
		widget.NewSeparator(),
	)
}

func (s *appState) setScreen(call bool) {
	if call {
		s.mainTop.Hide()
		s.mainBottom.Hide()
		s.callTop.Show()
	} else {
		s.callTop.Hide()
		s.mainTop.Show()
		s.mainBottom.Show()
	}
}

func (s *appState) loadPrefs() {
	s.nameEntry.SetText(s.prefs.String("name"))
	url := s.prefs.String("rdvURL")
	if url == "" {
		url = defaultRendezvous
	}
	s.urlEntry.SetText(url)
	s.rdvc = rdv.NewClient(url)

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
	} else {
		s.setStatus("Аудио недоступно: не удалось инициализировать звуковую систему.")
	}
	s.refreshDevices() // устройства выбираются ещё до подключения

	s.friends = s.loadFriends()
	s.renderFriends()
	s.updateFooter()

	s.pres = newPresence()
	s.pres.SetName(s.prefs.String("name"))
	s.pres.SetClient(s.rdvc)
	s.pres.SetFriends(s.friends)
	s.pres.onUpdate = func(id string, st peerStatus) { fyne.Do(func() { s.onPeerStatus(id, st) }) }
	s.pres.onServer = func(ok bool) { fyne.Do(func() { s.onServerState(ok) }) }
	go s.pres.Run()

	s.quit = make(chan struct{})
	go s.watchDevices(s.quit)
}

func (s *appState) onServerState(ok bool) {
	s.serverKnown, s.serverOK = true, ok
	s.updateFooter()
	s.renderFriends()
}

func (s *appState) updateFooter() {
	state := "сервер: проверяем…"
	if s.serverKnown {
		if s.serverOK {
			state = "сервер: онлайн"
		} else {
			state = "сервер недоступен — звонок напрямую"
		}
	}
	s.footerLabel.SetText("v" + version + " · " + state)
}

func (s *appState) onSaveServer() {
	url := s.urlEntry.Text
	if url == "" {
		url = defaultRendezvous
		s.urlEntry.SetText(url)
	}
	s.prefs.SetString("rdvURL", url)
	c := rdv.NewClient(url)
	s.rdvc = c
	s.pres.SetClient(c)
	s.urlResult.SetText("Проверяем…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := c.Health(ctx)
		fyne.Do(func() {
			if err != nil {
				s.urlResult.SetText("Не отвечает: " + err.Error())
			} else {
				s.urlResult.SetText("Сервер отвечает.")
			}
		})
	}()
}

// --- сессия ---

// ensureConn открывает UDP-сокет (на сохранённом порту, если получится).
func (s *appState) ensureConn() error {
	if s.conn != nil {
		return nil
	}
	conn, err := listenUDP(s.prefs.Int(prefUDPPort))
	if err != nil {
		return fmt.Errorf("не удалось открыть UDP-порт: %w", err)
	}
	s.conn = conn
	return nil
}

func (s *appState) myName() string {
	if n := s.nameEntry.Text; n != "" {
		return n
	}
	return "Без имени"
}

func (s *appState) startSession(peer *proto.Code, waitFriend bool) {
	if s.session != nil {
		s.setStatus("Разговор уже идёт — сначала завершите его.")
		return
	}
	if s.engine == nil {
		s.setStatus("Аудио недоступно: не удалось инициализировать звуковую систему.")
		s.phase = phaseIdle
		return
	}

	sess, err := proto.NewSession(s.conn, s.myCode, peer)
	if err != nil {
		s.setStatus("Ошибка: " + err.Error())
		s.phase = phaseIdle
		return
	}

	s.setReason("")
	s.session = sess
	s.peerCode = peer
	s.phase = phaseSession
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	if waitFriend {
		s.setStatus("Ждём друга: он тоже должен нажать «Позвонить»…")
	} else {
		s.setStatus("Соединяемся…")
	}
	s.renderFriends()

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
				fyne.Do(func() {
					s.rememberFriendNow(ev.Remote)
					s.showCall(ev.Peer)
				})
			case proto.EvLost:
				fyne.Do(func() {
					s.connected = false
					s.callStatus.SetText("Связь прервалась — переподключаемся…")
				})
			case proto.EvStalled:
				// 90 секунд без успеха: прекращаем попытки и возвращаемся
				// на главный экран.
				s.setReason("Не удалось соединиться. Возможно, у друга закрыто приложение или у кого-то сложная сеть.")
				sess.Stop()
			case proto.EvRTT:
				rtt := ev.RTTms
				fyne.Do(func() { s.rtt = rtt; s.updateCallStatus() })
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

// rememberFriendNow (UI-поток) сохраняет друга и порт нашего сокета.
func (s *appState) rememberFriendNow(remote string) {
	if s.myCode == nil || s.peerCode == nil {
		return
	}
	s.upsertFriend(rememberConnection(s.myCode, s.peerCode, remote))
	if s.conn != nil {
		s.prefs.SetInt(prefUDPPort, s.conn.LocalAddr().(*net.UDPAddr).Port)
	}
	s.pres.SetFriends(s.friends)
	s.renderFriends()
}

func (s *appState) showCall(peerName string) {
	s.inCall = true
	s.connected = true
	s.callStart = time.Now()
	s.rtt = 0
	s.pres.SetBusy(true)
	s.dismissCallDialog()

	s.callHead.Objects = []fyne.CanvasObject{
		container.NewCenter(newAvatar(peerName, 84)),
		heading(peerName, fyne.TextAlignCenter),
	}
	s.callHead.Refresh()
	s.updateCallStatus()
	s.setScreen(true)
	s.refreshDevices() // открывает выбранные устройства, раз разговор идёт

	if s.tickStop == nil {
		stop := make(chan struct{})
		s.tickStop = stop
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					fyne.Do(s.updateCallStatus)
				}
			}
		}()
	}
}

func (s *appState) updateCallStatus() {
	if !s.inCall || !s.connected {
		return
	}
	d := int(time.Since(s.callStart).Seconds())
	txt := fmt.Sprintf("Подключено · %02d:%02d", d/60, d%60)
	if s.rtt > 0 {
		txt += fmt.Sprintf(" · %d мс", s.rtt)
	}
	s.callStatus.SetText(txt)
}

func (s *appState) setMuted(m bool) {
	s.muted = m
	if s.engine != nil {
		s.engine.SetMicMuted(m)
	}
	if m {
		s.muteBtn.SetText("Включить микрофон")
		s.muteBtn.SetIcon(theme.VolumeMuteIcon())
		s.muteBtn.Importance = widget.WarningImportance
	} else {
		s.muteBtn.SetText("Выключить микрофон")
		s.muteBtn.SetIcon(theme.VolumeUpIcon())
		s.muteBtn.Importance = widget.MediumImportance
	}
	s.muteBtn.Refresh()
}

// --- устройства ---

// deviceErr показывает ошибку устройства на текущем экране.
func (s *appState) deviceErr(msg string) {
	if s.inCall {
		s.callStatus.SetText(msg)
	} else {
		s.setStatus(msg)
	}
}

// watchDevices раз в пару секунд проверяет, не подключили ли наушники
// или не отключили ли устройство, и обновляет списки в интерфейсе.
func (s *appState) watchDevices(done <-chan struct{}) {
	if s.engine == nil {
		return
	}
	snapshot := func() string {
		var out string
		for _, k := range []audio.DeviceKind{audio.KindCapture, audio.KindPlayback} {
			list, _ := s.engine.Devices(k)
			out += fmt.Sprint(k) + ":"
			for _, d := range list {
				out += d.Name + "|"
			}
		}
		return out
	}
	last := snapshot()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if cur := snapshot(); cur != last {
				last = cur
				fyne.Do(s.refreshDevices)
			}
		}
	}
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
		s.deviceErr("Не удалось перечислить устройства.")
		return
	}
	if !s.inCall {
		return // до разговора устройства не открываем (индикатор микрофона не горит)
	}

	micDev := s.micSelect.Selected
	spkDev := s.spkSelect.Selected
	go func() {
		if err := s.engine.UseCaptureByName(micDev); err != nil {
			fyne.Do(func() { s.deviceErr("Микрофон: " + err.Error()) })
		}
		if err := s.engine.UsePlaybackByName(spkDev); err != nil {
			fyne.Do(func() { s.deviceErr("Динамик: " + err.Error()) })
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

// --- завершение ---

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
	if s.tickStop != nil {
		close(s.tickStop)
		s.tickStop = nil
	}
	s.inCall = false
	s.connected = false
	s.session = nil
	s.peerCode = nil
	s.phase = phaseIdle
	s.callFriend = nil
	s.setMuted(false)
	s.myCode = nil // новый разговор — свежие адреса
	s.pres.SetBusy(false)
	for _, f := range s.friends {
		s.pres.SetOwn(f.ID, stIdle, "", nil)
	}
	if reason == "" {
		reason = "Отключено."
	}
	s.setScreen(false)
	s.setStatus(reason)
	s.renderFriends()
}

func (s *appState) shutdown() {
	if s.quit != nil {
		close(s.quit)
		s.quit = nil
	}
	if s.pres != nil {
		s.pres.Stop()
	}
	// Сначала Stop (шлёт bye), потом cancel: иначе отмена контекста
	// успевает занять stopOnce, и друг не получит прощальный пакет.
	if s.session != nil {
		s.session.Stop()
	}
	if s.cancel != nil {
		s.cancel()
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

// --- ручной обмен кодами (запасной вариант без сервера) ---

func (s *appState) onCreateCode() {
	if s.engine == nil {
		s.setStatus("Аудио недоступно: не удалось инициализировать звуковую систему.")
		return
	}
	if s.phase != phaseIdle || !s.acquireSock() {
		s.setStatus("Сейчас идёт другое соединение — подождите.")
		return
	}
	s.createBtn.Disable()
	s.connectBtn.Disable()
	s.setStatus("Определяем адреса (STUN)…")
	name := s.myName()
	port := s.prefs.Int(prefUDPPort)
	haveConn := s.conn

	go func() {
		conn, code, warn, err := s.prepareCode(nil, haveConn, port, proto.RoleA, name)
		fyne.Do(func() {
			s.releaseSock()
			s.applyCode(conn, code)
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
		s.setStatus("Аудио недоступно: не удалось инициализировать звуковую систему.")
		return
	}
	peer, err := proto.UnmarshalCode(s.peerCodeEntry.Text)
	if err != nil {
		s.setStatus("Код друга не распознан: " + err.Error())
		return
	}
	if proto.SameCode(s.myCode, peer) {
		s.setStatus("Это похоже на ваш собственный код — нужен код друга.")
		return
	}
	if s.phase != phaseIdle || !s.acquireSock() {
		s.setStatus("Сейчас идёт другое соединение — подождите.")
		return
	}
	s.createBtn.Disable()
	s.connectBtn.Disable()
	s.setStatus("Определяем адреса (STUN)…")
	name := s.myName()
	have, haveConn, port := s.myCode, s.conn, s.prefs.Int(prefUDPPort)

	go func() {
		conn, code, warn, err := s.prepareCode(have, haveConn, port, proto.Opposite(peer.Role), name)
		fyne.Do(func() {
			s.releaseSock()
			s.applyCode(conn, code)
			s.createBtn.Enable()
			s.connectBtn.Enable()
			if err != nil {
				s.setStatus("Ошибка: " + err.Error())
				return
			}
			codeStr, _ := proto.MarshalCode(s.myCode)
			s.myCodeEntry.SetText(codeStr)
			s.startSession(peer, false)
			if s.myCode.Role == proto.RoleB {
				s.setStatus("Ответный код показан выше — отправьте его другу. Ищем связь…")
			}
			if warn != "" {
				s.setStatus(s.statusLabel.Text + " " + warn)
			}
		})
	}()
}

// prepareCode готовит код нужной роли и сокет. Работает в фоне и не трогает
// состояние UI: возвращает результат, который applyCode применит на
// главном потоке. have/haveConn — снимок текущих значений.
// Возвращает предупреждение (пусто, если всё хорошо) и ошибку.
func (s *appState) prepareCode(have *proto.Code, haveConn *net.UDPConn, port int, role proto.Role, name string) (*net.UDPConn, *proto.Code, string, error) {
	if have != nil && have.Role == role {
		return haveConn, have, "", nil
	}
	conn := haveConn
	if conn == nil {
		c, err := listenUDP(port)
		if err != nil {
			return nil, nil, "", fmt.Errorf("не удалось открыть UDP-порт: %w", err)
		}
		conn = c
	}

	public, lan := proto.DiscoverEndpoints(conn, stunServers(), 800*time.Millisecond)
	if len(public) == 0 && len(lan) == 0 {
		return conn, nil, "", fmt.Errorf("не удалось определить ни одного адреса — проверьте интернет")
	}
	warn := ""
	if len(public) == 0 {
		warn = "публичный адрес не определён (STUN недоступен) — связь возможна только в одной локальной сети"
	}

	code := &proto.Code{
		Name:      name,
		Role:      role,
		Endpoints: proto.Endpoints{Public: public, LAN: lan},
	}
	if role == proto.RoleA {
		key := make([]byte, 32)
		if _, err := randRead(key); err != nil {
			return conn, nil, "", err
		}
		code.Key = key
	}
	return conn, code, warn, nil
}

// applyCode (UI-поток) сохраняет сокет и код, подготовленные prepareCode.
func (s *appState) applyCode(conn *net.UDPConn, code *proto.Code) {
	if conn != nil {
		s.conn = conn
	}
	if code != nil {
		s.myCode = code
	}
}
