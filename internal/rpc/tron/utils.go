package tron

// ConvertTronTimestamp converts TRON millisecond timestamp to Unix seconds
func ConvertTronTimestamp(tronTimestamp int64) uint64 {
	return uint64(tronTimestamp / 1000)
}
