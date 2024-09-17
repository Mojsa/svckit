package nsq

import (
	"context"
	"fmt"
	"sync"

	"github.com/minus5/svckit/amp"
	"github.com/minus5/svckit/env"
	"github.com/minus5/svckit/log"
	"github.com/minus5/svckit/nsq"
	"github.com/pkg/errors"
)

type Requester struct {
	topic         string
	producer      *nsq.Producer
	consumer      *nsq.Consumer
	queue         map[uint64]*request // requests in process
	correlationNo uint64
	closed        chan struct{}
	sync.Mutex
}

type request struct {
	msg    *amp.Msg
	source amp.Subscriber
}

func MustRequester(ctx context.Context) *Requester {
	r, err := NewRequester(ctx)
	if err != nil {
		log.Fatal(err)
	}
	return r
}

func NewRequester(ctx context.Context) (*Requester, error) {
	p, err := nsq.NewProducer("")
	if err != nil {
		return nil, errors.WithStack(err)
	}
	r := &Requester{
		producer: p,
		queue:    make(map[uint64]*request),
		topic:    resposesTopicName(),
		closed:   make(chan struct{}),
	}
	c, err := nsq.NewConsumer(r.topic, r.responses)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	r.consumer = c
	go r.waitDone(ctx)
	return r, nil
}

func resposesTopicName() string {
	return fmt.Sprintf("z...rsp-%s-%s", env.AppName(), env.InstanceId())
}

func (r *Requester) responses(nm *nsq.Message) error {
	m := amp.ParseFromBackend(nm.Body)
	r.reply(m.CorrelationID, m)
	return nil
}

func (r *Requester) reply(correlationID uint64, m *amp.Msg) {
	r.Lock()
	req, ok := r.queue[correlationID]
	if ok {
		delete(r.queue, correlationID)
	}
	r.Unlock()

	if !ok {
		return
	}
	m.CorrelationID = req.msg.CorrelationID
	req.source.Send(m)
	return
}

func (r *Requester) Send(e amp.Subscriber, m *amp.Msg) {
	r.Lock()
	r.correlationNo++
	correlationID := r.correlationNo
	r.queue[correlationID] = &request{msg: m, source: e}
	r.Unlock()

	rm := m.Request()
	rm.CorrelationID = correlationID
	rm.ReplyTo = r.topic
	buf := rm.MarshalForBackend()

	go func() {
		err := r.producer.PublishTo(m.Topic(), buf)
		if err != nil {
			r.reply(correlationID, m.ResponseTransportError(err))
		}
	}()
}

// Current send current message for the uri
func (r *Requester) Current(uri string) {
	m := amp.NewCurrent(uri)
	topic := fmt.Sprintf("%s.%s", m.Topic(), "current")
	r.producer.PublishTo(topic, m.Marshal())
}

func (r *Requester) Unsubscribe(e amp.Subscriber) {
	r.Lock()
	defer r.Unlock()
	for key, req := range r.queue {
		if req.source == e {
			delete(r.queue, key)
		}
	}
}

func (r *Requester) waitDone(ctx context.Context) {
	<-ctx.Done()

	r.producer.Close()
	r.consumer.Close()
	r.Lock()
	defer r.Unlock()
	r.queue = make(map[uint64]*request)
	close(r.closed)
}

func (r *Requester) Wait() {
	<-r.closed
}
