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
	PairTTL       = 10 * time.Minute // жизнь слота приглашения
	FriendTTL     = 45 * time.Second // сколько живёт блок статуса без обновления
	MaxBlob       = 4096             // блок приглашения (код с адресами)
	MaxFriendBlob = 1024             // блок статуса: реально ~300 байт
	maxPairs      = 10000
	maxFriends    = 10000
	perIPPairs    = 20  // приглашений с одного IP одновременно
	perIPFriends  = 100 // блоков статуса с одного IP одновременно
	ratePerSec    = 40.0
	rateBurst     = 120.0
	sweepEvery    = 10 * time.Second
	bucketIdle    = 5 * time.Minute
	maxBuckets    = 100000
	healthString  = "ok\n"
)

var (
	reSlot = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reFID  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type entry struct {
	blob    []byte
	created time.Time
	expires time.Time
	owner   string // ключ клиента (IP или /64), для квот
	friend  bool
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
	counts  map[string]*[2]int // по владельцу: [приглашения, статусы]
	total   [2]int
}

func NewServer() *Server {
	return &Server{now: time.Now, m: map[string]*entry{}, buckets: map[string]*bucket{}, counts: map[string]*[2]int{}}
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
			s.remove(k)
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

// clientKey: IPv4 — как есть, IPv6 — по префиксу /64 (иначе одна сеть
// плодила бы бесконечно много «разных» клиентов).
func clientKey(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}

// limit — ограничение частоты запросов с одного клиента (token bucket).
func (s *Server) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := clientKey(r.RemoteAddr)
		now := s.now()
		s.mu.Lock()
		b := s.buckets[key]
		if b == nil {
			if len(s.buckets) >= maxBuckets {
				// Не отказываем новым клиентам: вытесняем одну произвольную
				// корзину (порядок обхода map случаен).
				for k := range s.buckets {
					delete(s.buckets, k)
					break
				}
			}
			b = &bucket{tokens: rateBurst, last: now}
			s.buckets[key] = b
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

// remove удаляет запись и снимает её с квот (вызывать под s.mu).
func (s *Server) remove(key string) {
	e, ok := s.m[key]
	if !ok {
		return
	}
	delete(s.m, key)
	kind := 0
	if e.friend {
		kind = 1
	}
	s.total[kind]--
	if c := s.counts[e.owner]; c != nil {
		c[kind]--
		if c[0] <= 0 && c[1] <= 0 {
			delete(s.counts, e.owner)
		}
	}
}

// add сохраняет запись с учётом квот (под s.mu). false — квота исчерпана.
func (s *Server) add(key string, e *entry) bool {
	kind, perIP, global := 0, perIPPairs, maxPairs
	if e.friend {
		kind, perIP, global = 1, perIPFriends, maxFriends
	}
	if _, exists := s.m[key]; exists {
		s.remove(key) // перезапись (heartbeat): сначала снимаем старую
	}
	c := s.counts[e.owner]
	if s.total[kind] >= global || (c != nil && c[kind] >= perIP) {
		return false
	}
	if c == nil {
		c = &[2]int{}
		s.counts[e.owner] = c
	}
	c[kind]++
	s.total[kind]++
	s.m[key] = e
	return true
}

func validSide(v string) bool { return v == "a" || v == "b" }

func readBlob(w http.ResponseWriter, r *http.Request, max int64) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
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
			s.remove(key)
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
	body, ok := readBlob(w, r, MaxBlob)
	if !ok {
		return
	}
	now := s.now()
	owner := clientKey(r.RemoteAddr)

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
	if !s.add("p/"+slot+"/"+side, &entry{blob: body, created: now, expires: exp, owner: owner}) {
		http.Error(w, "quota", http.StatusServiceUnavailable)
		return
	}
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
	s.remove("p/" + slot + "/a")
	s.remove("p/" + slot + "/b")
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
	body, ok := readBlob(w, r, MaxFriendBlob)
	if !ok {
		return
	}
	now := s.now()
	key := "f/" + fid + "/" + side

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.add(key, &entry{blob: body, created: now, expires: now.Add(FriendTTL), owner: clientKey(r.RemoteAddr), friend: true}) {
		http.Error(w, "quota", http.StatusServiceUnavailable)
		return
	}
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
