package main

import (
	"context"
	"testing"
	"time"
)

func TestWatchParentCancelsWhenParentDisappears(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	child := watchParent(parent, 42, func(int) bool { return false })
	select {
	case <-child.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("parent loss did not stop daemon context")
	}
}

func TestWatchParentLeavesServiceContextAlone(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	child := watchParent(parent, 0, func(int) bool { return false })
	select {
	case <-child.Done():
		t.Fatal("service daemon unexpectedly stopped")
	default:
	}
}
