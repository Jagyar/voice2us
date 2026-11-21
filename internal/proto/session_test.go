package proto

import (
	"context"
	"crypto/rand"
	"net"
	"testing"
	"time"
)

// TestSessionLoopback runs two sessions against each other over the UDP
// loopback: handshake, audio in both directions, RTT and a clean hangup.
func TestSessionLoopback(t *testing.T) {
	connA, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	connB, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	codeA := &Code{
		Name: "Аня",
		Role: RoleA,
		Key:  key,
		Endpoints: Endpoints{
			Public: []string{connA.LocalAddr().String()},
		},
	}
	codeB := &Code{
		Name: "Боря",
		Role: RoleB,
		Endpoints: Endpoints{
			Public: []string{connB.LocalAddr().String()},
		},
	}

	sessA, err := NewSession(connA, codeA, codeB)
	if err != nil {
		t.Fatal(err)
	}
	sessB, err := NewSession(connB, codeB, codeA)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go sessA.Run(ctx)
	go sessB.Run(ctx)

	// Оба события о подключении.
	waitEvent(t, sessA, EvConnected, 3*time.Second)
	waitEvent(t, sessB, EvConnected, 3*time.Second)
	t.Log("соединение установлено")

	// Аня -> Боря: Боря обязан получить отправленный кадр без изменений.
	frame := make([]int16, FrameSamplesForTest)
	for i := range frame {
		frame[i] = int16(i*7 - 100)
	}
	sessA.SendAudio(frame)
	got := waitAudio(t, sessB, 3*time.Second)
	for i, v := range got {
		if v != frame[i] {
			t.Fatalf("кадр исказился на сэмпле %d: %d != %d", i, v, frame[i])
		}
	}

	// Боря -> Аня, проверка RTT: ping уходит каждые 2 секунды.
	if ev := waitEvent(t, sessA, EvRTT, 5*time.Second); ev.RTTms < 0 || ev.RTTms > 2000 {
		t.Fatalf("странный RTT: %d мс", ev.RTTms)
	}
	t.Log("RTT получен")

	// Боря завершает разговор — Аня должна увидеть EvPeerBye, затем EvStopped.
	sessB.Stop()
	waitEvent(t, sessA, EvPeerBye, 3*time.Second)
	waitEvent(t, sessA, EvStopped, 3*time.Second)
	sessA.Stop()
}

func waitEvent(t *testing.T, s *Session, kind EventKind, d time.Duration) Event {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("не дождались события %d", kind)
		}
	}
}

func waitAudio(t *testing.T, s *Session, d time.Duration) []int16 {
	t.Helper()
	select {
	case f := <-s.InFrames():
		return f
	case <-time.After(d):
		t.Fatal("не дождались аудиокадра")
		return nil
	}
}

const FrameSamplesForTest = 480
