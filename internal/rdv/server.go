package rdv

import (
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	PairTTL      = 10 * time.Minute // жизнь слота приглашения
	FriendTTL    = 45 * time.Second // сколько живёт блок статуса без обновления
	MaxBlob      = 4096
	maxEntries   = 50000
	ratePerSec   = 40.0
	rateBurst    = 120.0
	sweepEvery   = 10 * time.Second
	bucketIdle   = 5 * time.Minute
	maxBuckets   = 100000
	healthString = "ok\n"
)

var (
	reSlot = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reFID  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type entry struct {
	blob    []byte
	created time.Time
	expires time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// Server хранит блоки в памяти. Содержимое непрозрачно для сервера и никогда
// не пишется в лог (адреса содержат секретные идентификаторы).
type Server struct {
	now func() time.Time

	mu      sync.Mutex
	m       map[string]*entry
	buckets map[string]*bucket
}

func NewServer() *Server {
	return &Server{now: time.Now, m: map[string]*entry{}, buckets: map[string]*bucket{}}
}

// Run запускает фоновую очистку до закрытия done.
func (s *Server) Run(done <-chan struct{}) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			s.Sweep()
		}
	}
}

// Sweep удаляет просроченные блоки и давно молчавшие лимитеры.
func (s *Server) Sweep() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.m {
		if now.After(e.expires) {
			delete(s.m, k)
		}
	}
	for ip, b := range s.buckets {
		if now.Sub(b.last) > bucketIdle {
			delete(s.buckets, ip)
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, healthString)
	})
	mux.HandleFunc("PUT /v1/pair/{slot}/{side}", s.putPair)
	mux.HandleFunc("GET /v1/pair/{slot}/{side}", s.getPair)
	mux.HandleFunc("DELETE /v1/pair/{slot}", s.deletePair)
	mux.HandleFunc("PUT /v1/friend/{fid}/{side}", s.putFriend)
	mux.HandleFunc("GET /v1/friend/{fid}/{side}", s.getFriend)
	return s.limit(mux)
}

// limit — ограничение частоты запросов с одного IP (token bucket).
func (s *Server) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		now := s.now()
		s.mu.Lock()
		b := s.buckets[ip]
		if b == nil {
			if len(s.buckets) >= maxBuckets {
				s.mu.Unlock()
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			b = &bucket{tokens: rateBurst, last: now}
			s.buckets[ip] = b
		}
		b.tokens += now.Sub(b.last).Seconds() * ratePerSec
		if b.tokens > rateBurst {
			b.tokens = rateBurst
		}
		b.last = now
		ok := b.tokens >= 1
		if ok {
			b.tokens--
		}
		s.mu.Unlock()
		if !ok {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validSide(v string) bool { return v == "a" || v == "b" }

func readBlob(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBlob))
	if err != nil || len(body) == 0 {
		http.Error(w, "bad body", http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

func (s *Server) get(key string) (*entry, bool) {
	e, ok := s.m[key]
	if !ok || s.now().After(e.expires) {
		if ok {
			delete(s.m, key)
		}
		return nil, false
	}
	return e, true
}

func (s *Server) reply(w http.ResponseWriter, e *entry) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Age-Ms", strconv.FormatInt(s.now().Sub(e.created).Milliseconds(), 10))
	_, _ = w.Write(e.blob)
}

// --- слоты приглашения ---

func (s *Server) putPair(w http.ResponseWriter, r *http.Request) {
	slot, side := r.PathValue("slot"), r.PathValue("side")
	if !reSlot.MatchString(slot) || !validSide(side) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	body, ok := readBlob(w, r)
	if !ok {
		return
	}
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.get("p/" + slot + "/" + side); exists {
		http.Error(w, "exists", http.StatusConflict)
		return
	}
	exp := now.Add(PairTTL)
	if side == "b" {
		a, ok := s.get("p/" + slot + "/a")
		if !ok {
			http.Error(w, "no such slot", http.StatusNotFound)
			return
		}
		exp = a.expires // ответ живёт не дольше самого приглашения
	}
	if len(s.m) >= maxEntries {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	s.m["p/"+slot+"/"+side] = &entry{blob: body, created: now, expires: exp}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) getPair(w http.ResponseWriter, r *http.Request) {
	slot, side := r.PathValue("slot"), r.PathValue("side")
	if !reSlot.MatchString(slot) || !validSide(side) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	e, ok := s.get("p/" + slot + "/" + side)
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.reply(w, e)
}

func (s *Server) deletePair(w http.ResponseWriter, r *http.Request) {
	slot := r.PathValue("slot")
	if !reSlot.MatchString(slot) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	delete(s.m, "p/"+slot+"/a")
	delete(s.m, "p/"+slot+"/b")
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// --- статус друзей ---

func (s *Server) putFriend(w http.ResponseWriter, r *http.Request) {
	fid, side := r.PathValue("fid"), r.PathValue("side")
	if !reFID.MatchString(fid) || !validSide(side) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	body, ok := readBlob(w, r)
	if !ok {
		return
	}
	now := s.now()
	key := "f/" + fid + "/" + side

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.m[key]; !exists && len(s.m) >= maxEntries {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	s.m[key] = &entry{blob: body, created: now, expires: now.Add(FriendTTL)}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getFriend(w http.ResponseWriter, r *http.Request) {
	fid, side := r.PathValue("fid"), r.PathValue("side")
	if !reFID.MatchString(fid) || !validSide(side) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	e, ok := s.get("f/" + fid + "/" + side)
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.reply(w, e)
}
