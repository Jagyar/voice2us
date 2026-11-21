package proto

// Packet framing and encryption.
//
// Every packet on the wire looks like:
//
//	[8 bytes epoch][4 bytes seq32][chacha20poly1305 ciphertext + 16-byte tag]
//
// where plaintext = [1 byte packet type][payload].
//
// The 12-byte nonce is implicit and never goes on the wire: it is
// 4 direction bytes + 8 bytes of the reconstructed 64-bit sequence number.
// Each direction derives its own key from the shared key with HKDF, salted
// with the sender's random per-session epoch: the shared key may live for
// many calls (saved friend), but every call gets fresh keys, so the same
// key+nonce pair is never reused even though seq restarts at 0.

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	PktAudio     byte = 1
	PktKeepalive byte = 2
	PktPing      byte = 3
	PktPong      byte = 4
	PktBye       byte = 5

	wireEpochBytes = 8
	wireSeqBytes   = 4
	wireOverhead   = wireEpochBytes + wireSeqBytes + 16 // epoch + seq32 + Poly1305 tag
	maxPlaintext   = 4096
	replayWindow   = 64 // сколько последних номеров помним для анти-replay
)

// Role tells which side of the call we are. It fixes the direction of our
// key/nonce space and prevents both peers from, say, generating a key each.
type Role byte

const (
	RoleA Role = 'a' // создатель кода с ключом сессии
	RoleB Role = 'b' // ответивший
)

// Opposite returns the peer's role.
func Opposite(r Role) Role {
	if r == RoleA {
		return RoleB
	}
	return RoleA
}

var (
	noncePrefixA2B = [4]byte{1, 0, 0, 0}
	noncePrefixB2A = [4]byte{2, 0, 0, 0}
)

// Crypto seals and opens packets for one session participant.
type Crypto struct {
	shared     []byte
	our        Role
	send       cipher.AEAD
	sendEpoch  [wireEpochBytes]byte
	sendPrefix [4]byte
	recvPrefix [4]byte

	recvMu    sync.Mutex
	recv      cipher.AEAD // nil, пока не принят первый валидный пакет
	recvEpoch [wireEpochBytes]byte
	recvInit  bool
	recvLast  uint64
	recvMask  uint64 // бит i = получен пакет с номером recvLast-i
}

func deriveAEAD(shared []byte, sender Role, epoch []byte) (cipher.AEAD, error) {
	info := fmt.Sprintf("voice2us/v2/key/%c-to-%c", sender, Opposite(sender))
	r := hkdf.New(sha256.New, shared, epoch, []byte(info))
	k := make([]byte, 32)
	if _, err := io.ReadFull(r, k); err != nil {
		return nil, err
	}
	return chacha20poly1305.New(k)
}

// NewCrypto prepares a participant with a fresh random send epoch.
func NewCrypto(sessionKey []byte, our Role) (*Crypto, error) {
	if len(sessionKey) != 32 {
		return nil, errors.New("ключ сессии должен быть 32 байта")
	}
	c := &Crypto{shared: append([]byte(nil), sessionKey...), our: our}
	if _, err := rand.Read(c.sendEpoch[:]); err != nil {
		return nil, err
	}
	send, err := deriveAEAD(c.shared, our, c.sendEpoch[:])
	if err != nil {
		return nil, err
	}
	c.send = send
	if our == RoleA {
		c.sendPrefix, c.recvPrefix = noncePrefixA2B, noncePrefixB2A
	} else {
		c.sendPrefix, c.recvPrefix = noncePrefixB2A, noncePrefixA2B
	}
	return c, nil
}

// Seal appends a complete wire packet carrying pktType and payload to buf.
// Safe for concurrent use.
func (c *Crypto) Seal(buf, payload []byte, seq uint64, pktType byte) []byte {
	plain := make([]byte, 1+len(payload))
	plain[0] = pktType
	copy(plain[1:], payload)

	out := append(buf[:0], c.sendEpoch[:]...)
	out = binary.BigEndian.AppendUint32(out, uint32(seq))
	return c.send.Seal(out, nonce(c.sendPrefix, seq), plain, nil)
}

// ResetPeer забывает эпоху собеседника: после потери связи он мог
// перезапустить приложение, и его новая сессия пришлёт новую эпоху.
func (c *Crypto) ResetPeer() {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	c.recv = nil
	c.recvInit = false
	c.recvMask = 0
}

// Open validates and decrypts a wire packet, returning its type and payload.
// Packets that are too old, replayed or forged are rejected. Важно: счётчик
// анти-replay сдвигается ТОЛЬКО после успешной расшифровки — иначе мусорный
// пакет (неверный ключ) отравил бы окно. Эпоха собеседника принимается
// тоже только после успешной расшифровки и дальше «пришпиливается»:
// пакеты с чужой эпохой отбрасываются без траты CPU на расшифровку.
func (c *Crypto) Open(wire []byte) (byte, []byte, error) {
	if len(wire) < wireOverhead || len(wire) > wireOverhead+maxPlaintext {
		return 0, nil, errors.New("пакет неверного размера")
	}
	epoch := wire[:wireEpochBytes]
	seq32 := binary.BigEndian.Uint32(wire[wireEpochBytes : wireEpochBytes+wireSeqBytes])
	body := wire[wireEpochBytes+wireSeqBytes:]

	c.recvMu.Lock()
	aead := c.recv
	pinned := aead != nil
	sameEpoch := pinned && string(epoch) == string(c.recvEpoch[:])
	c.recvMu.Unlock()

	if pinned && !sameEpoch {
		return 0, nil, errors.New("чужая сессия")
	}
	if !pinned {
		var err error
		if aead, err = deriveAEAD(c.shared, Opposite(c.our), epoch); err != nil {
			return 0, nil, err
		}
	}

	seq64, ok := c.peekSeq(seq32)
	if !ok {
		return 0, nil, errors.New("повтор или устаревший пакет")
	}
	plain, err := aead.Open(nil, nonce(c.recvPrefix, seq64), body, nil)
	if err != nil {
		return 0, nil, err
	}

	c.recvMu.Lock()
	if c.recv == nil {
		c.recv = aead
		copy(c.recvEpoch[:], epoch)
	}
	c.recvMu.Unlock()
	c.commitSeq(seq64)
	return plain[0], plain[1:], nil
}

// peekSeq реконструирует полный 64-битный счётчик из усечённого, не меняя
// состояние. false = повтор или пакет старше окна в replayWindow номеров.
// Небольшое окно позволяет принимать слегка переупорядоченные пакеты.
func (c *Crypto) peekSeq(seq32 uint32) (uint64, bool) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()

	if !c.recvInit {
		return uint64(seq32), true
	}
	delta := int32(seq32 - uint32(c.recvLast))
	if delta > 0 {
		return c.recvLast + uint64(delta), true
	}
	back := uint64(-int64(delta))
	if back >= replayWindow || back > c.recvLast {
		return 0, false
	}
	if c.recvMask&(1<<back) != 0 {
		return 0, false // уже принимали
	}
	return c.recvLast - back, true
}

// commitSeq фиксирует счётчик после успешной расшифровки.
func (c *Crypto) commitSeq(seq uint64) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	switch {
	case !c.recvInit:
		c.recvInit = true
		c.recvLast = seq
		c.recvMask = 1
	case seq > c.recvLast:
		if shift := seq - c.recvLast; shift >= replayWindow {
			c.recvMask = 1
		} else {
			c.recvMask = c.recvMask<<shift | 1
		}
		c.recvLast = seq
	default:
		c.recvMask |= 1 << (c.recvLast - seq)
	}
}

func nonce(prefix [4]byte, seq uint64) []byte {
	n := make([]byte, 12)
	copy(n, prefix[:])
	binary.BigEndian.PutUint64(n[4:], seq)
	return n
}
