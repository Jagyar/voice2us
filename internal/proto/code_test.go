package proto

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestCodeRoundTrip(t *testing.T) {
	in := &Code{Name: "Аня", Role: RoleA, Key: bytes.Repeat([]byte{7}, 32),
		Endpoints: Endpoints{Public: []string{"1.2.3.4:5000"}, LAN: []string{"192.168.1.2:5000"}}}
	s, err := MarshalCode(in)
	if err != nil {
		t.Fatal(err)
	}
	// мессенджеры переносят строки и добавляют пробелы
	out, err := UnmarshalCode("  " + s[:20] + "\n " + s[20:] + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "Аня" || !bytes.Equal(out.Key, in.Key) || out.Public[0] != "1.2.3.4:5000" {
		t.Fatalf("round trip mismatch: %+v", out)
	}
	if !SameCode(in, out) {
		t.Fatal("SameCode должен узнавать тот же код")
	}
}

func TestUnmarshalRejects(t *testing.T) {
	for _, s := range []string{"", "hello", "V2U1.!!!", "V2U1." + base64.RawURLEncoding.EncodeToString([]byte("nope"))} {
		if _, err := UnmarshalCode(s); err == nil {
			t.Errorf("ожидалась ошибка для %q", s)
		}
	}
	// роль A без ключа
	s, _ := MarshalCode(&Code{Name: "x", Role: RoleA, Key: []byte{1}})
	if _, err := UnmarshalCode(s); err == nil {
		t.Error("код без 32-байтного ключа должен отвергаться")
	}
}

func TestUnmarshalGzipBomb(t *testing.T) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(bytes.Repeat([]byte("a"), 10<<20))
	w.Close()
	if _, err := UnmarshalCode(codeMagic + base64.RawURLEncoding.EncodeToString(buf.Bytes())); err == nil {
		t.Fatal("gzip-бомба должна отвергаться")
	}
}

func TestEndpointsSanitized(t *testing.T) {
	var eps []string
	for i := 1; i <= 50; i++ {
		eps = append(eps, fmt.Sprintf("10.0.0.%d:4000", i))
	}
	eps = append(eps, "evil.example.com:80", "0.0.0.0:1", "1.1.1.1:0", "[::1]:5", "10.0.0.1:4000", "224.0.0.1:9")
	s, _ := MarshalCode(&Code{Name: "b", Role: RoleB, Endpoints: Endpoints{Public: eps}})
	c, err := UnmarshalCode(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Public) != maxEndpoints {
		t.Fatalf("ожидали %d адресов, получили %d", maxEndpoints, len(c.Public))
	}
	for _, a := range c.Public {
		if strings.Contains(a, "evil") {
			t.Fatal("имя хоста не должно проходить")
		}
	}
}
