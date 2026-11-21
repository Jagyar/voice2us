package main

// Сохранённый друг: после первого удачного обмена кодами приложение помнит
// общий ключ и адреса друга, и дальше достаточно нажать «Позвонить».
// Ключ лежит в настройках приложения (у пользователя на диске); каждая
// сессия всё равно получает свои ключи шифрования (см. proto/wire.go).

import (
	"encoding/json"
	"net"
	"net/netip"

	"github.com/Jagyar/voice2us/internal/proto"
)

const (
	prefFriend  = "friend"
	prefUDPPort = "udpPort"
)

type friend struct {
	Name string          `json:"name"`
	Role proto.Role      `json:"role"` // наша роль в этой паре
	Key  []byte          `json:"key"`
	Peer proto.Endpoints `json:"peer"`
}

func (s *appState) loadFriend() *friend {
	raw := s.prefs.String(prefFriend)
	if raw == "" {
		return nil
	}
	var f friend
	if json.Unmarshal([]byte(raw), &f) != nil || len(f.Key) != 32 ||
		(f.Role != proto.RoleA && f.Role != proto.RoleB) {
		return nil
	}
	return &f
}

func (s *appState) saveFriend(f *friend) {
	if raw, err := json.Marshal(f); err == nil {
		s.prefs.SetString(prefFriend, string(raw))
	}
}

func (s *appState) forgetFriend() {
	s.prefs.RemoveValue(prefFriend)
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

// rememberConnection запоминает друга после успешного соединения: ключ,
// его адреса из кода и тот адрес, с которого он реально ответил (с ним
// повторное подключение чаще всего и срабатывает).
func rememberConnection(our, peer *proto.Code, remote string) *friend {
	key := our.Key
	if our.Role != proto.RoleA {
		key = peer.Key
	}
	f := &friend{Name: peer.Name, Role: our.Role, Key: key, Peer: peer.Endpoints}
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
// публичный порт совпадает с локальным, и адрес не меняется между
// звонками. Если порт занят — любой свободный.
func listenUDP(port int) (*net.UDPConn, error) {
	if port > 0 {
		if c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port}); err == nil {
			return c, nil
		}
	}
	return net.ListenUDP("udp4", nil)
}
