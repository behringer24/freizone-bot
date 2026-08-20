package control

import (
	"bufio"
	"encoding/json"
	"net"
	"time"

	"github.com/behringer24/freizone-bot/internal/ipc"
)

// rawRequest writes a line exactly as given, bypassing ipc.Do -- which stamps
// the current protocol version and would only ever produce well-formed JSON.
// Testing the refusals needs the ability to send something the client cannot.
func rawRequest(addr, line string) (*ipc.Response, error) {
	conn, err := net.DialTimeout("unix", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close() //nolint:errcheck
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		return nil, err
	}
	raw, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(raw) == 0 {
		return nil, err
	}
	var resp ipc.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
