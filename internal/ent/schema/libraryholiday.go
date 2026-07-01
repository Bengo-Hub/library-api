package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LibraryHoliday defines a branch-level (or tenant-wide) closure day.
// NULL branch_id means all branches observe the holiday. is_recurring means
// the closure repeats every year on the same month+day.
type LibraryHoliday struct {
	ent.Schema
}

func (LibraryHoliday) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (LibraryHoliday) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("branch_id", uuidType()).Optional().Nillable(),
		field.Time("holiday_date").Default(time.Now),
		field.String("description").Optional(),
		field.Bool("is_recurring").Default(false),
	}
}

func (LibraryHoliday) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "branch_id", "holiday_date"),
	}
}
