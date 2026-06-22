package syncer

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"windnssyncagent/internal/config"
	"windnssyncagent/internal/dns"
)

const defaultRecordBatchSize = 50

type Result struct {
	DryRun           bool
	ZonesCreated     []string
	ZonesDeleted     []string
	RecordsAdded     []dns.Record
	RecordsDeleted   []dns.Record
	RecordsUpdated   []dns.RecordUpdate
	RecordsRewritten []dns.Record
	Messages         []string
}

type Logger func(string)

type zoneSelection struct {
	Name            string
	Subtree         string
	ExcludeSubtrees []string
}

func Run(ctx context.Context, cfg config.Sync) (Result, error) {
	return RunWithLogger(ctx, cfg, nil)
}

func RunWithLogger(ctx context.Context, cfg config.Sync, logger Logger) (Result, error) {
	result := Result{DryRun: cfg.DryRun}
	logger = synchronizedLogger(logger)
	requestTimeout := time.Duration(cfg.RequestTimeoutSeconds) * time.Second
	source := newClient(cfg.SourceAgent, cfg.SourceAPIKey, requestTimeout)
	target := newClient(cfg.TargetAgent, cfg.TargetAPIKey, requestTimeout)

	if err := source.health(ctx); err != nil {
		return result, fmt.Errorf("source agent health check failed: %w", err)
	}
	if err := target.health(ctx); err != nil {
		return result, fmt.Errorf("target agent health check failed: %w", err)
	}

	sourceZones, err := source.listZones(ctx)
	if err != nil {
		return result, fmt.Errorf("list source zones: %w", err)
	}
	targetZones, err := target.listZones(ctx)
	if err != nil {
		return result, fmt.Errorf("list target zones: %w", err)
	}

	selectedSelections := selectZoneSelections(cfg.IncludeZones, cfg.ExcludeZones, sourceZones)
	selectedZones := selectionZoneNames(selectedSelections)
	targetZoneMap := zoneMap(targetZones)
	sourceZoneMap := zoneMap(sourceZones)
	selectedZoneSet := stringSet(selectedZones)
	excludedTargetZoneDeletes := targetZoneDeleteExclusionSet(cfg.ExcludeZones, targetZones)

	if cfg.SyncMode == "mirror" {
		for _, targetZone := range targetZones {
			if targetZone.Reverse || isSystemZone(targetZone.Name) || !isSyncableZone(targetZone) {
				continue
			}
			if excludedTargetZoneDeletes[normalizedZoneKey(targetZone.Name)] {
				continue
			}
			if len(cfg.IncludeZones) > 0 && !selectedZoneSet[targetZone.Name] {
				continue
			}
			if _, exists := sourceZoneMap[targetZone.Name]; exists {
				continue
			}
			if cfg.DryRun {
				result.ZonesDeleted = append(result.ZonesDeleted, targetZone.Name)
				addMessage(&result, logger, fmt.Sprintf("delete zone %s", targetZone.Name))
				continue
			}
			if err := target.deleteZone(ctx, targetZone.Name); err != nil {
				return result, fmt.Errorf("delete target zone %s: %w", targetZone.Name, err)
			}
			result.ZonesDeleted = append(result.ZonesDeleted, targetZone.Name)
			addMessage(&result, logger, fmt.Sprintf("delete zone %s", targetZone.Name))
		}
	}

	zoneResults, err := syncZones(ctx, cfg, source, target, selectedSelections, sourceZoneMap, targetZoneMap, logger)
	if err != nil {
		return mergeResults(result, zoneResults), err
	}
	result = mergeResults(result, zoneResults)

	if cfg.EnableRewrite {
		if err := applyRewrites(ctx, cfg, target, &result, logger); err != nil {
			return result, err
		}
	} else if len(cfg.RewriteRecords) > 0 {
		addMessage(&result, logger, "rewriteRecords skipped because enableRewriteRecords=false")
	}

	return result, nil
}

func syncZones(ctx context.Context, cfg config.Sync, source, target client, selectedSelections []zoneSelection, sourceZoneMap, targetZoneMap map[string]dns.Zone, logger Logger) ([]Result, error) {
	concurrency := cfg.ZoneConcurrency
	if concurrency <= 0 {
		concurrency = 2
	}
	if concurrency > len(selectedSelections) && len(selectedSelections) > 0 {
		concurrency = len(selectedSelections)
	}
	if concurrency <= 0 {
		return nil, nil
	}

	jobs := make(chan zoneSelection)
	results := make(chan Result, len(selectedSelections))
	errs := make(chan error, len(selectedSelections))
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for selection := range jobs {
				zoneResult, err := syncZone(ctx, cfg, source, target, selection, sourceZoneMap, targetZoneMap, logger)
				results <- zoneResult
				if err != nil {
					errs <- err
				}
			}
		}()
	}

	for _, selection := range selectedSelections {
		jobs <- selection
	}
	close(jobs)
	wg.Wait()
	close(results)
	close(errs)

	zoneResults := make([]Result, 0, len(selectedSelections))
	for zoneResult := range results {
		zoneResults = append(zoneResults, zoneResult)
	}
	var firstErr error
	for err := range errs {
		if firstErr == nil {
			firstErr = err
		}
	}
	sort.Slice(zoneResults, func(i, j int) bool {
		return firstMessage(zoneResults[i]) < firstMessage(zoneResults[j])
	})
	return zoneResults, firstErr
}

func syncZone(ctx context.Context, cfg config.Sync, source, target client, selection zoneSelection, sourceZoneMap, targetZoneMap map[string]dns.Zone, logger Logger) (Result, error) {
	result := Result{DryRun: cfg.DryRun}
	zoneName := selection.Name
	sourceZone, ok := sourceZoneMap[zoneName]
	if !ok {
		return result, fmt.Errorf("zone %s not found on source", zoneName)
	}
	if !isSyncableZone(sourceZone) {
		return result, nil
	}

	if _, exists := targetZoneMap[zoneName]; !exists {
		if cfg.DryRun {
			result.ZonesCreated = append(result.ZonesCreated, zoneName)
			addMessage(&result, logger, fmt.Sprintf("create zone %s", zoneName))
		} else {
			if err := target.createZone(ctx, sourceZone); err != nil {
				return result, fmt.Errorf("create target zone %s: %w", zoneName, err)
			}
			result.ZonesCreated = append(result.ZonesCreated, zoneName)
			addMessage(&result, logger, fmt.Sprintf("create zone %s", zoneName))
		}
	}

	sourceRecords, err := source.listRecords(ctx, zoneName)
	if err != nil {
		return result, fmt.Errorf("list source records for %s: %w", zoneName, err)
	}
	sourceRecords = filterSyncRecords(sourceRecords)
	sourceRecords = filterRecordsBySelection(sourceRecords, selection)
	sourceRecords = filterRecordsByExcludePatterns(zoneName, sourceRecords, cfg.ExcludeRecords)
	targetRecords, err := target.listRecords(ctx, zoneName)
	if err != nil {
		return result, fmt.Errorf("list target records for %s: %w", zoneName, err)
	}
	targetRecords = filterSyncRecords(targetRecords)
	targetRecords = filterRecordsBySelection(targetRecords, selection)
	targetRecords = filterRecordsByExcludePatterns(zoneName, targetRecords, cfg.ExcludeRecords)

	adds, deletes, updates := diffRecords(zoneName, sourceRecords, targetRecords, cfg.SyncMode)
	for i := range adds {
		adds[i] = withCreatePTR(adds[i], cfg.CreatePTR)
	}
	for i := range updates {
		updates[i].New = withCreatePTR(updates[i].New, cfg.CreatePTR)
	}

	if cfg.DryRun {
		addPlannedRecordMessages(&result, logger, zoneName, adds, deletes, updates)
		return result, nil
	}
	recordBatchSize := cfg.RecordBatchSize
	if recordBatchSize <= 0 {
		recordBatchSize = defaultRecordBatchSize
	}

	for start := 0; start < len(updates); start += recordBatchSize {
		end := min(start+recordBatchSize, len(updates))
		batchUpdates := updates[start:end]
		if err := target.applyRecordBatch(ctx, zoneName, dns.RecordBatch{Update: batchUpdates}); err != nil {
			return result, fmt.Errorf("update record batch for %s: %w", zoneName, err)
		}
		addPlannedRecordMessages(&result, logger, zoneName, nil, nil, batchUpdates)
	}
	for start := 0; start < len(adds); start += recordBatchSize {
		end := min(start+recordBatchSize, len(adds))
		batchAdds := adds[start:end]
		if err := target.applyRecordBatch(ctx, zoneName, dns.RecordBatch{Add: batchAdds}); err != nil {
			return result, fmt.Errorf("add record batch for %s: %w", zoneName, err)
		}
		addPlannedRecordMessages(&result, logger, zoneName, batchAdds, nil, nil)
	}
	for start := 0; start < len(deletes); start += recordBatchSize {
		end := min(start+recordBatchSize, len(deletes))
		batchDeletes := deletes[start:end]
		if err := target.applyRecordBatch(ctx, zoneName, dns.RecordBatch{Delete: batchDeletes}); err != nil {
			return result, fmt.Errorf("delete record batch for %s: %w", zoneName, err)
		}
		addPlannedRecordMessages(&result, logger, zoneName, nil, batchDeletes, nil)
	}
	return result, nil
}

func addPlannedRecordMessages(result *Result, logger Logger, zoneName string, adds, deletes []dns.Record, updates []dns.RecordUpdate) {
	for _, record := range adds {
		result.RecordsAdded = append(result.RecordsAdded, record)
		addMessage(result, logger, addRecordMessage(zoneName, record))
	}
	for _, update := range updates {
		result.RecordsUpdated = append(result.RecordsUpdated, update)
		addMessage(result, logger, updateRecordMessage(zoneName, update))
	}
	for _, record := range deletes {
		result.RecordsDeleted = append(result.RecordsDeleted, record)
		addMessage(result, logger, deleteRecordMessage(zoneName, record))
	}
}

func addRecordMessage(zoneName string, record dns.Record) string {
	return fmt.Sprintf("add %s %s %s %s ttl=%d", zoneName, record.Type, record.Name, record.Value, normalizedTTL(record.TTL))
}

func updateRecordMessage(zoneName string, update dns.RecordUpdate) string {
	return fmt.Sprintf("update %s %s %s %s -> %s ttl=%d", zoneName, update.New.Type, update.New.Name, update.Old.Value, update.New.Value, normalizedTTL(update.New.TTL))
}

func deleteRecordMessage(zoneName string, record dns.Record) string {
	return fmt.Sprintf("delete %s %s %s %s", zoneName, record.Type, record.Name, record.Value)
}

func addMessage(result *Result, logger Logger, message string) {
	result.Messages = append(result.Messages, message)
	if logger != nil {
		logger(message)
	}
}

func synchronizedLogger(logger Logger) Logger {
	if logger == nil {
		return nil
	}
	var mu sync.Mutex
	return func(message string) {
		mu.Lock()
		defer mu.Unlock()
		logger(message)
	}
}

func mergeResults(base Result, results []Result) Result {
	merged := base
	for _, result := range results {
		merged.ZonesCreated = append(merged.ZonesCreated, result.ZonesCreated...)
		merged.ZonesDeleted = append(merged.ZonesDeleted, result.ZonesDeleted...)
		merged.RecordsAdded = append(merged.RecordsAdded, result.RecordsAdded...)
		merged.RecordsDeleted = append(merged.RecordsDeleted, result.RecordsDeleted...)
		merged.RecordsUpdated = append(merged.RecordsUpdated, result.RecordsUpdated...)
		merged.RecordsRewritten = append(merged.RecordsRewritten, result.RecordsRewritten...)
		merged.Messages = append(merged.Messages, result.Messages...)
	}
	return merged
}

func firstMessage(result Result) string {
	if len(result.Messages) == 0 {
		return ""
	}
	return result.Messages[0]
}

func selectZones(configured []string, sourceZones []dns.Zone) []string {
	return selectionZoneNames(selectZoneSelections(configured, nil, sourceZones))
}

func selectZoneSelections(includeConfigured, excludeConfigured []string, sourceZones []dns.Zone) []zoneSelection {
	if len(includeConfigured) > 0 {
		selections := make([]zoneSelection, 0, len(includeConfigured))
		for _, value := range includeConfigured {
			selection := resolveZoneSelection(value, sourceZones)
			if selection.Name == "" {
				selection.Name = strings.TrimSpace(value)
			}
			selections = append(selections, selection)
		}
		sortZoneSelections(selections)
		return applyExcludeZoneSelections(dedupeZoneSelections(selections), excludeConfigured, sourceZones)
	}

	selections := make([]zoneSelection, 0, len(sourceZones))
	for _, zone := range sourceZones {
		if zone.Reverse || isSystemZone(zone.Name) || !isSyncableZone(zone) {
			continue
		}
		selections = append(selections, zoneSelection{Name: zone.Name})
	}
	sortZoneSelections(selections)
	return applyExcludeZoneSelections(selections, excludeConfigured, sourceZones)
}

func resolveZoneSelection(value string, sourceZones []dns.Zone) zoneSelection {
	name := strings.Trim(strings.TrimSpace(value), ".")
	if name == "" {
		return zoneSelection{}
	}
	matchedZone := ""
	for _, zone := range sourceZones {
		zoneName := strings.Trim(strings.TrimSpace(zone.Name), ".")
		if zoneName == "" {
			continue
		}
		if strings.EqualFold(name, zoneName) || strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(zoneName)) {
			if len(zoneName) > len(matchedZone) {
				matchedZone = zoneName
			}
		}
	}
	if matchedZone == "" {
		return zoneSelection{Name: name}
	}
	if strings.EqualFold(name, matchedZone) {
		return zoneSelection{Name: matchedZone}
	}
	subtree := strings.TrimSuffix(name[:len(name)-len(matchedZone)], ".")
	return zoneSelection{Name: matchedZone, Subtree: subtree}
}

func selectionZoneNames(selections []zoneSelection) []string {
	zones := make([]string, 0, len(selections))
	seen := make(map[string]bool, len(selections))
	for _, selection := range selections {
		name := strings.TrimSpace(selection.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		zones = append(zones, name)
	}
	sort.Strings(zones)
	return zones
}

func dedupeZoneSelections(selections []zoneSelection) []zoneSelection {
	result := make([]zoneSelection, 0, len(selections))
	seen := make(map[string]bool, len(selections))
	for _, selection := range selections {
		name := strings.TrimSpace(selection.Name)
		if name == "" {
			continue
		}
		subtree := strings.Trim(strings.TrimSpace(selection.Subtree), ".")
		excludeSubtrees := normalizeExcludeSubtrees(selection.ExcludeSubtrees)
		key := strings.ToLower(name) + "|" + strings.ToLower(subtree)
		if len(excludeSubtrees) > 0 {
			key += "|" + strings.ToLower(strings.Join(excludeSubtrees, ","))
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, zoneSelection{Name: name, Subtree: subtree, ExcludeSubtrees: excludeSubtrees})
	}
	return result
}

func sortZoneSelections(selections []zoneSelection) {
	sort.Slice(selections, func(i, j int) bool {
		left := strings.ToLower(selections[i].Name + "|" + selections[i].Subtree + "|" + strings.Join(selections[i].ExcludeSubtrees, ","))
		right := strings.ToLower(selections[j].Name + "|" + selections[j].Subtree + "|" + strings.Join(selections[j].ExcludeSubtrees, ","))
		return left < right
	})
}

func normalizeExcludeSubtrees(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = appendNormalizedExclusion(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result
}

func applyExcludeZoneSelections(selections []zoneSelection, configured []string, sourceZones []dns.Zone) []zoneSelection {
	if len(configured) == 0 || len(selections) == 0 {
		return selections
	}
	excludes := make([]zoneSelection, 0, len(configured))
	for _, value := range configured {
		selection := resolveZoneSelection(value, sourceZones)
		if selection.Name == "" {
			selection.Name = strings.TrimSpace(value)
		}
		excludes = append(excludes, selection)
	}
	result := make([]zoneSelection, 0, len(selections))
	for _, selection := range selections {
		result = append(result, subtractExcludedSelections(selection, excludes)...)
	}
	sortZoneSelections(result)
	return dedupeZoneSelections(result)
}

func targetZoneDeleteExclusionSet(configured []string, targetZones []dns.Zone) map[string]bool {
	result := make(map[string]bool)
	if len(configured) == 0 {
		return result
	}
	for _, value := range configured {
		selection := resolveZoneSelection(value, targetZones)
		if selection.Name == "" {
			selection.Name = strings.TrimSpace(value)
		}
		if normalizeNodeName(selection.Subtree) != "@" {
			continue
		}
		if key := normalizedZoneKey(selection.Name); key != "" {
			result[key] = true
		}
	}
	return result
}

func subtractExcludedSelections(selection zoneSelection, excludes []zoneSelection) []zoneSelection {
	parts := []zoneSelection{selection}
	for _, exclude := range excludes {
		if !strings.EqualFold(selection.Name, exclude.Name) {
			continue
		}
		next := make([]zoneSelection, 0, len(parts))
		for _, part := range parts {
			next = append(next, subtractExcludedSelection(part, exclude)...)
		}
		parts = next
		if len(parts) == 0 {
			break
		}
	}
	return parts
}

func subtractExcludedSelection(selection, exclude zoneSelection) []zoneSelection {
	includeSubtree := normalizeNodeName(selection.Subtree)
	excludeSubtree := normalizeNodeName(exclude.Subtree)
	if excludeSubtree == "" {
		return nil
	}
	if includeSubtree == "" {
		return []zoneSelection{{Name: selection.Name, Subtree: "", ExcludeSubtrees: []string{excludeSubtree}}}
	}
	if sameOrChildNode(includeSubtree, excludeSubtree) {
		return nil
	}
	if sameOrChildNode(excludeSubtree, includeSubtree) {
		selection.ExcludeSubtrees = appendNormalizedExclusion(selection.ExcludeSubtrees, excludeSubtree)
	}
	return []zoneSelection{selection}
}

func appendNormalizedExclusion(values []string, value string) []string {
	value = normalizeNodeName(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if strings.EqualFold(existing, value) {
			return values
		}
	}
	return append(values, value)
}

func zoneMap(zones []dns.Zone) map[string]dns.Zone {
	result := make(map[string]dns.Zone, len(zones))
	for _, zone := range zones {
		result[zone.Name] = zone
	}
	return result
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func normalizedZoneKey(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "."))
}

func isSystemZone(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "TrustAnchors")
}

func isSyncableZone(zone dns.Zone) bool {
	typeName := strings.ToLower(strings.TrimSpace(zone.Type))
	if typeName == "" {
		return true
	}
	switch typeName {
	case "primary", "secondary", "stub":
		return true
	default:
		return false
	}
}

func diffRecords(zone string, sourceRecords, targetRecords []dns.Record, mode string) ([]dns.Record, []dns.Record, []dns.RecordUpdate) {
	sourceMap := make(map[string]dns.Record, len(sourceRecords))
	targetMap := make(map[string]dns.Record, len(targetRecords))
	sourceNameTypeMap := make(map[string][]dns.Record, len(sourceRecords))
	targetNameTypeMap := make(map[string][]dns.Record, len(targetRecords))
	for _, record := range sourceRecords {
		record = withDefaultTTL(record)
		sourceMap[record.IdentityKey(zone)] = record
		sourceNameTypeMap[nameTypeKey(record)] = append(sourceNameTypeMap[nameTypeKey(record)], record)
	}
	for _, record := range targetRecords {
		record = withDefaultTTL(record)
		targetMap[record.IdentityKey(zone)] = record
		targetNameTypeMap[nameTypeKey(record)] = append(targetNameTypeMap[nameTypeKey(record)], record)
	}

	usedSource := make(map[string]bool)
	usedTarget := make(map[string]bool)
	var updates []dns.RecordUpdate
	for key, sources := range sourceNameTypeMap {
		targets := targetNameTypeMap[key]
		if mode == "mirror" && len(sources) == 1 && len(targets) == 1 && canUpdateRecord(sources[0]) && canUpdateRecord(targets[0]) {
			sourceRecord := sources[0]
			targetRecord := targets[0]
			if sourceRecord.IdentityKey(zone) != targetRecord.IdentityKey(zone) {
				updates = append(updates, dns.RecordUpdate{Old: targetRecord, New: sourceRecord})
				usedSource[sourceRecord.IdentityKey(zone)] = true
				usedTarget[targetRecord.IdentityKey(zone)] = true
			}
		}
	}

	var adds []dns.Record
	for key, record := range sourceMap {
		if usedSource[key] {
			continue
		}
		if _, exists := targetMap[key]; !exists {
			adds = append(adds, record)
		}
	}

	var deletes []dns.Record
	if mode == "mirror" {
		for key, record := range targetMap {
			if usedTarget[key] {
				continue
			}
			if _, exists := sourceMap[key]; !exists && !isProtectedRecord(record) {
				deletes = append(deletes, record)
			}
		}
	}

	sortRecords(adds)
	sortRecords(deletes)
	sortUpdates(updates)
	return adds, deletes, updates
}

func applyRewrites(ctx context.Context, cfg config.Sync, target client, result *Result, logger Logger) error {
	for _, rewrite := range cfg.RewriteRecords {
		if err := validateRewriteRecord(rewrite); err != nil {
			return err
		}

		records, err := target.listRecords(ctx, rewrite.Zone)
		if err != nil {
			return fmt.Errorf("list target records for rewrite %s/%s: %w", rewrite.Zone, rewrite.Name, err)
		}

		var oldRecords []dns.Record
		targetExists := false
		for _, record := range records {
			if !sameRecordName(record.Name, rewrite.Name) || !strings.EqualFold(record.Type, rewrite.Type) {
				continue
			}
			if sameRecordIP(record.Value, rewrite.TargetIP) {
				targetExists = true
				continue
			}
			if !sameRecordIP(record.Value, rewrite.OldIP) {
				continue
			}
			oldRecords = append(oldRecords, record)
		}

		if targetExists {
			if len(oldRecords) == 0 {
				addMessage(result, logger, fmt.Sprintf("rewrite skip existing %s %s %s %s", rewrite.Zone, rewrite.Type, rewrite.Name, rewrite.TargetIP))
				continue
			}
			for _, record := range oldRecords {
				result.RecordsDeleted = append(result.RecordsDeleted, record)
				addMessage(result, logger, fmt.Sprintf("rewrite delete old %s %s %s %s", rewrite.Zone, record.Type, record.Name, record.Value))
			}
			if !cfg.DryRun {
				if err := target.applyRecordBatch(ctx, rewrite.Zone, dns.RecordBatch{Delete: oldRecords}); err != nil {
					return fmt.Errorf("rewrite delete old batch: %w", err)
				}
			}
			continue
		}
		if len(oldRecords) == 0 {
			return fmt.Errorf("rewrite source record not found: zone=%s name=%s type=%s oldIp=%s", rewrite.Zone, rewrite.Name, rewrite.Type, rewrite.OldIP)
		}

		updates := make([]dns.RecordUpdate, 0, len(oldRecords))
		for _, record := range oldRecords {
			next := dns.Record{ZoneID: rewrite.Zone, Name: record.Name, Type: rewrite.Type, Value: rewrite.TargetIP, TTL: record.TTL}
			if rewrite.TTL > 0 {
				next.TTL = rewrite.TTL
			}
			next = withDefaultTTL(next)
			next = withCreatePTR(next, cfg.CreatePTR)
			updates = append(updates, dns.RecordUpdate{Old: record, New: next})
		}
		for _, update := range updates {
			result.RecordsRewritten = append(result.RecordsRewritten, update.New)
			addMessage(result, logger, fmt.Sprintf("rewrite update %s %s %s %s -> %s ttl=%d", rewrite.Zone, update.New.Type, update.New.Name, update.Old.Value, update.New.Value, update.New.TTL))
		}
		if !cfg.DryRun {
			if err := target.applyRecordBatch(ctx, rewrite.Zone, dns.RecordBatch{Update: updates}); err != nil {
				return fmt.Errorf("rewrite update batch: %w", err)
			}
		}
	}
	return nil
}

func validateRewriteRecord(rewrite config.RewriteRecord) error {
	if strings.TrimSpace(rewrite.Zone) == "" || strings.TrimSpace(rewrite.Name) == "" || strings.TrimSpace(rewrite.OldIP) == "" || strings.TrimSpace(rewrite.TargetIP) == "" {
		return fmt.Errorf("rewriteRecords requires zone, name, oldIp and targetIp")
	}
	if rewrite.Type != "A" && rewrite.Type != "AAAA" {
		return fmt.Errorf("rewriteRecords only supports A and AAAA records, got %s", rewrite.Type)
	}
	if rewrite.Type == "A" && net.ParseIP(rewrite.OldIP).To4() == nil {
		return fmt.Errorf("invalid old IPv4 address: %s", rewrite.OldIP)
	}
	if rewrite.Type == "AAAA" && net.ParseIP(rewrite.OldIP).To16() == nil {
		return fmt.Errorf("invalid old IPv6 address: %s", rewrite.OldIP)
	}
	if rewrite.Type == "A" && net.ParseIP(rewrite.TargetIP).To4() == nil {
		return fmt.Errorf("invalid target IPv4 address: %s", rewrite.TargetIP)
	}
	if rewrite.Type == "AAAA" && net.ParseIP(rewrite.TargetIP).To16() == nil {
		return fmt.Errorf("invalid target IPv6 address: %s", rewrite.TargetIP)
	}
	return nil
}

func isProtectedRecord(record dns.Record) bool {
	return isProtectedRecordTypeAtName(record.Type, record.Name)
}

func filterSyncRecords(records []dns.Record) []dns.Record {
	result := make([]dns.Record, 0, len(records))
	for _, record := range records {
		if isExcludedRecord(record) {
			continue
		}
		result = append(result, record)
	}
	return result
}

func filterRecordsBySubtree(records []dns.Record, subtree string) []dns.Record {
	node := normalizeNodeName(subtree)
	if node == "" || node == "@" {
		return records
	}
	result := make([]dns.Record, 0, len(records))
	for _, record := range records {
		name := normalizeNodeName(record.Name)
		if sameOrChildNode(name, node) {
			result = append(result, record)
		}
	}
	return result
}

func filterRecordsBySelection(records []dns.Record, selection zoneSelection) []dns.Record {
	result := filterRecordsBySubtree(records, selection.Subtree)
	if len(selection.ExcludeSubtrees) == 0 {
		return result
	}
	filtered := make([]dns.Record, 0, len(result))
	for _, record := range result {
		name := normalizeNodeName(record.Name)
		excluded := false
		for _, subtree := range selection.ExcludeSubtrees {
			if sameOrChildNode(name, subtree) {
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func filterRecordsByExcludePatterns(zoneName string, records []dns.Record, patterns []config.RecordPattern) []dns.Record {
	if len(patterns) == 0 || len(records) == 0 {
		return records
	}
	filtered := make([]dns.Record, 0, len(records))
	for _, record := range records {
		excluded := false
		for _, pattern := range patterns {
			if recordMatchesExcludePattern(zoneName, record, pattern) {
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func recordMatchesExcludePattern(zoneName string, record dns.Record, pattern config.RecordPattern) bool {
	if strings.TrimSpace(pattern.Zone) != "" && !sameDNSName(zoneName, pattern.Zone) {
		return false
	}
	if strings.TrimSpace(pattern.Name) != "" && !sameDNSName(record.Name, pattern.Name) {
		return false
	}
	if strings.TrimSpace(pattern.Type) != "" && !strings.EqualFold(strings.TrimSpace(record.Type), strings.TrimSpace(pattern.Type)) {
		return false
	}
	if strings.TrimSpace(pattern.Value) != "" && !sameRecordValue(record.Type, record.Value, pattern.Value) {
		return false
	}
	return true
}

func normalizeNodeName(value string) string {
	name := strings.Trim(strings.TrimSpace(value), ".")
	if name == "" {
		return "@"
	}
	return name
}

func sameOrChildNode(name, node string) bool {
	name = normalizeNodeName(name)
	node = normalizeNodeName(node)
	if node == "@" {
		return true
	}
	return strings.EqualFold(name, node) || strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(node))
}

func isExcludedRecord(record dns.Record) bool {
	return isProtectedRecordTypeAtName(record.Type, record.Name)
}

func isProtectedRecordTypeAtName(recordType, name string) bool {
	typeName := strings.ToUpper(strings.TrimSpace(recordType))
	if typeName == "SOA" {
		return true
	}
	return typeName == "NS" && isRootRecordName(name)
}

func isRootRecordName(name string) bool {
	normalized := normalizeNodeName(name)
	return normalized == "@"
}

func withDefaultTTL(record dns.Record) dns.Record {
	if record.TTL <= 0 {
		record.TTL = 3600
	}
	return record
}

func withCreatePTR(record dns.Record, enabled bool) dns.Record {
	if enabled && strings.EqualFold(record.Type, "A") {
		record.CreatePTR = true
	}
	return record
}

func normalizedTTL(ttl int) int {
	if ttl <= 0 {
		return 3600
	}
	return ttl
}

func sameRecordName(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func sameDNSName(left, right string) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(left), "."), strings.Trim(strings.TrimSpace(right), "."))
}

func sameRecordValue(recordType, left, right string) bool {
	typeName := strings.ToUpper(strings.TrimSpace(recordType))
	if typeName == "A" || typeName == "AAAA" {
		return sameRecordIP(left, right)
	}
	return sameDNSName(left, right)
}

func sameRecordIP(left, right string) bool {
	leftIP := net.ParseIP(strings.TrimSpace(left))
	rightIP := net.ParseIP(strings.TrimSpace(right))
	if leftIP == nil || rightIP == nil {
		return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
	}
	return leftIP.Equal(rightIP)
}

func nameTypeKey(record dns.Record) string {
	return strings.ToUpper(strings.TrimSpace(record.Type)) + "|" + strings.ToLower(strings.TrimSpace(record.Name))
}

func canUpdateRecord(record dns.Record) bool {
	typeName := strings.ToUpper(strings.TrimSpace(record.Type))
	return typeName == "A" || typeName == "AAAA"
}

func sortRecords(records []dns.Record) {
	sort.Slice(records, func(i, j int) bool {
		return records[i].FullKey(records[i].ZoneID) < records[j].FullKey(records[j].ZoneID)
	})
}

func sortUpdates(updates []dns.RecordUpdate) {
	sort.Slice(updates, func(i, j int) bool {
		return updates[i].New.FullKey(updates[i].New.ZoneID) < updates[j].New.FullKey(updates[j].New.ZoneID)
	})
}
