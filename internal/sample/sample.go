// Package sample defines the row/batch shapes collectors hand to exporters.
//
// Collectors build a TypedBatch[T] over their row struct. Exporters iterate
// by index and either JSON-encode each row (stdout) or bind it into a
// ClickHouse native-protocol block via AppendStruct (clickhouse). For
// collectors that emit rows to more than one table (e.g. process →
// process_stats + process_summary), MultiBatch wraps several sub-batches.
package sample

// Batch is a typed set of rows targeting one ClickHouse table.
type Batch interface {
	// Table is the destination ClickHouse table name.
	Table() string
	// Len is the row count.
	Len() int
	// At returns row i. Exporters either JSON-encode it (stdout) or
	// type-assert to the concrete row pointer for columnar binding.
	At(i int) any
}

// TypedBatch is a generic Batch implementation for collector-defined row types.
//
//	b := &sample.TypedBatch[cpu.CoreRow]{TableName: "cpu_core_stats", Rows: rows}
type TypedBatch[T any] struct {
	TableName string
	Rows      []T
}

// Table implements Batch.
func (b *TypedBatch[T]) Table() string { return b.TableName }

// Len implements Batch.
func (b *TypedBatch[T]) Len() int { return len(b.Rows) }

// At implements Batch. Returns a pointer into the underlying slice so
// exporters can stream rows into ClickHouse's native AppendStruct without
// reflecting over a value copy. The pointer is valid until the next mutation
// of Rows.
func (b *TypedBatch[T]) At(i int) any { return &b.Rows[i] }

// MultiBatch aggregates multiple target-table batches into a single result
// for collectors that emit to more than one table (e.g. process → stats +
// summary). The ClickHouse exporter unpacks via Parts().
type MultiBatch struct{ Batches []Batch }

// Table returns a sentinel; exporters should use Parts instead.
func (m *MultiBatch) Table() string { return "<multi>" }

// Len is the total row count across sub-batches.
func (m *MultiBatch) Len() int {
	var n int
	for _, p := range m.Batches {
		n += p.Len()
	}
	return n
}

// At returns row i by seeking through sub-batches.
func (m *MultiBatch) At(i int) any {
	for _, p := range m.Batches {
		if i < p.Len() {
			return p.At(i)
		}
		i -= p.Len()
	}
	return nil
}

// Parts exposes the sub-batches so exporters can ship each to its own table.
func (m *MultiBatch) Parts() []Batch { return m.Batches }
