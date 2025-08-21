package repository

type OrderType string

const (
	OrderTypeAsc  OrderType = "ASC"
	OrderTypeDesc OrderType = "DESC"
)

type WhereType map[string]interface{}
type SelectType []string
type Relations map[string]SelectType
type RelationFilters map[string]WhereType // (New) Optional filters to apply on the preloaded relations.
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
