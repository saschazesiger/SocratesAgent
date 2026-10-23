package agenthost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Conn is a client's connection to a host: the server's, one per live chat.
type Conn struct {
	nc net.Conn
	r  *bufio.Reader

	wmu sync.Mutex
	// Hello is what the host said it is.
	Hello Hello
}

// Dial connects to the host on sock and says hello, asking for every event
// after since. The frames that follow are the replay and then the live tail,
// with no seam between them.
func Dial(ctx context.Context, sock string, since int64) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, err
	}
	c := &Conn{nc: nc, r: bufio.NewReaderSize(nc, 256<<10)}
	_ = nc.SetDeadline(time.Now().Add(helloWait))
	if err := c.Send(Command{Op: OpHello, Proto: Protocol, Since: since}); err != nil {
		nc.Close()
		return nil, err
	}
	f, err := c.Next()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("the agent host did not say hello: %w", err)
	}
	switch {
	case f.Fail != nil:
		nc.Close()
		return nil, fmt.Errorf("the agent host refused the connection: %s", f.Fail.Msg)
	case f.Hello == nil:
		nc.Close()
		return nil, errors.New("the agent host did not say hello first")
	case f.Hello.Proto < MinProtocol || f.Hello.Proto > Protocol:
		nc.Close()
		return nil, fmt.Errorf("the agent host speaks protocol %d, and this server %d to %d", f.Hello.Proto, MinProtocol, Protocol)
	}
	c.Hello = *f.Hello
	_ = nc.SetDeadline(time.Time{})
	return c, nil
}

// Send writes one command.
func (c *Conn) Send(cmd Command) error {
	raw, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.nc.SetWriteDeadline(time.Now().Add(frameWriteWait))
	_, err = c.nc.Write(append(raw, '\n'))
	return err
}

// Next reads the next frame. It is called from one goroutine only.
func (c *Conn) Next() (Frame, error) {
	line, err := readLine(c.r)
	if err != nil {
		return Frame{}, err
	}
	return DecodeFrame(line)
}

// Close drops the connection. The host carries on.
func (c *Conn) Close() error { return c.nc.Close() }
