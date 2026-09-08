package vectorresident

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

var ErrUnsupportedOptions = errors.New("semantic search resident does not support query options; restart the resident to use the updated companion")

// Request is one semantic search against the shared resident.
type Request struct {
	Query           string  `json:"query"`
	K               int     `json:"k"`
	Databases       string  `json:"databases,omitempty"`
	ExpandTemplates bool    `json:"expand_templates,omitempty"`
	MinScore        float64 `json:"min_score,omitempty"`
}

type envelope struct {
	Kind      string          `json:"kind"`
	Stage     string          `json:"stage,omitempty"`
	ID        int64           `json:"id,omitempty"`
	Message   string          `json:"message,omitempty"`
	Error     string          `json:"error,omitempty"`
	ElapsedMS int64           `json:"elapsed_ms,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Extra     map[string]any  `json:"extra,omitempty"`
}

// Client is one connection to the shared embedding resident.
type Client struct {
	stdin        io.WriteCloser
	conn         io.Closer
	encoder      *json.Encoder
	status       io.Writer
	closeOnce    sync.Once
	writeMu      sync.Mutex
	stateMu      sync.Mutex
	pendingMu    sync.Mutex
	ready        chan struct{}
	failed       chan struct{}
	readyErr     error
	failure      error
	prewarmMS    int64
	queryOptions map[string]bool
	nextID       int64
	pending      map[int64]chan envelope
	closing      bool
}

// NewClient decodes resident protocol from conn and writes product progress to
// status. The caller owns closing the client, which does not stop the resident.
func NewClient(conn io.ReadWriteCloser, status io.Writer) *Client {
	client := &Client{
		stdin: conn, conn: conn, encoder: json.NewEncoder(conn),
		status: status, ready: make(chan struct{}), failed: make(chan struct{}),
		pending: make(map[int64]chan envelope),
	}
	go client.decode(conn)
	return client
}

func (c *Client) decode(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var response envelope
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			c.fail(fmt.Errorf("semantic search protocol: %w", err))
			return
		}
		switch response.Kind {
		case "progress":
			if c.status != nil && strings.TrimSpace(response.Message) != "" {
				fmt.Fprintln(c.status, response.Message)
			}
			continue
		case "result":
			if response.Stage == "prewarm" {
				if response.Extra != nil {
					if ms, ok := response.Extra["prewarm_ms"].(float64); ok {
						c.stateMu.Lock()
						c.prewarmMS = int64(ms)
						c.stateMu.Unlock()
					}
				}
				c.stateMu.Lock()
				c.queryOptions = make(map[string]bool)
				if options, ok := response.Extra["query_options"].([]any); ok {
					for _, option := range options {
						if name, ok := option.(string); ok {
							c.queryOptions[name] = true
						}
					}
				}
				c.stateMu.Unlock()
				c.markReady(nil)
				continue
			}
			c.route(response)
		case "error":
			if response.Stage == "prewarm" {
				c.markReady(fmt.Errorf("%s", firstNonEmpty(response.Error, response.Message, "semantic search is not ready")))
				continue
			}
			c.route(response)
		default:
			c.fail(fmt.Errorf("semantic search protocol: unknown message %q", response.Kind))
			return
		}
	}
	if err := scanner.Err(); err != nil {
		c.fail(err)
		return
	}
	c.stateMu.Lock()
	closing := c.closing
	c.stateMu.Unlock()
	if !closing {
		c.fail(io.ErrUnexpectedEOF)
	}
}

func (c *Client) route(response envelope) {
	c.pendingMu.Lock()
	ch := c.pending[response.ID]
	c.pendingMu.Unlock()
	if ch != nil {
		select {
		case ch <- response:
		default:
		}
	}
}

func (c *Client) markReady(err error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.readyErr == nil {
		c.readyErr = err
	}
	select {
	case <-c.ready:
	default:
		close(c.ready)
	}
}

func (c *Client) fail(err error) {
	c.markReady(err)
	c.stateMu.Lock()
	if c.failure == nil {
		c.failure = err
		close(c.failed)
	}
	c.stateMu.Unlock()
}

// WaitReady blocks until the resident has prepared the model or failed.
func (c *Client) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ready:
		c.stateMu.Lock()
		defer c.stateMu.Unlock()
		return c.readyErr
	}
}

// Query asks the resident for neighbors. The caller must have WaitReady'd.
func (c *Client) Query(ctx context.Context, in Request) (json.RawMessage, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if in.K == 0 {
		in.K = 10
	}
	if in.K < 1 || in.K > 100 {
		return nil, fmt.Errorf("k must be between 1 and 100")
	}
	c.stateMu.Lock()
	unsupported := in.ExpandTemplates && !c.queryOptions["expand_templates"] || in.MinScore != 0 && !c.queryOptions["min_score"]
	c.stateMu.Unlock()
	if unsupported {
		return nil, ErrUnsupportedOptions
	}
	c.pendingMu.Lock()
	c.nextID++
	id := c.nextID
	responseCh := make(chan envelope, 1)
	c.pending[id] = responseCh
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()
	request := map[string]any{"id": id, "op": "query", "query": query, "k": in.K}
	if strings.TrimSpace(in.Databases) != "" {
		request["databases"] = in.Databases
	}
	if in.ExpandTemplates {
		request["expand_templates"] = true
	}
	if in.MinScore != 0 {
		request["min_score"] = in.MinScore
	}
	c.writeMu.Lock()
	err := c.encoder.Encode(request)
	c.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("ask semantic search: %w", err)
	}
	var response envelope
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.failed:
		c.stateMu.Lock()
		err := c.failure
		c.stateMu.Unlock()
		return nil, err
	case response = <-responseCh:
	}
	if response.Kind == "error" || response.Error != "" {
		return nil, fmt.Errorf("%s", firstNonEmpty(response.Error, response.Message, "semantic search failed"))
	}
	if len(response.Result) == 0 {
		return json.RawMessage("null"), nil
	}
	return response.Result, nil
}

// Close drops this client. The shared resident keeps running for other callers.
func (c *Client) Close() error {
	c.stateMu.Lock()
	c.closing = true
	c.stateMu.Unlock()
	c.closeOnce.Do(func() {
		if c.stdin != nil {
			_ = c.stdin.Close()
		} else if c.conn != nil {
			_ = c.conn.Close()
		}
	})
	return nil
}

// CurrentQueryCapable reports whether the listening resident advertised the
// query options this companion uses. A pre-upgrade resident answers plain
// queries but still runs the old snapshot copy path.
func (c *Client) CurrentQueryCapable() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.queryOptions["expand_templates"] && c.queryOptions["min_score"]
}

func connectReady(ctx context.Context, opts Options) (*Client, error) {
	conn, err := DialOrSpawn(ctx, opts)
	if err != nil {
		return nil, err
	}
	client := NewClient(conn, opts.Status)
	if err := client.WaitReady(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// ConnectCurrent dials or starts a resident that can run the current query
// path. A listening legacy resident is left running; this companion then
// starts its own socket beside it so the query uses live reads.
func ConnectCurrent(ctx context.Context, opts Options) (*Client, error) {
	socket, lock := currentQueryPaths(opts)
	if conn, err := Dial(socket); err == nil {
		client := NewClient(conn, opts.Status)
		if err := client.WaitReady(ctx); err != nil {
			_ = client.Close()
			return nil, err
		}
		return client, nil
	}
	client, err := connectReady(ctx, opts)
	if err != nil {
		return nil, err
	}
	if client.CurrentQueryCapable() {
		return client, nil
	}
	_ = client.Close()
	opts.Socket, opts.Lock = socket, lock
	return connectReady(ctx, opts)
}

// QueryOnce dials or starts the resident, waits until it is ready, and runs one
// query. Closing the connection does not stop the resident.
func QueryOnce(ctx context.Context, opts Options, in Request) (json.RawMessage, error) {
	client, err := ConnectCurrent(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return client.Query(ctx, in)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// PrewarmMS is how long the resident spent preparing, when it reported one.
func (c *Client) PrewarmMS() int64 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.prewarmMS
}
