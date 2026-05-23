package syncer

import (
	"strings"
	"testing"

	"windnssyncagent/internal/config"
	"windnssyncagent/internal/dns"
)

func TestDiffRecordsMirror(t *testing.T) {
	source := []dns.Record{{ZoneID: "example.com", Name: "www", Type: "A", Value: "10.0.0.1", TTL: 60}}
	target := []dns.Record{
		{ZoneID: "example.com", Name: "www", Type: "A", Value: "10.0.0.1", TTL: 60},
		{ZoneID: "example.com", Name: "old", Type: "A", Value: "10.0.0.2", TTL: 60},
	}

	adds, deletes, updates := diffRecords("example.com", source, target, "mirror")
	if len(adds) != 0 {
		t.Fatalf("expected no adds, got %d", len(adds))
	}
	if len(updates) != 0 {
		t.Fatalf("expected no updates, got %d", len(updates))
	}
	if len(deletes) != 1 || deletes[0].Name != "old" {
		t.Fatalf("expected old record delete, got %#v", deletes)
	}
}

func TestDiffRecordsAddOnly(t *testing.T) {
	source := []dns.Record{{ZoneID: "example.com", Name: "www", Type: "A", Value: "10.0.0.1", TTL: 60}}
	target := []dns.Record{{ZoneID: "example.com", Name: "old", Type: "A", Value: "10.0.0.2", TTL: 60}}

	adds, deletes, updates := diffRecords("example.com", source, target, "addOnly")
	if len(adds) != 1 || adds[0].Name != "www" {
		t.Fatalf("expected www record add, got %#v", adds)
	}
	if len(updates) != 0 {
		t.Fatalf("expected no updates, got %d", len(updates))
	}
	if len(deletes) != 0 {
		t.Fatalf("expected no deletes, got %d", len(deletes))
	}
}

func TestDiffRecordsProtectsRootNS(t *testing.T) {
	_, deletes, _ := diffRecords("example.com", nil, []dns.Record{{ZoneID: "example.com", Name: "@", Type: "NS", Value: "ns1.example.com", TTL: 60}}, "mirror")
	if len(deletes) != 0 {
		t.Fatalf("expected protected NS record to remain, got %#v", deletes)
	}
}

func TestDiffRecordsSyncsDelegationNS(t *testing.T) {
	source := []dns.Record{{ZoneID: "example.com", Name: "delegated", Type: "NS", Value: "ns1.delegated.example.com.", TTL: 60}}
	adds, deletes, updates := diffRecords("example.com", source, nil, "mirror")
	if len(adds) != 1 || adds[0].Type != "NS" || adds[0].Name != "delegated" {
		t.Fatalf("expected delegated NS add, got adds=%#v deletes=%#v updates=%#v", adds, deletes, updates)
	}

	adds, deletes, updates = diffRecords("example.com", nil, source, "mirror")
	if len(deletes) != 1 || deletes[0].Type != "NS" || deletes[0].Name != "delegated" {
		t.Fatalf("expected delegated NS delete, got adds=%#v deletes=%#v updates=%#v", adds, deletes, updates)
	}
}

func TestFilterSyncRecordsExcludesSOAAndRootNS(t *testing.T) {
	records := filterSyncRecords([]dns.Record{
		{Type: "SOA", Name: "@"},
		{Type: "NS", Name: "@"},
		{Type: "NS", Name: "delegated", Value: "ns1.delegated.example.com."},
		{Type: "A", Name: "www", Value: "10.0.0.1"},
	})
	if len(records) != 2 || records[0].Type != "NS" || records[1].Type != "A" {
		t.Fatalf("expected delegated NS and A record, got %#v", records)
	}
}

func TestDiffRecordsUpdatesSingleARecordValue(t *testing.T) {
	source := []dns.Record{{ZoneID: "example.com", Name: "www", Type: "A", Value: "10.0.0.1", TTL: 60}}
	target := []dns.Record{{ZoneID: "example.com", Name: "www", Type: "A", Value: "10.0.0.2", TTL: 60}}

	adds, deletes, updates := diffRecords("example.com", source, target, "mirror")
	if len(adds) != 0 || len(deletes) != 0 {
		t.Fatalf("expected no add/delete for update, adds=%#v deletes=%#v", adds, deletes)
	}
	if len(updates) != 1 || updates[0].Old.Value != "10.0.0.2" || updates[0].New.Value != "10.0.0.1" {
		t.Fatalf("expected A record update, got %#v", updates)
	}
}

func TestDiffRecordsAddOnlyAddsSameNameDifferentIP(t *testing.T) {
	source := []dns.Record{{ZoneID: "example.com", Name: "www", Type: "A", Value: "10.0.0.1", TTL: 60}}
	target := []dns.Record{{ZoneID: "example.com", Name: "www", Type: "A", Value: "10.0.0.2", TTL: 60}}

	adds, deletes, updates := diffRecords("example.com", source, target, "addOnly")
	if len(adds) != 1 || adds[0].Value != "10.0.0.1" {
		t.Fatalf("expected same-name different-IP source record to be added, got %#v", adds)
	}
	if len(updates) != 0 || len(deletes) != 0 {
		t.Fatalf("expected addOnly to avoid update/delete, updates=%#v deletes=%#v", updates, deletes)
	}
}

func TestDiffRecordsMirrorComparesMultiValueRecordsByIP(t *testing.T) {
	source := []dns.Record{
		{ZoneID: "example.com", Name: "@", Type: "A", Value: "10.0.0.1", TTL: 60},
		{ZoneID: "example.com", Name: "@", Type: "A", Value: "10.0.0.2", TTL: 60},
	}
	target := []dns.Record{
		{ZoneID: "example.com", Name: "@", Type: "A", Value: "10.0.0.1", TTL: 60},
		{ZoneID: "example.com", Name: "@", Type: "A", Value: "10.0.0.3", TTL: 60},
	}

	adds, deletes, updates := diffRecords("example.com", source, target, "mirror")
	if len(adds) != 1 || adds[0].Name != "@" || adds[0].Value != "10.0.0.2" {
		t.Fatalf("expected missing parent-folder IP to be added, got %#v", adds)
	}
	if len(deletes) != 1 || deletes[0].Name != "@" || deletes[0].Value != "10.0.0.3" {
		t.Fatalf("expected extra parent-folder IP to be deleted, got %#v", deletes)
	}
	if len(updates) != 0 {
		t.Fatalf("expected multi-value records to avoid update path, got %#v", updates)
	}
}

func TestValidateRewriteRecordRequiresOldIP(t *testing.T) {
	err := validateRewriteRecord(config.RewriteRecord{Zone: "example.com", Name: "www", Type: "A", TargetIP: "10.0.0.2"})
	if err == nil || !strings.Contains(err.Error(), "oldIp") {
		t.Fatalf("expected oldIp required error, got %v", err)
	}
}

func TestValidateRewriteRecordValidatesOldIPFamily(t *testing.T) {
	err := validateRewriteRecord(config.RewriteRecord{Zone: "example.com", Name: "www", Type: "A", OldIP: "2001:db8::1", TargetIP: "10.0.0.2"})
	if err == nil || !strings.Contains(err.Error(), "old IPv4") {
		t.Fatalf("expected old IPv4 validation error, got %v", err)
	}
}

func TestSelectZonesDefaultsToForwardZonesOnly(t *testing.T) {
	zones := selectZones(nil, []dns.Zone{
		{Name: "example.com", Reverse: false},
		{Name: "1.168.192.in-addr.arpa", Reverse: true},
		{Name: "TrustAnchors", Reverse: false},
	})
	if len(zones) != 1 || zones[0] != "example.com" {
		t.Fatalf("expected only forward zones, got %#v", zones)
	}
}

func TestSelectZonesKeepsExplicitReverseZone(t *testing.T) {
	zones := selectZones([]string{"1.168.192.in-addr.arpa"}, []dns.Zone{
		{Name: "example.com", Reverse: false},
		{Name: "1.168.192.in-addr.arpa", Reverse: true},
	})
	if len(zones) != 1 || zones[0] != "1.168.192.in-addr.arpa" {
		t.Fatalf("expected explicit reverse zone, got %#v", zones)
	}
}

func TestSelectZonesSkipsConditionalForwarderZones(t *testing.T) {
	zones := selectZones(nil, []dns.Zone{
		{Name: "example.com", Type: "Primary"},
		{Name: "dc.k8s", Type: "Forwarder"},
		{Name: "sunline.lab", Type: "ConditionalForwarder"},
	})
	if len(zones) != 1 || zones[0] != "example.com" {
		t.Fatalf("expected only syncable zones, got %#v", zones)
	}
}

func TestSelectZoneSelectionsResolvesSubtreeUnderZone(t *testing.T) {
	selections := selectZoneSelections([]string{"test.cursor.com"}, nil, []dns.Zone{{Name: "cursor.com"}})
	if len(selections) != 1 || selections[0].Name != "cursor.com" || selections[0].Subtree != "test" {
		t.Fatalf("expected cursor.com/test selection, got %#v", selections)
	}
}

func TestSelectZoneSelectionsPrefersLongestZoneMatch(t *testing.T) {
	selections := selectZoneSelections([]string{"api.test.cursor.com"}, nil, []dns.Zone{{Name: "cursor.com"}, {Name: "test.cursor.com"}})
	if len(selections) != 1 || selections[0].Name != "test.cursor.com" || selections[0].Subtree != "api" {
		t.Fatalf("expected longest zone match, got %#v", selections)
	}
}

func TestSelectZoneSelectionsExcludesWholeZone(t *testing.T) {
	selections := selectZoneSelections(nil, []string{"cursor.com"}, []dns.Zone{{Name: "cursor.com"}, {Name: "example.com"}})
	if len(selections) != 1 || selections[0].Name != "example.com" {
		t.Fatalf("expected cursor.com excluded, got %#v", selections)
	}
}

func TestSelectZoneSelectionsExcludesSubtree(t *testing.T) {
	selections := selectZoneSelections([]string{"cursor.com"}, []string{"test.cursor.com"}, []dns.Zone{{Name: "cursor.com"}})
	if len(selections) != 1 || selections[0].Name != "cursor.com" || len(selections[0].ExcludeSubtrees) != 1 || selections[0].ExcludeSubtrees[0] != "test" {
		t.Fatalf("expected cursor.com with test excluded, got %#v", selections)
	}
}

func TestTargetZoneDeleteExclusionSetProtectsTargetOnlyZone(t *testing.T) {
	excluded := targetZoneDeleteExclusionSet([]string{"test.cursor.com"}, []dns.Zone{{Name: "cursor.com"}, {Name: "test.cursor.com"}})
	if !excluded[normalizedZoneKey("test.cursor.com")] {
		t.Fatalf("expected target-only zone to be protected, got %#v", excluded)
	}
}

func TestTargetZoneDeleteExclusionSetDoesNotProtectParentForSubtreeExclude(t *testing.T) {
	excluded := targetZoneDeleteExclusionSet([]string{"test.cursor.com"}, []dns.Zone{{Name: "cursor.com"}})
	if excluded[normalizedZoneKey("cursor.com")] {
		t.Fatalf("expected subtree exclusion to avoid protecting parent zone, got %#v", excluded)
	}
}

func TestFilterRecordsBySubtreeKeepsNodeAndChildren(t *testing.T) {
	records := filterRecordsBySubtree([]dns.Record{
		{Name: "test", Type: "A", Value: "1.1.1.1"},
		{Name: "www.test", Type: "A", Value: "2.2.2.2"},
		{Name: "other", Type: "A", Value: "3.3.3.3"},
		{Name: "badtest", Type: "A", Value: "4.4.4.4"},
	}, "test")
	if len(records) != 2 || records[0].Name != "test" || records[1].Name != "www.test" {
		t.Fatalf("expected only test subtree records, got %#v", records)
	}
}

func TestFilterRecordsBySelectionExcludesSubtree(t *testing.T) {
	records := filterRecordsBySelection([]dns.Record{
		{Name: "test", Type: "A", Value: "1.1.1.1"},
		{Name: "www.test", Type: "A", Value: "2.2.2.2"},
		{Name: "other", Type: "A", Value: "3.3.3.3"},
	}, zoneSelection{Name: "cursor.com", ExcludeSubtrees: []string{"test"}})
	if len(records) != 1 || records[0].Name != "other" {
		t.Fatalf("expected test subtree excluded, got %#v", records)
	}
}

func TestWithCreatePTROnlyMarksARecords(t *testing.T) {
	a := withCreatePTR(dns.Record{Type: "A"}, true)
	if !a.CreatePTR {
		t.Fatal("expected A record to create PTR")
	}
	cname := withCreatePTR(dns.Record{Type: "CNAME"}, true)
	if cname.CreatePTR {
		t.Fatal("expected non-A record to skip PTR")
	}
}

func TestStringSet(t *testing.T) {
	set := stringSet([]string{"example.com"})
	if !set["example.com"] || set["missing.local"] {
		t.Fatalf("unexpected set: %#v", set)
	}
}

func TestMergeResults(t *testing.T) {
	merged := mergeResults(Result{DryRun: true, Messages: []string{"base"}}, []Result{
		{ZonesCreated: []string{"example.com"}, Messages: []string{"create zone example.com"}},
		{ZonesDeleted: []string{"old.local"}, Messages: []string{"delete zone old.local"}},
	})
	if !merged.DryRun {
		t.Fatal("expected dryRun to be preserved")
	}
	if len(merged.ZonesCreated) != 1 || len(merged.ZonesDeleted) != 1 || len(merged.Messages) != 3 {
		t.Fatalf("unexpected merged result: %#v", merged)
	}
}
