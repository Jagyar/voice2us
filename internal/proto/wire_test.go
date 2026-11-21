package proto

import (
	"bytes"
	"testing"
)

func pair(t *testing.T) (*Crypto, *Crypto) {
	t.Helper()
	k := bytes.Repeat([]byte{9}, 32)
	a, err := NewCrypto(k, RoleA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewCrypto(k, RoleB)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestReplayWindow(t *testing.T) {
	a, b := pair(t)
	seal := func(seq uint64) []byte { return a.Seal(nil, []byte("x"), seq, PktAudio) }

	for _, seq := range []uint64{0, 1, 5, 3} { // 3 — переупорядочен, но свежий
		if _, _, err := b.Open(seal(seq)); err != nil {
			t.Fatalf("seq %d должен приниматься: %v", seq, err)
		}
	}
	for _, seq := range []uint64{3, 5, 0} { // повторы
		if _, _, err := b.Open(seal(seq)); err == nil {
			t.Fatalf("повтор seq %d должен отвергаться", seq)
		}
	}
	if _, _, err := b.Open(seal(200)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Open(seal(2)); err == nil { // старше окна
		t.Fatal("пакет старше окна должен отвергаться")
	}
}

func TestGarbageDoesNotPoisonWindow(t *testing.T) {
	a, b := pair(t)
	forged := a.Seal(nil, []byte("x"), 1000, PktAudio)
	forged[len(forged)-1] ^= 1
	if _, _, err := b.Open(forged); err == nil {
		t.Fatal("подделка должна отвергаться")
	}
	if _, _, err := b.Open(a.Seal(nil, []byte("x"), 1, PktAudio)); err != nil {
		t.Fatalf("валидный пакет после мусора: %v", err)
	}
}

func TestWrongDirectionRejected(t *testing.T) {
	a, _ := pair(t)
	if _, _, err := a.Open(a.Seal(nil, nil, 0, PktPing)); err == nil {
		t.Fatal("свой же пакет не должен приниматься")
	}
}

// Один и тот же ключ в разных сессиях (сохранённый друг): у каждой сессии
// своя эпоха и свои ключи, а старая эпоха после потери связи не мешает новой.
func TestSharedKeyAcrossSessions(t *testing.T) {
	a1, b := pair(t)
	k := bytes.Repeat([]byte{9}, 32)
	a2, _ := NewCrypto(k, RoleA) // перезапущенный A, seq снова с нуля

	p1 := a1.Seal(nil, []byte("x"), 0, PktAudio)
	p2 := a2.Seal(nil, []byte("x"), 0, PktAudio)
	if bytes.Equal(p1[wireEpochBytes+wireSeqBytes:], p2[wireEpochBytes+wireSeqBytes:]) {
		t.Fatal("одинаковый шифротекст при одинаковом seq: ключи сессий совпали")
	}
	if _, _, err := b.Open(p1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Open(p2); err == nil {
		t.Fatal("чужая эпоха должна отвергаться, пока связь жива")
	}
	b.ResetPeer()
	if _, _, err := b.Open(p2); err != nil {
		t.Fatalf("после ResetPeer новая сессия друга должна приниматься: %v", err)
	}
}
