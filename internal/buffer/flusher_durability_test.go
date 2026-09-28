package buffer

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"
)

// cmd/cctraced sizes each ring at 10000 and deploy/docker-compose.yml sets no
// stop_grace_period, so Docker sends SIGKILL 10s after SIGTERM. DrainOrSpill
// must therefore move a full ring to the WAL in well under 10s — it runs
// exactly when the database is already down, which is when the WAL is the only
// thing standing between a restart and lost telemetry.
//
// Measured per record and scaled down, so the test costs milliseconds either
// way: the per-record budget is the real requirement and it is preserved
// exactly. 10s / 10000 records = 1ms per record. 200 samples is enough to
// separate a buffered write (single-digit µs) from a per-record fsync
// (milliseconds) by orders of magnitude.
const (
	productionRingSize   = 10000
	shutdownGracePeriod  = 10 * time.Second
	perRecordBudget      = shutdownGracePeriod / productionRingSize
	drainTestRecordCount = 200
)

func TestDrainOrSpill_PerRecordCostFitsShutdownGracePeriod(t *testing.T) {
	ring := NewRing(drainTestRecordCount)
	for i := 0; i < drainTestRecordCount; i++ {
		ring.Push(make([]byte, 512)) // a typical OTLP record
	}
	spiller := NewDiskSpiller(t.TempDir(), 512<<20)
	t.Cleanup(func() { _ = spiller.Close() })
	defer spiller.Close()

	// The database is down — every flush fails, so every record goes to the WAL.
	f := NewFlusher(ring, func([]byte) error { return errors.New("database unavailable") }, "logs").
		WithSpiller(spiller)

	start := time.Now()
	f.DrainOrSpill()
	elapsed := time.Since(start)

	if ring.Len() != 0 {
		t.Fatalf("%d records left in the ring after DrainOrSpill", ring.Len())
	}

	perRecord := elapsed / drainTestRecordCount
	t.Logf("drained %d records in %s (%s/record, budget %s/record)",
		drainTestRecordCount, elapsed.Round(time.Millisecond),
		perRecord.Round(time.Microsecond), perRecordBudget)

	if perRecord > perRecordBudget {
		projected := perRecord * productionRingSize
		t.Fatalf("%s per record; a production ring of %d would need %s to reach the WAL "+
			"but Docker SIGKILLs %s after SIGTERM, stranding ~%.0f%% of the buffer",
			perRecord.Round(time.Microsecond), productionRingSize, projected.Round(time.Second),
			shutdownGracePeriod, 100*(1-shutdownGracePeriod.Seconds()/projected.Seconds()))
	}
}

// Counterweight: speed must not come from dropping records. Everything the ring
// held has to be readable back from the WAL, in order.
//
// Payloads are newline-free on purpose. The WAL frames records by '\n' and does
// not escape them, so a record containing a raw newline would be split in two on
// recovery. Production records are json.Marshal output, which escapes newlines
// inside strings, so the format holds — but a future caller spilling raw bytes
// would silently corrupt the WAL.
func TestDrainOrSpill_PersistsEveryRecord(t *testing.T) {
	const records = 64

	want := make([][]byte, records)
	ring := NewRing(records)
	for i := 0; i < records; i++ {
		want[i] = []byte(fmt.Sprintf("record-%02d", i))
		ring.Push(want[i])
	}
	spiller := NewDiskSpiller(t.TempDir(), 512<<20)
	t.Cleanup(func() { _ = spiller.Close() })
	defer spiller.Close()

	f := NewFlusher(ring, func([]byte) error { return errors.New("database unavailable") }, "logs").
		WithSpiller(spiller)
	f.DrainOrSpill()

	seen := 0
	for data := range spiller.Recover() {
		if seen >= records {
			t.Fatalf("WAL yielded more records than were spilled (%d)", records)
		}
		if !bytes.Equal(data, want[seen]) {
			t.Fatalf("record %d = %q, want %q — WAL lost or reordered records", seen, data, want[seen])
		}
		seen++
	}
	if seen != records {
		t.Fatalf("recovered %d of %d records from the WAL", seen, records)
	}
}
