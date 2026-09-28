package fixture

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// RangeTransfer records artifact bytes written for one ranged GET.
type RangeTransfer struct {
	Header  string
	Sent    int64
	Started time.Time
}

// HeldRangeOrigin stalls the first range after HoldAt bytes. It lets client
// fixtures stop and reopen the service after a durable partial-range checkpoint.
type HeldRangeOrigin struct {
	Body     []byte
	HoldAt   int64
	Release  chan struct{}
	held     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	stalled  bool
	requests []*RangeTransfer
}

func NewHeldRangeOrigin(body []byte, holdAt int64) *HeldRangeOrigin {
	return &HeldRangeOrigin{Body: body, HoldAt: holdAt, Release: make(chan struct{}), held: make(chan struct{})}
}

func (o *HeldRangeOrigin) Held() <-chan struct{} { return o.held }

func (o *HeldRangeOrigin) Unblock() { o.once.Do(func() { close(o.Release) }) }

func (o *HeldRangeOrigin) Transfers() []RangeTransfer {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := make([]RangeTransfer, 0, len(o.requests))
	for _, request := range o.requests {
		result = append(result, *request)
	}
	return result
}

// CheckResume requires one post-restart request for the missing tail and no
// repeated source bytes. The test calls it after its client reads the result.
func (o *HeldRangeOrigin) CheckResume(t testing.TB, restarted time.Time, bound time.Duration) {
	t.Helper()
	transfers := o.Transfers()
	var total int64
	var resumed []RangeTransfer
	for _, transfer := range transfers {
		total += transfer.Sent
		if !transfer.Started.Before(restarted) {
			resumed = append(resumed, transfer)
		}
	}
	if total != int64(len(o.Body)) {
		t.Fatalf("origin sent %d bytes for %d-byte artifact: %+v", total, len(o.Body), transfers)
	}
	want := fmt.Sprintf("bytes=%d-%d", o.HoldAt, len(o.Body)/2-1)
	if len(resumed) != 1 || resumed[0].Header != want {
		t.Fatalf("post-restart requests %+v, want one %s; all %+v", resumed, want, transfers)
	}
	if waited := resumed[0].Started.Sub(restarted); waited > bound {
		t.Fatalf("resumed after %s, bound %s", waited, bound)
	}
}

func (o *HeldRangeOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := r.Header.Get("Range")
	spec, ok := strings.CutPrefix(header, "bytes=")
	first, last, dash := strings.Cut(spec, "-")
	start, firstErr := strconv.ParseInt(first, 10, 64)
	end, lastErr := strconv.ParseInt(last, 10, 64)
	if !ok || !dash || firstErr != nil || lastErr != nil || start < 0 || start > end || end >= int64(len(o.Body)) {
		http.Error(w, "ranged requests only", http.StatusBadRequest)
		return
	}
	o.mu.Lock()
	transfer := &RangeTransfer{Header: header, Started: time.Now()}
	hold := !o.stalled && start < o.HoldAt && o.HoldAt <= end
	if hold {
		o.stalled = true
	}
	if header != "bytes=0-0" {
		o.requests = append(o.requests, transfer)
	}
	o.mu.Unlock()
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", `"held-range-v1"`)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(o.Body)))
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(http.StatusPartialContent)
	stop := end + 1
	if hold {
		stop = o.HoldAt
	}
	for at := start; at < stop; {
		n, err := w.Write(o.Body[at:min(stop, at+64<<10)])
		o.mu.Lock()
		transfer.Sent += int64(n)
		o.mu.Unlock()
		if err != nil {
			return
		}
		at += int64(n)
	}
	if hold {
		w.(http.Flusher).Flush()
		close(o.held)
		select {
		case <-r.Context().Done():
		case <-o.Release:
		}
	}
}
