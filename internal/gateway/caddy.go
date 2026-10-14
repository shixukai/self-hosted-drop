package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type M = map[string]any
type Route struct {
	ID, Host, Root, Entry, Mode string
	Status                      int
	Preview                     bool
}
type Options struct {
	InternalAddress, InternalKey, AdminListen string
	Routes, Probes                            []Route
}

func response(status int) M {
	return M{"handler": "static_response", "status_code": status, "body": http.StatusText(status)}
}
func route(match M, handlers ...M) M {
	r := M{"handle": handlers}
	if match != nil {
		r["match"] = []M{match}
	}
	return r
}
func fileHandlers(r Route) []M {
	handlers := []M{route(M{"not": []M{{"method": []string{"GET", "HEAD"}}}}, response(405)), route(M{"path": []string{"/_drop/*", "*/.*", "*.drop-etag"}}, response(404)), route(nil, M{"handler": "vars", "root": r.Root}), route(M{"path": []string{"/"}}, M{"handler": "rewrite", "uri": "/" + r.Entry})}
	if r.Mode == "spa" {
		handlers = append(handlers, route(M{"not": []M{{"file": M{"try_files": []string{"{http.request.uri.path}", "{http.request.uri.path}/index.html"}}}}, "header": M{"Accept": []string{"*text/html*"}}, "path_regexp": M{"pattern": `^[^.]*$`}}, M{"handler": "rewrite", "uri": "/" + r.Entry}))
	}
	handlers = append(handlers, route(nil, M{"handler": "file_server", "root": r.Root, "canonical_uris": false, "index_names": []string{"index.html"}, "hide": []string{"*.drop-etag"}, "etag_file_extensions": []string{".drop-etag"}}))
	return []M{{"handler": "subroute", "routes": handlers}}
}
func proxy(o Options, uri string, auth bool) M {
	p := M{"handler": "reverse_proxy", "upstreams": []M{{"dial": o.InternalAddress}}, "headers": M{"request": M{"set": M{"X-Drop-Internal": []string{o.InternalKey}, "X-Drop-Host": []string{"{http.request.host}"}, "X-Drop-URI": []string{"{http.request.uri}"}}, "delete": []string{"Authorization"}}}, "transport": M{"protocol": "http", "dial_timeout": 3000000000, "response_header_timeout": 5000000000}}
	if auth {
		p["rewrite"] = M{"method": "GET", "uri": uri}
		p["handle_response"] = []M{{"match": M{"status_code": []int{2}}, "routes": []M{route(nil, M{"handler": "headers", "request": M{"delete": []string{"X-Drop-Internal", "X-Drop-Host", "X-Drop-URI"}}})}}}
	}
	return p
}
func Build(o Options) M {
	build := func(routes []Route, probe bool) []M {
		out := []M{}
		for _, r := range routes {
			headers := M{"Content-Security-Policy": []string{"frame-ancestors 'self'"}, "Cache-Control": []string{"no-cache"}, "X-Content-Type-Options": []string{"nosniff"}, "X-Robots-Tag": []string{"noindex, nofollow"}}
			if r.Preview {
				headers["Cache-Control"] = []string{"private, no-store"}
				headers["Referrer-Policy"] = []string{"no-referrer"}
			}
			h := []M{{"handler": "headers", "response": M{"set": headers, "delete": []string{"Last-Modified"}, "deferred": true}}, {"handler": "headers", "request": M{"delete": []string{"If-Modified-Since", "If-Unmodified-Since"}}}}
			if r.Status != 0 {
				h = append(h, response(r.Status))
			} else {
				if r.Preview && !probe {
					h = append(h, M{"handler": "subroute", "routes": []M{M{"match": []M{{"path": []string{"/_drop/preview", "/_drop/exchange"}}}, "handle": []M{proxy(o, "", false)}, "terminal": true}, route(nil, proxy(o, "/_internal/preview-auth", true))}})
				}
				h = append(h, fileHandlers(r)...)
			}
			out = append(out, M{"@id": r.ID, "match": []M{{"host": []string{strings.Split(r.Host, ":")[0]}}}, "handle": h, "terminal": true})
		}
		out = append(out, route(nil, response(404)))
		return out
	}
	return M{"admin": M{"listen": o.AdminListen}, "apps": M{"http": M{"servers": M{"public": M{"listen": []string{":8080"}, "automatic_https": M{"disable": true}, "routes": build(o.Routes, false)}, "probe": M{"listen": []string{":8081"}, "automatic_https": M{"disable": true}, "routes": build(o.Probes, true)}}}}}
}
func Read(ctx context.Context, client *http.Client, admin string) (M, string, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", admin+"/config/", nil)
	if e != nil {
		return nil, "", e
	}
	resp, e := client.Do(req)
	if e != nil {
		return nil, "", e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("gateway returned %d", resp.StatusCode)
	}
	var cfg M
	e = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&cfg)
	return cfg, resp.Header.Get("ETag"), e
}
func Apply(ctx context.Context, client *http.Client, admin, etag string, cfg M) error {
	b, e := json.Marshal(cfg)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", admin+"/config/", bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("gateway rejected config (%d): %s", resp.StatusCode, string(b))
	}
	return nil
}
func HasRoute(cfg M, id string) bool {
	b, _ := json.Marshal(cfg)
	return bytes.Contains(b, []byte(`"@id":"`+id+`"`))
}

// WithProbe changes only the private probe server, preserving all public routes.
func WithProbe(current M, o Options) M {
	b, _ := json.Marshal(current)
	var cfg M
	json.Unmarshal(b, &cfg)
	desired := Build(o)
	apps, ok := cfg["apps"].(map[string]any)
	if !ok {
		apps = M{}
		cfg["apps"] = apps
	}
	h, ok := apps["http"].(map[string]any)
	if !ok {
		h = M{}
		apps["http"] = h
	}
	servers, ok := h["servers"].(map[string]any)
	if !ok {
		servers = M{}
		h["servers"] = servers
	}
	servers["probe"] = desired["apps"].(M)["http"].(M)["servers"].(M)["probe"]
	return cfg
}
