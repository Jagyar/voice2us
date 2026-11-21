package main

// Знакомство по короткому коду. Один создаёт приглашение — код вида
// K7QM2-XPD9T, второй вводит его. Приложения через «встречалку» обменивают
// зашифрованные кодом данные подключения (те же, что раньше копировали
// вручную), после чего соединяются напрямую и запоминают друг друга.

import (
	"context"
	"errors"
	"net"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Jagyar/voice2us/internal/proto"
	"github.com/Jagyar/voice2us/internal/rdv"
	"github.com/Jagyar/voice2us/internal/stun"
)

const (
	invitePoll = 1500 * time.Millisecond
	inviteKeep = 20 * time.Second
)

// --- приглашающий ---

func (s *appState) onInvite() {
	if s.phase != phaseIdle || !s.acquireSock() {
		s.setStatus("Сейчас идёт другое соединение — подождите.")
		return
	}
	if err := s.ensureConn(); err != nil {
		s.releaseSock()
		s.setStatus(err.Error())
		return
	}
	phrase, err := rdv.NewPhrase()
	if err != nil {
		s.releaseSock()
		s.setStatus("Ошибка: " + err.Error())
		return
	}

	code := canvas.NewText("· · · · · · · · · ·", theme.Color(theme.ColorNamePrimary))
	code.TextSize = 30
	code.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	code.Alignment = fyne.TextAlignCenter
	status := widget.NewLabel("Создаём приглашение…")
	status.Alignment = fyne.TextAlignCenter
	status.Importance = widget.LowImportance
	status.Wrapping = fyne.TextWrapWord

	ctx, cancel := context.WithCancel(context.Background())
	copyBtn := widget.NewButtonWithIcon("Скопировать", theme.ContentCopyIcon(), func() {
		s.win.Clipboard().SetContent(phrase)
	})
	copyBtn.Disable()
	cancelBtn := widget.NewButton("Отмена", cancel)
	intro := widget.NewLabel("Передайте этот код другу — один раз, голосом или в мессенджере.")
	intro.Alignment = fyne.TextAlignCenter
	intro.Wrapping = fyne.TextWrapWord
	d := dialog.NewCustomWithoutButtons("Пригласить друга", container.NewVBox(
		intro,
		container.NewPadded(code),
		status,
		container.NewGridWithColumns(2, copyBtn, cancelBtn),
	), s.win)
	d.Resize(fyne.NewSize(340, 0))
	d.Show()

	conn, port, name, client := s.conn, s.prefs.Int(prefUDPPort), s.myName(), s.rdvc
	go func() {
		defer cancel()
		my, peer, msg := s.runInvite(ctx, client, phrase, conn, port, name, func() {
			fyne.Do(func() {
				code.Text = phrase
				code.Refresh()
				copyBtn.Enable()
				status.SetText("Ждём друга… Код действует 10 минут.")
			})
		})
		fyne.Do(func() {
			d.Hide()
			s.releaseSock()
			if peer == nil {
				if msg != "" {
					s.setStatus(msg)
				}
				return
			}
			s.myCode = my
			s.startSession(peer, false)
		})
	}()
}

// runInvite выполняется в фоне и единолично владеет сокетом, пока идёт
// приглашение (сессия на нём ещё не запущена). Возвращает наш код и код
// друга, либо сообщение об ошибке (пустое — пользователь просто отменил).
func (s *appState) runInvite(ctx context.Context, c *rdv.Client, phrase string, conn *net.UDPConn,
	port int, name string, ready func()) (*proto.Code, *proto.Code, string) {

	keys, err := rdv.DerivePair(phrase)
	if err != nil {
		return nil, nil, "Ошибка: " + err.Error()
	}
	_, my, _, err := s.prepareCode(nil, conn, port, proto.RoleA, name)
	if err != nil {
		return nil, nil, "Ошибка: " + err.Error()
	}
	codeStr, err := proto.MarshalCode(my)
	if err != nil {
		return nil, nil, "Ошибка: " + err.Error()
	}
	blob, err := rdv.SealPair(keys, "a", []byte(codeStr))
	if err != nil {
		return nil, nil, "Ошибка: " + err.Error()
	}
	if err := c.PutPair(ctx, keys.Slot, "a", blob); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ""
		}
		return nil, nil, "Сервер недоступен (" + err.Error() + "). Попробуйте «Обмен кодами вручную»."
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c.DeletePair(dctx, keys.Slot)
	}()
	ready()

	deadline := time.NewTimer(rdv.PairTTL)
	defer deadline.Stop()
	poll := time.NewTicker(invitePoll)
	defer poll.Stop()
	keep := time.NewTicker(inviteKeep)
	defer keep.Stop()
	fails := 0

	for {
		select {
		case <-ctx.Done():
			return nil, nil, ""
		case <-deadline.C:
			return nil, nil, "Приглашение истекло. Создайте новое."
		case <-keep.C:
			// Пока ждём друга, не даём NAT забыть наш адрес.
			_, _ = stun.Query(conn, stun.DefaultServers[0], 800*time.Millisecond)
		case <-poll.C:
			reply, err := c.GetPair(ctx, keys.Slot, "b")
			if errors.Is(err, rdv.ErrNotFound) {
				fails = 0
				continue
			}
			if err != nil {
				if fails++; fails >= 5 {
					return nil, nil, "Потеряна связь с сервером."
				}
				continue
			}
			plain, err := rdv.OpenPair(keys, "b", reply)
			if err != nil {
				return nil, nil, "Ответ друга не удалось расшифровать."
			}
			peer, err := proto.UnmarshalCode(string(plain))
			if err != nil || peer.Role != proto.RoleB {
				return nil, nil, "Ответ друга повреждён."
			}
			return my, peer, ""
		}
	}
}

// --- принимающий ---

func (s *appState) onJoin() {
	if s.phase != phaseIdle || !s.acquireSock() {
		s.setStatus("Сейчас идёт другое соединение — подождите.")
		return
	}
	if err := s.ensureConn(); err != nil {
		s.releaseSock()
		s.setStatus(err.Error())
		return
	}

	entry := widget.NewEntry()
	entry.SetPlaceHolder("XXXXX-XXXXX")
	entry.TextStyle = fyne.TextStyle{Monospace: true}
	status := widget.NewLabel("")
	status.Alignment = fyne.TextAlignCenter
	status.Importance = widget.LowImportance
	status.Wrapping = fyne.TextWrapWord

	var d dialog.Dialog
	ctx, cancel := context.WithCancel(context.Background())
	connect := widget.NewButton("Подключиться", nil)
	connect.Importance = widget.HighImportance
	working := false
	cancelBtn := widget.NewButton("Отмена", func() {
		cancel()
		d.Hide()
		if !working { // иначе сокет освободит завершающаяся горутина
			s.releaseSock()
		}
	})
	connect.OnTapped = func() {
		if working {
			return
		}
		phrase := entry.Text
		if _, ok := rdv.NormalizePhrase(phrase); !ok {
			status.SetText("Код — 10 символов: цифры и латинские буквы, например K7QM2-XPD9T.")
			return
		}
		working = true
		connect.Disable()
		status.SetText("Ищем приглашение…")
		conn, port, name, client := s.conn, s.prefs.Int(prefUDPPort), s.myName(), s.rdvc
		go func() {
			my, peer, msg := s.runJoin(ctx, client, phrase, conn, port, name)
			fyne.Do(func() {
				working = false
				if ctx.Err() != nil { // пользователь закрыл окно
					s.releaseSock()
					return
				}
				if peer == nil {
					status.SetText(msg)
					connect.Enable()
					return
				}
				cancel()
				d.Hide()
				s.releaseSock()
				s.myCode = my
				s.startSession(peer, false)
			})
		}()
	}

	intro := widget.NewLabel("Введите код из приглашения друга.")
	intro.Alignment = fyne.TextAlignCenter
	d = dialog.NewCustomWithoutButtons("У меня есть код", container.NewVBox(
		intro, entry, status,
		container.NewGridWithColumns(2, connect, cancelBtn),
	), s.win)
	d.Resize(fyne.NewSize(340, 0))
	d.Show()
	s.win.Canvas().Focus(entry)
}

// runJoin: берём приглашение, отвечаем своим кодом. Работает в фоне.
func (s *appState) runJoin(ctx context.Context, c *rdv.Client, phrase string, conn *net.UDPConn,
	port int, name string) (*proto.Code, *proto.Code, string) {

	keys, err := rdv.DerivePair(phrase)
	if err != nil {
		return nil, nil, "Неверный формат кода."
	}
	blob, err := c.GetPair(ctx, keys.Slot, "a")
	if errors.Is(err, rdv.ErrNotFound) {
		return nil, nil, "Код не найден или уже устарел. Проверьте код или попросите новый."
	}
	if err != nil {
		return nil, nil, "Сервер недоступен (" + err.Error() + ")."
	}
	plain, err := rdv.OpenPair(keys, "a", blob)
	if err != nil {
		return nil, nil, "Приглашение не удалось расшифровать."
	}
	peer, err := proto.UnmarshalCode(string(plain))
	if err != nil || peer.Role != proto.RoleA {
		return nil, nil, "Приглашение повреждено."
	}

	_, my, warn, err := s.prepareCode(nil, conn, port, proto.RoleB, name)
	if err != nil {
		return nil, nil, "Ошибка: " + err.Error()
	}
	_ = warn
	codeStr, err := proto.MarshalCode(my)
	if err != nil {
		return nil, nil, "Ошибка: " + err.Error()
	}
	reply, err := rdv.SealPair(keys, "b", []byte(codeStr))
	if err != nil {
		return nil, nil, "Ошибка: " + err.Error()
	}
	if err := c.PutPair(ctx, keys.Slot, "b", reply); err != nil {
		if errors.Is(err, rdv.ErrExists) {
			return nil, nil, "Это приглашение уже использовано."
		}
		return nil, nil, "Не удалось отправить ответ: " + err.Error()
	}
	return my, peer, ""
}
