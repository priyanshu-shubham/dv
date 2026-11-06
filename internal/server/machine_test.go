package server

import (
	"encoding/json"
	"testing"
)

func TestMachineReadsThisComputer(t *testing.T) {
	dir := t.TempDir()
	w := get(t, HandleMachine(func() []string { return []string{dir, dir} }), "/api/machine")
	var m Machine
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err, w.Body)
	}
	if m.Host == "" || m.CPU.Cores == 0 || m.Memory.Total == 0 || m.Memory.Used > m.Memory.Total {
		t.Errorf("machine: %+v", m)
	}
	if len(m.Disks) == 0 || m.DV.Processes == 0 || m.DV.Memory == 0 {
		t.Errorf("disks %+v, dv %+v", m.Disks, m.DV)
	}
	seen := map[string]bool{}
	for _, d := range m.Disks {
		if seen[d.Mount] {
			t.Errorf("%s twice", d.Mount)
		}
		seen[d.Mount] = true
	}
}
