package main

import (
	"fmt"
	"sync"
	"time"

	"messenger/model"
)

// Store keeps messages in memory: a stand fixture, not a product.
type Store struct {
	mu       sync.Mutex
	messages []*model.Message
	now      func() time.Time
}

func NewStore() *Store { return &Store{now: time.Now} }

// Send records a message from author to recipient.
func (s *Store) Send(author, recipient, text string) *model.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := &model.Message{
		ID:        fmt.Sprintf("msg-%d", len(s.messages)+1),
		Text:      text,
		Author:    &model.User{ID: author},
		Recipient: &model.User{ID: recipient},
		SentAt:    s.now().UTC(),
	}
	s.messages = append(s.messages, m)
	return m
}

// Inbox returns the messages addressed to userID, oldest first.
func (s *Store) Inbox(userID string) []*model.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*model.Message{}
	for _, m := range s.messages {
		if m.Recipient.ID == userID {
			out = append(out, m)
		}
	}
	return out
}

// Find returns a message by id for entity resolution.
func (s *Store) Find(id string) (*model.Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.messages {
		if m.ID == id {
			return m, true
		}
	}
	return nil, false
}
