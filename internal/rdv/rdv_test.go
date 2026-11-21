package rdv

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T) (*Client, *Server, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	srv := NewServer()
	srv.now = clk.Now
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return NewClient(ts.URL), srv, clk
}

var ctx = context.Background()

func TestPhrase(t *testing.T) {
	p, err := NewPhrase()
	if err != nil || len(p) != 11 || p[5] != '-' {
		t.Fatalf("%q %v", p, err)
	}
	norm, ok := NormalizePhrase(strings.ToLower(p))
	if !ok || len(norm) != 10 {
		t.Fatal("нормализация")
	}
	a, _ := NormalizePhrase("o1il-0000 00")
	if a != "0111000000" {
		t.Fatalf("подмена O/I/L: %q", a)
	}
	for _, bad := range []string{"", "ABC", "UUUUU-UUUUU", "ПРИВЕТ-ПРИВЕТ", "0000000000x"} {
		if _, ok := NormalizePhrase(bad); ok {
			t.Errorf("%q не должна проходить", bad)
		}
	}
}

func TestPairCrypto(t *testing.T) {
	k1, err := DerivePair("K7QM2-XPD9T")
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := DerivePair("k7qm2xpd9t")
	if k1.Slot != k2.Slot || !bytes.Equal(k1.Key, k2.Key) {
		t.Fatal("одна фраза — одни ключи")
	}
	other, _ := DerivePair("K7QM2-XPD9V")
	if other.Slot == k1.Slot || bytes.Equal(other.Key, k1.Key) {
		t.Fatal("разные фразы — разные ключи")
	}
	if len(k1.Slot) != 32 || len(k1.Key) != 32 {
		t.Fatal("размеры")
	}
	blob, _ := SealPair(k1, "a", []byte("secret"))
	if got, err := OpenPair(k1, "a", blob); err != nil || string(got) != "secret" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := OpenPair(k1, "b", blob); err == nil {
		t.Fatal("блок стороны a не должен открываться как b")
	}
	if _, err := OpenPair(other, "a", blob); err == nil {
		t.Fatal("чужой ключ")
	}
}

func TestFriendCrypto(t *testing.T) {
	shared := bytes.Repeat([]byte{5}, 32)
	fid := FriendID(shared)
	if len(fid) != 64 || fid != FriendID(shared) || fid == FriendID(bytes.Repeat([]byte{6}, 32)) {
		t.Fatal("friend id")
	}
	blob, _ := SealFriend(shared, "a", []byte("hi"))
	if got, err := OpenFriend(shared, "a", blob); err != nil || string(got) != "hi" {
		t.Fatal(err)
	}
	if _, err := OpenFriend(shared, "b", blob); err == nil {
		t.Fatal("сторона важна")
	}
}

func TestPairFlow(t *testing.T) {
	c, _, clk := setup(t)
	slot := strings.Repeat("ab", 16)

	if _, err := c.GetPair(ctx, slot, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("пустой слот: %v", err)
	}
	if err := c.PutPair(ctx, slot, "b", []byte("b1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("b без a: %v", err)
	}
	if err := c.PutPair(ctx, slot, "a", []byte("code-a")); err != nil {
		t.Fatal(err)
	}
	if err := c.PutPair(ctx, slot, "a", []byte("evil")); !errors.Is(err, ErrExists) {
		t.Fatalf("перезапись a: %v", err)
	}
	if got, err := c.GetPair(ctx, slot, "a"); err != nil || string(got) != "code-a" {
		t.Fatalf("%q %v", got, err)
	}
	clk.Advance(9 * time.Minute)
	if err := c.PutPair(ctx, slot, "b", []byte("code-b")); err != nil {
		t.Fatal(err)
	}
	if err := c.PutPair(ctx, slot, "b", []byte("evil")); !errors.Is(err, ErrExists) {
		t.Fatalf("перезапись b: %v", err)
	}
	clk.Advance(2 * time.Minute) // 11 минут: приглашение просрочено вместе с ответом
	if _, err := c.GetPair(ctx, slot, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a должен протухнуть: %v", err)
	}
	if _, err := c.GetPair(ctx, slot, "b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("b должен протухнуть вместе с a: %v", err)
	}
	// после протухания слот можно занять снова
	if err := c.PutPair(ctx, slot, "a", []byte("again")); err != nil {
		t.Fatal(err)
	}
	c.DeletePair(ctx, slot)
	if _, err := c.GetPair(ctx, slot, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete")
	}
}

func TestFriendPresenceTTL(t *testing.T) {
	c, srv, clk := setup(t)
	fid := strings.Repeat("cd", 32)
	if err := c.PutFriend(ctx, fid, "a", []byte("x1")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(20 * time.Second)
	blob, age, err := c.GetFriend(ctx, fid, "a")
	if err != nil || string(blob) != "x1" || age < 19*time.Second || age > 21*time.Second {
		t.Fatalf("%q %v %v", blob, age, err)
	}
	if err := c.PutFriend(ctx, fid, "a", []byte("x2")); err != nil { // heartbeat перезаписывает
		t.Fatal(err)
	}
	clk.Advance(40 * time.Second)
	if _, age, err := c.GetFriend(ctx, fid, "a"); err != nil || age < 39*time.Second {
		t.Fatalf("после heartbeat запись жива: %v %v", age, err)
	}
	clk.Advance(10 * time.Second) // 50 с без обновления — офлайн
	if _, _, err := c.GetFriend(ctx, fid, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("должен стать офлайн: %v", err)
	}
	srv.Sweep()
	srv.mu.Lock()
	n := len(srv.m)
	srv.mu.Unlock()
	if n != 0 {
		t.Fatalf("sweep не очистил: %d", n)
	}
}

func TestValidationAndLimits(t *testing.T) {
	c, _, _ := setup(t)
	for _, tc := range []struct{ method, path string }{
		{"PUT", "/v1/pair/short/a"},
		{"PUT", "/v1/pair/" + strings.Repeat("ab", 16) + "/c"},
		{"GET", "/v1/friend/" + strings.Repeat("ab", 16) + "/a"}, // fid должен быть 64 hex
		{"PUT", "/v1/friend/ZZ" + strings.Repeat("a", 62) + "/a"},
	} {
		if _, _, err := c.do(ctx, tc.method, tc.path, []byte("x")); err == nil {
			t.Errorf("%s %s должен отвергаться", tc.method, tc.path)
		}
	}
	slot := strings.Repeat("ef", 16)
	if err := c.PutPair(ctx, slot, "a", make([]byte, MaxBlob+1)); err == nil {
		t.Error("слишком большой блок")
	}
	if err := c.PutPair(ctx, slot, "a", nil); err == nil {
		t.Error("пустой блок")
	}
	if err := c.PutPair(ctx, slot, "a", make([]byte, MaxBlob)); err != nil {
		t.Errorf("блок максимального размера: %v", err)
	}
	if err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimit(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	srv := NewServer()
	srv.now = clk.Now
	h := srv.Handler()
	var limited int
	for i := 0; i < 200; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/healthz", nil)
		req.RemoteAddr = "203.0.113.9:5555"
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited < 70 || limited > 90 { // 200 запросов при «ведре» на 120
		t.Fatalf("ограничено %d из 200", limited)
	}
	// другой IP не страдает
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "203.0.113.10:1"
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("лимит должен быть по IP")
	}
	clk.Advance(3 * time.Second) // ведро пополнилось
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("после паузы лимит должен сниматься")
	}
}

func TestClientNoBase(t *testing.T) {
	if err := NewClient("").Health(ctx); err == nil {
		t.Fatal("пустой адрес — ошибка")
	}
}
