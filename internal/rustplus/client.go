package rustplus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"rustwatch/internal/pb"
	"rustwatch/internal/ws"
)

// Client is a connection to one Rust server's Rust+ (companion) websocket.
type Client struct {
	Host        string
	Port        int
	PlayerID    uint64
	PlayerToken int32
	UseProxy    bool // connect through wss://companion-rust.facepunch.com

	OnBroadcast    func(*Broadcast)
	OnDisconnected func(error)

	mu      sync.Mutex
	conn    *ws.Conn
	seq     uint32
	pending map[uint32]chan *Response
	closed  bool

	// token bucket (Rust+ rate limits requests per player)
	tokens   float64
	lastFill time.Time
	bucketMu sync.Mutex
}

var ErrNotConnected = errors.New("not connected to server")

func (c *Client) URL() string {
	if c.UseProxy {
		return fmt.Sprintf("wss://companion-rust.facepunch.com/game/%s/%d", c.Host, c.Port)
	}
	return fmt.Sprintf("ws://%s:%d", c.Host, c.Port)
}

func (c *Client) Connect() error {
	conn, err := ws.Dial(c.URL(), 15*time.Second)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.pending = map[uint32]chan *Response{}
	c.closed = false
	c.tokens = 20
	c.lastFill = time.Now()
	c.mu.Unlock()
	go c.readLoop(conn)
	return nil
}

func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

func (c *Client) readLoop(conn *ws.Conn) {
	var err error
	for {
		var data []byte
		_, data, err = conn.ReadMessage()
		if err != nil {
			break
		}
		resp, bc := DecodeAppMessage(data)
		if resp != nil {
			c.mu.Lock()
			ch := c.pending[resp.Seq]
			delete(c.pending, resp.Seq)
			c.mu.Unlock()
			if ch != nil {
				ch <- resp
			}
		}
		if bc != nil && c.OnBroadcast != nil {
			c.OnBroadcast(bc)
		}
	}
	c.mu.Lock()
	wasClosed := c.closed
	if c.conn == conn {
		c.conn = nil
	}
	for s, ch := range c.pending {
		close(ch)
		delete(c.pending, s)
	}
	c.mu.Unlock()
	conn.Close()
	if !wasClosed && c.OnDisconnected != nil {
		c.OnDisconnected(err)
	}
}

func (c *Client) takeToken(ctx context.Context, cost float64) error {
	for {
		c.bucketMu.Lock()
		now := time.Now()
		c.tokens += now.Sub(c.lastFill).Seconds() * 2.5
		if c.tokens > 20 {
			c.tokens = 20
		}
		c.lastFill = now
		if c.tokens >= cost {
			c.tokens -= cost
			c.bucketMu.Unlock()
			return nil
		}
		wait := time.Duration((cost - c.tokens) / 2.5 * float64(time.Second))
		c.bucketMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// request sends an AppRequest where body writes the request-specific field.
func (c *Client) request(ctx context.Context, cost float64, entityID uint32, body func(w *pb.Writer)) (*Response, error) {
	if err := c.takeToken(ctx, cost); err != nil {
		return nil, err
	}
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return nil, ErrNotConnected
	}
	c.seq++
	seq := c.seq
	ch := make(chan *Response, 1)
	c.pending[seq] = ch
	c.mu.Unlock()

	w := &pb.Writer{}
	w.Uint(1, uint64(seq))
	w.Uint(2, c.PlayerID)
	w.Int(3, int64(c.PlayerToken))
	if entityID != 0 {
		w.Uint(4, uint64(entityID))
	}
	body(w)
	if err := conn.WriteMessage(ws.OpBinary, w.Bytes()); err != nil {
		c.mu.Lock()
		delete(c.pending, seq)
		c.mu.Unlock()
		return nil, err
	}
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	select {
	case r, ok := <-ch:
		if !ok {
			return nil, ErrNotConnected
		}
		if r.Error != "" {
			return r, fmt.Errorf("server error: %s", r.Error)
		}
		return r, nil
	case <-timeout.C:
		c.mu.Lock()
		delete(c.pending, seq)
		c.mu.Unlock()
		return nil, errors.New("request timed out")
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, seq)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func empty(field int) func(w *pb.Writer) {
	return func(w *pb.Writer) { w.Message(field, nil) }
}

func (c *Client) GetInfo(ctx context.Context) (*Info, error) {
	r, err := c.request(ctx, 1, 0, empty(8))
	if err != nil {
		return nil, err
	}
	if r.Info == nil {
		return nil, errors.New("no info in response")
	}
	return r.Info, nil
}

func (c *Client) GetTime(ctx context.Context) (*Time, error) {
	r, err := c.request(ctx, 1, 0, empty(9))
	if err != nil {
		return nil, err
	}
	if r.Time == nil {
		return nil, errors.New("no time in response")
	}
	return r.Time, nil
}

func (c *Client) GetMap(ctx context.Context) (*MapData, error) {
	r, err := c.request(ctx, 5, 0, empty(10))
	if err != nil {
		return nil, err
	}
	if r.Map == nil {
		return nil, errors.New("no map in response")
	}
	return r.Map, nil
}

func (c *Client) GetTeamInfo(ctx context.Context) (*TeamInfo, error) {
	r, err := c.request(ctx, 1, 0, empty(11))
	if err != nil {
		return nil, err
	}
	if r.TeamInfo == nil {
		return nil, errors.New("no team info in response")
	}
	return r.TeamInfo, nil
}

func (c *Client) GetTeamChat(ctx context.Context) ([]TeamMessage, error) {
	r, err := c.request(ctx, 1, 0, empty(12))
	if err != nil {
		return nil, err
	}
	return r.TeamChat, nil
}

func (c *Client) SendTeamMessage(ctx context.Context, msg string) error {
	_, err := c.request(ctx, 2, 0, func(w *pb.Writer) {
		m := &pb.Writer{}
		m.String(1, msg)
		w.Message(13, m)
	})
	return err
}

func (c *Client) GetEntityInfo(ctx context.Context, entityID uint32) (*EntityInfo, error) {
	r, err := c.request(ctx, 1, entityID, empty(14))
	if err != nil {
		return nil, err
	}
	if r.EntityInfo == nil {
		return nil, errors.New("no entity info in response")
	}
	return r.EntityInfo, nil
}

func (c *Client) SetEntityValue(ctx context.Context, entityID uint32, value bool) error {
	_, err := c.request(ctx, 1, entityID, func(w *pb.Writer) {
		m := &pb.Writer{}
		m.Bool(1, value)
		w.Message(15, m)
	})
	return err
}

func (c *Client) GetMapMarkers(ctx context.Context) ([]Marker, error) {
	r, err := c.request(ctx, 1, 0, empty(18))
	if err != nil {
		return nil, err
	}
	return r.Markers, nil
}

func (c *Client) PromoteToLeader(ctx context.Context, steamID uint64) error {
	_, err := c.request(ctx, 1, 0, func(w *pb.Writer) {
		m := &pb.Writer{}
		m.Uint(1, steamID)
		w.Message(20, m)
	})
	return err
}
