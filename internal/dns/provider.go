package dns

import "context"

type Provider interface {
	ListZones(ctx context.Context) ([]Zone, error)
	CreateZone(ctx context.Context, zone Zone) error
	DeleteZone(ctx context.Context, name string) error
	ListRecords(ctx context.Context, zone string) ([]Record, error)
	CreateRecord(ctx context.Context, zone string, record Record) error
	DeleteRecord(ctx context.Context, zone string, record Record) error
	ApplyRecordBatch(ctx context.Context, zone string, batch RecordBatch) error
}
