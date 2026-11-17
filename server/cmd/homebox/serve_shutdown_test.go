package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"homebox/internal/store"
)

func TestServeDrainsActualRequestBeforeReleasingDatabase(t *testing.T) {
	database, web := serverFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output, result, done := make(startupOutput, 1), make(chan error, 1), make(chan struct{})
	go func() {
		defer close(done)
		result <- serve(ctx, []string{"--listen", "127.0.0.1:0", "--database", database, "--web-dir", web}, output, io.Discard)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server remained active during test cleanup")
		}
	}()
	var ready map[string]string
	select {
	case data := <-output:
		if err := json.Unmarshal(data, &ready); err != nil {
			t.Fatal(err)
		}
	case err := <-result:
		t.Fatalf("server did not start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not report its listener")
	}
	conn, err := net.DialTimeout("tcp", ready["listening"], 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "POST /api/v1/auth/login HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nContent-Type: application/json\r\nContent-Length: 2\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n", ready["listening"], ready["public_origin"])
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	interim, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil || interim.StatusCode != http.StatusContinue {
		t.Fatal("the server did not admit and start reading the request", interim, err)
	}
	_ = interim.Body.Close()
	cancel()
	select {
	case err := <-result:
		t.Fatal("server returned while the admitted request body was incomplete", err)
	case <-time.After(100 * time.Millisecond):
	}
	other, err := store.Open(t.Context(), store.Config{Path: database})
	if other != nil {
		_ = other.Close()
	}
	if !errors.Is(err, store.ErrLocked) {
		t.Fatal("server released the database before the request completed", err)
	}
	if _, err := io.WriteString(conn, "{}"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "VALIDATION_ERROR") {
		t.Fatal("admitted request did not finish through the real handler", response.StatusCode, readErr)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after its request completed")
	}
	if _, err := invoke(t, "db", "check", "--database", database); err != nil {
		t.Fatal("server did not release the database after draining", err)
	}
}

func TestDrainingHandlerWaitsForCanceledRequestAndRejectsNewAdmissions(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	handler := newDrainingHandler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
		<-release
	}))
	server := httptest.NewServer(handler)
	defer server.Close()
	defer unblock()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, err := server.Client().Get(server.URL)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not enter")
	}
	handler.stop()
	server.CloseClientConnections()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the connection did not cancel the request")
	}
	waited := make(chan struct{})
	go func() { handler.wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("drain returned while a canceled handler was still running")
	case <-time.After(100 * time.Millisecond):
	}
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "http://example.test/", nil))
	if rejected.Code != http.StatusServiceUnavailable || rejected.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("shutdown admitted or cached a new request", rejected.Code)
	}
	unblock()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not finish after the admitted handler returned")
	}
	select {
	case <-requestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("closed client request did not finish")
	}
}

func TestDrainingHandlerSerializesConcurrentAdmissionAndWait(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	entered := make(chan struct{}, 64)
	handler := newDrainingHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	results := make(chan int, 64)
	for range 64 {
		go func() {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "http://example.test/", nil))
			results <- response.Code
		}()
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no request entered the drain")
	}
	handler.stop()
	waited := make(chan struct{})
	go func() { handler.wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("drain did not wait for admitted requests")
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent requests did not drain")
	}
	for range 64 {
		select {
		case code := <-results:
			if code != http.StatusOK && code != http.StatusServiceUnavailable {
				t.Fatal("request escaped both admission outcomes", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent request did not return")
		}
	}
}
