package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/itemnotes"
)

type pdfBackend struct {
	fakeBackend
	data string
}

func (b *pdfBackend) Attachment(context.Context, string, string) (io.ReadCloser, itemnotes.Attachment, error) {
	return io.NopCloser(strings.NewReader(b.data)), itemnotes.Attachment{Name: "report.pdf", Type: "application/pdf", Size: int64(len(b.data))}, nil
}

func TestPDFLinksUseBrowserMIMEAndSupportRanges(t *testing.T) {
	b := &pdfBackend{data: "%PDF-1.7\nhello\n%%EOF"}
	s := local(t, b)
	attachment := "/api/items/" + itemID + "/attachments/" + strings.Repeat("a", 64)
	document := "/api/items/" + itemID + "/document?path=report.pdf&claim_id=claim&claim_generation=1"
	for _, url := range []string{attachment, document} {
		w := request(s, "GET", url, "")
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline;") || !strings.HasPrefix(w.Body.String(), "%PDF-") {
			t.Fatalf("inline %s: %d %v %s", url, w.Code, w.Header(), w.Body.String())
		}
		separator := "?"
		if strings.Contains(url, "?") {
			separator = "&"
		}
		w = request(s, "GET", url+separator+"download=1", "")
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
			t.Fatalf("download: %d %v", w.Code, w.Header())
		}
		req := httptest.NewRequest("GET", "http://"+s.host+url, nil)
		req.Header.Set("Range", "bytes=0-4")
		w = httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != http.StatusPartialContent || w.Body.String() != "%PDF-" {
			t.Fatalf("range %s: %d %s", url, w.Code, w.Body.String())
		}
	}
	if w := request(s, "GET", document, ""); w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("mutable document cached", w.Header())
	}
	if w := request(s, "GET", "/api/items/"+missingItem+"/document?path=a.pdf&claim_generation=1", ""); w.Code != 404 {
		t.Fatal("document crossed item boundary", w.Code)
	}
	if w := request(s, "GET", "/api/items/"+itemID+"/document?path=a.pdf&claim_generation=bad", ""); w.Code != 400 {
		t.Fatal("bad generation accepted", w.Code)
	}
	b.data = "<script>not really a PDF</script>"
	w := request(s, "GET", attachment, "")
	if w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("spoofed PDF rendered inline: %v", w.Header())
	}
}
