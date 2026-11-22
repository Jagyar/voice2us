package main

// Друзья: после первого удачного соединения приложение помнит общий ключ
// дружбы и адреса друга. Ключ лежит в настройках приложения (на диске
// пользователя); каждая сессия всё равно получает свои ключи шифрования
// (см. proto/wire.go). Идентификатор дружбы на «встречалке» выводится
// из ключа, поэтому друзья из версии 1.x подхватываются без повторного
// обмена кодами.

import (
	cryptorand "crypto/rand"
	"encoding/json"
	"net"
	"net/netip"
	"strings"

	"github.com/Jagyar/voice2us/internal/proto"
	"github.com/Jagyar/voice2us/internal/rdv"
)

const (
	prefFriend  = "friend"  // v1.x: единственный друг
	prefFriends = "friends" // v2: список
	prefUDPPort = "udpPort"
	maxFriends  = 30
)

type friend struct {
	ID   string          `json:"id"` // rdv.FriendID(Key)
	Name string          `json:"name"`
	Role proto.Role      `json:"role"` // наша роль в этой паре
	Key  []byte          `json:"key"`
	Peer proto.Endpoints `json:"peer"` // последние известные адреса (запасной прямой звонок)
}

// side — наша сторона на «встречалке» ("a"/"b").
func (f *friend) side() string { return string(rune(f.Role)) }

// peerSide — сторона друга.
func (f *friend) peerSide() string { return string(rune(proto.Opposite(f.Role))) }

func (f *friend) valid() bool {
	return len(f.Key) == 32 && (f.Role == proto.RoleA || f.Role == proto.RoleB)
}

func (s *appState) loadFriends() []*friend {
	var list []*friend
	if raw := s.prefs.String(prefFriends); raw != "" {
		_ = json.Unmarshal([]byte(raw), &list)
	} else if old := s.prefs.String(prefFriend); old != "" {
		// Миграция с 1.x: один сохранённый друг.
		var f friend
		if json.Unmarshal([]byte(old), &f) == nil {
			list = []*friend{&f}
		}
	}
	out := list[:0]
	for _, f := range list {
		if f != nil && f.valid() {
			f.ID = rdv.FriendID(f.Key)
			f.Name = strings.TrimSpace(f.Name)
			if f.Name == "" {
				f.Name = "Друг"
			}
			out = append(out, f)
		}
	}
	return out
}

func (s *appState) saveFriends(list []*friend) {
	raw, err := json.Marshal(list)
	if err != nil {
		return
	}
	s.prefs.SetString(prefFriends, string(raw))
	s.prefs.RemoveValue(prefFriend)
}

func (s *appState) findFriend(id string) *friend {
	for _, f := range s.friends {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// upsertFriend добавляет друга или обновляет адреса уже известного
// (имя, которое пользователь мог поменять, при этом не затирается).
func (s *appState) upsertFriend(nf *friend) bool {
	if old := s.findFriend(nf.ID); old != nil {
		old.Peer = nf.Peer
		s.saveFriends(s.friends)
		return true
	}
	if len(s.friends) >= maxFriends {
		return false
	}
	s.friends = append(s.friends, nf)
	s.saveFriends(s.friends)
	return true
}

func (s *appState) removeFriend(id string) {
	out := s.friends[:0]
	for _, f := range s.friends {
		if f.ID != id {
			out = append(out, f)
		}
	}
	s.friends = out
	s.saveFriends(s.friends)
}

// codes восстанавливает пару кодов (наш, друга) для NewSession.
func (f *friend) codes(myName string) (our, peer *proto.Code) {
	our = &proto.Code{Name: myName, Role: f.Role}
	peer = &proto.Code{Name: f.Name, Role: proto.Opposite(f.Role), Endpoints: f.Peer}
	if f.Role == proto.RoleA {
		our.Key = f.Key
	} else {
		peer.Key = f.Key
	}
	return our, peer
}

// rememberConnection строит запись друга после успешного соединения: ключ,
// его адреса из кода и тот адрес, с которого он реально ответил.
func rememberConnection(our, peer *proto.Code, remote string) *friend {
	key := our.Key
	if our.Role != proto.RoleA {
		key = peer.Key
	}
	f := &friend{ID: rdv.FriendID(key), Name: peer.Name, Role: our.Role, Key: key, Peer: peer.Endpoints}
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		if ap.Addr().IsPrivate() {
			f.Peer.LAN = prepend(f.Peer.LAN, remote)
		} else {
			f.Peer.Public = prepend(f.Peer.Public, remote)
		}
	}
	return f
}

func prepend(list []string, v string) []string {
	out := []string{v}
	for _, x := range list {
		if x != v && len(out) < 8 {
			out = append(out, x)
		}
	}
	return out
}

// listenUDP открывает сокет на сохранённом порту: у многих роутеров
// публичный порт совпадает с локальным. Если порт занят — любой свободный.
func listenUDP(port int) (*net.UDPConn, error) {
	if port > 0 {
		if c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port}); err == nil {
			return c, nil
		}
	}
	return net.ListenUDP("udp4", nil)
}

func randRead(b []byte) (int, error) { return cryptorand.Read(b) }
