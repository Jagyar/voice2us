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
	"strings"
)

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
	rawJSON, err := io.ReadAll(zr)
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
	return &c, nil
}
