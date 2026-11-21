// Package rdv — «встречалка»: крошечный HTTP-сервис, через который два
// приложения находят друг друга (обмен адресами по короткой фразе, статус
// онлайн и вызовы друзей). Сервер хранит только непрозрачные зашифрованные
// блоки в памяти и ничего не знает ни о ключах, ни о содержимом.
package rdv

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/scrypt"
)

// Алфавит Crockford: без I, L, O, U — их не спутать при диктовке.
const phraseAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const phraseLen = 10 // 50 бит

// NewPhrase генерирует код приглашения вида "K7QM2-XPD9T".
func NewPhrase() (string, error) {
	var raw [phraseLen]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	b := make([]byte, 0, phraseLen+1)
	for i, v := range raw {
		if i == phraseLen/2 {
			b = append(b, '-')
		}
		b = append(b, phraseAlphabet[int(v)%32]) // 256 % 32 == 0 — без смещения
	}
	return string(b), nil
}

// NormalizePhrase приводит ввод пользователя к каноническому виду:
// регистр, пробелы и дефисы, путаница O/0 и I/L/1.
func NormalizePhrase(s string) (string, bool) {
	var out []byte
	for _, r := range strings.ToUpper(s) {
		switch r {
		case ' ', '-', '\t', '\n', '\r', '_':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		if r > 127 || strings.IndexByte(phraseAlphabet, byte(r)) < 0 {
			return "", false
		}
		out = append(out, byte(r))
	}
	if len(out) != phraseLen {
		return "", false
	}
	return string(out), true
}

// PairKeys — производные от фразы: адрес слота на сервере и ключ, которым
// шифруется передача кодов. Две непересекающиеся части одного вывода scrypt.
type PairKeys struct {
	Slot string // 32 hex-символа
	Key  []byte // 32 байта
}

// DerivePair выводит слот и ключ из фразы. scrypt делает подбор 50-битной
// фразы по перехваченному блоку непрактичным.
func DerivePair(phrase string) (*PairKeys, error) {
	norm, ok := NormalizePhrase(phrase)
	if !ok {
		return nil, errors.New("неверный формат кода")
	}
	out, err := scrypt.Key([]byte(norm), []byte("voice2us/rdv/pair/v1"), 1<<15, 8, 1, 48)
	if err != nil {
		return nil, err
	}
	return &PairKeys{Slot: hex.EncodeToString(out[:16]), Key: out[16:48]}, nil
}

// FriendID — идентификатор дружбы на сервере, из общего ключа (отдельная
// метка HKDF: по нему ключ не восстановить). Известен только двоим.
func FriendID(shared []byte) string {
	return hex.EncodeToString(derive(shared, "voice2us/v2/friend-id"))
}

func derive(secret []byte, info string) []byte {
	k := make([]byte, 32)
	_, _ = io.ReadFull(hkdf.New(sha256.New, secret, nil, []byte(info)), k)
	return k
}

// seal / open: XChaCha20-Poly1305 со случайным 24-байтным nonce; назначение
// блока (aad) входит в аутентификацию, чтобы блок одного вида не подошёл
// на место другого.
func seal(key []byte, aad string, plain []byte) ([]byte, error) {
	a, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize(), a.NonceSize()+len(plain)+a.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, plain, []byte(aad)), nil
}

func open(key []byte, aad string, blob []byte) ([]byte, error) {
	a, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < a.NonceSize()+a.Overhead() {
		return nil, errors.New("блок слишком короткий")
	}
	return a.Open(nil, blob[:a.NonceSize()], blob[a.NonceSize():], []byte(aad))
}

// SealPair / OpenPair шифруют код при передаче через слот приглашения.
func SealPair(k *PairKeys, side string, plain []byte) ([]byte, error) {
	return seal(k.Key, "voice2us/pair/"+side, plain)
}

func OpenPair(k *PairKeys, side string, blob []byte) ([]byte, error) {
	return open(k.Key, "voice2us/pair/"+side, blob)
}

// SealFriend / OpenFriend шифруют блок статуса стороны side ("a" или "b")
// ключом дружбы.
func SealFriend(shared []byte, side string, plain []byte) ([]byte, error) {
	return seal(derive(shared, "voice2us/v2/presence/"+side), "voice2us/friend/"+side, plain)
}

func OpenFriend(shared []byte, side string, blob []byte) ([]byte, error) {
	return open(derive(shared, "voice2us/v2/presence/"+side), "voice2us/friend/"+side, blob)
}
