// Copyright 2026 Mitja Martini
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package duckdbquack

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
)

// TestPrimarySecret_ScopedToItsURI proves the source's token is offered to
// its own Quack server only. An un-scoped quack secret is sent to whatever
// host a quack_query names, so SQL that reaches quack_query could hand the
// token to any listener.
func TestPrimarySecret_ScopedToItsURI(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var buf bytes.Buffer
		b := make([]byte, 4096)
		for {
			n, err := c.Read(b)
			buf.Write(b[:n])
			if err != nil || bytes.Contains(buf.Bytes(), []byte("\r\n\r\n")) && buf.Len() > 200 {
				break
			}
		}
		got <- buf.Bytes()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	other := fmt.Sprintf("quack:%s", l.Addr().String())
	_, _ = src.DuckDBQuackDB().ExecContext(ctx, fmt.Sprintf(
		"SELECT * FROM quack_query('%s', 'SELECT 1', disable_ssl := true)", other))

	select {
	case req := <-got:
		if req == nil {
			return // no connection at all: nothing was sent
		}
		if bytes.Contains(req, []byte(srv.tok)) {
			t.Fatalf("the source's token was sent to %s", other)
		}
	case <-time.After(5 * time.Second):
		// The client did not connect: with a secret scoped to its own
		// URI there is no token for the other host. (Before the fix it
		// connected and sent the token.)
	}
}

// TestLocalFiles_DisabledByDefault proves that the client DuckDB cannot read
// local files unless policy.allow_local_files is set, and that the remote
// catalog keeps working either way.
func TestLocalFiles_DisabledByDefault(t *testing.T) {
	srv := startInProcessQuackServer(t)
	f := filepath.Join(t.TempDir(), "x.csv")
	if err := os.WriteFile(f, []byte("a\n1\n2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, allow := range []bool{false, true} {
		src := newSource(t, srv, duckdbquack.Policy{AllowLocalFiles: allow})
		db := src.DuckDBQuackDB()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM remote.sales").Scan(&n); err != nil {
			t.Fatalf("allow_local_files=%v: remote read: %v", allow, err)
		}
		err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM read_csv('%s')", f)).Scan(&n)
		switch {
		case !allow && (err == nil || !strings.Contains(err.Error(), "disabled")):
			t.Errorf("allow_local_files=false: read_csv of a local file: want the filesystem disabled, got n=%d err=%v", n, err)
		case allow && (err != nil || n != 2):
			t.Errorf("allow_local_files=true: read_csv of a local file: n=%d err=%v", n, err)
		}
	}
}

// TestExecuteSQL_RefusesAgentEscapes proves agent-supplied SQL cannot use
// quack_query or quack_query_by_name, paths to a Quack server around the
// source's ATTACH.
func TestExecuteSQL_RefusesAgentEscapes(t *testing.T) {
	srv := startInProcessQuackServer(t)
	tool, srcs := newExecuteTool(t, srv, duckdbquack.Policy{})
	for _, q := range []string{
		fmt.Sprintf("SELECT * FROM quack_query('%s', 'SELECT 1', disable_ssl := true)", srv.uri),
		fmt.Sprintf("SELECT * FROM quack_query_by_name('%s', 'x')", srv.uri),
	} {
		_, errMsg := invoke(t, tool, srcs, q)
		if !strings.Contains(errMsg, "rejected by policy") {
			t.Errorf("%q: want a policy rejection, got %q", q, errMsg)
		}
	}
}
