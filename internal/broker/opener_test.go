package broker_test

import (
	"context"
	"errors"
	"testing"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
)

// SubscriberFrom exists because Go has no covariant return types: a method
// returning *semantik.Subscription does not satisfy an interface method
// returning Stream. The conversion is one line, and a mistake in it means the
// subscribe tool talks to nothing.
func TestSubscriberFromPassesTheRequestThrough(t *testing.T) {
	opener := &recordingOpener{}
	subscriber := broker.SubscriberFrom(opener)

	req := semantik.SubscribeRequest{Query: "MATCH", Namespace: "incidents", Model: "model-a", Dimensions: 512}
	_, err := subscriber.Subscribe(context.Background(), req)
	if err == nil {
		t.Fatal("expected the opener's error to be surfaced")
	}

	if opener.got != req {
		t.Errorf("expected the request to reach the SDK unchanged, got %+v", opener.got)
	}
}

// A failed open must surface as an error, not as a non-nil Stream wrapping a
// nil subscription — that would defer the failure to the first read, where it
// arrives with no context at all.
func TestSubscriberFromReturnsNoStreamOnFailure(t *testing.T) {
	sentinel := errors.New("handshake refused")
	subscriber := broker.SubscriberFrom(&recordingOpener{err: sentinel})

	stream, err := subscriber.Subscribe(context.Background(), semantik.SubscribeRequest{})

	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the underlying error, got %v", err)
	}
	if stream != nil {
		t.Error("expected no stream alongside the error")
	}
}

type recordingOpener struct {
	err error
	got semantik.SubscribeRequest
}

func (r *recordingOpener) Subscribe(_ context.Context, req semantik.SubscribeRequest) (*semantik.Subscription, error) {
	r.got = req
	if r.err != nil {
		return nil, r.err
	}
	// A *semantik.Subscription cannot be constructed outside its package, so a
	// successful open is exercised end to end by the integration suite instead.
	return nil, errors.New("no live subscription in a unit test")
}
