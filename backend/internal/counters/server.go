package counters

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServerBotID marks server metrics in minute records (stored in m_server).
const ServerBotID = -1

// Server is one sample of the host.
type Server struct {
	TS        int64     `cbor:"1,keyasint" json:"ts"`
	CPU       float64   `cbor:"2,keyasint" json:"cpu"` // % of all cores
	PerCore   []float64 `cbor:"3,keyasint" json:"per_core,omitempty"`
	Load1     float64   `cbor:"4,keyasint" json:"load1"`
	Load5     float64   `cbor:"5,keyasint" json:"load5"`
	Load15    float64   `cbor:"6,keyasint" json:"load15"`
	MemTotal  int64     `cbor:"7,keyasint" json:"mem_total"`
	MemUsed   int64     `cbor:"8,keyasint" json:"mem_used"`
	SwapTotal int64     `cbor:"9,keyasint" json:"swap_total"`
	SwapUsed  int64     `cbor:"10,keyasint" json:"swap_used"`
	DiskTotal int64     `cbor:"11,keyasint" json:"disk_total"`
	DiskUsed  int64     `cbor:"12,keyasint" json:"disk_used"`
	DiskRead  float64   `cbor:"13,keyasint" json:"disk_read"`  // bytes/s
	DiskWrite float64   `cbor:"14,keyasint" json:"disk_write"` // bytes/s
	NetRx     float64   `cbor:"15,keyasint" json:"net_rx"`     // bytes/s
	NetTx     float64   `cbor:"16,keyasint" json:"net_tx"`     // bytes/s
}

// RAMPercent and DiskPercent are what the header shows.
func (s Server) RAMPercent() float64  { return pct(s.MemUsed, s.MemTotal) }
func (s Server) DiskPercent() float64 { return pct(s.DiskUsed, s.DiskTotal) }

func pct(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

// HostSampler reads the host's /proc. CPU, memory, load and disks are
// host-wide even inside a container; the network is not (/proc/net shows
// the reader's own namespace), so it is read from /host/proc/1/net/dev —
// the network of the host's PID 1.
type HostSampler struct {
	Proc    string // /host/proc, or /proc when not mounted
	DataDir string // for statfs

	mu        sync.Mutex
	last      time.Time
	cpu       [][2]uint64 // idle, total per line ("cpu", "cpu0"…)
	disk      [2]uint64   // sectors read, written
	net       [2]uint64   // rx, tx bytes
	haveFirst bool
}

// Root picks /host/proc if it exists, else /proc.
func Root(hostProc string) string {
	if _, err := os.Stat(filepath.Join(hostProc, "stat")); err == nil {
		return hostProc
	}
	return "/proc"
}

// Sample returns the current state; rates are since the previous call.
func (h *HostSampler) Sample() Server {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	s := Server{TS: now.Unix()}
	dt := now.Sub(h.last).Seconds()
	cpu := h.readCPU()
	if h.haveFirst && len(cpu) == len(h.cpu) {
		for i := range cpu {
			idle := float64(cpu[i][0] - h.cpu[i][0])
			total := float64(cpu[i][1] - h.cpu[i][1])
			v := 0.0
			if total > 0 {
				v = (1 - idle/total) * 100
			}
			if i == 0 {
				s.CPU = v
			} else {
				s.PerCore = append(s.PerCore, v)
			}
		}
	}
	h.cpu = cpu
	if b, err := os.ReadFile(filepath.Join(h.Proc, "loadavg")); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			s.Load1, _ = strconv.ParseFloat(f[0], 64)
			s.Load5, _ = strconv.ParseFloat(f[1], 64)
			s.Load15, _ = strconv.ParseFloat(f[2], 64)
		}
	}
	mem := readKV(filepath.Join(h.Proc, "meminfo"))
	s.MemTotal = mem["MemTotal"] << 10
	s.MemUsed = (mem["MemTotal"] - mem["MemAvailable"]) << 10
	s.SwapTotal = mem["SwapTotal"] << 10
	s.SwapUsed = (mem["SwapTotal"] - mem["SwapFree"]) << 10
	s.DiskTotal, s.DiskUsed = diskUsage(h.DataDir)
	disk := readDiskstats(filepath.Join(h.Proc, "diskstats"))
	net := readNetDev(filepath.Join(h.Proc, "1", "net", "dev"))
	if h.haveFirst && dt > 0 {
		s.DiskRead = float64(disk[0]-h.disk[0]) * 512 / dt
		s.DiskWrite = float64(disk[1]-h.disk[1]) * 512 / dt
		s.NetRx = float64(net[0]-h.net[0]) / dt
		s.NetTx = float64(net[1]-h.net[1]) / dt
	}
	h.disk, h.net, h.last, h.haveFirst = disk, net, now, true
	return s
}

func (h *HostSampler) readCPU() [][2]uint64 {
	f, err := os.Open(filepath.Join(h.Proc, "stat"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out [][2]uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		var total uint64
		var vals []uint64
		for _, x := range fields[1:] {
			v, _ := strconv.ParseUint(x, 10, 64)
			vals = append(vals, v)
			total += v
		}
		idle := vals[3]
		if len(vals) > 4 {
			idle += vals[4] // iowait
		}
		out = append(out, [2]uint64{idle, total})
	}
	return out
}

func readKV(path string) map[string]int64 {
	out := map[string]int64{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) > 0 {
			out[k], _ = strconv.ParseInt(fields[0], 10, 64)
		}
	}
	return out
}

// readDiskstats sums sectors of whole disks (not partitions, not loops).
func readDiskstats(path string) [2]uint64 {
	var out [2]uint64
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		x := strings.Fields(sc.Text())
		if len(x) < 10 {
			continue
		}
		name := x[2]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "dm-") ||
			(len(name) > 0 && name[len(name)-1] >= '0' && name[len(name)-1] <= '9' && !strings.HasPrefix(name, "nvme") && !strings.HasPrefix(name, "vd") && !strings.HasPrefix(name, "xvd")) ||
			(strings.HasPrefix(name, "nvme") && strings.Contains(name, "p")) {
			continue
		}
		r, _ := strconv.ParseUint(x[5], 10, 64)
		w, _ := strconv.ParseUint(x[9], 10, 64)
		out[0] += r
		out[1] += w
	}
	return out
}

// readNetDev sums rx/tx bytes of all interfaces except loopback.
func readNetDev(path string) [2]uint64 {
	var out [2]uint64
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || strings.TrimSpace(name) == "lo" {
			continue
		}
		x := strings.Fields(rest)
		if len(x) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(x[0], 10, 64)
		tx, _ := strconv.ParseUint(x[8], 10, 64)
		out[0] += rx
		out[1] += tx
	}
	return out
}

// RecordServer puts a sample into the server minute as gauges (max over the
// minute) and counters for traffic.
func (c *Counters) RecordServer(s Server) {
	g := map[string]float64{
		"cpu": s.CPU, "load1": s.Load1, "ram": s.RAMPercent(), "mem_used": float64(s.MemUsed),
		"swap_used": float64(s.SwapUsed), "disk": s.DiskPercent(), "disk_used": float64(s.DiskUsed),
		"disk_read": s.DiskRead, "disk_write": s.DiskWrite, "net_rx": s.NetRx, "net_tx": s.NetTx,
	}
	for k, v := range g {
		c.Gauge(ServerBotID, k, v)
	}
}
