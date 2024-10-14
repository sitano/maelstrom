package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	m "github.com/jepsen-io/maelstrom/demo/go"
)

var p *ProgramState

func main() {
	n := m.NewNode()
	p = NewProgramState(n)

	n.Handle("init", func(msg m.Message) error {
		p.SetTopology(n.NodeIDs())
		return nil
	})

	n.Handle("read", func(msg m.Message) error {
		var body kvReadMessageBody
		if err := json.Unmarshal(msg.Body, &body); err != nil {
			return err
		}

		var response = kvReadOKMessageBody{MessageBody: body.MessageBody, Value: nil}
		response.Type = "read_ok"

		return n.Reply(msg, response)
	})

	n.Handle("write", func(msg m.Message) error {
		var body kvWriteMessageBody
		if err := json.Unmarshal(msg.Body, &body); err != nil {
			return err
		}

		p.AppendOp()

		return n.Reply(msg, map[string]any{"type": "write_ok"})
	})

	n.Handle("cas", func(msg m.Message) error {
		var body kvCASMessageBody
		if err := json.Unmarshal(msg.Body, &body); err != nil {
			return err
		}

		// return n.Reply(msg, map[string]any{"type": "cas_err", "err": m.ErrorCodeText(m.Timeout)})
		return nil
	})

	// Execute the node's message loop. This will run until STDIN is closed.
	if err := n.Run(); err != nil {
		log.Printf("ERROR: %s", err)
		os.Exit(1)
	}
}

// kvReadMessageBody represents the body for the KV "read" message.
type kvReadMessageBody struct {
	m.MessageBody
	Key uint32 `json:"key"`
}

// kvReadOKMessageBody represents the response body for the KV "read_ok" message.
type kvReadOKMessageBody struct {
	m.MessageBody
	Value any `json:"value"`
}

// kvWriteMessageBody represents the body for the KV "write" message.
type kvWriteMessageBody struct {
	m.MessageBody
	Key   uint32 `json:"key"`
	Value any    `json:"value"`
}

// kvCASMessageBody represents the body for the KV "cas" message.
type kvCASMessageBody struct {
	m.MessageBody
	Key               uint32 `json:"key"`
	From              any    `json:"from"`
	To                any    `json:"to"`
	CreateIfNotExists bool   `json:"create_if_not_exists,omitempty"`
}

type ProgramState struct {
	m        sync.Mutex
	cond     sync.Cond
	topology []string
	busy     bool
	p        *SDPaxos
	n        *m.Node
}

func NewProgramState(n *m.Node) *ProgramState {
	s := &ProgramState{}
	s.n = n
	return s
}

func (s *ProgramState) SetTopology(nodes []string) {
	s.m.Lock()
	defer s.m.Unlock()
	s.topology = append([]string{}, nodes...)
}

func (s *ProgramState) AppendOp() {
	var ok bool
	for !ok {
		s.m.Lock()
		for s.busy {
			s.cond.Wait()
		}

		s.busy = true
		s.p = NewSDPaxos(s.n, s.topology, 0)
		s.m.Unlock()

		// attempt
		ok = s.p.Run()

		s.m.Lock()
		s.busy = false
		s.cond.Signal()
		s.m.Unlock()
	}
}

func sendAtLeastOnce(n *m.Node, to string, msg map[string]any) {
until_done:
	ctx, f := context.WithTimeout(context.Background(), time.Second)
	defer f()

	if _, err := n.SyncRPC(ctx, to, msg); err != nil {
		fmt.Fprintln(os.Stderr, "repeat broadcast message", n.ID(), "->", to, ":", msg, ", err:", err)
		goto until_done
	}
}

type Prepare struct {
	Epoch uint32          `json:"epoch"`
	Op    json.RawMessage `json:"op"`
}

type Promise struct {
	Epoch uint32 `json:"epoch"`
	Value any    `json:"value"`
}

type Accept struct {
	Epoch uint32 `json:"epoch"`
	Value any    `json:"value"`
}

type Accepted struct {
	Epoch uint32 `json:"epoch"`
}

type SDPaxos struct {
	node      *m.Node
	acceptors []string
	epoch     uint32
}

type PromiseResponse struct {
	Promise Promise
	Src     string
	Dest    string
}

func NewSDPaxos(node *m.Node, acceptors []string, epoch uint32) *SDPaxos {
	return &SDPaxos{
		node,
		acceptors,
		epoch,
	}
}

// With synchronous rpc calls we can omit Paxos instance ID.
func (s *SDPaxos) Run() bool {
	promises := s.callPrepare(Prepare{
		Epoch: 0,
		Op:    nil,
	})

	fmt.Fprintln(os.Stderr, "got promises: ", s.node.ID(), promises)

	return true
}

func (s *SDPaxos) syncRPC(msg any) map[string]m.Message {
	var m map[string]m.Message

	var x sync.Mutex
	var g sync.WaitGroup

	ctx, f := context.WithTimeout(context.Background(), time.Second)
	defer f()

	for _, to := range s.acceptors {
		g.Add(1)

		go func(to string) {
			defer g.Done()

			res, err := s.node.SyncRPC(ctx, to, msg)
			if err != nil {
				fmt.Fprintln(os.Stderr, "timeout: failure to make rpc call", s.node.ID(), "->", to, ":", msg, ", err:", err)
			}

			x.Lock()
			m[to] = res
			x.Unlock()
		}(to)
	}

	g.Wait()

	return m
}

func (s *SDPaxos) callPrepare(msg Prepare) map[string]PromiseResponse {
	var t map[string]PromiseResponse
	var data = map[string]any{
		"type": "prepare",
	}

	var bytes, err = json.Marshal(msg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "timeout: failure to marshal request: ", msg, ", err:", err)
	}
	if err := json.Unmarshal(bytes, &data); err != nil {
		fmt.Fprintln(os.Stderr, "timeout: failure to remarshal request: ", msg, ", err:", err)
	}

	for k, v := range s.syncRPC(data) {
		var o PromiseResponse

		if err := json.Unmarshal(v.Body, &o.Promise); err != nil {
			fmt.Fprintln(os.Stderr, "timeout: failure to read rpc response", v.Src, "->", v.Dest, ":", msg, ", err:", err)
		}

		o.Src = v.Src
		o.Dest = v.Dest

		t[k] = o
	}

	return t
}
