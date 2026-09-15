package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	base := flag.String("base-path", "/", "URL prefix, e.g. /pipeek/ (nginx must preserve it)")
	interval := flag.Duration("interval", 3*time.Second, "shared sampling and browser refresh interval (1s–1m)")
	history := flag.Duration("history", 10*time.Minute, "in-memory history (up to 24h; at most 3600 intervals)")
	disks := flag.String("disks", "/", "comma-separated filesystem paths")
	mounts := flag.String("mounts", "", "comma-separated required mount points; added to monitored disks")
	interfaces := flag.String("interfaces", "", "comma-separated interfaces; default all except loopback")
	flag.Parse()
	if *interval < time.Second || *interval > time.Minute || *history < *interval || *history > 24*time.Hour || *history / *interval > 3600 {
		log.Fatal("invalid interval/history: interval 1s–1m, history >= interval and <= 24h, maximum 3600 intervals")
	}
	prefix, err := normalizeBase(*base)
	if err != nil {
		log.Fatal(err)
	}
	diskPaths := splitList(*disks)
	requiredMounts := map[string]bool{}
	for _, path := range splitList(*mounts) {
		requiredMounts[path] = true
		found := false
		for _, diskPath := range diskPaths {
			if diskPath == path {
				found = true
			}
		}
		if !found {
			diskPaths = append(diskPaths, path)
		}
	}
	if len(diskPaths) == 0 {
		log.Fatal("at least one disk path is required")
	}
	selected := map[string]bool{}
	for _, name := range splitList(*interfaces) {
		selected[name] = true
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "Linux host"
	}
	app, err := newApp(prefix, host, *interval, *history)
	if err != nil {
		log.Fatal(err)
	}
	c := &collector{proc: "/proc", sys: "/sys", disks: diskPaths, interfaces: selected, requiredMounts: requiredMounts}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.update(c.collect(time.Now()))
	go func() {
		ticker := time.NewTicker(*interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				app.update(c.collect(time.Now()))
			case <-ctx.Done():
				return
			}
		}
	}()
	server := &http.Server{Addr: *listen, Handler: app.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("PiPeek listening on http://%s%s · sample %s · history %s", *listen, prefix, *interval, *history)
	if err := serveUntilCanceled(ctx, server, listener, 5*time.Second); err != nil {
		log.Fatal(err)
	}
}

// Wait for active handlers to finish before main exits; force-close on timeout.
func serveUntilCanceled(ctx context.Context, server *http.Server, listener net.Listener, grace time.Duration) error {
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case err := <-served:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		err := server.Shutdown(shutdown)
		if err != nil {
			_ = server.Close()
		}
		<-served
		return err
	}
}

func splitList(s string) []string {
	var result []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			result = append(result, v)
		}
	}
	return result
}
func normalizeBase(s string) (string, error) {
	if s == "/" {
		return s, nil
	}
	s = strings.TrimSuffix(s, "/")
	if !strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("base-path must start with /")
	}
	for _, segment := range strings.Split(s[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid base-path")
		}
		for _, r := range segment {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return "", fmt.Errorf("base-path supports letters, digits, /, - and _")
			}
		}
	}
	return s + "/", nil
}
