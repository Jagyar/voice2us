// Package update проверяет, не вышла ли на GitHub версия новее установленной.
// Только уведомление: ничего не скачивается и не устанавливается.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// LatestAPI — последний опубликованный (не черновик и не предварительный) релиз.
const LatestAPI = "https://api.github.com/repos/Jagyar/voice2us/releases/latest"

type Release struct {
	Tag string // например "v2.1.0"
	URL string // страница релиза
}

// Latest запрашивает последний релиз. userAgent — общий (GitHub требует
// непустой), без каких-либо данных о пользователе.
func Latest(ctx context.Context, client *http.Client, apiURL, userAgent string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub ответил %d", resp.StatusCode)
	}
	var body struct {
		Tag        string `json:"tag_name"`
		URL        string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	if body.Draft || body.Prerelease || body.Tag == "" {
		return nil, errors.New("нет опубликованного релиза")
	}
	return &Release{Tag: body.Tag, URL: body.URL}, nil
}

type version struct {
	nums [3]int
	pre  string
}

// Parse сообщает, можно ли разобрать строку как версию (для "dev" — нет).
func Parse(v string) (ok bool) {
	_, ok = parse(v)
	return ok
}

func parse(v string) (version, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var out version
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, out.pre = v[:i], v[i+1:]
	}
	parts := strings.Split(v, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out.nums[i] = n
	}
	return out, true
}

// Newer сообщает, что candidate новее current. Сборки, чью версию разобрать
// нельзя (например "dev"), обновлений не получают. Предварительная версия
// считается старше такого же полного релиза (2.1.0-rc.1 < 2.1.0).
func Newer(current, candidate string) bool {
	c, ok1 := parse(current)
	n, ok2 := parse(candidate)
	if !ok1 || !ok2 {
		return false
	}
	for i := range c.nums {
		if n.nums[i] != c.nums[i] {
			return n.nums[i] > c.nums[i]
		}
	}
	switch {
	case c.pre != "" && n.pre == "":
		return true
	case c.pre != "" && n.pre != "":
		return n.pre > c.pre
	}
	return false
}
