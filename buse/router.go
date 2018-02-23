package buse

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-errors/errors"
	"github.com/itchio/butler/mansion"
	"github.com/itchio/wharf/state"
	"github.com/sourcegraph/jsonrpc2"
)

type RequestHandler func(rc *RequestContext) (interface{}, error)

type Router struct {
	Handlers       map[string]RequestHandler
	MansionContext *mansion.Context
}

func (r Router) Register(method string, rh RequestHandler) {
	if _, ok := r.Handlers[method]; ok {
		panic(fmt.Sprintf("Can't register handler twice for %s", method))
	}
	r.Handlers[method] = rh
}

func (r Router) Dispatch(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	method := req.Method
	var res interface{}

	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				if rErr, ok := r.(error); ok {
					err = errors.Wrap(rErr, 0)
				} else {
					err = errors.New(r)
				}
			}
		}()

		if h, ok := r.Handlers[method]; ok {
			conn := &jsonrpc2Conn{conn}
			var consumer *state.Consumer
			consumer, err = NewStateConsumer(&NewStateConsumerParams{
				Ctx:  ctx,
				Conn: conn,
			})
			if err != nil {
				return
			}

			rc := &RequestContext{
				Ctx:            ctx,
				Harness:        NewProductionHarness(),
				Consumer:       consumer,
				Params:         req.Params,
				Conn:           conn,
				MansionContext: r.MansionContext,
			}
			res, err = h(rc)
		} else {
			err = StandardRpcError(jsonrpc2.CodeMethodNotFound)
		}
		return
	}()

	if err == nil {
		conn.Reply(ctx, req.ID, res)
		return
	}

	if ee, ok := asBuseError(err); ok {
		conn.ReplyWithError(ctx, req.ID, ee.AsJsonRpc2())
		return
	}

	var errStack *json.RawMessage
	if se, ok := err.(*errors.Error); ok {
		input := map[string]interface{}{
			"stack": se.ErrorStack(),
		}
		es, err := json.Marshal(input)
		if err == nil {
			rm := json.RawMessage(es)
			errStack = &rm
		}
	}
	conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
		Code:    jsonrpc2.CodeInternalError,
		Message: err.Error(),
		Data:    errStack,
	})
}
