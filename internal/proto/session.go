package proto

// Session is the live UDP connection between the two peers: hole punching,
// keepalives, ping/RTT and the audio stream. One session per call; the UDP
// socket is owned by the caller (so the endpoint discovered via STUN for
// this very socket is what the peer got in the connection code).

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Jagyar/voice2us/internal/stun"
)

type EventKind int

const (
	EvConnected EventKind = iota // установлено соединение
	EvLost                       // связь оборвалась, идёт переподключение
	EvStalled                    // переподключение затянулось — вероятно, нужен новый код
	EvRTT                        // пришёл pong, известна задержка
	EvPeerBye                    // друг завершил разговор
	EvStopped                    // сессия полностью завершена
)

type Event struct {
	Kind   EventKind
	Peer   string
	Remote string // для EvConnected: адрес, с которого ответил друг
	RTTms  int64
	Reason string
}

const (
	blastInterval    = 100 * time.Millisecond // частота «прострелов» при пробитии NAT
	keepaliveEvery   = 1 * time.Second
	pingEvery        = 2 * time.Second
	connectedTimeout = 4 * time.Second // тишина дольше — считаем связь потерянной
	stallAfter       = 90 * time.Second
	audioChanSize    = 64 // ~0.6 s входящего звука
)

// Session manages one call over the caller's UDP socket.
type Session struct {
	conn *net.UDPConn
	our  *Code
	peer *Code
	cr   *Crypto

	events   chan Event
	inFrames chan []int16 // принятые кадры PCM (моно 48 кГц)
	outAudio chan []int16
	seq      atomic.Uint64

	connected atomic.Bool
	remote    atomic.Pointer[net.UDPAddr]
	alt       atomic.Pointer[net.UDPAddr] // запасной адрес, если друг виден и так и так
	lastRecv  atomic.Int64
	lastAlt   atomic.Int64

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewSession creates a call session. The session key is taken from our code
// if we are RoleA, otherwise from the peer's code.
func NewSession(conn *net.UDPConn, our, peer *Code) (*Session, error) {
	if conn == nil || our == nil || peer == nil {
		return nil, errors.New("нужны сокет и оба кода")
	}
	if our.Role == peer.Role {
		return nil, errors.New("у обеих сторон одинаковая роль — обмен кодами выполнен в неверном порядке")
	}

	key := peer.Key
	if our.Role == RoleA {
		key = our.Key
	}
	cr, err := NewCrypto(key, our.Role)
	if err != nil {
		return nil, err
	}

	return &Session{
		conn:     conn,
		our:      our,
		peer:     peer,
		cr:       cr,
		events:   make(chan Event, 16),
		inFrames: make(chan []int16, audioChanSize),
		outAudio: make(chan []int16, 16),
		stopCh:   make(chan struct{}),
	}, nil
}

func (s *Session) Events() <-chan Event     { return s.events }
func (s *Session) InFrames() <-chan []int16 { return s.inFrames }
func (s *Session) IsConnected() bool        { return s.connected.Load() }

// SendAudio queues one PCM frame (mono 48 kHz, s16le samples) for sending.
// The frame is copied, so the caller may reuse its buffer immediately.
// Frames are dropped if the queue is full — live audio prefers fresh data.
func (s *Session) SendAudio(frame []int16) {
	cp := make([]int16, len(frame))
	copy(cp, frame)
	select {
	case s.outAudio <- cp:
	default:
	}
}

// Stop tears the session down: sends bye (three times, UDP is lossy —
// losing the only one would leave the peer in "reconnecting" forever).
// The UDP socket is NOT closed: it belongs to the caller and must stay
// usable for the next call (its NAT mapping is what the peer's code has).
func (s *Session) Stop() {
	s.stopOnce.Do(func() {
		if a := s.remote.Load(); a != nil && s.connected.Load() {
			for i := 0; i < 3; i++ {
				wire := s.cr.Seal(nil, nil, s.seq.Add(1)-1, PktBye)
				_, _ = s.conn.WriteToUDP(wire, a)
			}
		}
		s.doShutdown()
	})
}

// shutdown гарантирует однократность через stopOnce; вызывается из recvLoop
// и Run. Из-под Stop (уже внутри Once) звать только doShutdown — иначе
// вложенный Once.Do сам себя заблокирует.
func (s *Session) shutdown() {
	s.stopOnce.Do(s.doShutdown)
}

func (s *Session) doShutdown() {
	s.connected.Store(false)
	close(s.stopCh)
	// Сокет намеренно не закрываем — он общий на несколько разговоров
	// и принадлежит владельцу (см. Stop).
}

// Run blocks until the session ends (stop, peer bye or ctx cancelled).
// EvStopped излучается только когда завершились ВСЕ горутины сессии,
// включая recvLoop (тот выходит по таймауту чтения, до 200 мс): после
// события сокет гарантированно свободен для следующего разговора.
func (s *Session) Run(ctx context.Context) {
	s.drainSocket()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.recvLoop() }()
	go func() { defer wg.Done(); s.audioOut() }()

	s.sendLoop(ctx)
	s.shutdown() // если вышли по ctx/bye — остановим остальных
	wg.Wait()
	s.emit(Event{Kind: EvStopped})
}

// drainSocket вычитывает и выбрасывает всё, что осталось в буфере приёма
// от прошлых сессий на этом сокете — иначе хвосты старого разговора
// попадут в новый и будут отброшены уже в процессе.
func (s *Session) drainSocket() {
	_ = s.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 2048)
	for {
		if _, _, err := s.conn.ReadFromUDP(buf); err != nil {
			break
		}
	}
	_ = s.conn.SetReadDeadline(time.Time{})
}

func (s *Session) emit(ev Event) {
	select {
	case s.events <- ev:
	default:
	}
}

// peerAddrs resolves the addresses we should blast packets to.
func (s *Session) peerAddrs() []*net.UDPAddr {
	var list []*net.UDPAddr
	seen := map[string]bool{}
	for _, str := range append(append([]string{}, s.peer.Public...), s.peer.LAN...) {
		if str == "" || seen[str] {
			continue
		}
		if addr, err := net.ResolveUDPAddr("udp4", str); err == nil {
			list = append(list, addr)
			seen[str] = true
		}
	}
	return list
}

func (s *Session) recvLoop() {
	buf := make([]byte, 2048)
	for {
		// Читаем с коротким дедлайном: сокет остаётся открытым и после
		// сессии, поэтому единственный способ выйти — заметить stopCh
		// между таймаутами чтения.
		_ = s.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, src, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				select {
				case <-s.stopCh:
					return
				default:
				}
			}
			continue
		}

		pktType, payload, err := s.cr.Open(buf[:n])
		if err != nil {
			continue // мусор или подделка
		}

		now := time.Now().UnixMilli()
		if !s.connected.Load() {
			s.remote.Store(src)
			s.connected.Store(true)
			s.emit(Event{Kind: EvConnected, Peer: s.peer.Name, Remote: src.String()})
		} else if cur := s.remote.Load(); cur != nil && !sameAddr(cur, src) {
			// Друг виден через ещё один путь (LAN vs интернет): запоминаем
			// как запасной, но основной не меняем без причины.
			s.alt.Store(src)
			s.lastAlt.Store(now)
			continue
		}
		s.lastRecv.Store(now)

		switch pktType {
		case PktAudio:
			if len(payload) >= 2 && len(payload)%2 == 0 {
				frame := make([]int16, len(payload)/2)
				for i := range frame {
					frame[i] = int16(binary.LittleEndian.Uint16(payload[i*2:]))
				}
				select {
				case s.inFrames <- frame:
				default:
				}
			}
		case PktPing:
			if len(payload) == 8 && s.connected.Load() {
				wire := s.cr.Seal(nil, payload, s.seq.Add(1)-1, PktPong)
				_, _ = s.conn.WriteToUDP(wire, s.remote.Load())
			}
		case PktPong:
			if len(payload) == 8 {
				t := int64(binary.BigEndian.Uint64(payload))
				rtt := now - t
				if rtt >= 0 && rtt < 10000 {
					s.emit(Event{Kind: EvRTT, RTTms: rtt})
				}
			}
		case PktBye:
			s.emit(Event{Kind: EvPeerBye, Peer: s.peer.Name})
			s.shutdown()
			return
		}
	}
}

func (s *Session) sendLoop(ctx context.Context) {
	addrs := s.peerAddrs()
	tick := time.NewTicker(blastInterval)
	defer tick.Stop()

	blastSince := time.Now()
	stallEmitted := false
	lastKeep := time.Now()
	lastPing := time.Now()

	send := func(pkt byte, payload []byte, addr *net.UDPAddr) {
		wire := s.cr.Seal(nil, payload, s.seq.Add(1)-1, pkt)
		_, _ = s.conn.WriteToUDP(wire, addr)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-tick.C:
		}

		now := time.Now()

		if s.connected.Load() {
			silence := now.UnixMilli() - s.lastRecv.Load()
			if silence > connectedTimeout.Milliseconds() {
				s.connected.Store(false)
				s.alt.Store(nil)
				s.cr.ResetPeer() // друг мог перезапуститься — примем его новую эпоху
				blastSince = now
				stallEmitted = false
				s.emit(Event{Kind: EvLost, Reason: "связь прервалась"})
			} else {
				if a := s.alt.Load(); a != nil && silence > 2000 && now.UnixMilli()-s.lastAlt.Load() < 1000 {
					// Основной путь молчит, запасной живой — переключаемся.
					s.remote.Store(a)
					s.alt.Store(nil)
				}
				if now.Sub(lastKeep) >= keepaliveEvery {
					lastKeep = now
					send(PktKeepalive, nil, s.remote.Load())
				}
				if now.Sub(lastPing) >= pingEvery {
					lastPing = now
					send(PktPing, packTime(now.UnixMilli()), s.remote.Load())
				}
				continue
			}
		}

		// Пробитие NAT: сыплем пакеты во все известные адреса друга.
		for _, a := range addrs {
			send(PktKeepalive, nil, a)
		}
		if !stallEmitted && now.Sub(blastSince) > stallAfter {
			stallEmitted = true
			s.emit(Event{Kind: EvStalled})
		}
	}
}

func (s *Session) audioOut() {
	for {
		select {
		case <-s.stopCh:
			return
		case f := <-s.outAudio:
			if !s.connected.Load() {
				continue
			}
			payload := make([]byte, len(f)*2)
			for i, v := range f {
				binary.LittleEndian.PutUint16(payload[i*2:], uint16(v))
			}
			wire := s.cr.Seal(nil, payload, s.seq.Add(1)-1, PktAudio)
			if a := s.remote.Load(); a != nil {
				_, _ = s.conn.WriteToUDP(wire, a)
			}
		}
	}
}

func sameAddr(a, b *net.UDPAddr) bool {
	return a.Port == b.Port && a.IP.Equal(b.IP)
}

func packTime(ms int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(ms))
	return b
}

// DiscoverEndpoints finds where the internet and the LAN see our socket.
// STUN servers are queried one by one (sharing the socket means sharing
// its read deadline); every distinct mapping is kept — a symmetric NAT
// gives different ports to different servers, and any of them may work.
func DiscoverEndpoints(conn *net.UDPConn, stunServers []string, perServerTimeout time.Duration) (public, lan []string) {
	port := conn.LocalAddr().(*net.UDPAddr).Port
	lan = lanAddrs(port)

	seen := map[string]bool{}
	for _, server := range stunServers {
		addr, err := stun.Query(conn, server, perServerTimeout)
		if err != nil {
			continue
		}
		str := addr.String()
		if !seen[str] {
			seen[str] = true
			public = append(public, str)
		}
		if len(public) >= 4 {
			break
		}
	}
	return public, lan
}

func lanAddrs(port int) []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || virtualIface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
				out = append(out, fmt.Sprintf("%s:%d", ipn.IP.String(), port))
			}
		}
	}
	return out
}

// virtualIface отсеивает интерфейсы контейнеров и виртуалок: их адреса
// бесполезны другу и лишь раскрывают лишнее о вашей машине.
func virtualIface(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "vethernet", "utun", "awdl", "llw", "bridge"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}
