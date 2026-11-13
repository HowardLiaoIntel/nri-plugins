// Copyright The NRI Plugins Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestParseSchemata(t *testing.T) {
	a := parseSchemata("MB:0=100;1=50\nL2:0=ff;1=ff;32=f\nL3:0=ffff;1=f0f\nbogus\nL3:x=1\nL4:0=1\n")
	if l3 := a.masks["L3"]; l3[0] != 0xffff || l3[1] != 0xf0f || len(l3) != 2 {
		t.Errorf("L3 masks = %v", l3)
	}
	if l2 := a.masks["L2"]; l2[0] != 0xff || l2[1] != 0xff || l2[32] != 0xf || len(l2) != 3 {
		t.Errorf("L2 masks = %v", l2)
	}
	if len(a.masks) != 2 {
		t.Errorf("unexpected cache levels: %v", a.masks)
	}
	if a.mbPct[0] != 100 || a.mbPct[1] != 50 || len(a.mbPct) != 2 {
		t.Errorf("mbPct = %v", a.mbPct)
	}
	if e := parseSchemata(""); len(e.masks["L2"]) != 0 || len(e.masks["L3"]) != 0 || len(e.mbPct) != 0 {
		t.Errorf("empty schemata gave %v", e)
	}
}

func TestAllocMetrics(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "schemata"), []byte("MB:0=100;1=50\nL2:0=ffff;1=ffff;2=3\nL3:0=ffff;1=f0f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rd := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(rd)).Meter("test")
	reg, err := registerAllocMetrics(meter, root)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Unregister()

	var rm metricdata.ResourceMetrics
	if err := rd.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]int64{} // metric -> "level/domain" -> value
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("%s is not an int64 gauge", m.Name)
			}
			got[m.Name] = map[string]int64{}
			for _, dp := range g.DataPoints {
				d, _ := dp.Attributes.Value("domain")
				l, _ := dp.Attributes.Value("cache_level")
				key := d.AsString()
				if l.AsString() != "" {
					key = l.AsString() + "/" + key
				}
				got[m.Name][key] = dp.Value
			}
		}
	}
	if w := got["resctrl.root.cat.allocated_ways"]; w["L3/0"] != 16 || w["L3/1"] != 8 || w["L2/0"] != 16 || w["L2/2"] != 2 || len(w) != 5 {
		t.Errorf("allocated_ways = %v, want L3/0:16 L3/1:8 L2/0:16 L2/1:16 L2/2:2", w)
	}
	if m := got["resctrl.root.mba.allocation"]; m["0"] != 100 || m["1"] != 50 {
		t.Errorf("mba allocation = %v", m)
	}
	if m := got["resctrl.root.cat.cbm_mask_info"]; len(m) != 5 || m["L2/0"] != 1 {
		t.Errorf("cbm_mask_info = %v", m)
	}
}

func TestAllocMetricsMissingSchemata(t *testing.T) {
	rd := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(rd)).Meter("test")
	reg, err := registerAllocMetrics(meter, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Unregister()
	var rm metricdata.ResourceMetrics
	if err := rd.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect must not fail when schemata is unreadable: %v", err)
	}
}
