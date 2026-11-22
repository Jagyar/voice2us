package main

// Статус друзей и вызовы через «встречалку». Каждые несколько секунд
// приложение сообщает серверу «я онлайн» (зашифрованный ключом дружбы блок)
// и узнаёт то же о друзьях. Адреса попадают в блок только на время вызова —
// их определяют непосредственно перед звонком (STUN), поэтому смена IP
// не мешает.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/Jagyar/voice2us/internal/proto"
	"github.com/Jagyar/voice2us/internal/rdv"
)

const (
	stIdle      = "idle"
	stCalling   = "calling"
	stAnswering = "answering"
	stDeclined  = "declined"
	stInCall    = "incall"

	putSlow   = 15 * time.Second // heartbeat в покое
	putFast   = 4 * time.Second  // во время вызова
	getSlow   = 3 * time.Second
	getFast   = 1 * time.Second // пока ждём ответа на свой вызов
	downRetry = 10 * time.Second
	ringFresh = 12 * time.Second // входящий вызов старше — считаем мёртвым
)

type peerStatus struct {
	Online bool
	State  string
	Call   string
	Name   string
	EP     proto.Endpoints
	Age    time.Duration
	Seen   time.Time // когда получен ответ сервера (возраст = Age + прошедшее с Seen)
	Err    bool      // сервер недоступен: статус неизвестен
}

type ownState struct {
	State string
	Call  string
	EP    *proto.Endpoints
}

type presenceBlob struct {
	V     int              `json:"v"`
	T     int64            `json:"t"`
	State string           `json:"s"`
	Call  string           `json:"c,omitempty"`
	Name  string           `json:"n,omitempty"`
	EP    *proto.Endpoints `json:"e,omitempty"`
}

type presence struct {
	mu       sync.Mutex
	client   *rdv.Client
	friends  []*friend
	name     string
	own      map[string]ownState
	busy     bool
	lastPut  map[string]time.Time
	lastGet  map[string]time.Time
	last     map[string]peerStatus
	down     time.Time // до этого момента сервер не трогаем
	serverOK *bool

	onUpdate func(id string, st peerStatus) // из горутины presence
	onServer func(ok bool)

	wake chan struct{}
	stop chan struct{}
}

func newPresence() *presence {
	return &presence{
		own:     map[string]ownState{},
		lastPut: map[string]time.Time{},
		lastGet: map[string]time.Time{},
		last:    map[string]peerStatus{},
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
	}
}

func (p *presence) poke() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *presence) SetClient(c *rdv.Client) {
	p.mu.Lock()
	p.client = c
	p.down = time.Time{}
	p.lastPut = map[string]time.Time{}
	p.lastGet = map[string]time.Time{}
	p.mu.Unlock()
	p.poke()
}

func (p *presence) SetName(n string) {
	p.mu.Lock()
	p.name = n
	p.mu.Unlock()
}

func (p *presence) SetFriends(list []*friend) {
	p.mu.Lock()
	p.friends = append([]*friend(nil), list...)
	keep := map[string]bool{}
	for _, f := range list {
		keep[f.ID] = true
	}
	for id := range p.last {
		if !keep[id] {
			delete(p.last, id)
		}
	}
	p.mu.Unlock()
	p.poke()
}

// SetOwn задаёт наше состояние относительно конкретного друга и сразу
// публикует его.
func (p *presence) SetOwn(id, state, call string, ep *proto.Endpoints) {
	p.mu.Lock()
	if state == stIdle {
		delete(p.own, id)
	} else {
		p.own[id] = ownState{State: state, Call: call, EP: ep}
	}
	delete(p.lastPut, id)
	p.mu.Unlock()
	p.poke()
}

// SetBusy — идёт разговор: всем друзьям показываем «в разговоре».
func (p *presence) SetBusy(b bool) {
	p.mu.Lock()
	p.busy = b
	p.lastPut = map[string]time.Time{}
	p.mu.Unlock()
	p.poke()
}

// Status возвращает последнее известное состояние друга.
func (p *presence) Status(id string) (peerStatus, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.last[id]
	return st, ok
}

func (p *presence) Stop() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
}

func (p *presence) Run() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
		case <-p.wake:
		}
		p.tick()
	}
}

func (p *presence) tick() {
	p.mu.Lock()
	client := p.client
	friends := append([]*friend(nil), p.friends...)
	down := time.Now().Before(p.down)
	p.mu.Unlock()
	if client == nil || client.Base == "" || down {
		return
	}
	for _, f := range friends {
		select {
		case <-p.stop:
			return
		default:
		}
		now := time.Now()
		p.mu.Lock()
		own := p.own[f.ID]
		putEvery, getEvery := putSlow, getSlow
		if own.State == stCalling || own.State == stAnswering || own.State == stDeclined {
			putEvery = putFast
		}
		if own.State == stCalling {
			getEvery = getFast
		}
		putDue := now.Sub(p.lastPut[f.ID]) >= putEvery
		getDue := now.Sub(p.lastGet[f.ID]) >= getEvery
		if putDue {
			p.lastPut[f.ID] = now
		}
		if getDue {
			p.lastGet[f.ID] = now
		}
		p.mu.Unlock()

		if putDue {
			p.put(client, f)
		}
		if getDue {
			p.get(client, f)
		}
	}
}

func (p *presence) put(c *rdv.Client, f *friend) {
	p.mu.Lock()
	own := p.own[f.ID]
	busy := p.busy
	name := p.name
	p.mu.Unlock()

	b := presenceBlob{V: 1, T: time.Now().UnixMilli(), State: stIdle, Name: name}
	if own.State != "" {
		b.State, b.Call, b.EP = own.State, own.Call, own.EP
	}
	if busy {
		b.State, b.Call, b.EP = stInCall, "", nil
	}
	plain, _ := json.Marshal(b)
	blob, err := rdv.SealFriend(f.Key, f.side(), plain)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.serverResult(c.PutFriend(ctx, f.ID, f.side(), blob) == nil)
}

func (p *presence) get(c *rdv.Client, f *friend) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blob, age, err := c.GetFriend(ctx, f.ID, f.peerSide())

	var st peerStatus
	switch {
	case errors.Is(err, rdv.ErrNotFound):
		st = peerStatus{State: stIdle}
		p.serverResult(true)
	case err != nil:
		st = peerStatus{Err: true}
		p.serverResult(false)
	default:
		p.serverResult(true)
		plain, oerr := rdv.OpenFriend(f.Key, f.peerSide(), blob)
		var b presenceBlob
		if oerr != nil || json.Unmarshal(plain, &b) != nil {
			st = peerStatus{State: stIdle}
			break
		}
		st = peerStatus{Online: true, State: b.State, Call: b.Call, Name: b.Name, Age: age}
		if b.EP != nil {
			st.EP = b.EP.Sanitized()
		}
	}

	st.Seen = time.Now()
	p.mu.Lock()
	prev, had := p.last[f.ID]
	// Возраст меняется постоянно — не повод обновлять интерфейс.
	pc, sc := prev, st
	pc.Age, sc.Age = 0, 0
	pc.Seen, sc.Seen = time.Time{}, time.Time{}
	changed := !had || !reflect.DeepEqual(pc, sc)
	p.last[f.ID] = st
	cb := p.onUpdate
	p.mu.Unlock()
	if changed && cb != nil {
		cb(f.ID, st)
	}
}

func (p *presence) serverResult(ok bool) {
	p.mu.Lock()
	if !ok {
		p.down = time.Now().Add(downRetry)
	}
	changed := p.serverOK == nil || *p.serverOK != ok
	p.serverOK = &ok
	cb := p.onServer
	p.mu.Unlock()
	if changed && cb != nil {
		cb(ok)
	}
}
