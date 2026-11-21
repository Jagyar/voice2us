package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		cur, cand string
		want      bool
	}{
		{"2.0.1", "v2.1.0", true},
		{"2.0.1", "v2.0.1", false},
		{"2.1.0", "v2.0.9", false},
		{"2.0.9", "v2.0.10", true}, // числовое, а не строковое сравнение
		{"1.9.9", "v2.0.0", true},
		{"2.1.0-rc.1", "v2.1.0", true},
		{"2.1.0", "v2.1.0-rc.1", false},
		{"2.1.0-rc.1", "v2.1.0-rc.2", true},
		{"dev", "v9.0.0", false}, // сборка для разработки не обновляется
		{"2.0.1", "garbage", false},
		{"2.0.1", "", false},
		{"2.0", "v2.0.1", true},
	}
	for _, c := range cases {
		if got := Newer(c.cur, c.cand); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, ожидали %v", c.cur, c.cand, got, c.want)
		}
	}
}

func TestLatest(t *testing.T) {
	var ua string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"tag_name":"v2.1.0","html_url":"https://github.com/x/y/releases/tag/v2.1.0","draft":false,"prerelease":false}`))
		case "/pre":
			_, _ = w.Write([]byte(`{"tag_name":"v2.2.0-rc.1","html_url":"u","prerelease":true}`))
		case "/bad":
			_, _ = w.Write([]byte(`not json`))
		default:
			http.Error(w, "nope", http.StatusForbidden)
		}
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rel, err := Latest(ctx, ts.Client(), ts.URL+"/ok", "Voice2Us/2.0.1")
	if err != nil || rel.Tag != "v2.1.0" || rel.URL == "" || ua != "Voice2Us/2.0.1" {
		t.Fatalf("%+v %v ua=%q", rel, err, ua)
	}
	for _, p := range []string{"/pre", "/bad", "/limit"} {
		if _, err := Latest(ctx, ts.Client(), ts.URL+p, "x"); err == nil {
			t.Errorf("%s должен давать ошибку", p)
		}
	}
}
