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
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// rootAllocation is the allocation (CAT/MBA) configuration of the resctrl root
// group, read from its schemata file. Keys of the inner maps are cache/MB
// domain IDs. L2 domains are per L2 cache, so a node has many more of them
// than L3 domains.
type rootAllocation struct {
	masks map[string]map[int]uint64 // cache level ("L2", "L3") -> domain -> capacity bitmask
	mbPct map[int]uint64            // MB bandwidth percentage per domain
}

// parseSchemata parses the L2, L3 and MB lines of a resctrl schemata file, e.g.
//
//	MB:0=100;1=100
//	L2:0=ffff;1=ffff;2=ffff
//	L3:0=ffff;1=ffff
//
// Other resources and malformed entries are ignored. MB values are parsed as
// decimal, cache masks as hexadecimal.
func parseSchemata(data string) rootAllocation {
	a := rootAllocation{masks: map[string]map[int]uint64{"L2": {}, "L3": {}}, mbPct: map[int]uint64{}}
	for _, line := range strings.Split(data, "\n") {
		res, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		res = strings.TrimSpace(res)
		if res != "L2" && res != "L3" && res != "MB" {
			continue
		}
		for _, ent := range strings.Split(rest, ";") {
			id, val, ok := strings.Cut(strings.TrimSpace(ent), "=")
			if !ok {
				continue
			}
			dom, err := strconv.Atoi(strings.TrimSpace(id))
			if err != nil {
				continue
			}
			val = strings.TrimSpace(val)
			if res == "MB" {
				if v, err := strconv.ParseUint(val, 10, 64); err == nil {
					a.mbPct[dom] = v
				}
			} else if m, err := strconv.ParseUint(val, 16, 64); err == nil {
				a.masks[res][dom] = m
			}
		}
	}
	return a
}

// readRootAllocation reads the root group's allocation from resctrlRoot.
func readRootAllocation(resctrlRoot string) (rootAllocation, error) {
	data, err := os.ReadFile(filepath.Join(resctrlRoot, "schemata"))
	if err != nil {
		return rootAllocation{}, err
	}
	return parseSchemata(string(data)), nil
}

// registerAllocMetrics registers observable gauges describing the root group's
// CAT/MBA allocation. They are read from schemata each time metrics are
// collected, and are absent (no observations) if the file cannot be read.
//
//	resctrl_root_cat_allocated_ways{cache_level,domain}           number of cache ways allocated
//	resctrl_root_cat_cbm_mask_info{cache_level,domain,cbm_mask}   constant 1, the mask is the label
//	resctrl_root_mba_allocation{domain,mode="percent"}            memory bandwidth allocation
//
// The label names match the exporter behind the existing Intel RDT dashboard.
func registerAllocMetrics(meter otelmetric.Meter, resctrlRoot string) (otelmetric.Registration, error) {
	ways, err := meter.Int64ObservableGauge("resctrl.root.cat.allocated_ways",
		otelmetric.WithDescription("Number of cache ways (per cache level and domain) allocated to the resctrl root group"))
	if err != nil {
		return nil, err
	}
	mask, err := meter.Int64ObservableGauge("resctrl.root.cat.cbm_mask_info",
		otelmetric.WithDescription("Cache capacity bitmask of the resctrl root group (value is always 1, mask in label)"))
	if err != nil {
		return nil, err
	}
	mba, err := meter.Int64ObservableGauge("resctrl.root.mba.allocation",
		otelmetric.WithDescription("Memory bandwidth allocation of the resctrl root group"))
	if err != nil {
		return nil, err
	}

	return meter.RegisterCallback(func(_ context.Context, o otelmetric.Observer) error {
		a, err := readRootAllocation(resctrlRoot)
		if err != nil {
			return nil
		}
		for level, doms := range a.masks {
			for dom, m := range doms {
				l := attribute.String("cache_level", level)
				d := attribute.String("domain", strconv.Itoa(dom))
				o.ObserveInt64(ways, int64(bits.OnesCount64(m)), otelmetric.WithAttributes(l, d))
				o.ObserveInt64(mask, 1, otelmetric.WithAttributes(l, d,
					attribute.String("cbm_mask", strconv.FormatUint(m, 16))))
			}
		}
		for dom, v := range a.mbPct {
			o.ObserveInt64(mba, int64(v), otelmetric.WithAttributes(
				attribute.String("domain", strconv.Itoa(dom)),
				attribute.String("mode", "percent")))
		}
		return nil
	}, ways, mask, mba)
}
