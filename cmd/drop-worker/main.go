package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shixukai/self-hosted-drop/internal/archive"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type request struct {
	DeploymentID string           `json:"deploymentId"`
	AttemptID    string           `json:"attemptId"`
	Filename     string           `json:"filename"`
	Metadata     archive.Metadata `json:"metadata"`
	Limits       archive.Limits   `json:"limits"`
}
type response struct {
	Result  archive.Result `json:"result"`
	Code    string         `json:"code,omitempty"`
	Message string         `json:"message,omitempty"`
}

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "process" {
		process()
		return
	}
	data := os.Getenv("DROP_DATA_DIR")
	if data == "" {
		data = ".data"
	}
	key := os.Getenv("DROP_WORKER_KEY")
	if key == "" && os.Getenv("DROP_DEVELOPMENT_MODE") == "true" {
		key = os.Getenv("DROP_INTERNAL_KEY")
	}
	file := os.Getenv("DROP_WORKER_KEY_FILE")
	if file == "" && os.Getenv("DROP_DEVELOPMENT_MODE") == "true" {
		file = os.Getenv("DROP_INTERNAL_KEY_FILE")
	}
	if file != "" {
		b, e := os.ReadFile(file)
		if e != nil {
			panic(e)
		}
		key = strings.TrimSpace(string(b))
	}
	if len(key) < 32 {
		panic("worker internal key required")
	}
	listen := os.Getenv("DROP_WORKER_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:18182"
	}
	gate := make(chan struct{}, 2)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/process" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+key)) != 1 {
			w.WriteHeader(403)
			return
		}
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		default:
			w.WriteHeader(429)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var req request
		if json.NewDecoder(r.Body).Decode(&req) != nil || !validID.MatchString(req.DeploymentID) || !validID.MatchString(req.AttemptID) {
			w.WriteHeader(400)
			return
		}
		base := filepath.Join(data, "uploads", req.DeploymentID)
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()
		exe, e := os.Executable()
		if e != nil {
			w.WriteHeader(500)
			return
		}
		cmd := exec.CommandContext(ctx, exe, "process", base)
		cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
		b, _ := json.Marshal(req)
		cmd.Stdin = strings.NewReader(string(b))
		out, e := cmd.Output()
		if e != nil {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	})
	server := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 125 * time.Second, MaxHeaderBytes: 16 << 10}
	if e := server.ListenAndServe(); e != nil {
		panic(e)
	}
}
func process() {
	var req request
	if len(os.Args) != 3 || json.NewDecoder(io.LimitReader(os.Stdin, 64<<10)).Decode(&req) != nil || !validID.MatchString(req.AttemptID) {
		os.Exit(2)
	}
	base := os.Args[2]
	attempt := filepath.Join(base, "attempts", req.AttemptID)
	var out response
	err := os.MkdirAll(attempt, 0750)
	tree := filepath.Join(attempt, "tree")
	ctx, cancel := context.WithTimeout(context.Background(), 115*time.Second)
	defer cancel()
	if err == nil {
		err = archive.Extract(ctx, filepath.Join(base, "payload"), req.Filename, tree, req.Limits)
	}
	if err == nil {
		out.Result, err = archive.Inspect(ctx, tree, req.Metadata, max(req.Metadata.InspectionRevision, 1))
	}
	if err != nil {
		var ae *archive.Error
		if errors.As(err, &ae) {
			out.Code = ae.Code
			out.Message = ae.Message
		} else {
			out.Code = "PROCESSING_FAILED"
			out.Message = "文件处理失败，请检查归档或重试"
		}
		fmt.Fprintln(os.Stderr, "artifact processing failed")
	}
	json.NewEncoder(os.Stdout).Encode(out)
}
