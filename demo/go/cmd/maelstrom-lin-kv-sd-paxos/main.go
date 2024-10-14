// $ ./lein.bat run -- test -w lin-kv --bin ./demo/go/cmd/maelstrom-lin-kv-sd-paxos/maelstrom-lin-kv-sd-paxos --time-limit 10 --node-count 3 --rate 1 --concurrency 2n --key-count 1
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"sync"
	"time"

	m "github.com/jepsen-io/maelstrom/demo/go"
)

var p *ProgramState

var ErrNodeUnsynced = errors.New("node is unsynced")
var ErrAlreadyPromised = errors.New("already promised with higher epoch")
var ErrLearnTimeout = errors.New("learn timeout")
var ErrNewEpoch = errors.New("new epoch")

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

		var candidate *uint32
		for {
			// TODO: quorum lin-read algorithm
			candidate = p.ReadLocal(body.Key)
			op := Op{
				Type:  "read",
				Key:   body.Key,
				Value: candidate,
			}

			res := p.AppendOp(op)
			fmt.Fprintln(os.Stderr, "paxos round failed: canditate not accepted: ", res)
			break

			// fmt.Fprintln(os.Stderr, "paxos round failed: canditate not accepted: ", candidate)
		}

		var response = kvReadOKMessageBody{MessageBody: body.MessageBody, Value: candidate}
		response.Type = "read_ok"

		return n.Reply(msg, response)
	})

	n.Handle("write", func(msg m.Message) error {
		var body kvWriteMessageBody
		if err := json.Unmarshal(msg.Body, &body); err != nil {
			return err
		}

		v := uint32(body.Value.(float64))
		p.AppendOp(Op{
			Type:  "write",
			Key:   body.Key,
			Value: &v,
		})

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

	n.Handle("paxos_prepare", func(msg m.Message) error {
		return p.HandlePaxos(msg)
	})

	n.Handle("paxos_promise", func(msg m.Message) error {
		return p.HandlePaxos(msg)
	})

	n.Handle("paxos_accept", func(msg m.Message) error {
		return p.HandlePaxos(msg)
	})

	n.Handle("paxos_accepted", func(msg m.Message) error {
		return p.HandlePaxos(msg)
	})

	n.Handle("paxos_not_accepted", func(msg m.Message) error {
		return p.HandlePaxos(msg)
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

type Op struct {
	Type  string  `json:"type"`
	Key   uint32  `json:"key"`
	Value *uint32 `json:"value"`
}

type ProgramState struct {
	m    sync.Mutex
	cond *sync.Cond

	data map[uint32]uint32

	topology []string
	n        *m.Node

	p           SDPaxos
	busy        bool
	outdated    bool
}

func NewProgramState(n *m.Node) *ProgramState {
	s := &ProgramState{}
	s.cond = sync.NewCond(&s.m)
	s.data = map[uint32]uint32{}
	s.n = n
	s.resetPaxos()
	return s
}

func (s *ProgramState) SetTopology(nodes []string) {
	s.m.Lock()
	defer s.m.Unlock()
	s.topology = append([]string{}, nodes...)
	s.p.acceptors = append([]string{}, nodes...)
}

// test if the learned value for the latest slot was ours value
func (r *LearnResult) TestLearned(op any) bool {
	serialized := map[string]any{}
	fromJSON(toJSON(op), &serialized)
	res := reflect.DeepEqual(serialized, r.learned)
	fmt.Fprintln(os.Stderr, "test learned: ", serialized, "?", r.learned, ":", res)
	return res
}

func (s *ProgramState) AppendOp(op any) LearnResult {
	for {
		res, ok := s.TryAppendOp(op)
		if ok {
			if res.TestLearned(op) {
				return res
			}
		}
	}
}

func (s *ProgramState) TryAppendOp(op any) (LearnResult, bool) {
	return s.RunPaxos(op)
}

func (p *ProgramState) HandlePaxos(msg m.Message) error {
	var body m.MessageBody
	if err := json.Unmarshal(msg.Body, &body); err != nil {
		return err
	}

	switch body.Type {
	case "paxos_prepare":
		var prepare Prepare
		if err := json.Unmarshal(msg.Body, &prepare); err != nil {
			return err
		}
		return p.HandlePaxosPrepare(msg, prepare)
	case "paxos_promise":
		var promise Promise
		if err := json.Unmarshal(msg.Body, &promise); err != nil {
			return err
		}
		return p.HandlePaxosPromise(msg, promise)
	case "paxos_accept":
		var accept Accept
		if err := json.Unmarshal(msg.Body, &accept); err != nil {
			return err
		}
		return p.HandlePaxosAccept(msg, accept)
	case "paxos_accepted":
		var accepted Accepted
		if err := json.Unmarshal(msg.Body, &accepted); err != nil {
			return err
		}
		return p.HandlePaxosAccepted(msg, accepted)
	default:
		panic(fmt.Errorf("unimplemented method: %v", msg))
	}
}

type Prepare struct {
	m.MessageBody

	Lsn   uint32 `json:"lsn"`
	Epoch uint32 `json:"epoch"`
	Value any    `json:"value"`
}

type Promise struct {
	m.MessageBody

	Lsn   uint32 `json:"lsn"`
	Epoch uint32 `json:"epoch"`
	Value any    `json:"value"`
}

type Accept struct {
	Lsn   uint32 `json:"lsn"`
	Epoch uint32 `json:"epoch"`
	Value any    `json:"value"`
}

type Accepted struct {
	Lsn   uint32 `json:"lsn"`
	Epoch uint32 `json:"epoch"`
	Value any    `json:"value"`
}

type AcceptedMessage struct {
	Src      string   `json:"src,omitempty"`
	Dest     string   `json:"dest,omitempty"`
	Accepted Accepted `json:"body,omitempty"`
}

type LearnResult struct {
	learned  any
	accepted map[string]AcceptedMessage
}

type SDPaxos struct {
	acceptors []string

	lsn   uint32
	epoch uint32

	prepared map[string]Promise

	promised any
	accepted map[string]AcceptedMessage

	learned any
}

func (s *ProgramState) RunPaxos(value any) (LearnResult, bool) {
	s.m.Lock()
	for s.busy {
		s.cond.Wait()
	}

	s.busy = true
	s.outdated = false
	s.resetPaxos()
	s.m.Unlock()

	defer func() {
		s.m.Lock()
		s.busy = false
		s.outdated = false
		s.cond.Signal()
		s.m.Unlock()
	}()

	if err := s.callPrepare(value); err != nil {
		fmt.Fprintln(os.Stderr, "paxos round failed: preparing: ", err)
		if err == ErrLearnTimeout || err == ErrNewEpoch {
			return LearnResult{}, false
		}
		panic(err)
	}

	s.m.Lock()
	fmt.Fprintln(os.Stderr, "got enough promises: ", s.n.ID(), s.p.prepared)
	s.m.Unlock()

	if err := s.callAccept(value); err != nil {
		fmt.Fprintln(os.Stderr, "paxos round failed: proposing: ", err)
		if err == ErrLearnTimeout || err == ErrNewEpoch {
			return LearnResult{}, false
		}
		panic(err)
	}

	s.m.Lock()
	learned := LearnResult{
		learned:  s.p.learned,
		accepted: s.p.accepted,
	}
	s.resetPaxos()
	s.m.Unlock()

	return learned, true 
}

func (s *ProgramState) callPrepare(value any) error {
	var data = map[string]any{
		"type": "paxos_prepare",
	}

	s.m.Lock()
	// if it is another round over the undecided slot pick the largest epoch.
	if len(s.p.prepared) > 0 {
		s.p.epoch++
		for _, t := range s.p.prepared {
			if t.Epoch >= s.p.epoch {
				s.p.epoch = t.Epoch + 1
			}
		}
	}

	var msg = Prepare{
		Lsn:   s.p.lsn,
		Epoch: s.p.epoch,
		Value: value,
	}
	s.m.Unlock()

	fromJSON(toJSON(msg), &data)

	for _, dst := range p.p.acceptors {
		p.n.Send(dst, data)
	}

	return s.WaitPrepared()
}

func (p *ProgramState) HandlePaxosPrepare(msg m.Message, body Prepare) error {
	p.m.Lock()
	defer p.m.Unlock()

	if body.Lsn < p.p.lsn {
		fmt.Fprintln(os.Stderr, "prepare: got msg from decided slot: local lsn", p.p.lsn, " > global ", body.Lsn)
		return nil
	}

	if body.Lsn > p.p.lsn {
		fmt.Fprintln(os.Stderr, "prepare: node is unsynced: local lsn", p.p.lsn, " < global ", body.Lsn)
		return ErrNodeUnsynced
	}

	if body.Epoch < p.p.epoch {
		fmt.Fprintln(os.Stderr, "prepare: got outdated request", body.Epoch, "<", p.p.epoch, "from", msg.Src)
		return nil
	}

	if body.Epoch > p.p.epoch {
		fmt.Fprintln(os.Stderr, "prepare: there is newer proposer", body.Epoch, ">", p.p.epoch, "from", msg.Src)
		// set epoch if we are not-a-local-leader.
		p.p.epoch = body.Epoch
		if p.busy {
			p.outdated = true
		}
	}

	return p.n.Reply(msg, map[string]any{
		"type":  "paxos_promise",
		"lsn":   p.p.lsn,
		"epoch": p.p.epoch,
		"value": p.p.promised,
	})
}

func (p *ProgramState) HandlePaxosPromise(msg m.Message, body Promise) error {
	p.m.Lock()
	defer p.m.Unlock()

	if body.Lsn < p.p.lsn {
		fmt.Fprintln(os.Stderr, "promise: got msg from decided slot: local lsn", p.p.lsn, " > global ", body.Lsn)
		return nil
	}

	if body.Lsn > p.p.lsn {
		fmt.Fprintln(os.Stderr, "promise: node is unsynced: local lsn", p.p.lsn, " < global ", body.Lsn)
		return ErrNodeUnsynced
	}

	if body.Epoch < p.p.epoch {
		fmt.Fprintln(os.Stderr, "promise: got outdated request", body.Epoch, "<", p.p.epoch, "from", msg.Src)
		return nil
	}

	if body.Epoch > p.p.epoch {
		fmt.Fprintln(os.Stderr, "promise: there is newer proposer", body.Epoch, ">", p.p.epoch, "from", msg.Src)
		if p.busy {
			p.outdated = true
		}
		return nil
	}

	p.p.prepared[msg.Src] = body

	if p.p.promised == nil {
		p.p.promised = body.Value
	}

	fmt.Fprintln(os.Stderr, "promise: got promise", body.Epoch, "from", msg.Src)

	return nil
}

// value is only a proposition. if there is another value in the slot that was promised to be learned it will be learned instead.
func (p *ProgramState) callAccept(value any) error {
	var data = map[string]any{
		"type": "paxos_accept",
	}

	p.m.Lock()

	if p.p.promised != nil {
		value = p.p.promised
	}

	var msg = Accept{
		Lsn:   p.p.lsn,
		Epoch: p.p.epoch,
		Value: value,
	}

	p.m.Unlock()

	fromJSON(toJSON(msg), &data)

	for _, dst := range p.p.acceptors {
		p.n.Send(dst, data)
	}

	return p.WaitLearned(msg.Lsn)
}

func (p *ProgramState) callAccepted(msg Accepted) error {
	var data = map[string]any{
		"type": "paxos_accepted",
	}

	fromJSON(toJSON(msg), &data)

	for _, dst := range p.p.acceptors {
		p.n.Send(dst, data)
	}

	return nil
}

func (p *ProgramState) HandlePaxosAccept(msg m.Message, body Accept) error {
	p.m.Lock()
	defer p.m.Unlock()

	if body.Lsn < p.p.lsn {
		fmt.Fprintln(os.Stderr, "accept: got msg from decided slot: local lsn", p.p.lsn, " > global ", body.Lsn)
		return nil
	}

	if body.Lsn > p.p.lsn {
		fmt.Fprintln(os.Stderr, "accept: node is unsynced: local lsn", p.p.lsn, " < global ", body.Lsn)
		return ErrNodeUnsynced
	}

	if body.Epoch < p.p.epoch {
		fmt.Fprintln(os.Stderr, "accept: got outdated request", body.Epoch, "<", p.p.epoch, "from", msg.Src)
		return nil
	}

	if body.Epoch > p.p.epoch {
		fmt.Fprintln(os.Stderr, "accept: there is newer proposer", body.Epoch, ">", p.p.epoch, "from", msg.Src)
		return ErrAlreadyPromised // TODO?
	}

	if p.p.promised != nil {
		return p.n.Reply(msg, map[string]any{
			"type":  "paxos_not_accepted",
			"lsn":   p.p.lsn,
			"epoch": p.p.epoch,
			"value": p.p.promised,
		})
	}

	accepted := Accepted{
		Lsn:   body.Lsn,
		Epoch: body.Epoch,
		Value: body.Value,
	}

	p.p.promised = body.Value
	p.p.accepted[msg.Dest] = AcceptedMessage{
		Src:      msg.Src,
		Dest:     msg.Dest,
		Accepted: accepted,
	}

	return p.callAccepted(accepted)
}

func (p *ProgramState) HandlePaxosAccepted(msg m.Message, body Accepted) error {
	if err := p.handlePaxosAcceptedMessage(msg, body); err != nil {
		return err
	}

	return p.HandlePaxosLearn()
}

func (p *ProgramState) handlePaxosAcceptedMessage(msg m.Message, body Accepted) error {
	p.m.Lock()
	defer p.m.Unlock()

	// TODO: ignore for all learned things
	if body.Lsn < p.p.lsn {
		fmt.Fprintln(os.Stderr, "accepted: got msg from decided slot: local lsn", p.p.lsn, " > global ", body.Lsn)
		return nil
	}

	if body.Lsn > p.p.lsn {
		fmt.Fprintln(os.Stderr, "accepted: node is unsynced: local lsn", p.p.lsn, " < global ", body.Lsn)
		return ErrNodeUnsynced
	}

	if body.Epoch < p.p.epoch {
		fmt.Fprintln(os.Stderr, "accepted: got outdated request", body.Epoch, "<", p.p.epoch, "from", msg.Src)
		return nil
	}

	if body.Epoch > p.p.epoch {
		fmt.Fprintln(os.Stderr, "accepted: there is newer proposer", body.Epoch, ">", p.p.epoch, "from", msg.Src)
		return ErrAlreadyPromised // TODO?
	}

	if p.p.learned != nil {
		fmt.Fprintln(os.Stderr, "paxos: value already learned:", toJSON(body.Value))
		return nil
	}

	p.p.accepted[msg.Src] = AcceptedMessage{
		Src:      msg.Src,
		Dest:     msg.Dest,
		Accepted: body,
	}

	if p.accepted_by_majority(body.Value) {
		fmt.Fprintln(os.Stderr, "paxos: value decided for slot LSN =", p.p.lsn, "Epoch =", p.p.epoch, ":", toJSON(body.Value))
		p.p.learned = body.Value
	}

	return nil
}

func (p *ProgramState) HandlePaxosLearn() error {
	p.m.Lock()
	defer p.m.Unlock()

	if p.p.learned == nil {
		return nil
	}

	if err := p.learn(p.p.learned); err != nil {
		panic(fmt.Errorf("paxos: LSN = %d, Epoch = %d, learn error: %v", p.p.lsn, p.p.epoch, err))
	}

	// next slot
	p.p.lsn++
	// next paxos epoch
	p.p.epoch++

	return nil
}

func (p *ProgramState) learn(value any) error {
	fmt.Fprintln(os.Stderr, "applying: ", toJSON(value))

	var op Op
	fromJSON(toJSON(value), &op)

	switch op.Type {
	case "write":
		p.data[op.Key] = *op.Value
	default:
		panic(fmt.Sprint("unknown op: ", op))
	}

	return nil
}

func (p *ProgramState) accepted_by_majority(value any) bool {
	quorum := len(p.p.acceptors)/2 + 1
	count := 0
	for _, v := range p.p.accepted {
		if reflect.DeepEqual(v.Accepted.Value, value) {
			count++
		}
	}
	return count >= quorum
}

func (p *ProgramState) WaitPrepared() error {
	start := time.Now()

	for {
		p.m.Lock()

		quorum := len(p.p.acceptors)/2 + 1
		count := 0

		for _, t := range p.p.prepared {
			if t.Epoch == p.p.epoch {
				count++

				// prepared
				if count >= quorum {
					p.m.Unlock()
					return nil
				}
			}

			// found new leader
			if t.Epoch > p.p.epoch || p.outdated {
				p.m.Unlock()
				return ErrNewEpoch
			}
		}

		p.m.Unlock()

		if time.Since(start) > time.Second {
			return ErrLearnTimeout
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func (p *ProgramState) WaitLearned(lsn uint32) error {
	start := time.Now()

	for {
		p.m.Lock()
		if p.outdated {
			p.m.Unlock()
			return ErrNewEpoch
		}
		if p.p.learned != nil {
			p.m.Unlock()
			return nil
		}
		p.m.Unlock()

		if time.Since(start) > time.Second {
			return ErrLearnTimeout
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func (p *ProgramState) resetPaxos() {
	p.p.promised = nil
	p.p.learned = nil

	p.p.prepared = map[string]Promise{}
	p.p.accepted = map[string]AcceptedMessage{}
}

func toJSON(value any) string {
	var bytes, err = json.Marshal(value)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failure to marshal request: ", value, ", err:", err)
		panic(err)
	}

	return string(bytes)
}

func fromJSON(msg string, obj any) {
	if err := json.Unmarshal([]byte(msg), &obj); err != nil {
		fmt.Fprintln(os.Stderr, "failure to marshal request: ", msg, ", err:", err)
		panic(err)
	}
}

func (p *ProgramState) ReadLocal(key uint32) *uint32 {
	p.m.Lock()
	v, ok := p.data[key]
	p.m.Unlock()

	if !ok {
		return nil
	}

	return &v
}
