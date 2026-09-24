package session

import (
	"sync"
	"testing"
	"time"

	"github.com/nibra/gosiptea/internal/storage"
)

func TestPublishKeepsNewestSnapshotWhenFull(t *testing.T) {
	s := &Session{subscribers: make(map[uint64]chan Snapshot)}
	updates, unsubscribe := s.Subscribe(2)
	defer unsubscribe()

	s.publish(Snapshot{Revision: 1})
	s.publish(Snapshot{Revision: 2})
	for _, want := range []uint64{1, 2} {
		select {
		case snapshot := <-updates:
			if snapshot.Revision != want {
				t.Fatalf("revision = %d, want %d", snapshot.Revision, want)
			}
		default:
			t.Fatalf("missing revision %d", want)
		}
	}
}

func TestPublishConcurrentDrainDoesNotBlock(t *testing.T) {
	s := &Session{subscribers: make(map[uint64]chan Snapshot)}
	updates, unsubscribe := s.Subscribe(1)
	channel := s.subscribers[0]
	stop := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			select {
			case <-updates:
			case <-stop:
				return
			}
		}
	}()

	published := make(chan struct{})
	var publishers sync.WaitGroup
	for range 2 {
		publishers.Go(func() {
			for revision := range uint64(50000) {
				s.publish(Snapshot{Revision: revision})
			}
		})
	}
	go func() {
		publishers.Wait()
		close(published)
	}()
	t.Cleanup(func() {
		close(stop)
		<-consumerDone
		// Release a publisher blocked on an empty channel so a failed
		// regression test does not leave its goroutines running.
		select {
		case channel <- Snapshot{}:
		default:
		}
		select {
		case <-published:
			unsubscribed := make(chan struct{})
			go func() {
				unsubscribe()
				close(unsubscribed)
			}()
			select {
			case <-unsubscribed:
			case <-time.After(time.Second):
				t.Error("unsubscribe blocked after publishing")
			}
		case <-time.After(5 * time.Second):
			t.Error("publishers did not stop after cleanup")
		}
	})

	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked while subscriber drained its channel")
	}
}

func TestPublishClonesSnapshotsForEachSubscriber(t *testing.T) {
	s := &Session{subscribers: make(map[uint64]chan Snapshot)}
	first, cancelFirst := s.Subscribe(1)
	defer cancelFirst()
	second, cancelSecond := s.Subscribe(1)
	defer cancelSecond()
	snapshot := Snapshot{Contacts: []storage.Contact{{Name: "Alice", URI: "sip:alice@example.com"}}}
	s.publish(snapshot)

	got := <-first
	got.Contacts[0].Name = "Changed"
	if other := <-second; other.Contacts[0].Name != "Alice" {
		t.Fatalf("subscriber snapshots share contacts: %#v", other.Contacts)
	}
	if snapshot.Contacts[0].Name != "Alice" {
		t.Fatal("published snapshot was modified by subscriber")
	}
}
