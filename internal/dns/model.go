package dns

import "fmt"

type Zone struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Reverse       bool   `json:"reverse"`
	DynamicUpdate string `json:"dynamicUpdate"`
	ServerID      string `json:"serverId"`
}

type Record struct {
	ID        string `json:"id"`
	ZoneID    string `json:"zoneId"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	TTL       int    `json:"ttl"`
	CreatePTR bool   `json:"createPtr,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type RecordUpdate struct {
	Old Record `json:"old"`
	New Record `json:"new"`
}

type RecordBatch struct {
	Add    []Record       `json:"add"`
	Delete []Record       `json:"delete"`
	Update []RecordUpdate `json:"update"`
}

func (r Record) IdentityKey(zone string) string {
	return fmt.Sprintf("%s|%s|%s|%s", zone, r.Type, r.Name, r.Value)
}

func (r Record) FullKey(zone string) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d", zone, r.Type, r.Name, r.Value, r.TTL)
}
