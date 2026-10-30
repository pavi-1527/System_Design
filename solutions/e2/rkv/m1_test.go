package rkv

// Milestone 1: a single node with a write-ahead log.
//
//	go test -race -run TestM1 ./labs/e2/rkv/

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestM1_RefusesToListenOnANonLoopbackAddress(t *testing.T) {
	for _, args := range [][]string{
		{"node", "-id", "n1", "-addr", "0.0.0.0:0", "-data", t.TempDir()},
		{"coord", "-addr", "0.0.0.0:0", "-nodes", "n1=http://127.0.0.1:1"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var out, errb bytes.Buffer
		code := Run(ctx, args, &out, &errb)
		cancel()
		if code != 2 {
			t.Errorf("Run(%q) = %d, want 2: this store has no authentication, so it must never listen beyond loopback", args, code)
		}
		if strings.Contains(out.String(), "READY") {
			t.Errorf("Run(%q) printed READY on a non-loopback address", args)
		}
	}
}

func TestM1_PutGetDeleteOnASingleNode(t *testing.T) {
	n := tStartLeader(t, "n1")
	r1 := tMustPut(t, n, "acct-1", "100")
	r2 := tMustPut(t, n, "acct-1", "250")
	if r1.Index != 1 || r2.Index != 2 || r1.Epoch != 1 || r2.Epoch != 1 {
		t.Fatalf("two writes got (index, epoch) %v and %v; want (1,1) and (2,1): indexes are contiguous from 1 and the first epoch is 1", r1, r2)
	}
	code, body, hdr, err := tGet(n, "acct-1", "")
	if err != nil || code != http.StatusOK || body != "250" {
		t.Fatalf("GET acct-1 = %d %q %v; want 200 \"250\"", code, body, err)
	}
	if hdr.Get(HeaderIndex) != "2" || hdr.Get(HeaderRole) != RoleLeader {
		t.Errorf("GET headers %s=%q %s=%q; want 2 and leader", HeaderIndex, hdr.Get(HeaderIndex), HeaderRole, hdr.Get(HeaderRole))
	}
	code, body, _, err = tDo("DELETE", n.URL+"/kv/acct-1", "")
	if err != nil || code != http.StatusOK || !strings.Contains(body, `"index":3`) {
		t.Fatalf("DELETE = %d %q %v; want 200 with index 3 (a delete is a log entry too)", code, body, err)
	}
	if code, _, _, _ := tGet(n, "acct-1", ""); code != http.StatusNotFound {
		t.Fatalf("GET after DELETE = %d, want 404", code)
	}
	if code, _, _, _ := tGet(n, "never-written", ""); code != http.StatusNotFound {
		t.Fatalf("GET of a missing key = %d, want 404", code)
	}
	if code, _, _, _ := tPut(n, strings.Repeat("k", 300), "v"); code != http.StatusBadRequest {
		t.Errorf("PUT with a 300-byte key = %d, want 400", code)
	}
	st := tStatus(t, n)
	if st.Role != RoleLeader || st.LastIndex != 3 || st.Keys != 0 || st.Epoch != 1 || st.URL != n.URL {
		t.Fatalf("status = %+v; want leader, last_index 3, keys 0, epoch 1, url %s", st, n.URL)
	}
}

func TestM1_EveryAcknowledgedWriteSurvivesSIGKILL(t *testing.T) {
	n := tStartLeader(t, "n1")
	var mu sync.Mutex
	acked := map[string]string{}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				key, val := fmt.Sprintf("w%d-%d", w, i), strconv.Itoa(i*w)
				code, _, _, err := tPut(n, key, val)
				if err != nil {
					return // the process was killed mid-request
				}
				if code == http.StatusOK {
					mu.Lock()
					acked[key] = val
					mu.Unlock()
				}
			}
		}(w)
	}
	tEventually(t, 20*time.Second, func() (bool, string) {
		mu.Lock()
		defer mu.Unlock()
		return len(acked) >= 80, fmt.Sprintf("only %d writes acknowledged", len(acked))
	})
	n.Kill() // writers are still going: some requests die half-way
	close(stop)
	wg.Wait()

	n.Restart()
	mu.Lock()
	defer mu.Unlock()
	for k, v := range acked {
		code, body, _, err := tGet(n, k, "")
		if err != nil || code != http.StatusOK || body != v {
			t.Fatalf("acknowledged write %s=%s reads back as %d %q %v after SIGKILL; an ack must mean it is on disk (fsync before 200)", k, v, code, body, err)
		}
	}
	if st := tStatus(t, n); st.LastIndex < uint64(len(acked)) {
		t.Fatalf("last_index %d after restart, but %d writes were acknowledged", st.LastIndex, len(acked))
	}
}

func TestM1_TornWALTailIsCutAndTheNodeKeepsGoing(t *testing.T) {
	n := tStartLeader(t, "n1")
	for i := 1; i <= 5; i++ {
		tMustPut(t, n, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
	}
	n.Kill()
	// The crash happened in the middle of the 6th record: a header that
	// promises 60 bytes and only 3 of them.
	torn := []byte{0, 0, 0, 60, 0xDE, 0xAD, 0xBE}
	tAppendFile(t, filepath.Join(tDataDir(n), WALFileName), torn)

	n.Restart()
	st := tStatus(t, n)
	if st.LastIndex != 5 || st.RecoveredTruncatedBytes != int64(len(torn)) {
		t.Fatalf("after a torn tail: last_index %d, recovered_truncated_bytes %d; want 5 and %d (keep every complete record, cut the rest)",
			st.LastIndex, st.RecoveredTruncatedBytes, len(torn))
	}
	for i := 1; i <= 5; i++ {
		tExpectValue(t, n, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
	}
	if r := tMustPut(t, n, "k6", "v6"); r.Index != 6 {
		t.Fatalf("first write after recovery got index %d, want 6", r.Index)
	}
	n.Kill()
	n.Restart()
	if st := tStatus(t, n); st.LastIndex != 6 || st.RecoveredTruncatedBytes != 0 {
		t.Fatalf("second restart: last_index %d, truncated %d; want 6, 0 (recovery must leave a clean file behind)", st.LastIndex, st.RecoveredTruncatedBytes)
	}
}

func TestM1_SIGTERMShutsDownCleanlyAndRestartKeepsState(t *testing.T) {
	n := tStartLeader(t, "n1")
	tMustPut(t, n, "a", "1")
	if code := n.Stop(10 * time.Second); code != 0 {
		t.Fatalf("exit code %d after SIGTERM, want 0 (graceful shutdown)", code)
	}
	n.Restart()
	tExpectValue(t, n, "a", "1")
	if st := tStatus(t, n); st.Role != RoleLeader || st.Epoch != 1 {
		t.Fatalf("after restart: role %s epoch %d; want leader 1 from meta.json", st.Role, st.Epoch)
	}
}
