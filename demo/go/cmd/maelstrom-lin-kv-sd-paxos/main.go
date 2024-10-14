package main

import (
	"encoding/json"
	"log"
	"os"
	"sync"

	m "github.com/jepsen-io/maelstrom/demo/go"
)

var p *ProgramState = NewProgramState()

func main() {
	n := m.NewNode()

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
}

func NewProgramState() *ProgramState {
	return &ProgramState{}
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
		s.p = NewSDPaxos(s.topology, 0)
		s.m.Unlock()

		// attempt
		ok = true

		s.m.Lock()
		s.busy = false
		s.cond.Signal()
		s.m.Unlock()
	}
}

type Prepare struct {
	Epoch uint32
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
	acceptors []string
	epoch     uint32
}

func NewSDPaxos(acceptors []string, epoch uint32) *SDPaxos {
	return &SDPaxos{
		acceptors,
		epoch,
	}
}
