package server

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

type Machine struct {
	Host   string     `json:"host"`
	OS     string     `json:"os"`
	Uptime uint64     `json:"uptime"`
	CPU    machineCPU `json:"cpu"`
	Memory usage      `json:"memory"`
	Swap   usage      `json:"swap"`
	Disks  []diskUse  `json:"disks"`
	// DV is dv with everything it started: agents, terminals, their tools.
	DV struct {
		Memory    uint64 `json:"memory"`
		Processes int    `json:"processes"`
	} `json:"dv"`
	History []sample `json:"history"`
}

type machineCPU struct {
	Percent float64   `json:"percent"`
	Cores   int       `json:"cores"`
	Load    []float64 `json:"load,omitempty"`
}

type usage struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type diskUse struct {
	Mount string `json:"mount"`
	usage
	Free uint64 `json:"free"`
}

var platform = sync.OnceValue(func() string {
	i, err := host.Info()
	if err != nil {
		return runtime.GOOS + "/" + runtime.GOARCH
	}
	name := cmp.Or(i.Platform, i.OS)
	// gopsutil gives Linux distributions' ids, as "ubuntu".
	if name != "" {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	name = strings.TrimSpace(name + " " + i.PlatformVersion)
	return fmt.Sprintf("%s, %s", name, i.KernelArch)
})

var cores = sync.OnceValue(func() int {
	n, _ := cpu.Counts(true)
	return cmp.Or(n, runtime.NumCPU())
})

// A sample is CPU and memory in use at a moment, a point of the history a page
// draws as it opens rather than from when it did.
type sample struct {
	At  int64   `json:"at"` // Unix milliseconds
	CPU float64 `json:"cpu"`
	Mem uint64  `json:"mem"`
}

const (
	sampleEvery = 3 * time.Second
	keptSamples = 200 // ten minutes
)

var (
	samplesMu sync.Mutex
	samples   []sample
)

// takeSample is the only caller of cpu.Percent, whose figure with no interval
// is since its previous call, from any caller.
func takeSample(wait time.Duration) {
	var s sample
	if p, err := cpu.Percent(wait, false); err == nil && len(p) > 0 {
		s.CPU = p[0]
	}
	if v, err := mem.VirtualMemory(); err == nil {
		s.Mem = v.Total - v.Available
	}
	s.At = time.Now().UnixMilli()
	samplesMu.Lock()
	samples = append(samples, s)
	if len(samples) > keptSamples {
		samples = samples[len(samples)-keptSamples:]
	}
	samplesMu.Unlock()
}

// sampling starts once the computer is first asked about, and goes on while
// dv runs, at a few syscalls a sample.
var sampling = sync.OnceFunc(func() {
	takeSample(300 * time.Millisecond)
	go func() {
		for range time.Tick(sampleEvery) {
			takeSample(0)
		}
	}()
})

func history() []sample {
	sampling()
	samplesMu.Lock()
	defer samplesMu.Unlock()
	return slices.Clone(samples)
}

// MachineNow reads the computer dv runs on, with the disks holding home and
// dirs, each disk once.
func MachineNow(dirs []string) Machine {
	var m Machine
	m.Host, _ = os.Hostname()
	m.OS = platform()
	m.Uptime, _ = host.Uptime()
	m.History = history()
	m.CPU = machineCPU{Percent: m.History[len(m.History)-1].CPU, Cores: cores()}
	if l, err := load.Avg(); err == nil && runtime.GOOS != "windows" {
		m.CPU.Load = []float64{l.Load1, l.Load5, l.Load15}
	}
	if v, err := mem.VirtualMemory(); err == nil {
		// Available counts reclaimable cache, which Used on Linux leaves out
		// inconsistently across versions.
		m.Memory = usage{v.Total, v.Total - v.Available}
	}
	if s, err := mem.SwapMemory(); err == nil {
		m.Swap = usage{s.Total, s.Used}
	}
	m.Disks = disksOf(dirs)
	m.DV.Memory, m.DV.Processes = treeMemory(int32(os.Getpid()))
	return m
}

func disksOf(dirs []string) []diskUse {
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append([]string{home}, dirs...)
	}
	parts, _ := disk.Partitions(false)
	mountOf := func(dir string) string {
		best := ""
		for _, p := range parts {
			mp := p.Mountpoint
			if within(dir, mp) && len(mp) > len(best) {
				best = mp
			}
		}
		return cmp.Or(best, dir)
	}
	seen := map[string]bool{}
	var out []diskUse
	for _, d := range dirs {
		mp := mountOf(filepath.Clean(d))
		if seen[mp] {
			continue
		}
		seen[mp] = true
		u, err := disk.Usage(mp)
		if err != nil || u.Total == 0 {
			continue
		}
		out = append(out, diskUse{mp, usage{u.Total, u.Used}, u.Free})
	}
	return out
}

func within(dir, mount string) bool {
	if runtime.GOOS == "windows" {
		dir, mount = strings.ToLower(dir), strings.ToLower(mount)
	}
	rel, err := filepath.Rel(mount, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// treeMemory adds up the resident memory of pid and its descendants, which
// counts pages they share more than once: an upper bound.
func treeMemory(pid int32) (uint64, int) {
	var total uint64
	n := 0
	queue := []int32{pid}
	for len(queue) > 0 && n < 2000 {
		p, err := process.NewProcess(queue[0])
		queue = queue[1:]
		if err != nil {
			continue
		}
		if mi, err := p.MemoryInfo(); err == nil {
			total += mi.RSS
			n++
		}
		kids, _ := p.Children()
		for _, k := range kids {
			queue = append(queue, k.Pid)
		}
	}
	return total, n
}

// HandleMachine serves MachineNow, for the disks of dirs() as well as home's.
func HandleMachine(dirs func() []string) http.HandlerFunc {
	var (
		mu   sync.Mutex
		last Machine
		at   time.Time
	)
	return func(w http.ResponseWriter, r *http.Request) {
		// Shared for a moment, as pages asking close together would each get
		// a CPU sample of a few milliseconds.
		mu.Lock()
		if time.Since(at) > 2*time.Second {
			last, at = MachineNow(dirs()), time.Now()
		}
		m := last
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(m)
	}
}
