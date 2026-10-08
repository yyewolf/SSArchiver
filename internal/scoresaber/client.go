package scoresaber

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/yyewolf/ssarchiver/internal/buildinfo"
)

const (
	DefaultBaseURL = "https://scoresaber.com"
	ScoresPageSize = 100
)

var (
	ErrNotFound    = errors.New("scoresaber: not found")
	ErrRateLimited = errors.New("scoresaber: rate limited")
)

// StatusError is returned for unexpected non-2xx responses.
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("scoresaber: unexpected status %d: %s", e.StatusCode, e.Body)
}

type Client struct {
	baseURL   string
	hc        *http.Client
	limiter   *Limiter
	userAgent string
}

type Option func(*Client)

func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = u }
}

func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.hc = hc }
}

func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// NewClient returns a client whose every request goes through l.
func NewClient(l *Limiter, opts ...Option) *Client {
	c := &Client{
		baseURL:   DefaultBaseURL,
		hc:        &http.Client{Timeout: 2 * time.Minute},
		limiter:   l,
		userAgent: buildinfo.UserAgent(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) Limiter() *Limiter { return c.limiter }

// Player fetches a player's basic profile.
func (c *Client) Player(ctx context.Context, id string) (Player, error) {
	var p Player
	err := c.getJSON(ctx, "/api/v2/players/"+url.PathEscape(id)+"/basic", nil, &p)
	return p, err
}

// Scores fetches one page (most recent first, all plays incl. non-PB) of a player's scores.
func (c *Client) Scores(ctx context.Context, playerID string, page int) (ScorePage, error) {
	var sp ScorePage
	q := url.Values{
		"sort":         {"recent"},
		"limit":        {strconv.Itoa(ScoresPageSize)},
		"personalBest": {"all"},
		"page":         {strconv.Itoa(page)},
	}
	err := c.getJSON(ctx, "/api/v2/players/"+url.PathEscape(playerID)+"/scores", q, &sp)
	return sp, err
}

// Replay streams a replay file. The caller must close the body.
func (c *Client) Replay(ctx context.Context, scoreID int64) (io.ReadCloser, error) {
	resp, err := c.do(ctx, "/api/v2/scores/"+strconv.FormatInt(scoreID, 10)+"/replay", nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) do(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("scoresaber: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scoresaber: GET %s: %w", path, err)
	}
	c.limiter.Observe(resp.Header, resp.StatusCode)
	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusNotFound:
		drain(resp)
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	case http.StatusTooManyRequests:
		drain(resp)
		return nil, fmt.Errorf("%w: %s", ErrRateLimited, path)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, &StatusError{StatusCode: resp.StatusCode, Body: string(body)}
	}
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := c.do(ctx, path, q)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("scoresaber: decode %s: %w", path, err)
	}
	return nil
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}
