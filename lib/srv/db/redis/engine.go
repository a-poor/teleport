/*
Copyright 2022 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package redis

import (
	"bytes"
	"context"
	"net"

	"github.com/go-redis/redis/v8"
	"github.com/gravitational/teleport/lib/srv/db/common"
	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"
)

// Engine implements common.Engine.
type Engine struct {
	// Auth handles database access authentication.
	Auth common.Auth
	// Audit emits database access audit events.
	Audit common.Audit
	// Context is the database server close context.
	Context context.Context
	// Clock is the clock interface.
	Clock clockwork.Clock
	// Log is used for logging.
	Log logrus.FieldLogger
}

func (e *Engine) HandleConnection(ctx context.Context, sessionCtx *common.Session, conn net.Conn) error {
	tlsConfig, err := e.Auth.GetTLSConfig(ctx, sessionCtx)
	if err != nil {
		return trace.Wrap(err)
	}

	redisConn := redis.NewClient(&redis.Options{
		Addr:      sessionCtx.Database.GetURI(),
		TLSConfig: tlsConfig,
	})

	pingResp := redisConn.Ping(context.Background())
	if pingResp.Err() != nil {
		return trace.Wrap(err)
	}

	if err := process(ctx, conn, redisConn); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

func process(ctx context.Context, clientConn net.Conn, redisClient *redis.Client) error {
	clientReader := redis.NewReader(clientConn)
	buf := &bytes.Buffer{}
	wr := redis.NewWriter(buf)

	for {
		cmd := &redis.Cmd{}
		if err := cmd.ReadReply(clientReader); err != nil {
			return trace.Wrap(err)
		}

		//fmt.Printf("client cmd: %v\n", cmd)

		val := cmd.Val().([]interface{})
		nCmd := redis.NewCmd(ctx, val...)

		err := redisClient.Process(ctx, nCmd)
		if err != nil {
			return trace.Wrap(err)
		}

		vals, err := nCmd.Result()
		if err != nil {
			return trace.Wrap(err)
		}

		if err := writeCmd(wr, vals); err != nil {
			return trace.Wrap(err)
		}

		//fmt.Printf("redis err: %v args: %v\n", err, buf)

		if _, err := clientConn.Write(buf.Bytes()); err != nil {
			return trace.Wrap(err)
		}

		buf.Reset()
	}
}

func writeCmd(wr *redis.Writer, vals interface{}) error {
	switch val := vals.(type) {
	case []interface{}:
		if err := wr.WriteByte(redis.ArrayReply); err != nil {
			return err
		}
		n := len(val)
		if err := wr.WriteLen(n); err != nil {
			return err
		}

		for _, v0 := range val {
			if err := writeCmd(wr, v0); err != nil {
				return err
			}
		}
	case interface{}:
		err := wr.WriteArg(val)
		if err != nil {
			return err
		}
	}

	return nil
}
