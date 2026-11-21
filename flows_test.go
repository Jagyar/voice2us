package main

import (
	"bytes"
	"context"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Jagyar/voice2us/internal/proto"
	"github.com/Jagyar/voice2us/internal/rdv"
	"github.com/Jagyar/voice2us/internal/stun"
)

func lanAvailable() bool {
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Flags&net.FlagUp != 0 && i.Flags&net.FlagLoopback == 0 {
			if addrs, _ := i.Addrs(); len(addrs) > 0 {
				return true
			}
		}
	}
	return false
}

// Приглашение по короткому коду от начала до конца: обе стороны получают
// коды друг друга и один и тот же ключ дружбы.
func TestInviteJoinFlow(t *testing.T) {
	if !lanAvailable() {
		t.Skip("нет сетевого интерфейса для LAN-адресов")
	}
	old := stun.DefaultServers
	stun.DefaultServers = nil // без внешней сети: только LAN-адреса
	defer func() { stun.DefaultServers = old }()

	srv := rdv.NewServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := rdv.NewClient(ts.URL)

	connA, _ := net.ListenUDP("udp4", nil)
	connB, _ := net.ListenUDP("udp4", nil)
	defer connA.Close()
	defer connB.Close()

	s := &appState{}
	phrase, _ := rdv.NewPhrase()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	type res struct {
		my, peer *proto.Code
		msg      string
	}
	ready := make(chan struct{})
	inv := make(chan res, 1)
	go func() {
		my, peer, msg := s.runInvite(ctx, c, phrase, connA, 0, "Аня", func() { close(ready) })
		inv <- res{my, peer, msg}
	}()
	<-ready

	// Друг вводит код в «небрежном» виде: нижний регистр, без дефиса.
	jmy, jpeer, jmsg := s.runJoin(ctx, c, lower(phrase), connB, 0, "Боря")
	if jmsg != "" || jpeer == nil {
		t.Fatalf("join: %q", jmsg)
	}
	r := <-inv
	if r.msg != "" || r.peer == nil {
		t.Fatalf("invite: %q", r.msg)
	}
	if r.my.Role != proto.RoleA || r.peer.Role != proto.RoleB || jmy.Role != proto.RoleB || jpeer.Role != proto.RoleA {
		t.Fatal("роли")
	}
	if r.peer.Name != "Боря" || jpeer.Name != "Аня" {
		t.Fatalf("имена: %q %q", r.peer.Name, jpeer.Name)
	}
	if len(r.my.Key) != 32 || !bytes.Equal(r.my.Key, jpeer.Key) {
		t.Fatal("ключ дружбы должен совпасть")
	}
	if len(r.peer.LAN) == 0 {
		t.Fatal("в коде друга нет адресов")
	}
	// слот убран после обмена
	keys, _ := rdv.DerivePair(phrase)
	if _, err := c.GetPair(ctx, keys.Slot, "a"); err == nil {
		t.Fatal("слот приглашения должен быть удалён")
	}
	// и вторая попытка ввести тот же код не проходит
	if _, p2, msg := s.runJoin(ctx, c, phrase, connB, 0, "Вася"); p2 != nil || msg == "" {
		t.Fatalf("повторный ввод кода должен отвергаться, msg=%q", msg)
	}
}

func lower(p string) string {
	b := []byte(p)
	for i, ch := range b {
		if ch >= 'A' && ch <= 'Z' {
			b[i] = ch + 32
		} else if ch == '-' {
			b[i] = ' '
		}
	}
	return string(b)
}

func TestJoinWrongOrExpiredCode(t *testing.T) {
	srv := rdv.NewServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	s := &appState{}
	_, p, msg := s.runJoin(context.Background(), rdv.NewClient(ts.URL), "K7QM2-XPD9T", nil, 0, "x")
	if p != nil || msg == "" {
		t.Fatal("несуществующий код должен давать сообщение об ошибке")
	}
	_, p, msg = s.runJoin(context.Background(), rdv.NewClient("http://127.0.0.1:1"), "K7QM2-XPD9T", nil, 0, "x")
	if p != nil || msg == "" {
		t.Fatal("недоступный сервер должен давать сообщение об ошибке")
	}
}

// Две копии приложения (стороны a и b одной дружбы) видят друг друга онлайн,
// передают вызов с адресами и ответ.
func TestPresenceCallHandshake(t *testing.T) {
	srv := rdv.NewServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	key := bytes.Repeat([]byte{7}, 32)
	fa := &friend{ID: rdv.FriendID(key), Name: "Боря", Role: proto.RoleA, Key: key}
	fb := &friend{ID: rdv.FriendID(key), Name: "Аня", Role: proto.RoleB, Key: key}

	type upd struct {
		who string
		st  peerStatus
	}
	var mu sync.Mutex
	var log []upd
	mk := func(who string, f *friend) *presence {
		p := newPresence()
		p.SetName(who)
		p.SetClient(rdv.NewClient(ts.URL))
		p.SetFriends([]*friend{f})
		p.onUpdate = func(_ string, st peerStatus) {
			mu.Lock()
			log = append(log, upd{who, st})
			mu.Unlock()
		}
		go p.Run()
		return p
	}
	pa, pb := mk("Аня", fa), mk("Боря", fb)
	defer pa.Stop()
	defer pb.Stop()

	wait := func(what string, cond func(upd) bool) upd {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			for i := len(log) - 1; i >= 0; i-- {
				if cond(log[i]) {
					u := log[i]
					mu.Unlock()
					return u
				}
			}
			mu.Unlock()
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("не дождались: %s", what)
		return upd{}
	}

	wait("A видит B онлайн", func(u upd) bool {
		return u.who == "Аня" && u.st.Online && u.st.State == stIdle && u.st.Name == "Боря"
	})
	wait("B видит A онлайн", func(u upd) bool { return u.who == "Боря" && u.st.Online && u.st.Name == "Аня" })

	ep := proto.Endpoints{LAN: []string{"192.168.1.5:40000"}, Public: []string{"203.0.113.7:40000", "evil.example:1"}}
	pa.SetOwn(fa.ID, stCalling, "call1", &ep)
	got := wait("B видит вызов", func(u upd) bool { return u.who == "Боря" && u.st.State == stCalling })
	if got.st.Call != "call1" || len(got.st.EP.LAN) != 1 || len(got.st.EP.Public) != 1 {
		t.Fatalf("вызов дошёл неверно (адреса должны быть очищены): %+v", got.st)
	}

	epB := proto.Endpoints{LAN: []string{"192.168.1.6:41000"}}
	pb.SetOwn(fb.ID, stAnswering, "call1", &epB)
	ans := wait("A видит ответ", func(u upd) bool { return u.who == "Аня" && u.st.State == stAnswering })
	if ans.st.Call != "call1" || ans.st.EP.LAN[0] != "192.168.1.6:41000" {
		t.Fatalf("ответ: %+v", ans.st)
	}

	pb.SetBusy(true)
	wait("A видит «в разговоре»", func(u upd) bool { return u.who == "Аня" && u.st.State == stInCall })

	pb.Stop() // B пропал: heartbeat прекращается, через TTL A видит офлайн
}

func TestFriendMigrationAndID(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 32)
	f := &friend{Name: " Аня ", Role: proto.RoleB, Key: key}
	if !f.valid() || f.side() != "b" || f.peerSide() != "a" {
		t.Fatal("стороны")
	}
	our, peer := f.codes("я")
	if our.Role != proto.RoleB || peer.Role != proto.RoleA || !bytes.Equal(peer.Key, key) || our.Key != nil {
		t.Fatal("codes для роли B")
	}
	bad := &friend{Role: proto.RoleA, Key: []byte{1}}
	if bad.valid() {
		t.Fatal("ключ неверной длины")
	}
	if rdv.FriendID(key) == "" {
		t.Fatal("friend id")
	}
}
