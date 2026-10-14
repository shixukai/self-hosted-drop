// Package archive validates static artifacts without executing uploaded content.
package archive

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type Limits struct {
	MaxExpanded, MaxFile int64
	MaxFiles, MaxDepth   int
}
type Metadata struct {
	RootDirectory      string `json:"rootDirectory"`
	EntryPath          string `json:"entryPath"`
	RoutingMode        string `json:"routingMode"`
	Message            string `json:"message"`
	InteractionMode    string `json:"interactionMode"`
	PublishAfterReady  bool   `json:"publishAfterReady"`
	WarningsPolicy     string `json:"warningsPolicy"`
	ExpectedGeneration *int64 `json:"expectedGeneration,omitempty"`
	InspectionRevision int    `json:"inspectionRevision,omitempty"`
}
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Candidate struct {
	RootDirectory string `json:"rootDirectory"`
	EntryPath     string `json:"entryPath"`
}
type Warning struct {
	Code      string `json:"code"`
	File      string `json:"file"`
	Reference string `json:"reference"`
	Message   string `json:"message"`
}
type Result struct {
	Status        string      `json:"status"`
	Files         []File      `json:"files"`
	Candidates    []Candidate `json:"candidates"`
	Warnings      []Warning   `json:"warnings"`
	RootDirectory string      `json:"rootDirectory"`
	EntryPath     string      `json:"entryPath"`
	RoutingMode   string      `json:"routingMode"`
	ExpandedBytes int64       `json:"expandedBytes"`
	ArtifactHash  string      `json:"artifactHash"`
	ReportHash    string      `json:"inspectionReportHash"`
	Revision      int         `json:"revision"`
}
type Error struct{ Code, Message string }

func (e *Error) Error() string       { return e.Code + ": " + e.Message }
func invalid(code, msg string) error { return &Error{code, msg} }

var allowed = map[string]bool{}

func init() {
	for _, v := range strings.Fields(".html .htm .css .js .mjs .json .txt .xml .svg .png .jpg .jpeg .gif .webp .avif .ico .bmp .woff .woff2 .ttf .otf .wasm .pdf .mp3 .mp4 .webm .ogg .wav .m4a .vtt .webmanifest .map") {
		allowed[v] = true
	}
}
func ValidatePath(name string, maxDepth int) error {
	if !utf8.ValidString(name) || len(name) == 0 || len([]byte(name)) > 512 || strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") {
		return invalid("UNSAFE_PATH", "不安全的文件路径")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return invalid("UNSAFE_PATH", "文件名包含控制字符")
		}
	}
	parts := strings.Split(name, "/")
	if len(parts) > maxDepth {
		return invalid("UNSAFE_PATH", "目录层级过深")
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.TrimSpace(p) != p || strings.HasSuffix(p, ".") {
			return invalid("UNSAFE_PATH", "不安全的路径段")
		}
		low := strings.ToLower(p)
		if strings.HasPrefix(low, ".env") || low == ".git" || low == ".svn" || low == ".hg" || low == "_drop" || strings.HasPrefix(low, "id_rsa") || strings.HasPrefix(low, "id_ed25519") || strings.HasSuffix(low, ".pem") || strings.HasSuffix(low, ".key") || low == ".npmrc" || low == ".netrc" || low == ".ssh" || low == "dockerfile" || low == "caddyfile" || low == "nginx.conf" || low == "web.config" {
			return invalid("SECRET_FILE_DETECTED", "发现敏感或服务端配置文件")
		}
		if strings.HasSuffix(low, ".drop-etag") {
			return invalid("UNSAFE_PATH", "平台保留路径")
		}
	}
	return nil
}
func ignored(n string) bool { return path.Base(n) == ".DS_Store" || strings.HasPrefix(n, "__MACOSX/") }
func Extract(ctx context.Context, payload, filename, dest string, limits Limits) error {
	if err := os.Mkdir(dest, 0750); err != nil {
		return err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	var total int64
	count := 0
	seen := map[string]bool{}
	prefixes := map[string]bool{}
	fold := cases.Fold()
	write := func(name string, r io.Reader, mode fs.FileMode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ignored(name) {
			return nil
		}
		if err := ValidatePath(name, limits.MaxDepth); err != nil {
			return err
		}
		if !mode.IsRegular() {
			return invalid("INVALID_ARCHIVE", "归档包含链接或特殊文件")
		}
		if !allowed[strings.ToLower(path.Ext(name))] {
			return invalid("FILE_TYPE_NOT_ALLOWED", "文件类型不在允许清单内："+path.Ext(name))
		}
		key := fold.String(norm.NFC.String(name))
		if seen[key] {
			return invalid("DUPLICATE_PATH", "文件名存在大小写或 Unicode 冲突")
		}
		if prefixes[key] {
			return invalid("DUPLICATE_PATH", "文件与目录路径冲突")
		}
		parts := strings.Split(key, "/")
		for i := 1; i < len(parts); i++ {
			prefix := strings.Join(parts[:i], "/")
			if seen[prefix] {
				return invalid("DUPLICATE_PATH", "文件与目录路径冲突")
			}
			prefixes[prefix] = true
		}
		seen[key] = true
		count++
		if count > limits.MaxFiles {
			return invalid("TOO_MANY_FILES", "文件数量超过限制")
		}
		if err := root.MkdirAll(path.Dir(name), 0750); err != nil {
			return invalid("INVALID_ARCHIVE", "无法创建目录")
		}
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
		if err != nil {
			return invalid("INVALID_ARCHIVE", "文件路径冲突")
		}
		n, e := io.Copy(f, io.LimitReader(&contextReader{ctx, r}, min(limits.MaxFile, limits.MaxExpanded-total)+1))
		total += n
		if e == nil && (n > limits.MaxFile || total > limits.MaxExpanded) {
			e = invalid("EXPANDED_TOO_LARGE", "解压后文件大小超过限制")
		}
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".html" || ext == ".htm" {
		f, e := os.Open(payload)
		if e != nil {
			return e
		}
		defer f.Close()
		return write(filepath.Base(filename), f, 0640)
	}
	if ext != ".zip" {
		return invalid("INVALID_ARCHIVE", "仅支持 HTML 和 ZIP")
	}
	z, e := zip.OpenReader(payload)
	if e != nil {
		return invalid("INVALID_ARCHIVE", "ZIP 文件损坏或格式无效")
	}
	defer z.Close()
	if len(z.File) > limits.MaxFiles*2 {
		return invalid("TOO_MANY_FILES", "归档条目过多")
	}
	directories := map[string]string{}
	for _, f := range z.File {
		n := strings.TrimSuffix(f.Name, "/")
		if ignored(n) {
			continue
		}
		if e = ValidatePath(n, limits.MaxDepth); e != nil {
			return e
		}
		if f.Flags&1 != 0 || (f.Method != zip.Store && f.Method != zip.Deflate) {
			return invalid("INVALID_ARCHIVE", "不支持加密或此压缩方式")
		}
		if f.Mode()&os.ModeSymlink != 0 || (!f.Mode().IsRegular() && !f.FileInfo().IsDir()) {
			return invalid("INVALID_ARCHIVE", "不允许链接或特殊文件")
		}
		segments := strings.Split(n, "/")
		for i := 1; i < len(segments); i++ {
			d := strings.Join(segments[:i], "/")
			k := fold.String(norm.NFC.String(d))
			if old, ok := directories[k]; ok && old != d {
				return invalid("DUPLICATE_PATH", "目录名称冲突")
			}
			directories[k] = d
		}
		if f.FileInfo().IsDir() {
			k := fold.String(norm.NFC.String(n))
			if old, ok := directories[k]; ok && old != n {
				return invalid("DUPLICATE_PATH", "目录名称冲突")
			}
			directories[k] = n
			continue
		}
		if f.UncompressedSize64 > uint64(limits.MaxFile) {
			return invalid("EXPANDED_TOO_LARGE", "单文件大小超过限制")
		}
		r, e := f.Open()
		if e != nil {
			return invalid("INVALID_ARCHIVE", "ZIP 条目无法读取")
		}
		e = write(f.Name, r, f.Mode())
		ce := r.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	if count == 0 {
		return invalid("ENTRY_NOT_FOUND", "没有可发布文件")
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func Sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func FileHash(file string) (string, int64, error) {
	f, e := os.Open(file)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}
func Inspect(ctx context.Context, tree string, m Metadata, revision int) (Result, error) {
	out := Result{Files: []File{}, Candidates: []Candidate{}, Warnings: []Warning{}, RoutingMode: m.RoutingMode, Revision: revision}
	if out.RoutingMode == "" {
		out.RoutingMode = "static"
	}
	if out.RoutingMode != "static" && out.RoutingMode != "spa" {
		return out, invalid("INVALID_INPUT", "路由模式无效")
	}
	var all []string
	err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.Type()&os.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()) {
			return invalid("UNSAFE_PATH", "检测到符号链接")
		}
		if d.IsDir() {
			return nil
		}
		r, e := filepath.Rel(tree, p)
		if e != nil {
			return e
		}
		r = filepath.ToSlash(r)
		if e := ValidatePath(r, 20); e != nil {
			return e
		}
		if !allowed[strings.ToLower(path.Ext(r))] {
			return invalid("FILE_TYPE_NOT_ALLOWED", "文件类型不在允许清单内")
		}
		all = append(all, r)
		if ext := strings.ToLower(path.Ext(r)); ext == ".html" || ext == ".htm" {
			directory := path.Dir(r)
			if directory == "." {
				directory = ""
			}
			out.Candidates = append(out.Candidates, Candidate{RootDirectory: directory, EntryPath: path.Base(r)})
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	sort.Strings(all)
	root := m.RootDirectory
	entry := m.EntryPath
	if root == "." {
		root = ""
	}
	if root != "" {
		if err = ValidatePath(root, 20); err != nil {
			return out, err
		}
	}
	if entry != "" {
		if err = ValidatePath(entry, 20); err != nil {
			return out, err
		}
	}
	exists := func(p string) bool { i := sort.SearchStrings(all, p); return i < len(all) && all[i] == p }
	if entry == "" {
		if exists(path.Join(root, "index.html")) {
			entry = "index.html"
		} else if len(out.Candidates) == 1 {
			root = out.Candidates[0].RootDirectory
			entry = out.Candidates[0].EntryPath
		} else if len(out.Candidates) > 1 {
			out.Status = "AWAITING_INPUT"
			out.ReportHash = Sum(mustJSON(out))
			return out, nil
		} else {
			return out, invalid("ENTRY_NOT_FOUND", "没有 HTML 入口，请上传构建后的静态文件")
		}
	}
	if !exists(path.Join(root, entry)) || !(strings.HasSuffix(strings.ToLower(entry), ".html") || strings.HasSuffix(strings.ToLower(entry), ".htm")) {
		return out, invalid("ENTRY_NOT_FOUND", "指定 HTML 入口不存在")
	}
	out.RootDirectory = root
	out.EntryPath = entry
	for _, p := range all {
		if root != "" && !strings.HasPrefix(p, root+"/") {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		hash, size, e := FileHash(filepath.Join(tree, filepath.FromSlash(p)))
		if e != nil {
			return out, e
		}
		out.Files = append(out.Files, File{rel, size, hash})
		out.ExpandedBytes += size
	}
	present := map[string]bool{}
	for _, f := range out.Files {
		present[f.Path] = true
	}
	for _, f := range out.Files {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		ext := strings.ToLower(path.Ext(f.Path))
		if ext != ".html" && ext != ".htm" && ext != ".css" && ext != ".js" && ext != ".mjs" {
			continue
		}
		if f.Size > 8<<20 {
			out.Warnings = append(out.Warnings, Warning{"SCAN_LIMIT", f.Path, "", "此文件超过静态引用扫描上限"})
			continue
		}
		b, e := os.ReadFile(filepath.Join(tree, root, filepath.FromSlash(f.Path)))
		if e != nil {
			return out, e
		}
		if len(out.Warnings) < 1000 {
			warnings := scanReferences(f.Path, string(b), ext, present)
			out.Warnings = append(out.Warnings, warnings[:min(len(warnings), 1000-len(out.Warnings))]...)
		}
	}
	out.Status = "READY"
	out.ArtifactHash = Sum(mustJSON(out.Files))
	out.ReportHash = Sum(mustJSON(out))
	return out, nil
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

var cssRefs = regexp.MustCompile(`(?i)(?:url\(\s*|@import\s+)["']?([^\s"'\)]+)`)
var externalLiterals = regexp.MustCompile(`https?://[^\s"'<>` + "`" + `]+`)

func scanReferences(file, content, ext string, present map[string]bool) []Warning {
	type ref struct {
		value string
		local bool
	}
	var refs []ref
	base, _ := url.Parse("https://drop.invalid/" + file)
	if ext == ".html" || ext == ".htm" {
		t := html.NewTokenizer(strings.NewReader(content))
		for {
			tt := t.Next()
			if tt == html.ErrorToken {
				break
			}
			if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
				continue
			}
			tag := t.Token()
			for _, a := range tag.Attr {
				if tag.Data == "base" && a.Key == "href" {
					if u, e := base.Parse(a.Val); e == nil {
						base = u
					}
				}
				if a.Key == "src" || a.Key == "href" || a.Key == "poster" {
					refs = append(refs, ref{a.Val, tag.Data != "a" && tag.Data != "base"})
				}
				if a.Key == "srcset" {
					for _, part := range strings.Split(a.Val, ",") {
						if fields := strings.Fields(part); len(fields) > 0 {
							refs = append(refs, ref{fields[0], true})
						}
					}
				}
			}
		}
	}
	for _, m := range cssRefs.FindAllStringSubmatch(content, -1) {
		refs = append(refs, ref{m[1], true})
	}
	if ext == ".js" || ext == ".mjs" {
		for _, m := range externalLiterals.FindAllString(content, -1) {
			refs = append(refs, ref{m, false})
		}
	}
	warnings := []Warning{}
	seen := map[string]bool{}
	for _, r := range refs {
		if r.value == "" || strings.HasPrefix(r.value, "#") || strings.HasPrefix(r.value, "data:") || strings.HasPrefix(r.value, "mailto:") || strings.HasPrefix(r.value, "javascript:") {
			continue
		}
		u, e := base.Parse(r.value)
		if e != nil {
			continue
		}
		code, msg := "", ""
		if u.Host != "" && u.Host != "drop.invalid" {
			code = "EXTERNAL_RESOURCE"
			msg = "依赖外部地址，离线或跨域访问可能失败"
			if u.Scheme == "http" {
				code = "MIXED_CONTENT"
				msg = "HTTP 资源可能被 HTTPS 页面阻止"
			}
		} else if r.local {
			p, e := url.PathUnescape(u.EscapedPath())
			if e != nil {
				continue
			}
			p = strings.TrimPrefix(path.Clean(p), "/")
			if !present[p] && !present[path.Join(p, "index.html")] {
				code = "MISSING_RESOURCE"
				msg = "本地资源未找到"
			}
		}
		if code != "" && !seen[code+r.value] && len(warnings) < 500 {
			seen[code+r.value] = true
			warnings = append(warnings, Warning{code, file, r.value, msg})
		}
	}
	return warnings
}
func Verify(root string, result Result) error {
	expected := map[string]string{}
	for _, f := range result.Files {
		expected[f.Path] = f.SHA256
	}
	if e := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()) {
			return errors.New("non-regular artifact")
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if _, ok := expected[rel]; ok {
			return nil
		}
		if strings.HasSuffix(rel, ".drop-etag") {
			if h, ok := expected[strings.TrimSuffix(rel, ".drop-etag")]; ok {
				b, e := os.ReadFile(p)
				if e != nil {
					return e
				}
				if string(b) == "\""+h+"\"" {
					return nil
				}
			}
		}
		return fmt.Errorf("unexpected artifact file: %s", rel)
	}); e != nil {
		return e
	}
	for _, f := range result.Files {
		if e := ValidatePath(f.Path, 20); e != nil {
			return e
		}
		p := filepath.Join(root, filepath.FromSlash(f.Path))
		info, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("non-regular artifact")
		}
		h, n, e := FileHash(p)
		if e != nil {
			return e
		}
		if h != f.SHA256 || n != f.Size {
			return fmt.Errorf("artifact integrity mismatch: %s", f.Path)
		}
	}
	if Sum(mustJSON(result.Files)) != result.ArtifactHash {
		return errors.New("manifest hash mismatch")
	}
	return nil
}
func SyncTree(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		defer f.Close()
		return f.Sync()
	})
}
