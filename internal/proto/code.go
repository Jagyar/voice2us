package proto

// The connection code is the only setup information the peers exchange —
// via any messenger they like. The creator's code carries the session key,
// so both sides must treat these strings like passwords.

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
)

// maxEndpoints ограничивает число адресов каждого типа: код приходит от
// стороннего человека, и тысячи адресов превратили бы приложение в источник
// UDP-флуда.
const maxEndpoints = 8

// sanitizeEndpoints оставляет только валидные IPv4-литералы ip:port
// (никаких имён хостов — это были бы DNS-запросы по чужому указанию),
// без дублей и не более maxEndpoints штук.
func sanitizeEndpoints(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		ap, err := netip.ParseAddrPort(s)
		if err != nil || !ap.Addr().Is4() || ap.Port() == 0 ||
			ap.Addr().IsUnspecified() || ap.Addr().IsMulticast() {
			continue
		}
		str := ap.String()
		if seen[str] {
			continue
		}
		seen[str] = true
		out = append(out, str)
		if len(out) >= maxEndpoints {
			break
		}
	}
	return out
}

// SameCode reports whether two codes describe the same peer half
// (used to detect that the user pasted their own code).
func SameCode(a, b *Code) bool {
	if a == nil || b == nil || a.Role != b.Role {
		return false
	}
	if a.Role == RoleA {
		return bytes.Equal(a.Key, b.Key)
	}
	return a.Name == b.Name && strings.Join(a.Public, ",") == strings.Join(b.Public, ",") &&
		strings.Join(a.LAN, ",") == strings.Join(b.LAN, ",")
}

const codeMagic = "V2U1."

// Endpoints lists where the peer should try to reach us. Public addresses
// come from STUN discovery, LAN addresses from the local interfaces.
type Endpoints struct {
	Public []string `json:"p,omitempty"` // ip:port, как их видит интернет
	LAN    []string `json:"l,omitempty"` // ip:port в локальной сети
}

// Code is one peer's half of the connection data.
type Code struct {
	Version int    `json:"v"`
	Name    string `json:"n"`
	Role    Role   `json:"r"`
	Key     []byte `json:"k,omitempty"` // 32 байта, только в коде RoleA
	Endpoints
}

// MarshalCode packs the code into a compact string like "V2U1.<base64>".
func MarshalCode(c *Code) (string, error) {
	c.Version = 1
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return codeMagic + base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// UnmarshalCode parses a connection code, tolerating stray spaces and
// newlines that messengers like to insert into long strings.
func UnmarshalCode(s string) (*Code, error) {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, strings.TrimSpace(s))

	if !strings.HasPrefix(s, codeMagic) {
		return nil, errors.New("это не код Voice2Us")
	}

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, codeMagic))
	if err != nil {
		return nil, errors.New("код повреждён")
	}

	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("код повреждён")
	}
	// Лимит на случай «gzip-бомбы» во вставленном тексте.
	rawJSON, err := io.ReadAll(io.LimitReader(zr, 64*1024))
	if err != nil {
		return nil, errors.New("код повреждён")
	}

	var c Code
	if err := json.Unmarshal(rawJSON, &c); err != nil {
		return nil, errors.New("код повреждён")
	}
	if c.Version != 1 {
		return nil, errors.New("неизвестная версия кода")
	}
	if c.Role != RoleA && c.Role != RoleB {
		return nil, errors.New("код повреждён")
	}
	if c.Role == RoleA && len(c.Key) != 32 {
		return nil, errors.New("в коде нет ключа сессии")
	}
	c.Public = sanitizeEndpoints(c.Public)
	c.LAN = sanitizeEndpoints(c.LAN)
	if runes := []rune(c.Name); len(runes) > 64 {
		c.Name = string(runes[:64])
	}
	return &c, nil
}
