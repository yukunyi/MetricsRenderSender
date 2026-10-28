package main

import "testing"

func TestStaticCollectorRediscoveryPreservesState(t *testing.T) {
	collectors := []Collector{
		&GoNativeZramCollector{BaseCollector: NewBaseCollector(collectorGoNativeZram)},
		&GoNativeBtrfsRootCollector{BaseCollector: NewBaseCollector(collectorGoNativeBtrfsRoot)},
	}
	for _, collector := range collectors {
		t.Run(collector.Name(), func(t *testing.T) {
			manager := NewCollectorManager()
			manager.RegisterCollector(collector)
			entries := manager.snapshotCollectors()
			manager.discoverFromCollectors(entries, "test_initial")
			original := collector.GetAllItems()
			if len(original) == 0 {
				t.Fatal("expected static collector items")
			}
			versions := make(map[string]uint64, len(original))
			for name, item := range original {
				item.SetValue(42.5)
				item.SetEnabled(false)
				item.SetAvailable(false)
				versions[name] = item.Version()
			}

			manager.discoverFromCollectors(entries, "test_rediscovery")
			for name, originalItem := range original {
				item := manager.items[name]
				if item != originalItem {
					t.Fatalf("rediscovery replaced item %s", name)
				}
				if item.Version() != versions[name] || item.GetValue().Value != 42.5 || item.IsEnabled() || item.IsAvailable() {
					t.Fatalf("rediscovery reset state for %s", name)
				}
			}
		})
	}
}
