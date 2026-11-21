package rdv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("не найдено")
	ErrExists   = errors.New("уже существует")
)

// Client — клиент «встречалки». Все вызовы блокирующие и с таймаутом:
// из интерфейса их надо звать в отдельной горутине.
type Client struct {
	Base string
	HTTP *http.Client
}

func NewClient(base string) *Client {
	return &Client{
		Base: strings.TrimRight(strings.TrimSpace(base), "/"),
		HTTP: &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, []byte, error) {
	if c.Base == "" {
		return nil, nil, errors.New("адрес сервера не задан")
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBlob+1))
	if err != nil {
		return nil, nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return resp, data, nil
	case http.StatusNotFound:
		return resp, nil, ErrNotFound
	case http.StatusConflict:
		return resp, nil, ErrExists
	}
	return resp, nil, fmt.Errorf("сервер ответил %d", resp.StatusCode)
}

func (c *Client) Health(ctx context.Context) error {
	_, _, err := c.do(ctx, "GET", "/healthz", nil)
	return err
}

func (c *Client) PutPair(ctx context.Context, slot, side string, blob []byte) error {
	_, _, err := c.do(ctx, "PUT", "/v1/pair/"+slot+"/"+side, blob)
	return err
}

func (c *Client) GetPair(ctx context.Context, slot, side string) ([]byte, error) {
	_, data, err := c.do(ctx, "GET", "/v1/pair/"+slot+"/"+side, nil)
	return data, err
}

func (c *Client) DeletePair(ctx context.Context, slot string) {
	_, _, _ = c.do(ctx, "DELETE", "/v1/pair/"+slot, nil)
}

func (c *Client) PutFriend(ctx context.Context, fid, side string, blob []byte) error {
	_, _, err := c.do(ctx, "PUT", "/v1/friend/"+fid+"/"+side, blob)
	return err
}

// GetFriend возвращает блок и его возраст (сколько прошло с последнего PUT).
func (c *Client) GetFriend(ctx context.Context, fid, side string) ([]byte, time.Duration, error) {
	resp, data, err := c.do(ctx, "GET", "/v1/friend/"+fid+"/"+side, nil)
	if err != nil {
		return nil, 0, err
	}
	ms, _ := strconv.ParseInt(resp.Header.Get("X-Age-Ms"), 10, 64)
	return data, time.Duration(ms) * time.Millisecond, nil
}
