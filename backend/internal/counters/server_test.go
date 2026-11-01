package counters

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string) {
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHostSampler(t *testing.T) {
	dir := t.TempDir()
	set := func(cpuIdle, cpuTotalOther, rx, tx, rd, wr string) {
		write(t, dir, "stat", "cpu  "+cpuTotalOther+" 0 0 "+cpuIdle+" 0 0 0 0 0 0\ncpu0 "+cpuTotalOther+" 0 0 "+cpuIdle+" 0 0 0 0 0 0\nintr 1\n")
		write(t, dir, "1/net/dev", "Inter-|\n face |bytes\n    lo: 999 0 0 0 0 0 0 0 999 0 0 0 0 0 0 0\n  eth0: "+rx+" 0 0 0 0 0 0 0 "+tx+" 0 0 0 0 0 0 0\n")
		write(t, dir, "diskstats", "   8       0 sda 1 0 "+rd+" 0 1 0 "+wr+" 0 0 0 0\n   8       1 sda1 1 0 100000 0 1 0 100000 0 0 0 0\n   7       0 loop0 1 0 99999 0 1 0 99999 0 0 0 0\n")
	}
	write(t, dir, "loadavg", "0.50 0.40 0.30 1/100 123\n")
	write(t, dir, "meminfo", "MemTotal: 1000 kB\nMemAvailable: 250 kB\nSwapTotal: 100 kB\nSwapFree: 100 kB\n")
	set("100", "100", "1000", "500", "0", "0")
	h := &HostSampler{Proc: dir}
	h.Sample()
	time.Sleep(100 * time.Millisecond)
	set("150", "250", "2000", "600", "10", "20") // +50 idle of +200 total
	s := h.Sample()
	if s.CPU < 74 || s.CPU > 76 || len(s.PerCore) != 1 {
		t.Fatalf("cpu = %v %v", s.CPU, s.PerCore)
	}
	if s.RAMPercent() != 75 || s.Load1 != 0.5 || s.SwapUsed != 0 {
		t.Fatalf("mem %+v", s)
	}
	if s.NetRx <= 0 || s.NetTx <= 0 || s.NetRx < s.NetTx*5 {
		t.Fatalf("net rx=%v tx=%v (loopback must be skipped)", s.NetRx, s.NetTx)
	}
	if s.DiskRead <= 0 || s.DiskWrite <= s.DiskRead {
		t.Fatalf("disk r=%v w=%v (partitions and loops skipped)", s.DiskRead, s.DiskWrite)
	}
}

func TestHostSamplerReal(t *testing.T) {
	if _, err := os.Stat("/proc/stat"); err != nil {
		t.Skip("no /proc")
	}
	h := &HostSampler{Proc: Root("/host/proc"), DataDir: t.TempDir()}
	h.Sample()
	time.Sleep(300 * time.Millisecond)
	s := h.Sample()
	if s.MemTotal <= 0 || s.DiskTotal <= 0 || len(s.PerCore) == 0 {
		t.Fatalf("%+v", s)
	}
}
