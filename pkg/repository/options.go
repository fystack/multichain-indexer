package repository

type OrderType string

const (
	OrderTypeAsc  OrderType = "ASC"
	OrderTypeDesc OrderType = "DESC"
)

type WhereType map[string]any
type SelectType []string
type Order map[string]OrderType

type FindOptions struct {
	Select          SelectType
	Where           WhereType
	Relations       map[string]SelectType
	RelationFilters map[string]WhereType
	Order           Order
	Limit           uint
	Offset          uint
}

func Select(fields ...string) SelectType {
	return fields
}
