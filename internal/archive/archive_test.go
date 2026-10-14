package archive

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testLimits = Limits{MaxExpanded: 1 << 20, MaxFile: 1 << 18, MaxFiles: 100, MaxDepth: 20}

type entry struct {
	name, content string
	mode          os.FileMode
}

func fixture(t *testing.T, entries []entry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "input.zip")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	for _, v := range entries {
		h := &zip.FileHeader{Name: v.name, Method: zip.Deflate}
		if v.mode != 0 {
			h.SetMode(v.mode)
		}
		w, e := z.CreateHeader(h)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(v.content)); e != nil {
			t.Fatal(e)
		}
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestRejectUnsafeArchives(t *testing.T) {
	cases := []struct {
		name    string
		entries []entry
		code    string
	}{
		{"parent", []entry{{"../escape.html", "x", 0}}, "UNSAFE_PATH"},
		{"absolute", []entry{{"/escape.html", "x", 0}}, "UNSAFE_PATH"},
		{"windows", []entry{{`dir\escape.html`, "x", 0}}, "UNSAFE_PATH"},
		{"control", []entry{{"bad\n.html", "x", 0}}, "UNSAFE_PATH"},
		{"secret", []entry{{".env.production", "x", 0}}, "SECRET_FILE_DETECTED"},
		{"key", []entry{{"private.key", "x", 0}}, "SECRET_FILE_DETECTED"},
		{"server", []entry{{"index.php", "x", 0}}, "FILE_TYPE_NOT_ALLOWED"},
		{"symlink", []entry{{"index.html", "/etc/passwd", os.ModeSymlink | 0777}}, "INVALID_ARCHIVE"},
		{"fifo", []entry{{"index.html", "", os.ModeNamedPipe | 0600}}, "INVALID_ARCHIVE"},
		{"case", []entry{{"index.html", "x", 0}, {"INDEX.html", "y", 0}}, "DUPLICATE_PATH"},
		{"unicode", []entry{{"caf\u00e9.html", "x", 0}, {"cafe\u0301.html", "x", 0}}, "DUPLICATE_PATH"},
		{"directory-case", []entry{{"Dir/a.html", "x", 0}, {"dir/b.html", "x", 0}}, "DUPLICATE_PATH"},
		{"file-parent", []entry{{"index.html", "x", 0}, {"index.html/child.html", "x", 0}}, "DUPLICATE_PATH"},
		{"reserved", []entry{{"_drop/index.html", "x", 0}}, "SECRET_FILE_DETECTED"},
		{"sidecar", []entry{{"index.html.drop-etag", "x", 0}}, "UNSAFE_PATH"},
		{"oversize", []entry{{"index.html", strings.Repeat("x", 1<<19), 0}}, "EXPANDED_TOO_LARGE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := fixture(t, tc.entries)
			dest := filepath.Join(t.TempDir(), "tree")
			e := Extract(context.Background(), p, "site.zip", dest, testLimits)
			var ae *Error
			if !errors.As(e, &ae) || ae.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, e)
			}
		})
	}
}
func TestInspectSelectionAndReferences(t *testing.T) {
	p := fixture(t, []entry{{"dist/index.html", `<link href="/style.css"><img src="missing.png"><script src="http://example.invalid/a.js"></script>`, 0}, {"dist/style.css", "body{color:red}", 0}, {"docs/guide.html", "guide", 0}})
	tree := filepath.Join(t.TempDir(), "tree")
	if e := Extract(context.Background(), p, "site.zip", tree, testLimits); e != nil {
		t.Fatal(e)
	}
	r, e := Inspect(context.Background(), tree, Metadata{}, 1)
	if e != nil || r.Status != "AWAITING_INPUT" || len(r.Candidates) != 2 {
		t.Fatalf("selection: %+v %v", r, e)
	}
	r, e = Inspect(context.Background(), tree, Metadata{RootDirectory: "dist", EntryPath: "index.html", RoutingMode: "static"}, 2)
	if e != nil {
		t.Fatal(e)
	}
	if r.Status != "READY" || len(r.Files) != 2 || len(r.Warnings) != 2 || r.Revision != 2 {
		t.Fatalf("inspection: %+v", r)
	}
	if e = Verify(filepath.Join(tree, "dist"), r); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(tree, "dist", "extra.js"), []byte("extra"), 0600); e != nil {
		t.Fatal(e)
	}
	if Verify(filepath.Join(tree, "dist"), r) == nil {
		t.Fatal("unmanifested file accepted")
	}
}
func TestManifestDetectsSameSizeMutation(t *testing.T) {
	tree := t.TempDir()
	p := filepath.Join(tree, "index.html")
	os.WriteFile(p, []byte("version one"), 0600)
	r, e := Inspect(context.Background(), tree, Metadata{}, 1)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(p, []byte("version two"), 0600)
	if Verify(tree, r) == nil {
		t.Fatal("same-size mutation was not detected")
	}
}
func TestRejectTruncatedZIPAndCanceledWork(t *testing.T) {
	p := fixture(t, []entry{{"index.html", "hello", 0}})
	b, _ := os.ReadFile(p)
	os.WriteFile(p, b[:len(b)/2], 0600)
	if Extract(context.Background(), p, "site.zip", filepath.Join(t.TempDir(), "tree"), testLimits) == nil {
		t.Fatal("truncated ZIP accepted")
	}
	p = fixture(t, []entry{{"index.html", "hello", 0}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(Extract(ctx, p, "site.zip", filepath.Join(t.TempDir(), "tree"), testLimits), context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}
func TestRootIndexPreferred(t *testing.T) {
	tree := t.TempDir()
	os.WriteFile(filepath.Join(tree, "index.html"), []byte("home"), 0600)
	os.WriteFile(filepath.Join(tree, "about.html"), []byte("about"), 0600)
	r, e := Inspect(context.Background(), tree, Metadata{}, 1)
	if e != nil || r.EntryPath != "index.html" || r.Status != "READY" {
		t.Fatalf("root index %+v %v", r, e)
	}
}
