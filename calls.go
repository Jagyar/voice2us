package main

// Друзья на главном экране и весь жизненный цикл звонка: исходящий,
// входящий, встречные вызовы, отказ. Звонок идёт через «встречалку»:
// адреса обмениваются непосредственно перед соединением.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Jagyar/voice2us/internal/proto"
	"github.com/Jagyar/voice2us/internal/stun"
)

const (
	callTimeout     = 45 * time.Second
	incomingTimeout = 40 * time.Second
	keepEvery       = 20 * time.Second
)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- список друзей ---

// friendLine решает, что показать про друга: подпись, цвет точки, можно ли звонить.
func (s *appState) friendLine(f *friend) (text string, col fyne.ThemeColorName, canCall bool) {
	st, have := s.statuses[f.ID]
	switch {
	case s.serverKnown && !s.serverOK, have && st.Err:
		return "сервер недоступен — звонок напрямую", theme.ColorNameDisabled, true
	case !have:
		return "проверяем…", theme.ColorNameDisabled, false
	case !st.Online:
		return "не в сети", theme.ColorNameDisabled, false
	case st.State == stInCall:
		return "в разговоре", theme.ColorNameWarning, false
	case st.State == stCalling:
		return "звонит вам", theme.ColorNamePrimary, true
	default:
		return "в сети", theme.ColorNameSuccess, true
	}
}

func (s *appState) renderFriends() {
	if s.friendsBox == nil {
		return
	}
	s.friendsBox.Objects = nil
	if len(s.friends) == 0 {
		hint := widget.NewLabel("Пока никого нет. Нажмите «Пригласить друга» — получите короткий код, " +
			"который достаточно передать один раз.")
		hint.Wrapping = fyne.TextWrapWord
		hint.Importance = widget.LowImportance
		s.friendsBox.Add(newCard(hint))
	}
	for _, f := range s.friends {
		s.friendsBox.Add(s.friendRow(f))
	}
	s.friendsBox.Refresh()
}

func (s *appState) friendRow(f *friend) fyne.CanvasObject {
	text, col, can := s.friendLine(f)

	name := widget.NewLabelWithStyle(f.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	name.Truncation = fyne.TextTruncateEllipsis
	status := widget.NewLabel(text)
	status.Importance = widget.LowImportance

	call := widget.NewButton("Позвонить", func() { s.onCallFriend(f) })
	call.Importance = widget.HighImportance
	if !can || s.phase != phaseIdle {
		call.Disable()
	}
	more := widget.NewButtonWithIcon("", theme.MoreVerticalIcon(), nil)
	more.Importance = widget.LowImportance
	more.OnTapped = func() { s.showFriendMenu(f, more) }

	info := container.New(layout.NewCustomPaddedVBoxLayout(-4), name, container.NewHBox(vcenter(newDot(col)), status))
	row := container.NewBorder(nil, nil,
		vcenter(newAvatar(f.Name, 40)),
		vcenter(container.NewHBox(call, more)),
		vcenter(info))
	return newCard(row)
}

func (s *appState) showFriendMenu(f *friend, anchor fyne.CanvasObject) {
	m := fyne.NewMenu("",
		fyne.NewMenuItem("Переименовать", func() { s.renameFriend(f) }),
		fyne.NewMenuItem("Удалить", func() { s.deleteFriend(f) }),
	)
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(anchor)
	widget.ShowPopUpMenuAtPosition(m, s.win.Canvas(), pos.AddXY(0, anchor.Size().Height))
}

func (s *appState) renameFriend(f *friend) {
	entry := widget.NewEntry()
	entry.SetText(f.Name)
	d := dialog.NewCustomConfirm("Переименовать", "Сохранить", "Отмена", entry, func(ok bool) {
		if ok && entry.Text != "" {
			f.Name = entry.Text
			s.saveFriends(s.friends)
			s.renderFriends()
		}
	}, s.win)
	d.Resize(fyne.NewSize(320, 0))
	d.Show()
}

func (s *appState) deleteFriend(f *friend) {
	dialog.ShowConfirm("Удалить друга?",
		f.Name+" исчезнет из списка. Чтобы снова созваниваться, нужно будет заново пригласить друг друга.",
		func(ok bool) {
			if !ok {
				return
			}
			s.removeFriend(f.ID)
			s.pres.SetFriends(s.friends)
			delete(s.statuses, f.ID)
			s.renderFriends()
		}, s.win)
}

// --- статусы ---

func (s *appState) onPeerStatus(id string, st peerStatus) {
	s.statuses[id] = st
	f := s.findFriend(id)
	if f == nil {
		return
	}
	s.renderFriends()

	switch s.phase {
	case phaseIdle:
		if st.Online && st.State == stCalling && st.Age < ringFresh && !s.handled[st.Call] {
			s.handled[st.Call] = true
			s.showIncoming(f, st)
		}
	case phaseCalling:
		if s.callFriend != nil && s.callFriend.ID == id {
			s.onCallProgress(f, st)
		}
	case phaseIncoming:
		if s.callFriend != nil && s.callFriend.ID == id && (st.State != stCalling || st.Call != s.callID) {
			s.cancelIncoming("Звонок отменён.")
		}
	}
}

func (s *appState) onCallProgress(f *friend, st peerStatus) {
	if !st.Online {
		return
	}
	switch {
	case st.State == stAnswering && st.Call == s.callID:
		s.beginCall(f, st.EP)
	case st.State == stDeclined && st.Call == s.callID:
		s.endAttempt(f.Name + " отклонил звонок.")
	case st.State == stInCall:
		s.endAttempt(f.Name + " сейчас в разговоре.")
	case st.State == stCalling && st.Call != s.callID && st.Call < s.callID:
		// Оба нажали «Позвонить» одновременно: побеждает вызов с меньшим
		// идентификатором, второй участник сам становится отвечающим.
		id, ep := st.Call, st.EP
		s.handled[id] = true
		s.dismissCallDialog()
		s.pres.SetOwn(f.ID, stIdle, "", nil)
		s.phase = phaseSession
		s.stopKeep(func() { s.acceptCall(f, id, ep) })
	}
}

// --- исходящий звонок ---

func (s *appState) onCallFriend(f *friend) {
	if s.phase != phaseIdle {
		return
	}
	if s.engine == nil {
		s.setStatus("Аудио недоступно: не удалось инициализировать звуковую систему.")
		return
	}
	if err := s.ensureConn(); err != nil {
		s.setStatus(err.Error())
		return
	}
	st := s.statuses[f.ID]
	if (s.serverKnown && !s.serverOK) || st.Err {
		s.directCall(f)
		return
	}
	s.startCall(f)
}

// directCall — запасной вариант, когда сервер недоступен: сохранённые адреса,
// друг тоже должен нажать «Позвонить».
func (s *appState) directCall(f *friend) {
	if s.sockBusy {
		s.setStatus("Подождите: сокет занят.")
		return
	}
	our, peer := f.codes(s.myName())
	s.myCode = our
	s.callFriend = f
	s.startSession(peer, true)
}

func (s *appState) startCall(f *friend) {
	if !s.acquireSock() {
		s.setStatus("Подождите: идёт другое соединение.")
		return
	}
	id := randHex(8)
	s.phase = phaseCalling
	s.callFriend, s.callID = f, id
	s.showWaitDialog(f)
	s.renderFriends()

	conn := s.conn
	go func() {
		public, lan := proto.DiscoverEndpoints(conn, stunServers(), 800*time.Millisecond)
		fyne.Do(func() {
			if s.phase != phaseCalling || s.callID != id {
				s.releaseSock() // звонок отменили, пока определялись адреса
				return
			}
			if len(public)+len(lan) == 0 {
				s.releaseSock()
				s.endAttempt("Не удалось определить адрес — проверьте интернет.")
				return
			}
			ep := proto.Endpoints{Public: public, LAN: lan}
			s.pres.SetOwn(f.ID, stCalling, id, &ep)
			s.startKeep()
			s.callTimer = time.AfterFunc(callTimeout, func() {
				fyne.Do(func() {
					if s.phase == phaseCalling && s.callID == id {
						s.endAttempt(f.Name + " не отвечает.")
					}
				})
			})
		})
	}()
}

func (s *appState) cancelCall() { s.endAttempt("") }

// endAttempt прекращает исходящую попытку и возвращает интерфейс в покой.
func (s *appState) endAttempt(msg string) {
	if f := s.callFriend; f != nil {
		s.pres.SetOwn(f.ID, stIdle, "", nil)
	}
	s.dismissCallDialog()
	s.phase = phaseIdle
	s.callFriend = nil
	s.setStatus(msg)
	s.stopKeep(nil)
	s.renderFriends()
}

// beginCall: друг ответил — запускаем сессию с его адресами.
func (s *appState) beginCall(f *friend, ep proto.Endpoints) {
	s.phase = phaseSession
	s.dismissCallDialog()
	s.pres.SetOwn(f.ID, stIdle, "", nil)
	s.setStatus("Соединяемся с " + f.Name + "…")
	s.stopKeep(func() { s.launchSession(f, ep) })
}

// launchSession строит коды из записи друга и свежих адресов и стартует сессию.
func (s *appState) launchSession(f *friend, ep proto.Endpoints) {
	if s.phase != phaseSession || s.session != nil {
		return
	}
	our, peer := f.codes(s.myName())
	peer.Endpoints = proto.Endpoints{
		Public: mergeUnique(ep.Public, f.Peer.Public),
		LAN:    mergeUnique(ep.LAN, f.Peer.LAN),
	}
	s.myCode = our
	s.callFriend = f
	s.startSession(peer, false)
}

func mergeUnique(first, second []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range [][]string{first, second} {
		for _, v := range l {
			if v != "" && !seen[v] && len(out) < 8 {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

// --- сокет и STUN-подкачка NAT ---

func (s *appState) acquireSock() bool {
	if s.sockBusy {
		return false
	}
	s.sockBusy = true
	return true
}

func (s *appState) releaseSock() { s.sockBusy = false }

// startKeep поддерживает NAT-отображение, пока ждём ответа: адрес, который
// мы отдали другу, не должен протухнуть. Сокет в это время больше никто
// не читает, поэтому это безопасно. Освобождает сокет stopKeep.
func (s *appState) startKeep() {
	udp := s.conn
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.keepStop, s.keepDone = cancel, done
	go func() {
		defer close(done)
		t := time.NewTicker(keepEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = stun.Query(udp, stun.DefaultServers[0], 800*time.Millisecond)
			}
		}
	}()
}

// stopKeep останавливает подкачку и ждёт её завершения (чтобы сессия не
// делила сокет с STUN-запросом), затем освобождает сокет и вызывает after.
func (s *appState) stopKeep(after func()) {
	cancel, done := s.keepStop, s.keepDone
	s.keepStop, s.keepDone = nil, nil
	if cancel == nil {
		if after != nil {
			after()
		}
		return
	}
	go func() {
		cancel()
		<-done
		fyne.Do(func() {
			s.releaseSock()
			if after != nil {
				after()
			}
		})
	}()
}

// --- диалоги ---

func (s *appState) dismissCallDialog() {
	if s.callTimer != nil {
		s.callTimer.Stop()
		s.callTimer = nil
	}
	if s.callDlg != nil {
		s.callDlg.Hide()
		s.callDlg = nil
	}
}

func callDialogContent(name, subtitle string, buttons ...fyne.CanvasObject) fyne.CanvasObject {
	sub := widget.NewLabel(subtitle)
	sub.Alignment = fyne.TextAlignCenter
	sub.Importance = widget.LowImportance
	row := container.NewGridWithColumns(len(buttons), buttons...)
	return container.NewVBox(
		container.NewCenter(newAvatar(name, 72)),
		heading(name, fyne.TextAlignCenter),
		sub,
		row,
	)
}

func (s *appState) showWaitDialog(f *friend) {
	cancel := widget.NewButton("Отмена", s.cancelCall)
	d := dialog.NewCustomWithoutButtons("Звонок", callDialogContent(f.Name, "Звоним…", cancel), s.win)
	d.Resize(fyne.NewSize(300, 0))
	d.Show()
	s.callDlg = d
}

// --- входящий звонок ---

func (s *appState) showIncoming(f *friend, st peerStatus) {
	if s.sockBusy {
		return // идёт приглашение или другое соединение
	}
	if err := s.ensureConn(); err != nil || s.engine == nil {
		return
	}
	s.phase = phaseIncoming
	s.callFriend, s.callID, s.incomingEP = f, st.Call, st.EP
	s.renderFriends()

	s.app.SendNotification(fyne.NewNotification("Voice2Us", f.Name+" звонит вам"))
	s.win.RequestFocus()

	accept := widget.NewButtonWithIcon("Ответить", theme.ConfirmIcon(), func() {
		s.dismissCallDialog()
		s.acceptCall(f, st.Call, st.EP)
	})
	accept.Importance = widget.HighImportance
	decline := widget.NewButtonWithIcon("Отклонить", theme.CancelIcon(), s.declineCall)
	decline.Importance = widget.DangerImportance
	d := dialog.NewCustomWithoutButtons("Входящий звонок", callDialogContent(f.Name, "звонит вам", accept, decline), s.win)
	d.Resize(fyne.NewSize(320, 0))
	d.Show()
	s.callDlg = d
	s.callTimer = time.AfterFunc(incomingTimeout, func() {
		fyne.Do(func() {
			if s.phase == phaseIncoming && s.callID == st.Call {
				s.cancelIncoming("Пропущенный звонок от " + f.Name + ".")
			}
		})
	})
}

func (s *appState) cancelIncoming(msg string) {
	s.dismissCallDialog()
	s.phase = phaseIdle
	s.callFriend = nil
	s.setStatus(msg)
	s.renderFriends()
}

func (s *appState) declineCall() {
	f, id := s.callFriend, s.callID
	s.dismissCallDialog()
	s.phase = phaseIdle
	s.callFriend = nil
	if f != nil {
		s.pres.SetOwn(f.ID, stDeclined, id, nil)
		time.AfterFunc(8*time.Second, func() {
			fyne.Do(func() {
				if s.phase == phaseIdle {
					s.pres.SetOwn(f.ID, stIdle, "", nil)
				}
			})
		})
	}
	s.renderFriends()
}

// acceptCall: определяем свои адреса, публикуем их как ответ и сразу
// запускаем сессию к адресам звонящего.
func (s *appState) acceptCall(f *friend, id string, callerEP proto.Endpoints) {
	if !s.acquireSock() {
		s.cancelIncoming("Подождите: идёт другое соединение.")
		return
	}
	s.phase = phaseSession
	s.callFriend, s.callID = f, id
	s.setStatus("Отвечаем " + f.Name + "…")
	s.renderFriends()

	conn := s.conn
	go func() {
		public, lan := proto.DiscoverEndpoints(conn, stunServers(), 800*time.Millisecond)
		fyne.Do(func() {
			s.releaseSock()
			if s.phase != phaseSession || s.callID != id || s.session != nil {
				return
			}
			if st := s.statuses[f.ID]; st.State != stCalling || st.Call != id {
				s.abortSession("Звонок отменён.")
				return
			}
			if len(public)+len(lan) == 0 {
				s.abortSession("Не удалось определить адрес — проверьте интернет.")
				return
			}
			ep := proto.Endpoints{Public: public, LAN: lan}
			s.pres.SetOwn(f.ID, stAnswering, id, &ep)
			s.launchSession(f, callerEP)
		})
	}()
}

func (s *appState) abortSession(msg string) {
	s.phase = phaseIdle
	s.callFriend = nil
	s.setStatus(msg)
	s.renderFriends()
}
