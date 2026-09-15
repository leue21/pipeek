package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestShutdownDrainsActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "complete")
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveUntilCanceled(ctx, server, listener, time.Second) }()
	response := make(chan string, 1)
	go func() {
		res, e := http.Get("http://" + listener.Addr().String())
		if e != nil {
			response <- e.Error()
			return
		}
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		if e != nil {
			response <- e.Error()
			return
		}
		response <- string(b)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		close(release)
		t.Fatalf("returned before handler completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case got := <-response:
		if got != "complete" {
			t.Fatalf("response truncated: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("response timeout")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

func TestShutdownDeadlineClosesActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started, closed := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(closed) })}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveUntilCanceled(ctx, server, listener, 30*time.Millisecond) }()
	go func() {
		res, e := http.Get("http://" + listener.Addr().String())
		if e == nil {
			res.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unbounded shutdown")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("request not force-closed")
	}
}
